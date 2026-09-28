package mobi

import (
	"bytes"
	"context"
	"errors"
	"io"
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
			if err := ExtractPDF(context.Background(), &out, bytes.NewReader(data), int64(len(data))); err != nil {
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
	err := ExtractPDF(context.Background(), &out, bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, ErrPDFNotFound) {
		t.Fatalf("ExtractAZW4PDF error = %v; want ErrPDFNotFound", err)
	}
}

func TestExtractAZW4PDFRejectsShortRead(t *testing.T) {
	data := []byte("%PDF-1.7\nbody\n%%EOF")
	var out bytes.Buffer
	err := ExtractPDF(t.Context(), &out, bytes.NewReader(data), int64(len(data)+1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("ExtractPDF error = %v; want source read error", err)
	}
	if out.Len() != 0 {
		t.Fatalf("ExtractPDF wrote %d bytes after a short source read", out.Len())
	}
}

func TestExtractAZW4PDFContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := []byte("%PDF-1.7\nbody\n%%EOF")
	var out bytes.Buffer

	err := ExtractPDF(ctx, &out, bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExtractPDF error = %v; want context.Canceled", err)
	}
	if out.Len() != 0 {
		t.Fatalf("ExtractPDF wrote %d bytes after cancellation", out.Len())
	}
}
