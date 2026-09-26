package format

import (
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
		{"polka beats stale calibre", "History", `<meta name="calibre:user_metadata:#genre" content='{"#value#":["Fiction"]}'/><meta name="polka:tags" content='["Read"]'/>`, []string{"History"}, []string{"Read"}},
		{"polka clear", "", `<meta name="polka:tags" content='[]'/><meta property="calibre:user_metadata">{"#genre":{"#value#":["Fiction"]},"#extra_tags":{"#value#":["Read"]}}</meta>`, nil, nil},
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
	const custom = `<meta name="calibre:user_metadata:#genre" content='{"#value#":["Old"],"label":"Genre"}'/>`
	for _, version := range []string{"2.0", "3.0"} {
		t.Run(version, func(t *testing.T) {
			src := testWritebackEPUB(t, []testWritebackEntry{
				{name: "META-INF/container.xml", data: []byte(testWritebackContainer("content.opf"))},
				{name: "content.opf", data: []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="` + version + `"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Book</dc:title><dc:subject>Old</dc:subject>` + custom + `</metadata></package>`)},
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
				got, err := ParseOPF(strings.NewReader(opf))
				if err != nil || !slices.Equal(got.Genres, meta.Genres) || !slices.Equal(got.Tags, meta.Tags) {
					t.Fatalf("roundtrip = %+v, %v; want %+v", got, err, meta)
				}
				src = out
			}
		})
	}
}
