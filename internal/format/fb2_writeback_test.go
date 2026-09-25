package format

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/levmv/polka/internal/bookmeta"
)

const fb2WritebackSample = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
<description>
  <title-info xmlns:extra="urn:example:metadata">
    <genre>sf</genre>
    <author><first-name>Old</first-name><last-name>Name</last-name><home-page>https://example.org/author</home-page></author>
    <book-title>Old Title</book-title>
    <annotation><p>A <emphasis>short</emphasis> introduction.</p></annotation>
    <date value="1990-01-02">January 1990</date>
    <coverpage><image l:href="#cover.jpg"/></coverpage>
    <lang>en</lang>
    <src-lang>de</src-lang>
    <translator><first-name>Ann</first-name><last-name>Smith</last-name><id>translator-1</id></translator>
    <sequence name="Old series" number="3"/>
    <extra:record>Preserved extension</extra:record>
  </title-info>
  <src-title-info><book-title>Original title</book-title><date>1980</date></src-title-info>
  <document-info>
    <author><nickname>scanner</nickname></author>
    <program-used>SomeTool</program-used>
    <date value="2020-01-01">2020</date>
    <id>abc-123</id>
    <version>1.0</version>
  </document-info>
  <publish-info>
    <publisher>Old Publisher</publisher>
    <city>London</city>
    <year>2000</year>
    <sequence name="Publisher series" number="5"/>
  </publish-info>
  <custom-info info-type="review">Keep</custom-info>
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
		Genres:      []string{"Fantasy"},
		Description: "A grand tale.",
		Date:        "2021",
		Identifier:  "isbn:9780306406157",
		PageCount:   123,
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
	foreign := `<custom-info info-type="bookorbit:page_count">9</custom-info>
  <custom-info info-type="calibre:user_metadata:#pages">20</custom-info>`
	source := bytes.Replace([]byte(fb2WritebackSample), []byte("</description>"), []byte(foreign+"</description>"), 1)
	if got := extractFB2(t, source).PageCount; got != 20 {
		t.Fatalf("fallback count = %d; want Calibre count 20", got)
	}
	source = bytes.Replace(source, []byte("</description>"), []byte(`<custom-info info-type="schema:numberOfPages">55</custom-info></description>`), 1)
	if got := extractFB2(t, source).PageCount; got != 55 {
		t.Fatalf("declared count = %d; want canonical count 55", got)
	}
	out := rewriteFB2(t, source, meta)
	if !bytes.Contains(out, []byte(foreign)) {
		t.Fatalf("writeback changed foreign counts: %s", out)
	}

	got := extractFB2(t, out)
	if !reflect.DeepEqual(*got, meta) {
		t.Errorf("metadata after write-back:\n got %+v\nwant %+v", *got, meta)
	}
	meta.PageCount = 0
	if unknown := extractFB2(t, rewriteFB2(t, out, meta)); unknown.PageCount != got.PageCount {
		t.Fatal("unknown count erased the FB2 declaration")
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
		`<custom-info info-type="review">Keep</custom-info>`,
		`<src-lang>de</src-lang>`,
		`<translator><first-name>Ann</first-name><last-name>Smith</last-name><id>translator-1</id></translator>`,
		`xmlns:extra="urn:example:metadata"`,
		`<extra:record>Preserved extension</extra:record>`,
		`<city>London</city>`,
		`<year>2000</year>`,
		`<sequence name="Publisher series" number="5"/>`,
		`<src-title-info><book-title>Original title</book-title><date>1980</date></src-title-info>`,
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

func TestRewriteFB2MetadataTagOnlyEdit(t *testing.T) {
	src := []byte(fb2WritebackSample)
	meta := *extractFB2(t, src)
	meta.Tags = []string{"New tag"}
	out := rewriteFB2(t, src, meta)
	for _, want := range []string{
		`<author><first-name>Old</first-name><last-name>Name</last-name><home-page>https://example.org/author</home-page></author>`,
		`<annotation><p>A <emphasis>short</emphasis> introduction.</p></annotation>`,
		`<date value="1990-01-02">January 1990</date>`,
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("tag-only edit changed unrelated metadata %q", want)
		}
	}
	if got := extractFB2(t, out); !reflect.DeepEqual(*got, meta) {
		t.Fatalf("metadata after tag-only edit:\n got %+v\nwant %+v", *got, meta)
	}
}

func TestRewriteFB2MetadataDatePrecision(t *testing.T) {
	src := []byte(fb2WritebackSample)
	for _, tt := range []struct{ name, date, xml string }{
		{"year", "2021", `<date>2021</date>`},
		{"month", "2021-06", `<date>2021-06</date>`},
		{"day", "2021-06-15", `<date value="2021-06-15">2021-06-15</date>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			meta := *extractFB2(t, src)
			meta.Date = tt.date
			out := rewriteFB2(t, src, meta)
			if got := extractFB2(t, out).Date; got != tt.date {
				t.Errorf("date after write-back = %q; want %q", got, tt.date)
			}
			if !bytes.Contains(out, []byte(tt.xml)) {
				t.Errorf("date precision not preserved: want %s", tt.xml)
			}
		})
	}
}

func TestRewriteFB2MetadataPreservesDateSources(t *testing.T) {
	const titleDate = `<date value="1990-01-02">January 1990</date>`
	const sourceDate = `<date>1980</date>`
	const printYear = `<year>2000</year>`
	for _, tc := range []struct {
		name, titleDate, sourceDate string
	}{
		{"source date", "", sourceDate},
		{"print year", "", ""},
		{"unrecognized date", `<date>unknown</date>`, sourceDate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(strings.ReplaceAll(strings.ReplaceAll(fb2WritebackSample, titleDate, tc.titleDate), sourceDate, tc.sourceDate))
			meta := *extractFB2(t, src)
			meta.Tags = []string{"New tag"}
			out := rewriteFB2(t, src, meta)
			for _, record := range []string{tc.titleDate, tc.sourceDate, printYear} {
				if record != "" && !bytes.Contains(out, []byte(record)) {
					t.Fatalf("tag edit lost date record %s:\n%s", record, out)
				}
			}
			if got := extractFB2(t, out).Date; got != meta.Date {
				t.Fatalf("date after tag edit = %q; want %q", got, meta.Date)
			}
			// A fallback date must not be copied into a new title-info/date.
			if bytes.Count(out, []byte("<date")) != bytes.Count(src, []byte("<date")) {
				t.Fatalf("tag edit changed the number of date records:\n%s", out)
			}
		})
	}
}

func TestRewriteFB2MetadataClearsFallbacks(t *testing.T) {
	src := []byte(fb2WritebackSample)
	for _, tt := range []struct{ field, preserved string }{
		{"date", `<sequence name="Publisher series" number="5"/>`},
		{"series", `<year>2000</year>`},
	} {
		t.Run(tt.field, func(t *testing.T) {
			meta := *extractFB2(t, src)
			if tt.field == "date" {
				meta.Date = ""
			} else {
				meta.Series, meta.SeriesIndex = "", 0
			}
			out := rewriteFB2(t, src, meta)
			if got := extractFB2(t, out); !reflect.DeepEqual(*got, meta) {
				t.Fatalf("metadata after clearing %s:\n got %+v\nwant %+v", tt.field, *got, meta)
			}
			for _, unchanged := range []string{tt.preserved, `<book-title>Original title</book-title>`, `<date value="2020-01-01">2020</date>`} {
				if !bytes.Contains(out, []byte(unchanged)) {
					t.Errorf("clearing %s lost unrelated metadata %s", tt.field, unchanged)
				}
			}
		})
	}
}

func TestRewriteFB2MetadataIdempotent(t *testing.T) {
	first := rewriteFB2(t, []byte(fb2WritebackSample), newMetaSnapshot())
	second := rewriteFB2(t, first, newMetaSnapshot())
	if !bytes.Equal(first, second) {
		t.Fatalf("second pass differs from first:\n--- first ---\n%s\n--- second ---\n%s", first, second)
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
	utf8 = strings.Replace(utf8, "<first-name>Ann</first-name>", "<first-name>Анна</first-name>", 1)
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
	// Both body and unedited metadata keep their original Cyrillic bytes.
	for _, fragment := range []string{"<p>Привет.</p>", "<first-name>Анна</first-name>"} {
		encoded, err := charmap.Windows1251.NewEncoder().Bytes([]byte(fragment))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, encoded) {
			t.Errorf("windows-1251 fragment not preserved: %s", fragment)
		}
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
	got := extractFB2(t, out)
	if got.Title != "中文标题" {
		t.Errorf("title = %q; want 中文标题", got.Title)
	}
	for _, fragment := range []string{"<p>Привет.</p>", "<first-name>Анна</first-name>"} {
		if !bytes.Contains(out, []byte(fragment)) {
			t.Errorf("preserved fragment was not converted to UTF-8: %s", fragment)
		}
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

func TestFB2ClassificationWriteback(t *testing.T) {
	const genre = `<genre match="80">sf</genre>`
	const keywords = `<keywords>Favourite; Reviewed</keywords>`
	src := []byte(`<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0"><description><title-info>` + genre + keywords + `<book-title>Book</book-title></title-info><src-title-info><genre>old</genre><keywords>old</keywords></src-title-info></description><body><section><p>Text</p></section></body></FictionBook>`)
	for _, field := range []string{"genres", "tags"} {
		t.Run(field, func(t *testing.T) {
			for _, values := range [][]string{{"New"}, nil} {
				meta := *extractFB2(t, src)
				keep := keywords
				if field == "genres" {
					meta.Genres = values
				} else {
					meta.Tags = values
					keep = genre
				}
				out := rewriteFB2(t, src, meta)
				got := extractFB2(t, out)
				if !slices.Equal(got.Genres, meta.Genres) || !slices.Equal(got.Tags, meta.Tags) || !strings.Contains(string(out), keep) {
					t.Fatalf("roundtrip = %+v; want %+v, preserving %s", got, meta, keep)
				}
			}
		})
	}
}
