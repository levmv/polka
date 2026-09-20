// Prepare the pinned DjVuTang WASM. Called by the frontend build on a cache miss.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type manifest struct {
	Version       string `json:"version"`
	URL           string `json:"url"`
	ArchiveSHA256 string `json:"archive_sha256"`
	WASMSHA256    string `json:"wasm_sha256"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	data, err := os.ReadFile("internal/format/djvu/djvutang.json")
	if err != nil {
		return err
	}
	var spec manifest
	if err := json.Unmarshal(data, &spec); err != nil {
		return err
	}
	return prepare("internal/format/djvu/generated", spec)
}

func prepare(dir string, spec manifest) error {
	destination := filepath.Join(dir, "djvutang.wasm")
	if data, err := os.ReadFile(destination); err == nil && fmt.Sprintf("%x", sha256.Sum256(data)) == spec.WASMSHA256 {
		return nil
	}
	fmt.Printf("Downloading DjVuTang %s (cached between builds).\n", spec.Version)
	client := &http.Client{Timeout: 2 * time.Minute}
	response, err := client.Get(spec.URL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download DjVuTang: %s", response.Status)
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(archive)) != spec.ArchiveSHA256 {
		return fmt.Errorf("DjVuTang archive SHA-256 mismatch")
	}
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer compressed.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)

	// The archive hash pins this layout. Extract only the binary; the browser
	// adapter is vendored and notices live in Polka's ThirdPartyNotices.txt.
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return fmt.Errorf("DjVuTang release contains no djvutang.wasm")
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || header.Name != "djvutang-"+spec.Version+"-wasm/djvutang.wasm" {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != spec.WASMSHA256 {
			return fmt.Errorf("DjVuTang WASM SHA-256 mismatch")
		}
		path := filepath.Join(temp, "djvutang.wasm")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		return os.Rename(path, destination)
	}
}
