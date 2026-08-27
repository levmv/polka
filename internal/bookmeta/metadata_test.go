package bookmeta

import "testing"

func TestAuthorSort(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Isaac Asimov", "Asimov, Isaac"},
		{"Unknown Author", "Unknown Author"},
		{"Михаил Булгаков", "Булгаков, Михаил"},
		{"Cher", "Cher"},
		{"Asimov, Isaac", "Asimov, Isaac"},
		{"Ursula K. Le Guin", "Guin, Ursula K. Le"},
		{"Don Team Smith", "Don Team Smith"},
		{"Acme Inc.", "Acme Inc."},
		{"Mrs. Jane Q. Doe III", "Doe, Jane Q. III"},
		{"John [x] von Neumann (III)", "Neumann, John von"},
		{"James Wesley, Rawles", "James Wesley, Rawles"},
		{"Seventh Author [7]", "Author, Seventh"},
	}

	for _, tc := range tests {
		got := AuthorSort(tc.in)
		if got != tc.want {
			t.Errorf("AuthorSort(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestAuthorSortHeuristicSets(t *testing.T) {
	for _, word := range []string{"company", "corp", "foundation", "llc", "press", "team", "university"} {
		if _, ok := authorNameCopyWords[word]; !ok {
			t.Fatalf("authorNameCopyWords missing %q", word)
		}
	}
	for _, word := range []string{"dr", "mrs.", "prof", "sir"} {
		if _, ok := authorNamePrefixes[word]; !ok {
			t.Fatalf("authorNamePrefixes missing %q", word)
		}
	}
	for _, word := range []string{"jr.", "phd", "iii", "v", "senior"} {
		if _, ok := authorNameSuffixes[word]; !ok {
			t.Fatalf("authorNameSuffixes missing %q", word)
		}
	}
}

func TestParseAuthorList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "semicolon separated",
			in:   "Ursula K. Le Guin; Terry Pratchett",
			want: []string{"Ursula K. Le Guin", "Terry Pratchett"},
		},
		{
			name: "calibre ampersand separated",
			in:   "Ursula K. Le Guin & Terry Pratchett",
			want: []string{"Ursula K. Le Guin", "Terry Pratchett"},
		},
		{
			name: "comma kept inside name",
			in:   "Le Guin, Ursula K.; Pratchett, Terry",
			want: []string{"Le Guin, Ursula K.", "Pratchett, Terry"},
		},
		{
			name: "literal ampersand",
			in:   "AT&T Labs; Research && Development",
			want: []string{"AT&T Labs", "Research & Development"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAuthorList(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseAuthorList(%q) = %#v; want %#v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ParseAuthorList(%q) = %#v; want %#v", tt.in, got, tt.want)
				}
			}
		})
	}
}

func TestFormatAuthorListEscapesAmpersand(t *testing.T) {
	names := []string{"Le Guin, Ursula K.", "Research & Development"}
	formatted := FormatAuthorList(names)
	if formatted != "Le Guin, Ursula K.; Research && Development" {
		t.Fatalf("FormatAuthorList = %q", formatted)
	}

	parsed := ParseAuthorList(formatted)
	if len(parsed) != 2 || parsed[0] != names[0] || parsed[1] != names[1] {
		t.Fatalf("roundtrip = %#v; want %#v", parsed, names)
	}
}
