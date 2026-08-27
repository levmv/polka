package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPrepareCache(t *testing.T) {
	module := append(testImportModule(t, "env.callback"), testModule(t, "keep")[8:]...)
	spec := manifest{Schema: 1, Imports: []string{"env.callback"}, Exports: []string{"keep"}}
	spec.GoPDFium.Version = "v1.0.0"
	spec.Binaryen.Version = "108.0.0"
	spec.Output.Path = "internal/pdfcover/generated/pdfium-cover.wasm"
	spec.Output.Bytes = len(module)
	spec.Output.SHA256 = fmt.Sprintf("%x", sha256.Sum256(module))
	manifestJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		module  []byte
		goPin   string
		npmPin  string
		wantErr string
	}{
		{"valid", module, "v1.0.0", "108.0.0", ""},
		{"missing", nil, "v1.0.0", "108.0.0", "download go-pdfium"},
		{"corrupt", make([]byte, len(module)), "v1.0.0", "108.0.0", "download go-pdfium"},
		{"changed Go pin", module, "v1.0.1", "108.0.0", "go.mod uses"},
		{"changed npm pin", module, "v1.0.0", "109.0.0", "package.json uses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string][]byte{
				manifestPath:   manifestJSON,
				"go.mod":       []byte(fmt.Sprintf("module fixture\nrequire %s %s\n", goPDFiumPath, tc.goPin)),
				"package.json": []byte(fmt.Sprintf(`{"devDependencies":{"binaryen":%q}}`, tc.npmPin)),
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
			// Invalid artifacts must reach preparation instead of being accepted.
			t.Setenv("PATH", t.TempDir())
			err := prepare(root)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("prepare error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestFilterExports(t *testing.T) {
	input := testModule(t, "keep", "remove", "also-keep")
	filtered, removed, err := filterExports(input, map[string]struct{}{
		"keep":      {},
		"also-keep": {},
	})
	if err != nil {
		t.Fatalf("filterExports: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	names, err := exportNames(filtered)
	if err != nil {
		t.Fatalf("exportNames: %v", err)
	}
	want := []string{"keep", "also-keep"}
	if !slices.Equal(names, want) {
		t.Fatalf("exports = %q, want %q", names, want)
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

func TestFilterExportsRejectsInvalidModule(t *testing.T) {
	if _, _, err := filterExports([]byte("not wasm"), nil); err == nil {
		t.Fatalf("filterExports unexpectedly accepted invalid input")
	}
}

func TestFilterExportsRejectsShortEntry(t *testing.T) {
	module := append([]byte("\x00asm\x01\x00\x00\x00"), 7, 1, 1)
	if _, _, err := filterExports(module, nil); err == nil {
		t.Fatalf("filterExports unexpectedly accepted a short export entry")
	}
}

func TestReadU32RejectsOverflow(t *testing.T) {
	if _, _, err := readU32([]byte{0x80, 0x80, 0x80, 0x80, 0x10}); err == nil {
		t.Fatalf("readU32 unexpectedly accepted an overflowing value")
	}
}

func testModule(t *testing.T, names ...string) []byte {
	t.Helper()
	var payload bytes.Buffer
	writeU32(&payload, uint32(len(names)))
	for index, name := range names {
		writeU32(&payload, uint32(len(name)))
		payload.WriteString(name)
		payload.WriteByte(0) // function
		writeU32(&payload, uint32(index))
	}
	var module bytes.Buffer
	module.WriteString("\x00asm\x01\x00\x00\x00")
	module.WriteByte(7)
	writeU32(&module, uint32(payload.Len()))
	module.Write(payload.Bytes())
	return module.Bytes()
}

func testImportModule(t *testing.T, names ...string) []byte {
	t.Helper()
	var payload bytes.Buffer
	writeU32(&payload, uint32(len(names)))
	for _, name := range names {
		moduleName, importName, found := bytes.Cut([]byte(name), []byte{'.'})
		if !found {
			t.Fatalf("import %q has no module separator", name)
		}
		writeU32(&payload, uint32(len(moduleName)))
		payload.Write(moduleName)
		writeU32(&payload, uint32(len(importName)))
		payload.Write(importName)
		payload.WriteByte(0) // function
		writeU32(&payload, 0)
	}
	var module bytes.Buffer
	module.WriteString("\x00asm\x01\x00\x00\x00")
	module.WriteByte(2)
	writeU32(&module, uint32(payload.Len()))
	module.Write(payload.Bytes())
	return module.Bytes()
}
