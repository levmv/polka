package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestRunContextCancelsConvertAndCleansOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.txt")
	dst := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(src, []byte("chapter"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := RunContext(ctx, []string{"convert", "--to", "epub", src, dst})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunContext error = %v; want context.Canceled", err)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled conversion left output; stat error = %v", statErr)
	}
}

func TestRunConvertEPUBToKEPUB(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.epub")
	dst := filepath.Join(dir, "book.kepub.epub")
	writeCLIEPUB(t, src, "Kobo CLI Book", "CLI body.")

	if err := RunContext(t.Context(), []string{"convert", "--to", ".kepub", src, dst}); err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if mimetype := cliZipEntry(t, got, "mimetype"); mimetype != "application/epub+zip" {
		t.Fatalf("mimetype = %q; want application/epub+zip", mimetype)
	}
	xhtml := cliZipEntry(t, got, "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, "koboSpan") || !strings.Contains(xhtml, "CLI body.") {
		t.Fatalf("KEPUB text.xhtml missing Kobo spans/body:\n%s", xhtml)
	}
}

func cliZipEntry(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", name, err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read zip entry %s: %v", name, err)
		}
		return string(body)
	}
	t.Fatalf("zip entry %s not found", name)
	return ""
}

func writeCLIEPUB(t *testing.T, path, title, body string) {
	t.Helper()
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + title + `</dc:title><dc:language>en</dc:language></metadata>
  <manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`)
	content := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>` + title + `</title></head><body><p>` + body + `</p></body></html>`)
	data := testfixture.EPUB(t, opf, map[string][]byte{"OEBPS/text.xhtml": content})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
}
