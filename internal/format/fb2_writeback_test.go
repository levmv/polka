package format

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/levmv/polka/internal/bookmeta"
)

const fb2WritebackSample = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
<description>
  <title-info>
    <genre>sf</genre>
    <author><first-name>Old</first-name><last-name>Name</last-name></author>
    <book-title>Old Title</book-title>
    <coverpage><image l:href="#cover.jpg"/></coverpage>
    <lang>en</lang>
  </title-info>
  <document-info>
    <author><nickname>scanner</nickname></author>
    <program-used>SomeTool</program-used>
    <date value="2020-01-01">2020</date>
    <id>abc-123</id>
    <version>1.0</version>
  </document-info>
  <publish-info>
    <publisher>Old Publisher</publisher>
  </publish-info>
</description>
<body><section><p>Hello.</p></section></body>
<binary id="cover.jpg" content-type="image/jpeg">/9j/AAA=</binary>
</FictionBook>`

func newMetaSnapshot() bookmeta.Metadata {
	return bookmeta.Metadata{
		Title:       "New Title",
		Authors:     []bookmeta.AuthorMeta{{Name: "Jane Roe", SortName: "Roe, Jane"}},
		Language:    "ru",
		Publisher:   "New Publisher",
		Series:      "Chronicles",
		SeriesIndex: 2,
		Tags:        []string{"Fantasy"},
		Description: "A grand tale.",
		Date:        "2021",
		Identifier:  "isbn:9780306406157",
	}
}

func rewriteFB2(t *testing.T, src []byte, meta bookmeta.Metadata) []byte {
	t.Helper()
	out, err := RewriteFB2Metadata(src, meta)
	if err != nil {
		t.Fatalf("RewriteFB2Metadata: %v", err)
	}
	return out
}

func extractFB2(t *testing.T, raw []byte) *Metadata {
	t.Helper()
	meta, err := ExtractFB2Metadata(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("ExtractFB2Metadata: %v", err)
	}
	return meta
}

func TestRewriteFB2MetadataRoundTrip(t *testing.T) {
	meta := newMetaSnapshot()
	out := rewriteFB2(t, []byte(fb2WritebackSample), meta)

	got := extractFB2(t, out)
	if got.Title != "New Title" {
		t.Errorf("title = %q; want New Title", got.Title)
	}
	if len(got.Authors) != 1 || got.Authors[0].Name != "Jane Roe" || got.Authors[0].SortName != "Roe, Jane" {
		t.Errorf("authors = %+v; want one Jane Roe / Roe, Jane", got.Authors)
	}
	if got.Language != "ru" {
		t.Errorf("language = %q; want ru", got.Language)
	}
	if got.Publisher != "New Publisher" {
		t.Errorf("publisher = %q; want New Publisher", got.Publisher)
	}
	if got.Series != "Chronicles" || got.SeriesIndex != 2 {
		t.Errorf("series = %q/%v; want Chronicles/2", got.Series, got.SeriesIndex)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "Fantasy" {
		t.Errorf("tags = %v; want [Fantasy]", got.Tags)
	}
	if got.Description != "A grand tale." {
		t.Errorf("description = %q; want A grand tale.", got.Description)
	}
	if got.Date != "2021" {
		t.Errorf("date = %q; want 2021", got.Date)
	}
	if !strings.Contains(got.Identifier, "9780306406157") {
		t.Errorf("identifier = %q; want it to contain the ISBN", got.Identifier)
	}
}

func TestRewriteFB2MetadataPreservesContent(t *testing.T) {
	out := rewriteFB2(t, []byte(fb2WritebackSample), newMetaSnapshot())
	text := string(out)

	for _, want := range []string{
		"<body><section><p>Hello.</p></section></body>",                      // body untouched
		`<binary id="cover.jpg" content-type="image/jpeg">/9j/AAA=</binary>`, // cover binary untouched
		"<program-used>SomeTool</program-used>",                              // document-info preserved
		"<id>abc-123</id>",                                                   // document-info preserved
		`l:href="#cover.jpg"`,                                                // cover reference preserved
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing preserved fragment %q\n---\n%s", want, text)
		}
	}
	// The stale title-info and publish-info values must be gone.
	if strings.Contains(text, "Old Title") || strings.Contains(text, "Old Publisher") {
		t.Errorf("output still carries stale metadata:\n%s", text)
	}
}

func TestRewriteFB2MetadataIdempotent(t *testing.T) {
	first := rewriteFB2(t, []byte(fb2WritebackSample), newMetaSnapshot())
	second := rewriteFB2(t, first, newMetaSnapshot())
	if !bytes.Equal(first, second) {
		t.Fatalf("second pass differs from first:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestRewriteFB2MetadataDropsCoverpageWhenAbsent(t *testing.T) {
	src := strings.Replace(fb2WritebackSample, `    <coverpage><image l:href="#cover.jpg"/></coverpage>`+"\n", "", 1)
	out := rewriteFB2(t, []byte(src), newMetaSnapshot())
	if strings.Contains(string(out), "<coverpage>") {
		t.Errorf("coverpage synthesized where source had none:\n%s", out)
	}
}

func TestRewriteFB2MetadataIgnoresDescriptionLiteralsInComments(t *testing.T) {
	for _, tt := range []struct {
		name        string
		old         string
		replacement string
		marker      string
	}{
		{
			name:        "opening tag",
			old:         "<description>",
			replacement: "<!-- misleading <description> in comment -->\n<description>",
			marker:      "<!-- misleading <description> in comment -->",
		},
		{
			name:        "closing tag",
			old:         "<program-used>SomeTool</program-used>",
			replacement: "<!-- misleading </description> in comment --><program-used>SomeTool</program-used>",
			marker:      "<!-- misleading </description> in comment -->",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := strings.Replace(fb2WritebackSample, tt.old, tt.replacement, 1)
			out := rewriteFB2(t, []byte(src), newMetaSnapshot())

			if got := extractFB2(t, out).Title; got != "New Title" {
				t.Fatalf("title = %q; want New Title", got)
			}
			if !bytes.Contains(out, []byte(tt.marker)) {
				t.Fatalf("comment was not preserved:\n%s", out)
			}
		})
	}
}

func windows1251Sample(t *testing.T) []byte {
	t.Helper()
	utf8 := strings.Replace(fb2WritebackSample, `encoding="utf-8"`, `encoding="windows-1251"`, 1)
	utf8 = strings.Replace(utf8, "<p>Hello.</p>", "<p>Привет.</p>", 1)
	encoded, err := charmap.Windows1251.NewEncoder().Bytes([]byte(utf8))
	if err != nil {
		t.Fatalf("encode windows-1251 sample: %v", err)
	}
	return encoded
}

func TestRewriteFB2MetadataKeepsRepresentableEncoding(t *testing.T) {
	src := windows1251Sample(t)
	meta := newMetaSnapshot()
	meta.Title = "Война и мир" // representable in windows-1251
	out := rewriteFB2(t, src, meta)

	if !bytes.Contains(out, []byte(`encoding="windows-1251"`)) {
		t.Errorf("declaration should stay windows-1251:\n% x", out[:80])
	}
	// The Cyrillic body bytes must be spliced through untouched.
	bodyBytes, err := charmap.Windows1251.NewEncoder().Bytes([]byte("<p>Привет.</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, bodyBytes) {
		t.Errorf("windows-1251 body bytes not preserved verbatim")
	}
	if got := extractFB2(t, out); got.Title != "Война и мир" {
		t.Errorf("title = %q; want Война и мир", got.Title)
	}
}

func TestRewriteFB2MetadataConvertsWhenUnrepresentable(t *testing.T) {
	src := windows1251Sample(t)
	meta := newMetaSnapshot()
	meta.Title = "中文标题" // not representable in windows-1251
	out := rewriteFB2(t, src, meta)

	if !bytes.Contains(out, []byte(`encoding="utf-8"`)) {
		t.Errorf("declaration should convert to utf-8:\n% x", out[:80])
	}
	if bytes.Contains(out, []byte(`encoding="windows-1251"`)) {
		t.Errorf("stale windows-1251 declaration left behind")
	}
	got := extractFB2(t, out)
	if got.Title != "中文标题" {
		t.Errorf("title = %q; want 中文标题", got.Title)
	}
	// The body must have survived the whole-document conversion.
	if got.Language != "ru" {
		t.Errorf("language = %q; want ru (document intact)", got.Language)
	}
}

func TestRewriteFB2MetadataZipContainer(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	book, _ := zw.Create("book.fb2")
	book.Write([]byte(fb2WritebackSample))
	extra, _ := zw.Create("extra.txt")
	extra.Write([]byte("keep me"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	out := rewriteFB2(t, buf.Bytes(), newMetaSnapshot())

	// The archive still opens and the other entry is intact.
	zr, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("reopen rewritten zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == "extra.txt" {
			data, err := readZipFileLimited(f, int64(len("keep me")))
			if err != nil {
				t.Fatalf("read preserved extra.txt: %v", err)
			}
			if string(data) != "keep me" {
				t.Errorf("extra.txt = %q; want keep me", data)
			}
		}
	}
	if !names["book.fb2"] || !names["extra.txt"] {
		t.Fatalf("missing entries after repack: %v", names)
	}

	if got := extractFB2(t, out); got.Title != "New Title" {
		t.Errorf("zip title = %q; want New Title", got.Title)
	}

	second := rewriteFB2(t, out, newMetaSnapshot())
	if !bytes.Equal(out, second) {
		t.Errorf("zip write-back is not idempotent")
	}
}

func TestRewriteFB2MetadataGzipContainer(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Name = "book.fb2"
	if _, err := gw.Write([]byte(fb2WritebackSample)); err != nil {
		t.Fatalf("write gzip source: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip source: %v", err)
	}

	out := rewriteFB2(t, buf.Bytes(), newMetaSnapshot())

	if got := extractFB2(t, out); got.Title != "New Title" {
		t.Errorf("gzip title = %q; want New Title", got.Title)
	}

	gr, err := gzip.NewReader(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("open rewritten gzip: %v", err)
	}
	body, err := io.ReadAll(gr)
	closeErr := gr.Close()
	if err != nil {
		t.Fatalf("read rewritten gzip: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close rewritten gzip: %v", closeErr)
	}
	if !bytes.Contains(body, []byte("<body><section><p>Hello.</p></section></body>")) {
		t.Fatalf("gzip body fragment missing:\n%s", body)
	}
}

func TestRewriteFB2MetadataMissingDescription(t *testing.T) {
	_, err := RewriteFB2Metadata([]byte(`<?xml version="1.0"?><FictionBook><body/></FictionBook>`), newMetaSnapshot())
	if err == nil {
		t.Fatal("expected an error when <description> is absent")
	}
}
