package storage

import (
	"testing"
)

func TestDefaultBookPath(t *testing.T) {
	tests := []struct {
		name       string
		title      string
		author     string
		authorSort string
		assetID    string
		ext        string
		want       string
	}{
		{
			name:       "Latin basic",
			title:      "Foundation",
			author:     "Isaac Asimov",
			authorSort: "Asimov, Isaac",
			assetID:    "a_01HX9K9Q",
			ext:        "epub",
			want:       "A/Asimov, Isaac/Foundation [a_01HX9K9Q].epub",
		},
		{
			name:       "Cyrillic basic",
			title:      "Мастер и Маргарита",
			author:     "Михаил Булгаков",
			authorSort: "Булгаков, Михаил",
			assetID:    "a_01HX9M3D",
			ext:        ".epub", // with dot
			want:       "Б/Булгаков, Михаил/Мастер и Маргарита [a_01HX9M3D].epub",
		},
		{
			name:       "Cyrillic Yo bucket",
			title:      "Тест",
			author:     "Пётр Ёлкин",
			authorSort: "Ёлкин, Пётр",
			assetID:    "a_yo",
			ext:        "epub",
			want:       "Ё/Ёлкин, Пётр/Тест [a_yo].epub",
		},
		{
			name:       "Digits bucket",
			title:      "1984",
			author:     "100 Authors",
			authorSort: "100 Authors",
			assetID:    "a_111",
			ext:        "pdf",
			want:       "0-9/100 Authors/1984 [a_111].pdf",
		},
		{
			name:       "Other bucket",
			title:      "Test",
			author:     "Ümlaut",
			authorSort: "Ümlaut",
			assetID:    "a_222",
			ext:        "txt",
			want:       "_Other/Ümlaut/Test [a_222].txt",
		},
		{
			name:       "Unknown bucket (empty author)",
			title:      "Some Book",
			author:     "",
			authorSort: "",
			assetID:    "a_01HX9ZZY",
			ext:        "epub",
			want:       "_Unknown/Unknown Author/Some Book [a_01HX9ZZY].epub",
		},
		{
			name:       "Empty title uses canonical fallback",
			title:      "",
			author:     "Ada Writer",
			authorSort: "Writer, Ada",
			assetID:    "a_untitled",
			ext:        "epub",
			want:       "W/Writer, Ada/Untitled [a_untitled].epub",
		},
		{
			name:       "Unknown bucket (explicit Unknown)",
			title:      "Some Book",
			author:     "Unknown Author",
			authorSort: "Unknown Author",
			assetID:    "a_01HX9ZZY",
			ext:        "epub",
			want:       "_Unknown/Unknown Author/Some Book [a_01HX9ZZY].epub",
		},
		{
			name:       "Unknown bucket (literal alias)",
			title:      "Some Book",
			author:     "Unknown",
			authorSort: "Unknown",
			assetID:    "a_unknown",
			ext:        "epub",
			want:       "_Unknown/Unknown/Some Book [a_unknown].epub",
		},
		{
			name:       "Sanitization unsafe characters",
			title:      "Title <with> :bad/chars\\|?",
			author:     "Author *Name\"",
			authorSort: "Name, Author*",
			assetID:    "a_333",
			ext:        "mobi",
			want:       "N/Name, Author/Title with badchars [a_333].mobi",
		},
		{
			name:       "Sanitization collapse spaces",
			title:      "Title   With \t Spaces",
			author:     "Author \n Name",
			authorSort: "Name,   Author",
			assetID:    "a_444",
			ext:        "epub",
			want:       "N/Name, Author/Title With Spaces [a_444].epub",
		},
		{
			name:       "Empty extension",
			title:      "Plain Text",
			author:     "Ada Writer",
			authorSort: "Writer, Ada",
			assetID:    "a_noext",
			ext:        "",
			want:       "W/Writer, Ada/Plain Text [a_noext]",
		},
		{
			name:       "Missing author sort derives from author",
			title:      "Plain Text",
			author:     "Ada Writer",
			authorSort: "",
			assetID:    "a_sort",
			ext:        "epub",
			want:       "W/Writer, Ada/Plain Text [a_sort].epub",
		},
		{
			name:       "Leading dot in title is stripped",
			title:      ".NET Core in Action",
			author:     "Dustin Metzgar",
			authorSort: "Metzgar, Dustin",
			assetID:    "a_net",
			ext:        "epub",
			want:       "M/Metzgar, Dustin/NET Core in Action [a_net].epub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BookPath(DefaultBookPathTemplate, BookPathData{
				Title:      tt.title,
				Author:     tt.author,
				AuthorSort: tt.authorSort,
				AssetID:    tt.assetID,
				Ext:        tt.ext,
			})
			if err != nil {
				t.Fatalf("BookPath: %v", err)
			}
			if got != tt.want {
				t.Errorf("BookPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderBookPathTemplate(t *testing.T) {
	data := BookPathData{
		Title:       "Dune Messiah",
		SortTitle:   "Dune Messiah",
		Author:      "Frank Herbert",
		AuthorSort:  "Herbert, Frank",
		Series:      "Dune",
		SeriesIndex: "02",
		AssetID:     "a_123",
		WorkID:      "w_123",
		Ext:         ".EPUB",
	}
	got, err := RenderBookPathTemplate(
		"books/{author_bucket}/{author_sort}/{series}/{series_index} - {title} [{asset_id}]{dot_ext}",
		data,
	)
	if err != nil {
		t.Fatalf("RenderBookPathTemplate: %v", err)
	}
	want := "books/H/Herbert, Frank/Dune/02 - Dune Messiah [a_123].epub"
	if got != want {
		t.Fatalf("path = %q; want %q", got, want)
	}
}

func TestRenderBookPathTemplateFallbacks(t *testing.T) {
	got, err := RenderBookPathTemplate("books/{series}/{series_index|Standalone}/{title}{dot_ext}", BookPathData{
		Title:  "Loose Book",
		Author: "Someone",
		Ext:    "pdf",
	})
	if err != nil {
		t.Fatalf("RenderBookPathTemplate: %v", err)
	}
	want := "books/_No Series/Standalone/Loose Book.pdf"
	if got != want {
		t.Fatalf("path = %q; want %q", got, want)
	}
}

func TestRenderBookPathTemplateSanitizesSegments(t *testing.T) {
	got, err := RenderBookPathTemplate("books/{author_sort}/{title}{dot_ext}", BookPathData{
		Title:      `Title <with> :bad/chars\|?`,
		AuthorSort: `Name, Author*`,
		Ext:        "epub",
	})
	if err != nil {
		t.Fatalf("RenderBookPathTemplate: %v", err)
	}
	want := "books/Name, Author/Title with badchars.epub"
	if got != want {
		t.Fatalf("path = %q; want %q", got, want)
	}
}

// TestRenderBookPathTemplateStripsLeadingDots locks in that no rendered segment
// begins with a dot: RootLooksEmpty ignores dot-entries, so a dot-leading
// top-level segment would make a populated library read as empty, and a literal
// ".staging" segment would land books inside the staging area.
func TestRenderBookPathTemplateStripsLeadingDots(t *testing.T) {
	tests := []struct {
		name     string
		template string
		data     BookPathData
		want     string
	}{
		{
			name:     "dot-leading title segment",
			template: "{title}{dot_ext}",
			data:     BookPathData{Title: ".NET Core in Action", Ext: "epub"},
			want:     "NET Core in Action.epub",
		},
		{
			name:     "multiple leading dots and space",
			template: "{title}{dot_ext}",
			data:     BookPathData{Title: ". . .And Then", Ext: "epub"},
			want:     "And Then.epub",
		},
		{
			name:     "literal .staging segment cannot capture books",
			template: ".staging/{title}{dot_ext}",
			data:     BookPathData{Title: "Book", Ext: "epub"},
			want:     "staging/Book.epub",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderBookPathTemplate(tt.template, tt.data)
			if err != nil {
				t.Fatalf("RenderBookPathTemplate(%q): %v", tt.template, err)
			}
			if got != tt.want {
				t.Errorf("RenderBookPathTemplate(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}

// TestRenderBookPathTemplateRejectsAllDotsSegment locks in that a segment made
// only of dots trips the "rendered empty" guard after sanitizePathSegment
// strips its leading dots.
func TestRenderBookPathTemplateRejectsAllDotsSegment(t *testing.T) {
	for _, title := range []string{".", "..", "..."} {
		if got, err := RenderBookPathTemplate("{author_sort}/{title}", BookPathData{AuthorSort: "A", Title: title}); err == nil {
			t.Fatalf("RenderBookPathTemplate(title %q) = %q, want error", title, got)
		}
	}
}

func TestRenderBookPathTemplateRejectsInvalidTemplates(t *testing.T) {
	tests := []struct {
		name     string
		template string
	}{
		{name: "absolute", template: "/books/{title}"},
		{name: "parent traversal", template: "books/../{title}"},
		{name: "empty segment", template: "books//{title}"},
		{name: "unknown field", template: "books/{unknown}/{title}"},
		{name: "unmatched open", template: "books/{title"},
		{name: "unmatched close", template: "books/title}"},
		{name: "rendered empty segment", template: "books/{series_index}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := RenderBookPathTemplate(tt.template, BookPathData{Title: "Book"}); err == nil {
				t.Fatalf("RenderBookPathTemplate() = %q, want error", got)
			}
		})
	}
}

// TestRenderBookPathTemplateRelativeToRoot locks in that a rendered path is
// relative to the books root itself: the root *is* the books tree, so the
// template is not prefixed with books/ and literal segments render verbatim.
func TestRenderBookPathTemplateRelativeToRoot(t *testing.T) {
	data := BookPathData{Title: "Book", AuthorSort: "Author, A", AssetID: "a_1", Ext: "epub"}
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "sub-path template",
			template: "{author_sort}/{title} [{asset_id}]{dot_ext}",
			want:     "Author, A/Book [a_1].epub",
		},
		{
			name:     "a literal books segment is no longer special",
			template: "books/{author_sort}/{title} [{asset_id}]{dot_ext}",
			want:     "books/Author, A/Book [a_1].epub",
		},
		{
			name:     "a literal leading segment renders verbatim",
			template: "shelf/{title}{dot_ext}",
			want:     "shelf/Book.epub",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderBookPathTemplate(tt.template, data)
			if err != nil {
				t.Fatalf("RenderBookPathTemplate(%q): %v", tt.template, err)
			}
			if got != tt.want {
				t.Errorf("RenderBookPathTemplate(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}

func TestDetectBookPathCollisions(t *testing.T) {
	got := DetectBookPathCollisions([]BookPathCandidate{
		{AssetID: "a_2", Path: "books/A/Same.epub"},
		{AssetID: "a_1", Path: "books/A/Same.epub"},
		{AssetID: "a_3", Path: "books/B/Other.epub"},
		{AssetID: "a_4", Path: "books/C/Again.epub"},
		{AssetID: "a_5", Path: "books/C/Again.epub"},
	})
	if len(got) != 2 {
		t.Fatalf("collisions = %+v; want 2", got)
	}
	if got[0].Path != "books/A/Same.epub" || got[0].AssetIDs[0] != "a_1" || got[0].AssetIDs[1] != "a_2" {
		t.Fatalf("first collision = %+v", got[0])
	}
	if got[1].Path != "books/C/Again.epub" || got[1].AssetIDs[0] != "a_4" || got[1].AssetIDs[1] != "a_5" {
		t.Fatalf("second collision = %+v", got[1])
	}
}
