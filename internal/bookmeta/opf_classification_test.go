package bookmeta

import (
	"slices"
	"strings"
	"testing"
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
