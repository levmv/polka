package format

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExtractAZW4PDF(t *testing.T) {
	for _, tt := range []struct {
		name   string
		prefix string
		pdf    []byte
		suffix string
	}{
		{
			name:   "wrapper bytes",
			prefix: "azw4 wrapper prefix",
			pdf:    []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF"),
			suffix: "trailing container bytes",
		},
		{
			name:   "last EOF",
			prefix: "prefix",
			pdf:    []byte("%PDF-1.7\n%%EOF\nxref update\n%%EOF"),
			suffix: "suffix",
		},
		{
			name:   "markers across scan chunks",
			prefix: strings.Repeat("x", azw4ScanChunkSize-2),
			pdf:    []byte("%PDF-1.7\nbody\n%%EOF"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := append([]byte(tt.prefix), tt.pdf...)
			data = append(data, tt.suffix...)

			var out bytes.Buffer
			if err := ExtractAZW4PDFContext(context.Background(), &out, bytes.NewReader(data), int64(len(data))); err != nil {
				t.Fatalf("ExtractAZW4PDF: %v", err)
			}
			if !bytes.Equal(out.Bytes(), tt.pdf) {
				t.Fatalf("ExtractAZW4PDF bytes = %q; want %q", out.Bytes(), tt.pdf)
			}
		})
	}
}

func TestExtractAZW4PDFNoEmbeddedPDF(t *testing.T) {
	data := []byte("azw4 wrapper without pdf")
	var out bytes.Buffer
	err := ExtractAZW4PDFContext(context.Background(), &out, bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, ErrAZW4PDFNotFound) {
		t.Fatalf("ExtractAZW4PDF error = %v; want ErrAZW4PDFNotFound", err)
	}
}

func TestHasAZW4PDF(t *testing.T) {
	withPDF := []byte("prefix%PDF-1.7\nbody\n%%EOFsuffix")
	if !HasAZW4PDF(bytes.NewReader(withPDF), int64(len(withPDF))) {
		t.Fatalf("HasAZW4PDF = false; want true for embedded PDF markers")
	}
	withoutPDF := []byte("azw4 wrapper without pdf")
	if HasAZW4PDF(bytes.NewReader(withoutPDF), int64(len(withoutPDF))) {
		t.Fatalf("HasAZW4PDF = true; want false without embedded PDF markers")
	}
}

func TestExtractAZW4PDFContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := []byte("%PDF-1.7\nbody\n%%EOF")
	var out bytes.Buffer

	err := ExtractAZW4PDFContext(ctx, &out, bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExtractAZW4PDFContext error = %v; want context.Canceled", err)
	}
	if out.Len() != 0 {
		t.Fatalf("ExtractAZW4PDFContext wrote %d bytes after cancellation", out.Len())
	}
}
