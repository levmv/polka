package djvu

import (
	"bytes"
	"encoding/binary"
	"os"
	"slices"
	"testing"
)

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
	meta, err := ExtractMetadata(t.Context(), r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
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
	if !slices.Equal(meta.Tags, wantTags) {
		t.Fatalf("Tags = %+v; want %+v", meta.Tags, wantTags)
	}
	if meta.Identifier != "isbn:978-0-306-40615-7" {
		t.Fatalf("Identifier = %q; want isbn", meta.Identifier)
	}
}

func TestExtractDJVUMetadataSharedANTzAndLateXMP(t *testing.T) {
	data, err := os.ReadFile("../../testfixture/metadata.djvu")
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	meta, err := ExtractMetadata(t.Context(), r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
	}
	if meta.Title != "Annotated DjVu" || meta.Language != "en" || meta.Date != "1843" || meta.PageCount != 3 {
		t.Fatalf("shared and last-page metadata: %+v", meta)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Ada Lovelace" || meta.Authors[0].SortName != "Lovelace, Ada" {
		t.Fatalf("Authors = %+v; want XMP author, ignoring scanner Creator", meta.Authors)
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

func TestDjVuPagesExcludeSharedResources(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
		want int
	}{
		{"single", testDJVUForm("DJVU", testDJVUChunk("INFO", []byte{0})), 1},
		{"bundled", testDJVUForm("DJVM", testDJVUFormChunk("DJVI", testDJVUChunk("Djbz", []byte{1})), testDJVUFormChunk("DJVU"), testDJVUFormChunk("DJVU")), 2},
		{"indirect", testDJVUForm("DJVM", testDJVUChunk("DIRM", []byte{0, 0, 3})), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CountPages(bytes.NewReader(tt.raw), int64(len(tt.raw)))
			if err != nil || got != tt.want {
				t.Fatalf("pages = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}
