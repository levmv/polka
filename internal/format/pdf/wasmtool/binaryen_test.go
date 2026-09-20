package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallBinaryen(t *testing.T) {
	script, wasm := []byte("script fixture"), []byte("wasm fixture")
	spec := binaryenSpec{
		Version:    "test",
		JSSHA256:   fmt.Sprintf("%x", sha256.Sum256(script)),
		WASMSHA256: fmt.Sprintf("%x", sha256.Sum256(wasm)),
	}
	for _, tc := range []struct {
		name       string
		wasm       []byte
		badArchive bool
		wantErr    string
	}{
		{"valid", wasm, false, ""},
		{"wrong archive hash", wasm, true, "archive SHA-256"},
		{"wrong file hash", []byte("changed"), false, "wasm-opt.wasm SHA-256"},
		{"missing file", nil, false, "extracted Binaryen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archive bytes.Buffer
			compressed := gzip.NewWriter(&archive)
			writer := tar.NewWriter(compressed)
			files := map[string][]byte{"wasm-opt.js": script}
			if tc.wasm != nil {
				files["wasm-opt.wasm"] = tc.wasm
			}
			for name, data := range files {
				if err := writer.WriteHeader(&tar.Header{Name: "binaryen-version_test/" + name, Mode: 0o644, Size: int64(len(data))}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			spec := spec
			spec.ArchiveSHA256 = fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			if tc.badArchive {
				spec.ArchiveSHA256 = strings.Repeat("0", 64)
			}
			dir := t.TempDir()
			// A failed download or extraction must leave the previous tool intact.
			previous := []byte("previous tool")
			for _, name := range []string{"wasm-opt.cjs", "wasm-opt.wasm"} {
				if err := os.WriteFile(filepath.Join(dir, name), previous, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := installBinaryen(dir, archive.Bytes(), spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if err := verifyBinaryen(filepath.Join(dir, "wasm-opt.cjs"), spec); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("install error = %v; want %q", err, tc.wantErr)
				}
				for _, name := range []string{"wasm-opt.cjs", "wasm-opt.wasm"} {
					data, err := os.ReadFile(filepath.Join(dir, name))
					if err != nil || !bytes.Equal(data, previous) {
						t.Fatalf("previous %s changed: %q, %v", name, data, err)
					}
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("installation left temporary files: %v, %v", entries, err)
			}
		})
	}
}
