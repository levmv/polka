// pdfium-wasm verifies and regenerates Polka's tailored PDFium WebAssembly
// module. It deliberately uses only the standard library: this build
// boundary must not become a runtime dependency.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	manifestPath = "internal/pdfcover/pdfium-wasm.json"
	goPDFiumPath = "github.com/klippa-app/go-pdfium"
)

type manifest struct {
	Schema   int `json:"schema"`
	GoPDFium struct {
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		PDFiumVersion int    `json:"pdfium_version"`
		WASMBytes     int    `json:"wasm_bytes"`
		WASMSHA256    string `json:"wasm_sha256"`
	} `json:"go_pdfium"`
	Binaryen binaryenSpec `json:"binaryen"`
	Output   struct {
		Path   string `json:"path"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"output"`
	Imports []string `json:"imports"`
	Exports []string `json:"exports"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || (args[0] != "prepare" && args[0] != "verify") {
		return errors.New("usage: pdfium-wasm <prepare|verify> [options]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := flags.String("root", ".", "Polka repository root")
	var force bool
	if args[0] == "prepare" {
		flags.BoolVar(&force, "force", false, "regenerate even if the cached module is valid")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	spec, err := loadManifest(*root)
	if err != nil {
		return err
	}
	if err := verifyGoPDFiumVersion(filepath.Join(*root, "go.mod"), spec.GoPDFium.Version); err != nil {
		return err
	}
	if !force {
		output, err := checkedModule(filepath.Join(*root, spec.Output.Path), spec)
		if args[0] == "verify" {
			if err != nil {
				return fmt.Errorf("tailored module: %w", err)
			}
			fmt.Printf("verified %s (%d bytes, PDFium %d from go-pdfium %s)\n",
				spec.Output.Path, len(output), spec.GoPDFium.PDFiumVersion, spec.GoPDFium.Version)
		}
		if err == nil {
			return nil
		}
	}
	return prepare(*root, spec)
}

func prepare(root string, spec manifest) error {
	fmt.Println("Preparing PDFium WebAssembly; subsequent builds reuse the verified module.")
	download := exec.Command("go", "mod", "download", "-json", goPDFiumPath+"@"+spec.GoPDFium.Version)
	download.Dir = root
	download.Stderr = os.Stderr
	data, downloadErr := download.Output()
	var module struct {
		Dir   string
		Error string
	}
	if err := json.Unmarshal(data, &module); err != nil {
		if downloadErr != nil {
			return fmt.Errorf("download go-pdfium: %w", downloadErr)
		}
		return fmt.Errorf("decode go-pdfium module location: %w", err)
	}
	if module.Error != "" {
		return fmt.Errorf("download go-pdfium: %s", module.Error)
	}
	if downloadErr != nil {
		return fmt.Errorf("download go-pdfium: %w", downloadErr)
	}
	if module.Dir == "" {
		return errors.New("go mod download returned no go-pdfium module directory")
	}
	inputPath := filepath.Join(module.Dir, "webassembly", "pdfium.wasm")
	input, err := checkedFile(inputPath, spec.GoPDFium.WASMBytes, spec.GoPDFium.WASMSHA256)
	if err != nil {
		return fmt.Errorf("upstream module: %w", err)
	}
	// Keep runtime helpers such as memset in the manifest: go-pdfium uses them
	// to avoid copying Go buffers for guest-memory operations.
	removed, err := exportsToRemove(input, spec.Exports)
	if err != nil {
		return err
	}
	if err := verifyImports(input, spec.Imports); err != nil {
		return fmt.Errorf("upstream module: %w", err)
	}
	wasmOptPath, err := prepareBinaryen(root, spec.Binaryen)
	if err != nil {
		return err
	}

	destination := filepath.Join(root, spec.Output.Path)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp(filepath.Dir(destination), ".pdfium-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	outputPath := filepath.Join(tempDir, "pdfium-cover.wasm")
	commandArgs := []string{wasmOptPath}
	if len(removed) > 0 {
		removePath := filepath.Join(tempDir, "remove-exports.txt")
		if err := os.WriteFile(removePath, []byte(strings.Join(removed, "\n")), 0o644); err != nil {
			return err
		}
		commandArgs = append(commandArgs, "--remove-exports=@"+removePath)
	}
	// Manifest feature flags must match wazero's support. --all-features can
	// enable Binaryen encodings that the runtime cannot read.
	commandArgs = append(commandArgs, spec.Binaryen.Arguments...)
	commandArgs = append(commandArgs, inputPath, "-o", outputPath)
	command := exec.Command("node", commandArgs...)
	command.Env = append(os.Environ(), "BINARYEN_CORES=1")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run wasm-opt: %w", err)
	}

	output, err := checkedModule(outputPath, spec)
	if err != nil {
		return fmt.Errorf("derived module: %w", err)
	}
	// Publish only a fully verified module, on the same filesystem as its
	// destination. A failed or interrupted derivation leaves the old file intact.
	if err := os.Rename(outputPath, destination); err != nil {
		return err
	}
	fmt.Printf("derived %s: removed %d exports, %d -> %d bytes\n",
		spec.Output.Path, len(removed), len(input), len(output))
	return nil
}

func loadManifest(root string) (manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, manifestPath))
	if err != nil {
		return manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var result manifest
	if err := json.Unmarshal(data, &result, json.RejectUnknownMembers(true)); err != nil {
		return manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if result.Schema != 1 || result.GoPDFium.Version == "" || result.Output.Path == "" ||
		len(result.Imports) == 0 || len(result.Exports) == 0 {
		return manifest{}, errors.New("manifest is incomplete or uses an unsupported schema")
	}
	return result, nil
}

func verifyGoPDFiumVersion(path, expected string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "require" {
			fields = fields[1:]
		}
		if len(fields) >= 2 && fields[0] == goPDFiumPath {
			if fields[1] != expected {
				return fmt.Errorf("go.mod uses %s %s; tailored module expects %s", goPDFiumPath, fields[1], expected)
			}
			return nil
		}
	}
	return fmt.Errorf("%s is not required by go.mod", goPDFiumPath)
}

func checkedFile(path string, expectedBytes int, expectedSHA256 string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if expectedBytes > 0 && len(data) != expectedBytes {
		return nil, fmt.Errorf("%s is %d bytes; want %d", path, len(data), expectedBytes)
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != expectedSHA256 {
		return nil, fmt.Errorf("%s SHA-256 is %s; want %s", path, actual, expectedSHA256)
	}
	return data, nil
}

func checkedModule(path string, spec manifest) ([]byte, error) {
	output, err := checkedFile(path, spec.Output.Bytes, spec.Output.SHA256)
	if err != nil {
		return nil, err
	}
	if err := verifyExports(output, spec.Exports); err != nil {
		return nil, err
	}
	if err := verifyImports(output, spec.Imports); err != nil {
		return nil, err
	}
	return output, nil
}

func verifyExports(module []byte, expected []string) error {
	actual, err := exportNames(module)
	if err != nil {
		return err
	}
	actual = slices.Sorted(slices.Values(actual))
	expected = slices.Sorted(slices.Values(expected))
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("exports are %q; want %q", actual, expected)
	}
	return nil
}

func verifyImports(module []byte, expected []string) error {
	actual, err := importNames(module)
	if err != nil {
		return err
	}
	actual = slices.Sorted(slices.Values(actual))
	expected = slices.Sorted(slices.Values(expected))
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("imports are %q; want %q", actual, expected)
	}
	return nil
}

func exportsToRemove(module []byte, keep []string) ([]string, error) {
	names, err := exportNames(module)
	if err != nil {
		return nil, err
	}
	var removed, retained []string
	for _, name := range names {
		if slices.Contains(keep, name) {
			retained = append(retained, name)
		} else {
			removed = append(removed, name)
		}
	}
	slices.Sort(retained)
	keep = slices.Sorted(slices.Values(keep))
	if !slices.Equal(retained, keep) {
		return nil, fmt.Errorf("upstream exports selected by manifest are %q; want %q", retained, keep)
	}
	return removed, nil
}

func exportNames(module []byte) ([]string, error) {
	payload, err := wasmSection(module, 7)
	if err != nil {
		return nil, err
	}
	count, cursor, err := readU32(payload)
	if err != nil {
		return nil, err
	}
	var names []string
	for range count {
		name, next, err := readExportName(payload, cursor)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
		cursor = next
	}
	if cursor != len(payload) {
		return nil, errors.New("trailing export-section bytes")
	}
	return names, nil
}

func importNames(module []byte) ([]string, error) {
	payload, err := wasmSection(module, 2)
	if err != nil {
		return nil, err
	}
	count, cursor, err := readU32(payload)
	if err != nil {
		return nil, err
	}
	var names []string
	for range count {
		moduleName, next, err := readName(payload, cursor)
		if err != nil {
			return nil, err
		}
		importName, next, err := readName(payload, next)
		if err != nil {
			return nil, err
		}
		cursor = next
		if cursor >= len(payload) {
			return nil, errors.New("short import entry")
		}
		kind := payload[cursor]
		cursor++
		if kind != 0 {
			return nil, fmt.Errorf("unsupported non-function import kind %d", kind)
		}
		_, indexBytes, err := readU32(payload[cursor:])
		if err != nil {
			return nil, err
		}
		cursor += indexBytes
		names = append(names, moduleName+"."+importName)
	}
	if cursor != len(payload) {
		return nil, errors.New("trailing import-section bytes")
	}
	return names, nil
}

func wasmSection(module []byte, wanted byte) ([]byte, error) {
	if len(module) < 8 || !bytes.Equal(module[:8], []byte("\x00asm\x01\x00\x00\x00")) {
		return nil, errors.New("not a WebAssembly 1.0 module")
	}
	var payload []byte
	for offset := 8; offset < len(module); {
		id := module[offset]
		offset++
		size, sizeBytes, err := readU32(module[offset:])
		if err != nil {
			return nil, err
		}
		offset += sizeBytes
		end := offset + int(size)
		if end < offset || end > len(module) {
			return nil, errors.New("section extends past input")
		}
		if id == wanted {
			if payload != nil {
				return nil, fmt.Errorf("module has multiple sections %d", wanted)
			}
			payload = module[offset:end]
		}
		offset = end
	}
	if payload == nil {
		return nil, fmt.Errorf("module has no section %d", wanted)
	}
	return payload, nil
}

func readExportName(payload []byte, cursor int) (string, int, error) {
	name, cursor, err := readName(payload, cursor)
	if err != nil {
		return "", 0, err
	}
	if cursor >= len(payload) {
		return "", 0, errors.New("short export entry")
	}
	cursor++ // export kind
	_, indexBytes, err := readU32(payload[cursor:])
	if err != nil {
		return "", 0, err
	}
	return name, cursor + indexBytes, nil
}

func readName(input []byte, cursor int) (string, int, error) {
	if cursor < 0 || cursor > len(input) {
		return "", 0, errors.New("name starts past input")
	}
	length, lengthBytes, err := readU32(input[cursor:])
	if err != nil {
		return "", 0, err
	}
	cursor += lengthBytes
	end := cursor + int(length)
	if end < cursor || end > len(input) {
		return "", 0, errors.New("short name")
	}
	return string(input[cursor:end]), end, nil
}

func readU32(input []byte) (uint32, int, error) {
	var result uint32
	for index := 0; index < 5 && index < len(input); index++ {
		value := input[index]
		if index == 4 && value&0xf0 != 0 {
			return 0, 0, errors.New("invalid u32 LEB128")
		}
		result |= uint32(value&0x7f) << (7 * index)
		if value&0x80 == 0 {
			return result, index + 1, nil
		}
	}
	return 0, 0, errors.New("invalid u32 LEB128")
}
