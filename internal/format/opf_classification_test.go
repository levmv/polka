package format

import (
	"encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOPFClassification(t *testing.T) {
	for _, tc := range []struct {
		name, subjects, extra string
		genres, tags          []string
	}{
		{"subjects", "Fiction, History", "", []string{"Fiction", "History"}, nil},
		{"calibre columns", "Favourite", `<meta name="calibre:user_metadata:#genre" content='{ "#value#": ["Fiction", "fiction"] }'/><meta name="calibre:user_metadata:#extra_tags" content='{ "#value#": ["Read", "favourite"] }'/>`, []string{"Fiction"}, []string{"Favourite", "Read"}},
		{"calibre aggregate", "Favourite", `<meta property="calibre:user_metadata">{"#genre":{"#value#":"Fiction"},"#extra_tags":{"#value#":["Read"]},"#rating":{"#value#":4}}</meta>`, []string{"Fiction"}, []string{"Favourite", "Read"}},
		{"extra tags only", "Fiction", `<meta name="calibre:user_metadata:#extra_tags" content='{"#value#":["Favourite"]}'/>`, []string{"Fiction"}, []string{"Favourite"}},
		{"empty genre column", "Favourite", `<meta name="calibre:user_metadata:#genre" content='{"#value#":null}'/>`, nil, []string{"Favourite"}},
		{"malformed column", "Fiction", `<meta name="calibre:user_metadata:#genre" content='{"#value#":42}'/>`, []string{"Fiction"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opf := `<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:subject>` + tc.subjects + `</dc:subject>` + tc.extra + `</metadata>`
			meta, err := ParseOPF(strings.NewReader(opf))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(meta.Genres, tc.genres) || !slices.Equal(meta.Tags, tc.tags) {
				t.Fatalf("genres/tags = %v / %v; want %v / %v", meta.Genres, meta.Tags, tc.genres, tc.tags)
			}
		})
	}
}

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
				got, err := ParseOPF(strings.NewReader(opf))
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
	if err := decodeOPFBytes(raw, &doc); err != nil {
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
