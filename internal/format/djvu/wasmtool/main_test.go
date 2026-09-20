package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPreparationCacheAndChecksum(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, "wrong archive")
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "djvutang.wasm")
	data := []byte("cached module")
	spec := manifest{
		URL: server.URL, ArchiveSHA256: strings.Repeat("0", 64),
		WASMSHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepare(dir, spec); err != nil || requests.Load() != 0 {
		t.Fatalf("valid cache should need no download: %v", err)
	}
	spec.WASMSHA256 = strings.Repeat("0", 64)
	if err := prepare(dir, spec); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("expected archive checksum rejection, got %v", err)
	}
	if requests.Load() != 1 {
		t.Fatal("mismatched cache should trigger a download")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("rejected download changed the cached file: %v", err)
	}
}
