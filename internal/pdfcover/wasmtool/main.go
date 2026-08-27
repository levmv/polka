// pdfium-wasm verifies and regenerates Polka's tailored PDFium WebAssembly
// module. It deliberately uses only the standard library: this maintenance
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
	Binaryen struct {
		Version       string   `json:"version"`
		WASMOptSHA256 string   `json:"wasm_opt_sha256"`
		Arguments     []string `json:"arguments"`
	} `json:"binaryen"`
	Output struct {
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
	if len(args) == 0 {
		return errors.New("usage: pdfium-wasm <verify|derive> [options]")
	}

	switch args[0] {
	case "verify":
		flags := flag.NewFlagSet("verify", flag.ContinueOnError)
		root := flags.String("root", ".", "Polka repository root")
		input := flags.String("input", "", "optional upstream pdfium.wasm to verify")
		wasmOpt := flags.String("wasm-opt", "", "optional pinned wasm-opt to verify")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: pdfium-wasm verify [-root DIR] [-input FILE] [-wasm-opt FILE]")
		}
		return verify(*root, *input, *wasmOpt)
	case "derive":
		flags := flag.NewFlagSet("derive", flag.ContinueOnError)
		root := flags.String("root", ".", "Polka repository root")
		input := flags.String("input", "", "upstream go-pdfium pdfium.wasm")
		wasmOpt := flags.String("wasm-opt", "", "pinned Binaryen wasm-opt executable")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *input == "" || *wasmOpt == "" {
			return errors.New("usage: pdfium-wasm derive [-root DIR] -input FILE -wasm-opt FILE")
		}
		return derive(*root, *input, *wasmOpt)
	default:
		return fmt.Errorf("unknown command %q; want verify or derive", args[0])
	}
}

func verify(root, inputPath, wasmOptPath string) error {
	spec, err := loadManifest(root)
	if err != nil {
		return err
	}
	if err := verifyGoPDFiumVersion(filepath.Join(root, "go.mod"), spec.GoPDFium.Version); err != nil {
		return err
	}
	outputPath := filepath.Join(root, spec.Output.Path)
	output, err := checkedFile(outputPath, spec.Output.Bytes, spec.Output.SHA256)
	if err != nil {
		return fmt.Errorf("tailored module: %w", err)
	}
	if err := verifyExports(output, spec.Exports); err != nil {
		return fmt.Errorf("tailored module: %w", err)
	}
	if err := verifyImports(output, spec.Imports); err != nil {
		return fmt.Errorf("tailored module: %w", err)
	}
	if inputPath != "" {
		if _, err := checkedFile(inputPath, spec.GoPDFium.WASMBytes, spec.GoPDFium.WASMSHA256); err != nil {
			return fmt.Errorf("upstream module: %w", err)
		}
	}
	if wasmOptPath != "" {
		if _, err := checkedFile(wasmOptPath, 0, spec.Binaryen.WASMOptSHA256); err != nil {
			return fmt.Errorf("wasm-opt: %w", err)
		}
	}

	fmt.Printf("verified %s (%d bytes, PDFium %d from go-pdfium %s)\n",
		spec.Output.Path, len(output), spec.GoPDFium.PDFiumVersion, spec.GoPDFium.Version)
	return nil
}

func derive(root, inputPath, wasmOptPath string) error {
	spec, err := loadManifest(root)
	if err != nil {
		return err
	}
	if err := verifyGoPDFiumVersion(filepath.Join(root, "go.mod"), spec.GoPDFium.Version); err != nil {
		return err
	}
	input, err := checkedFile(inputPath, spec.GoPDFium.WASMBytes, spec.GoPDFium.WASMSHA256)
	if err != nil {
		return fmt.Errorf("upstream module: %w", err)
	}
	if _, err := checkedFile(wasmOptPath, 0, spec.Binaryen.WASMOptSHA256); err != nil {
		return fmt.Errorf("wasm-opt: %w", err)
	}

	keep := make(map[string]struct{}, len(spec.Exports))
	for _, name := range spec.Exports {
		if _, duplicate := keep[name]; duplicate {
			return fmt.Errorf("manifest contains duplicate export %q", name)
		}
		keep[name] = struct{}{}
	}
	filtered, removed, err := filterExports(input, keep)
	if err != nil {
		return fmt.Errorf("filter exports: %w", err)
	}
	if err := verifyExports(filtered, spec.Exports); err != nil {
		return fmt.Errorf("filtered module: %w", err)
	}
	if err := verifyImports(filtered, spec.Imports); err != nil {
		return fmt.Errorf("filtered module: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "polka-pdfium-wasm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	filteredPath := filepath.Join(tempDir, "filtered.wasm")
	outputPath := filepath.Join(tempDir, "pdfium-cover.wasm")
	if err := os.WriteFile(filteredPath, filtered, 0o644); err != nil {
		return err
	}
	commandArgs := slices.Clone(spec.Binaryen.Arguments)
	commandArgs = append(commandArgs, filteredPath, "-o", outputPath)
	command := exec.Command(wasmOptPath, commandArgs...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run wasm-opt: %w", err)
	}

	output, err := checkedFile(outputPath, spec.Output.Bytes, spec.Output.SHA256)
	if err != nil {
		return fmt.Errorf("derived module: %w", err)
	}
	if err := verifyExports(output, spec.Exports); err != nil {
		return fmt.Errorf("derived module: %w", err)
	}
	if err := verifyImports(output, spec.Imports); err != nil {
		return fmt.Errorf("derived module: %w", err)
	}
	destination := filepath.Join(root, spec.Output.Path)
	if err := os.WriteFile(destination, output, 0o644); err != nil {
		return err
	}
	fmt.Printf("derived %s: removed %d exports, %d -> %d bytes\n",
		spec.Output.Path, removed, len(input), len(output))
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

func filterExports(input []byte, keep map[string]struct{}) ([]byte, int, error) {
	if len(input) < 8 || !bytes.Equal(input[:8], []byte("\x00asm\x01\x00\x00\x00")) {
		return nil, 0, errors.New("not a WebAssembly 1.0 module")
	}
	var output bytes.Buffer
	output.Write(input[:8])
	removed := 0
	foundExports := false
	for offset := 8; offset < len(input); {
		id := input[offset]
		offset++
		size, sizeBytes, err := readU32(input[offset:])
		if err != nil {
			return nil, 0, err
		}
		offset += sizeBytes
		end := offset + int(size)
		if end < offset || end > len(input) {
			return nil, 0, errors.New("section extends past input")
		}
		payload := input[offset:end]
		offset = end
		if id != 7 {
			output.WriteByte(id)
			writeU32(&output, uint32(len(payload)))
			output.Write(payload)
			continue
		}
		if foundExports {
			return nil, 0, errors.New("module has multiple export sections")
		}
		foundExports = true

		count, countBytes, err := readU32(payload)
		if err != nil {
			return nil, 0, err
		}
		cursor := countBytes
		var entries bytes.Buffer
		var kept uint32
		for range count {
			start := cursor
			name, next, err := readExportName(payload, cursor)
			if err != nil {
				return nil, 0, err
			}
			cursor = next
			if _, ok := keep[name]; ok {
				entries.Write(payload[start:cursor])
				kept++
			} else {
				removed++
			}
		}
		if cursor != len(payload) {
			return nil, 0, errors.New("trailing export-section bytes")
		}
		var filtered bytes.Buffer
		writeU32(&filtered, kept)
		filtered.Write(entries.Bytes())
		output.WriteByte(id)
		writeU32(&output, uint32(filtered.Len()))
		output.Write(filtered.Bytes())
	}
	if !foundExports {
		return nil, 0, errors.New("module has no export section")
	}
	return output.Bytes(), removed, nil
}

func exportNames(module []byte) ([]string, error) {
	if len(module) < 8 || !bytes.Equal(module[:8], []byte("\x00asm\x01\x00\x00\x00")) {
		return nil, errors.New("not a WebAssembly 1.0 module")
	}
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
		if id != 7 {
			offset = end
			continue
		}
		payload := module[offset:end]
		count, countBytes, err := readU32(payload)
		if err != nil {
			return nil, err
		}
		cursor := countBytes
		names := make([]string, 0, count)
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
	return nil, errors.New("module has no export section")
}

func importNames(module []byte) ([]string, error) {
	if len(module) < 8 || !bytes.Equal(module[:8], []byte("\x00asm\x01\x00\x00\x00")) {
		return nil, errors.New("not a WebAssembly 1.0 module")
	}
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
		if id != 2 {
			offset = end
			continue
		}
		payload := module[offset:end]
		count, countBytes, err := readU32(payload)
		if err != nil {
			return nil, err
		}
		cursor := countBytes
		names := make([]string, 0, count)
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
	return nil, errors.New("module has no import section")
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

func writeU32(output *bytes.Buffer, value uint32) {
	for {
		current := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			current |= 0x80
		}
		output.WriteByte(current)
		if value == 0 {
			return
		}
	}
}
