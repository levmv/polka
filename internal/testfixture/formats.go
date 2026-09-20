package testfixture

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

// MinimalPDF contains one page with a black rectangle and an Info dictionary.
func MinimalPDF() []byte {
	const content = "0 0 0 rg 20 20 50 100 re f\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 144 216] /Resources << >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		"<< /Title (Rendered PDF) /Author (Ada Lovelace) >>",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return buf.Bytes()
}

// EPUB packages a literal OPF at OEBPS/content.opf and optional archive entries.
// It supplies only ZIP/container boilerplate; callers own the book contents.
func EPUB(t testing.TB, opf []byte, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	write := func(name string, data []byte, method uint16) {
		t.Helper()
		entry, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatalf("create EPUB entry %q: %v", name, err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatalf("write EPUB entry %q: %v", name, err)
		}
	}
	write("mimetype", []byte("application/epub+zip"), zip.Store)
	write("META-INF/container.xml", []byte(`<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`), zip.Deflate)
	write("OEBPS/content.opf", opf, zip.Deflate)
	for name, data := range entries {
		write(name, data, zip.Deflate)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close EPUB: %v", err)
	}
	return buf.Bytes()
}

// MinimalMOBI returns the smallest PalmDB/MOBI structure used by format
// detection tests. It contains no book content or metadata.
func MinimalMOBI() []byte {
	const (
		palmDBHeaderSize = 78
		palmDBRecordSize = 8
		record0Offset    = palmDBHeaderSize + palmDBRecordSize
	)

	data := make([]byte, record0Offset+32)
	copy(data[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(data[76:78], 1)
	binary.BigEndian.PutUint32(data[78:82], record0Offset)
	copy(data[record0Offset+16:record0Offset+20], "MOBI")
	return data
}

// MinimalDJVU returns a minimal IFF form with the requested DjVu form type.
func MinimalDJVU(formType string) []byte {
	data := []byte("AT&TFORM\x00\x00\x00\x10" + formType)
	return append(data, []byte("DJVU body")...)
}

// MinimalCHM returns a minimal CHM header with the requested format version.
func MinimalCHM(version uint32) []byte {
	data := make([]byte, 32)
	copy(data[:4], "ITSF")
	binary.LittleEndian.PutUint32(data[4:8], version)
	return data
}
