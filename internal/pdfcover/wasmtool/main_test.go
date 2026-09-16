package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestModuleCache(t *testing.T) {
	module := append(testImportModule(t, "env.callback"), testModule(t, "keep")[8:]...)
	spec := manifest{Schema: 1, Imports: []string{"env.callback"}, Exports: []string{"keep"}}
	spec.GoPDFium.Version = "v1.0.0"
	spec.Output.Path = "internal/pdfcover/generated/pdfium-cover.wasm"
	spec.Output.Bytes = len(module)
	spec.Output.SHA256 = fmt.Sprintf("%x", sha256.Sum256(module))
	manifestJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		args    []string
		module  []byte
		goPin   string
		wantErr string
	}{
		{"valid", []string{"prepare"}, module, "v1.0.0", ""},
		{"forced", []string{"prepare", "-force"}, module, "v1.0.0", "download go-pdfium"},
		{"missing", []string{"prepare"}, nil, "v1.0.0", "download go-pdfium"},
		{"corrupt", []string{"prepare"}, make([]byte, len(module)), "v1.0.0", "download go-pdfium"},
		{"changed Go pin", []string{"prepare"}, module, "v1.0.1", "go.mod uses"},
		{"verify valid", []string{"verify"}, module, "v1.0.0", ""},
		{"verify missing", []string{"verify"}, nil, "v1.0.0", "tailored module"},
		{"verify corrupt", []string{"verify"}, make([]byte, len(module)), "v1.0.0", "tailored module"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string][]byte{
				manifestPath: manifestJSON,
				"go.mod":     []byte(fmt.Sprintf("module fixture\nrequire %s %s\n", goPDFiumPath, tc.goPin)),
			}
			if tc.module != nil {
				files[spec.Output.Path] = tc.module
			}
			for name, data := range files {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			// Reusing a valid artifact needs neither downloads nor an optimizer.
			// Prepare must rebuild invalid artifacts; verify must only report them.
			t.Setenv("PATH", t.TempDir())
			err := run(append(slices.Clone(tc.args), "-root", root))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("run error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestExportsToRemove(t *testing.T) {
	input := testModule(t, "keep", "remove", "also-keep")
	for _, tc := range []struct {
		name    string
		keep    []string
		removed []string
		wantErr bool
	}{
		{"subset", []string{"keep", "also-keep"}, []string{"remove"}, false},
		{"all", []string{"also-keep", "remove", "keep"}, nil, false},
		{"missing required export", []string{"missing"}, nil, true},
		{"duplicate required export", []string{"keep", "keep"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			removed, err := exportsToRemove(input, tc.keep)
			if (err != nil) != tc.wantErr {
				t.Fatalf("exportsToRemove error = %v, wantErr %v", err, tc.wantErr)
			}
			if !slices.Equal(removed, tc.removed) {
				t.Fatalf("removed = %q, want %q", removed, tc.removed)
			}
		})
	}
}

func TestImportNames(t *testing.T) {
	module := testImportModule(t, "env.callback", "wasi_snapshot_preview1.fd_read")
	names, err := importNames(module)
	if err != nil {
		t.Fatalf("importNames: %v", err)
	}
	want := []string{"env.callback", "wasi_snapshot_preview1.fd_read"}
	if !slices.Equal(names, want) {
		t.Fatalf("imports = %q, want %q", names, want)
	}
}

func TestExportNamesRejectsInvalidModule(t *testing.T) {
	header := "\x00asm\x01\x00\x00\x00"
	for _, tc := range []struct {
		name   string
		module []byte
	}{
		{"invalid header", []byte("not wasm")},
		{"missing section", []byte(header)},
		{"short section", append([]byte(header), 7, 3, 1)},
		{"short entry", append([]byte(header), 7, 1, 1)},
		{"trailing bytes", append([]byte(header), 7, 2, 0, 0)},
		{"duplicate sections", append(testModule(t, "keep"), testModule(t, "other")[8:]...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exportNames(tc.module); err == nil {
				t.Fatal("exportNames unexpectedly accepted invalid input")
			}
		})
	}
}

func TestReadU32RejectsOverflow(t *testing.T) {
	if _, _, err := readU32([]byte{0x80, 0x80, 0x80, 0x80, 0x10}); err == nil {
		t.Fatalf("readU32 unexpectedly accepted an overflowing value")
	}
}

func testModule(t *testing.T, names ...string) []byte {
	t.Helper()
	payload := binary.AppendUvarint(nil, uint64(len(names)))
	for index, name := range names {
		payload = binary.AppendUvarint(payload, uint64(len(name)))
		payload = append(payload, name...)
		payload = append(payload, 0) // function
		payload = binary.AppendUvarint(payload, uint64(index))
	}
	module := append([]byte("\x00asm\x01\x00\x00\x00"), 7)
	module = binary.AppendUvarint(module, uint64(len(payload)))
	return append(module, payload...)
}

func testImportModule(t *testing.T, names ...string) []byte {
	t.Helper()
	payload := binary.AppendUvarint(nil, uint64(len(names)))
	for _, name := range names {
		moduleName, importName, found := strings.Cut(name, ".")
		if !found {
			t.Fatalf("import %q has no module separator", name)
		}
		payload = binary.AppendUvarint(payload, uint64(len(moduleName)))
		payload = append(payload, moduleName...)
		payload = binary.AppendUvarint(payload, uint64(len(importName)))
		payload = append(payload, importName...)
		payload = append(payload, 0, 0) // function, type index
	}
	module := append([]byte("\x00asm\x01\x00\x00\x00"), 2)
	module = binary.AppendUvarint(module, uint64(len(payload)))
	return append(module, payload...)
}
