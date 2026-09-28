package format

import (
	"encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
)

func TestEPUBClassificationWritebackRoundTrip(t *testing.T) {
	const custom = `<meta name="foreign:note" content="Keep this record"/>`
	const columns = `<meta name="calibre:user_metadata:#genre" content='{"#value#":["Old"],"label":"Genre"}'/>
<meta name="calibre:user_metadata:#pagecount" content='{"#value#":123,"datatype":"int"}'/>
<meta property="calibre:user_metadata">{"#genre":{"#value#":["Old"]},"#extra_tags":{"#value#":["Old tag"]},"#note":{"#value#":"Keep this column","datatype":"text"}}</meta>`
	for _, version := range []string{"2.0", "3.0"} {
		t.Run(version, func(t *testing.T) {
			src := testWritebackEPUB(t, []testWritebackEntry{
				{name: "META-INF/container.xml", data: []byte(testWritebackContainer("content.opf"))},
				{name: "content.opf", data: []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="` + version + `"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Book</dc:title><dc:subject>Old</dc:subject>` + columns + custom + `</metadata></package>`)},
			})
			for _, meta := range []Metadata{
				{Title: "Book", Genres: []string{"History", "Фантастика.Научная фантастика"}, Tags: []string{"History", `Reading.Read & "reviewed"`}},
				{Title: "Book", Tags: []string{"Keep"}},
				{Title: "Book", Genres: []string{"History"}},
				{Title: "Book"},
			} {
				out, err := RewriteEPUBMetadata(src, meta, time.Time{})
				if err != nil {
					t.Fatal(err)
				}
				opf := testZipEntryString(t, out, "content.opf")
				if !strings.Contains(opf, custom) {
					t.Fatalf("lost foreign metadata: %s", opf)
				}
				foreign := testOPFCalibreColumns(t, []byte(opf))
				delete(foreign, "#extra_tags")
				wantForeign := map[string]any{
					"#pagecount": map[string]any{"#value#": float64(123), "datatype": "int"},
					"#note":      map[string]any{"#value#": "Keep this column", "datatype": "text"},
				}
				if !reflect.DeepEqual(foreign, wantForeign) {
					t.Fatalf("foreign columns = %v; want %v", foreign, wantForeign)
				}
				got, err := bookmeta.ParseOPF(strings.NewReader(opf))
				if err != nil || !slices.Equal(got.Genres, meta.Genres) || !slices.Equal(got.Tags, meta.Tags) {
					t.Fatalf("roundtrip = %+v, %v; want %+v", got, err, meta)
				}
				src = out
			}
		})
	}
}

func testOPFCalibreColumns(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc opfDoc
	if err := bookmeta.DecodeOPFXML(raw, &doc); err != nil {
		t.Fatal(err)
	}
	columns := make(map[string]any)
	for _, meta := range doc.Metadata.Meta {
		if key, ok := strings.CutPrefix(meta.Name, "calibre:user_metadata:"); ok {
			var value any
			if err := json.Unmarshal([]byte(meta.Content), &value); err != nil {
				t.Fatal(err)
			}
			columns[key] = value
		} else if meta.Property == "calibre:user_metadata" || meta.Name == "calibre:user_metadata" {
			raw := meta.Text
			if meta.Name != "" {
				raw = meta.Content
			}
			if err := json.Unmarshal([]byte(raw), &columns); err != nil {
				t.Fatal(err)
			}
		}
	}
	return columns
}
