package format

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestDetectFormatDJVU(t *testing.T) {
	for _, tt := range []struct {
		name     string
		formType string
		want     Format
	}{
		{name: "single.djvu", formType: "DJVU", want: FormatDJVU},
		{name: "multi.djv", formType: "DJVM", want: FormatDJVU},
		{name: "shared.djvu", formType: "DJVI", want: FormatUnknown},
		{name: "thumbs.djvu", formType: "THUM", want: FormatUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := testfixture.MinimalDJVU(tt.formType)
			r := bytes.NewReader(data)
			if got := DetectFormat(tt.name, r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFormatDJVURejectsExtensionOnlyFiles(t *testing.T) {
	for _, name := range []string{"scan.djvu", "scan.djv"} {
		t.Run(name, func(t *testing.T) {
			data := []byte("not a djvu")
			r := bytes.NewReader(data)
			if got := DetectFormat(name, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}

func TestExtractDJVUMetadata(t *testing.T) {
	data := testDJVUForm("DJVU",
		testDJVUChunk("ANTa", []byte(`(metadata
  (title "DjVu \"Quoted\" Title")
  (author "Ada Lovelace")
  (publisher "DjVu Press")
  (language "eng")
  (date "1843-01-02")
  (subject "Math; Engines; math")
  (keywords "Scans, Math")
  (isbn "978-0-306-40615-7")
)`)),
		testDJVUChunk("ANTa", []byte(`(metadata (title "Later Page Title"))`)),
	)
	r := bytes.NewReader(data)
	meta, err := ExtractDJVUMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDJVUMetadata: %v", err)
	}
	if meta.Title != `DjVu "Quoted" Title` {
		t.Fatalf("Title = %q; want quoted title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Ada Lovelace" || meta.Authors[0].SortName != "Lovelace, Ada" {
		t.Fatalf("Authors = %+v; want Ada Lovelace with sort name", meta.Authors)
	}
	if meta.Publisher != "DjVu Press" || meta.Language != "en" || meta.Date != "1843-01-02" {
		t.Fatalf("Metadata = %+v; want publisher/language/date", meta)
	}
	wantTags := []string{"Math", "Engines", "Scans"}
	if !equalStrings(meta.Tags, wantTags) {
		t.Fatalf("Tags = %+v; want %+v", meta.Tags, wantTags)
	}
	if meta.Identifier != "isbn:978-0-306-40615-7" {
		t.Fatalf("Identifier = %q; want isbn", meta.Identifier)
	}
}

func TestExtractDJVUMetadataFromNestedMultipageForm(t *testing.T) {
	page := testDJVUFormChunk("DJVU", testDJVUChunk("ANTa", []byte(`(metadata (title "Nested Page Title"))`)))
	data := testDJVUForm("DJVM", page)
	r := bytes.NewReader(data)
	meta, err := ExtractDJVUMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDJVUMetadata: %v", err)
	}
	if meta.Title != "Nested Page Title" {
		t.Fatalf("Title = %q; want nested page title", meta.Title)
	}
}

func testDJVUForm(formType string, chunks ...[]byte) []byte {
	return append([]byte("AT&T"), testDJVUFormChunk(formType, chunks...)...)
}

func testDJVUFormChunk(formType string, chunks ...[]byte) []byte {
	payload := []byte(formType)
	for _, chunk := range chunks {
		payload = append(payload, chunk...)
	}
	return testDJVUChunk("FORM", payload)
}

func testDJVUChunk(id string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload)+1)
	copy(out[:4], id)
	binary.BigEndian.PutUint32(out[4:8], uint32(len(payload)))
	out = append(out, payload...)
	if len(payload)%2 != 0 {
		out = append(out, 0)
	}
	return out
}
