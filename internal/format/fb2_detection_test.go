package format

import (
	"bytes"
	"io"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestDetectLargeFB2Gzip(t *testing.T) {
	data := testfixture.FB2Gzip(t, 17<<20)
	r := &formatReadCounter{r: bytes.NewReader(data)}
	got := DetectFormat("large.fb2.gz", r, int64(len(data)))
	if got != FormatFB2 {
		t.Fatalf("large gzip format=%v", got)
	}
	if r.bytes > maxFB2GzipProbeBytes || r.calls > 8 {
		t.Fatalf("gzip recognition read beyond its prefix: %d reads/%d bytes", r.calls, r.bytes)
	}
	t.Logf("%d-byte gzip: %d reads/%d bytes", len(data), r.calls, r.bytes)
}

func TestDetectFB2GzipPrefix(t *testing.T) {
	longHeader := testFB2Gzip(t, []byte(`<FictionBook/>`))
	longHeader[3] |= 4 // FEXTRA, followed by a legal 65,535-byte optional field.
	extraHeader := append(bytes.Clone(longHeader[:10]), 0xff, 0xff)
	extraHeader = append(extraHeader, make([]byte, 65535)...)
	longHeader = append(extraHeader, longHeader[10:]...)
	for _, tt := range []struct {
		name       string
		data       []byte
		recognized bool
	}{
		{"repairable prolog", testFB2Gzip(t, []byte("\x00<?xml version=\"1.0\" encoding=\"utf-8\"?><!--\xcf\xf0--><FictionBook title=\"A & B\"/>")), true},
		{"different XML root", testFB2Gzip(t, []byte(`<html><FictionBook/></html>`)), false},
		{"plain gzip text", testFB2Gzip(t, []byte(`plain text`)), false},
		{"truncated gzip header", []byte{0x1f, 0x8b}, false},
		{"XML prefix limit", testFB2Gzip(t, append(bytes.Repeat([]byte(" "), maxFB2GzipProbeBytes+1), []byte(`<FictionBook/>`)...)), false},
		{"compressed prefix limit", longHeader, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &formatReadCounter{r: bytes.NewReader(tt.data)}
			got := DetectFormat("book.fb2.gz", r, int64(len(tt.data)))
			kind := FormatUnknown
			if tt.recognized {
				kind = FormatFB2
			}
			if got != kind {
				t.Fatalf("gzip prefix format=%v; want %v", got, kind)
			}
			if r.bytes > maxFB2GzipProbeBytes || r.calls > 32 {
				t.Fatalf("gzip prefix exceeded IO bounds: %d reads/%d bytes", r.calls, r.bytes)
			}
		})
	}
}

type formatReadCounter struct {
	r            io.ReaderAt
	calls, bytes int
}

func (r *formatReadCounter) ReadAt(p []byte, off int64) (int, error) {
	r.calls++
	r.bytes += len(p)
	return r.r.ReadAt(p, off)
}
