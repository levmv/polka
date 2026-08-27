package format

import (
	"archive/zip"
	"testing"
)

func TestNormalizeZipName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "images\\cover.png", want: "images/cover.png"},
		{name: "./images/../cover.png", want: "cover.png"},
		{name: "/absolute.png", want: ""},
		{name: "../escape.png", want: ""},
		{name: "nested/../../escape.png", want: ""},
		{name: ".", want: ""},
	}
	for _, tt := range tests {
		if got := NormalizeZipName(tt.name); got != tt.want {
			t.Fatalf("NormalizeZipName(%q) = %q; want %q", tt.name, got, tt.want)
		}
	}
}

func TestResolveZIPEntry(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		entries   []string
		want      string
		ambiguous bool
	}{
		{name: "exact wins", requested: "OEBPS/Text.xhtml", entries: []string{"oebps/text.xhtml", "OEBPS/Text.xhtml"}, want: "OEBPS/Text.xhtml"},
		{name: "unique case fallback", requested: "OEBPS/text.xhtml", entries: []string{"OEBPS/Text.xhtml"}, want: "OEBPS/Text.xhtml"},
		{name: "percent lookalike stays distinct", requested: "OEBPS/text.xhtml", entries: []string{"OEBPS/text%2Exhtml"}},
		{name: "unique Unicode fallback", requested: "OEBPS/café.xhtml", entries: []string{"OEBPS/cafe\u0301.xhtml"}, want: "OEBPS/cafe\u0301.xhtml"},
		{name: "ambiguous fallback", requested: "OEBPS/text.xhtml", entries: []string{"OEBPS/Text.xhtml", "oebps/text.xhtml"}, ambiguous: true},
		{name: "duplicate exact", requested: "OEBPS/text.xhtml", entries: []string{"OEBPS/text.xhtml", "OEBPS/text.xhtml"}, want: "OEBPS/text.xhtml", ambiguous: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zr := &zip.Reader{}
			for _, name := range tt.entries {
				zr.File = append(zr.File, &zip.File{Name: name})
			}
			file, ambiguous := ResolveZIPEntry(zr, tt.requested)
			if ambiguous != tt.ambiguous {
				t.Fatalf("ResolveZIPEntry ambiguity = %v; want %v", ambiguous, tt.ambiguous)
			}
			if tt.want == "" {
				if file != nil {
					t.Fatalf("ResolveZIPEntry file = %q; want nil", file.Name)
				}
				return
			}
			if file == nil || file.Name != tt.want {
				got := "<nil>"
				if file != nil {
					got = file.Name
				}
				t.Fatalf("ResolveZIPEntry file = %q; want %q", got, tt.want)
			}
		})
	}
}
