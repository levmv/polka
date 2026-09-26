package db

import (
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
)

func TestRenameTagQuery(t *testing.T) {
	for _, tc := range []struct {
		kind                   TagKind
		query, old, name, want string
	}{
		{TagKindTag, `title:Книга tag:"SCI-FI" status:reading`, "Sci-Fi", "Science Fiction", `title:Книга tag:"Science Fiction" status:reading`},
		{TagKindTag, `tag:"Sci-Fi" tag:"sci-fi"`, "Sci-Fi", `He said "yes"`, `tag:"He said ""yes""" tag:"He said ""yes"""`},
		{TagKindTag, `tag:sci-fi tag:"Other" "tag:sci-fi"`, "Sci-Fi", "New", `tag:sci-fi tag:"Other" "tag:sci-fi"`},
		{TagKindGenre, `genre:"Sci-Fi" genre:="Sci-Fi" tag:"Sci-Fi" genre:Sci-Fi`, "Sci-Fi", "New", `genre:"New" genre:="New" tag:"Sci-Fi" genre:Sci-Fi`},
	} {
		got, changed := renameTagQuery(tc.query, tc.kind, map[string]string{bookmeta.TagKey(tc.old): tc.name})
		if got != tc.want || changed != (tc.query != tc.want) {
			t.Errorf("rename %q = %q, %v; want %q", tc.query, got, changed, tc.want)
		}
	}
}
