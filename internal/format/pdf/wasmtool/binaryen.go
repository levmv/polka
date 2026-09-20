package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type binaryenSpec struct {
	Version       string   `json:"version"`
	ArchiveSHA256 string   `json:"archive_sha256"`
	JSSHA256      string   `json:"wasm_opt_js_sha256"`
	WASMSHA256    string   `json:"wasm_opt_wasm_sha256"`
	Arguments     []string `json:"arguments"`
}

func verifyBinaryen(script string, spec binaryenSpec) error {
	if _, err := checkedFile(script, 0, spec.JSSHA256); err != nil {
		return err
	}
	_, err := checkedFile(filepath.Join(filepath.Dir(script), "wasm-opt.wasm"), 0, spec.WASMSHA256)
	return err
}

func prepareBinaryen(root string, spec binaryenSpec) (string, error) {
	dir := filepath.Join(root, "internal/format/pdf/generated", "binaryen-"+spec.Version)
	script := filepath.Join(dir, "wasm-opt.cjs")
	if err := verifyBinaryen(script, spec); err == nil {
		return script, nil
	}

	fmt.Printf("Downloading Binaryen %s wasm-opt (cached between builds).\n", spec.Version)
	url := fmt.Sprintf("https://github.com/WebAssembly/binaryen/releases/download/version_%[1]s/binaryen-version_%[1]s-node.tar.gz", spec.Version)
	client := &http.Client{Timeout: 2 * time.Minute}
	response, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("download Binaryen: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download Binaryen: %s", response.Status)
	}
	const maxArchiveBytes = 16 << 20
	archive, err := io.ReadAll(io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		return "", fmt.Errorf("download Binaryen: %w", err)
	}
	if len(archive) > maxArchiveBytes {
		return "", fmt.Errorf("Binaryen archive exceeds %d bytes", maxArchiveBytes)
	}
	if err := installBinaryen(dir, archive, spec); err != nil {
		return "", err
	}
	return script, nil
}

func installBinaryen(dir string, archive []byte, spec binaryenSpec) error {
	if actual := fmt.Sprintf("%x", sha256.Sum256(archive)); actual != spec.ArchiveSHA256 {
		return fmt.Errorf("Binaryen archive SHA-256 is %s; want %s", actual, spec.ArchiveSHA256)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open Binaryen archive: %w", err)
	}
	defer compressed.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	// Extract only the two expected regular files, to paths chosen here.
	// .cjs keeps the upstream script's CommonJS semantics in our ESM repository.
	files := map[string]string{"wasm-opt.js": "wasm-opt.cjs", "wasm-opt.wasm": "wasm-opt.wasm"}
	prefix := "binaryen-version_" + spec.Version + "/"
	const maxExpandedBytes = 32 << 20
	reader := tar.NewReader(io.LimitReader(compressed, maxExpandedBytes))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read Binaryen archive: %w", err)
		}
		for source, destination := range files {
			if header.Name != prefix+source {
				continue
			}
			if header.Typeflag != tar.TypeReg || header.Size > maxExpandedBytes {
				return fmt.Errorf("invalid Binaryen archive entry %q", header.Name)
			}
			data, err := io.ReadAll(reader)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(tempDir, destination), data, 0o644); err != nil {
				return err
			}
		}
	}
	if err := verifyBinaryen(filepath.Join(tempDir, "wasm-opt.cjs"), spec); err != nil {
		return fmt.Errorf("extracted Binaryen: %w", err)
	}
	// Publish verified files atomically. An interrupted installation is detected
	// by the same hashes on the next run; concurrent installs publish equal bytes.
	for _, name := range files {
		if err := os.Rename(filepath.Join(tempDir, name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}
