package main

import (
	"bytes"
	"slices"
	"testing"
)

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
