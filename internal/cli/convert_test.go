package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRunConvertAZW4ToPDF(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "print-replica.azw4")
	dst := filepath.Join(dir, "print-replica.pdf")
	pdf := []byte("%PDF-1.7\nbody\n%%EOF")
	if err := os.WriteFile(src, testCLIMOBIWithPayload(pdf), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := runConvert(context.Background(), "", []string{"--to", "pdf", src, dst}); err != nil {
		t.Fatalf("runConvert: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(pdf) {
		t.Fatalf("output = %q; want %q", got, pdf)
	}
}

func TestRunConvertTXTToEPUB(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	dst := filepath.Join(dir, "notes.epub")
	if err := os.WriteFile(src, []byte("Plain CLI text.\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := runConvert(context.Background(), "", []string{"--to", "epub", src, dst}); err != nil {
		t.Fatalf("runConvert: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if xhtml := cliZipEntry(t, got, "OEBPS/text.xhtml"); !strings.Contains(xhtml, "Plain CLI text.") {
		t.Fatalf("text.xhtml = %s", xhtml)
	}
}

func TestRunConvertEPUBToKEPUB(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.epub")
	dst := filepath.Join(dir, "book.kepub.epub")
	if err := writeCLIEPUB(src, "Kobo CLI Book", "CLI body."); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := runConvert(context.Background(), "", []string{"--to", ".kepub", src, dst}); err != nil {
		t.Fatalf("runConvert: %v", err)
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

func writeCLIEPUB(path, title, body string) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("application/epub+zip")); err != nil {
		return err
	}
	for name, content := range map[string]string{
		"META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + title + `</dc:title><dc:language>en</dc:language></metadata>
  <manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`,
		"OEBPS/text.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>` + title + `</title></head><body><p>` + body + `</p></body></html>`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func testCLIMOBIWithPayload(payload []byte) []byte {
	const record0Offset = 78 + 8

	data := make([]byte, record0Offset+32)
	copy(data[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(data[76:78], 1)
	binary.BigEndian.PutUint32(data[78:82], record0Offset)
	copy(data[record0Offset+16:record0Offset+20], "MOBI")
	return append(data, payload...)
}
