package format

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding/unicode"

	"github.com/levmv/polka/internal/bookmeta"
)

func TestParseOPF(t *testing.T) {
	// A typical standalone metadata.opf sidecar.
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Foundation</dc:title>
    <dc:creator opf:file-as="Asimov, Isaac" opf:role="aut">Isaac Asimov</dc:creator>
    <dc:language>en</dc:language>
    <dc:publisher>Gnome Press</dc:publisher>
    <dc:date>1951-06-01T00:00:00+00:00</dc:date>
    <dc:identifier opf:scheme="ISBN">978-0-553-29335-0</dc:identifier>
    <dc:identifier opf:scheme="calibre">875f44e0-1234-5678-90ab-cdef12345678</dc:identifier>
    <dc:subject>Science Fiction</dc:subject>
    <meta name="calibre:title_sort" content="Foundation, The"/>
    <meta name="calibre:series" content="Foundation"/>
    <meta name="calibre:series_index" content="1.0"/>
    <meta name="calibre:timestamp" content="2014-01-08T22:00:58.123456+00:00"/>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Title != "Foundation" {
		t.Errorf("title = %q; want Foundation", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Isaac Asimov" || meta.Authors[0].SortName != "Asimov, Isaac" {
		t.Errorf("authors = %+v; want one Isaac Asimov / Asimov, Isaac", meta.Authors)
	}
	if meta.Publisher != "Gnome Press" {
		t.Errorf("publisher = %q", meta.Publisher)
	}
	if meta.Date != "1951-06-01" {
		t.Errorf("date = %q; want 1951-06-01", meta.Date)
	}
	if meta.SortTitle != "Foundation, The" {
		t.Errorf("sort title = %q; want Foundation, The", meta.SortTitle)
	}
	if meta.Series != "Foundation" || meta.SeriesIndex != 1.0 {
		t.Errorf("series = %q/%v; want Foundation/1", meta.Series, meta.SeriesIndex)
	}
	// The tool-private UUID identifier is dropped; only the ISBN remains.
	if meta.Identifier != "isbn:978-0-553-29335-0" {
		t.Errorf("identifier = %q; want isbn:978-0-553-29335-0", meta.Identifier)
	}
	if len(meta.Tags) != 1 || meta.Tags[0] != "Science Fiction" {
		t.Errorf("tags = %v; want [Science Fiction]", meta.Tags)
	}
	if meta.CalibreTimestamp != "2014-01-08T22:00:58.123456+00:00" {
		t.Errorf("calibre timestamp = %q; want import timestamp", meta.CalibreTimestamp)
	}
}

func TestParseOPFCalibreTimestampProperty(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" prefix="calibre: https://calibre-ebook.com">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Property Timestamp</dc:title>
    <meta property="calibre:timestamp">2018-04-03T12:34:56Z</meta>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.CalibreTimestamp != "2018-04-03T12:34:56Z" {
		t.Fatalf("CalibreTimestamp = %q; want EPUB 3 property value", meta.CalibreTimestamp)
	}
}

func TestParseOPFTrimsMetadataFields(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>  Trimmed Title  </dc:title>
    <dc:creator opf:file-as="  Author, Trim  " opf:role=" aut "> Trim Author </dc:creator>
    <dc:language> en </dc:language>
    <dc:description> Description with edge spaces. </dc:description>
    <dc:publisher> Press </dc:publisher>
    <dc:date> 2026-04-30T19:57:11+00:00 </dc:date>
    <meta name=" calibre:title_sort " content=" Title, Trimmed "/>
    <meta name=" calibre:series " content=" Series Name "/>
    <meta name=" calibre:series_index " content=" 2.5 "/>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Title != "Trimmed Title" || meta.SortTitle != "Title, Trimmed" {
		t.Fatalf("title fields = %q / %q; want trimmed", meta.Title, meta.SortTitle)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Trim Author" || meta.Authors[0].SortName != "Author, Trim" || meta.Authors[0].Role != "aut" {
		t.Fatalf("authors = %+v; want trimmed author fields", meta.Authors)
	}
	if meta.Language != "en" || meta.Description != "Description with edge spaces." || meta.Publisher != "Press" {
		t.Fatalf("simple fields = lang %q desc %q publisher %q; want trimmed", meta.Language, meta.Description, meta.Publisher)
	}
	if meta.Date != "2026-04-30" {
		t.Fatalf("date = %q; want normalized trimmed date", meta.Date)
	}
	if meta.Series != "Series Name" || meta.SeriesIndex != 2.5 {
		t.Fatalf("series = %q/%v; want trimmed", meta.Series, meta.SeriesIndex)
	}
}

func TestParseOPFNormalizesLanguage(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:language>English</dc:language>
    <dc:language>eng_us</dc:language>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Language != "en-US" {
		t.Fatalf("Language = %q; want first normalized code en-US", meta.Language)
	}
}

func TestParseOPFToleratesXML11Declaration(t *testing.T) {
	const opf = `<?xml version='1.1' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>XML 1.1 Declared Book</dc:title>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Title != "XML 1.1 Declared Book" {
		t.Fatalf("Title = %q; want XML 1.1 Declared Book", meta.Title)
	}
}

func TestParseOPFToleratesLegacyCharset(t *testing.T) {
	opf := "<?xml version='1.0' encoding='iso-8859-1'?>\n" +
		"<package xmlns=\"http://www.idpf.org/2007/opf\" version=\"2.0\">\n" +
		"  <metadata xmlns:dc=\"http://purl.org/dc/elements/1.1/\">\n" +
		"    <dc:title>Caf\xe9 Book</dc:title>\n" +
		"  </metadata>\n" +
		"</package>"

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Title != "Caf\u00e9 Book" {
		t.Fatalf("Title = %q; want decoded Latin-1 title", meta.Title)
	}
}

func TestParseOPFToleratesUTF16(t *testing.T) {
	raw := []byte(`<?xml version="1.0" encoding="UTF-16"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>UTF-16 Книга</dc:title>
  </metadata>
</package>`)
	opf, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes(raw)
	if err != nil {
		t.Fatalf("encode UTF-16 OPF: %v", err)
	}

	meta, err := ParseOPF(bytes.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Title != "UTF-16 Книга" {
		t.Fatalf("Title = %q; want decoded UTF-16 title", meta.Title)
	}
}

func TestParseOPFRemovesInvalidXML10Controls(t *testing.T) {
	opf := append([]byte(`<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Invalid`), 0x01)
	opf = append(opf, []byte(` OPF</dc:title>
  </metadata>
</package>`)...)

	meta, err := ParseOPF(bytes.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Title != "Invalid OPF" {
		t.Fatalf("Title = %q; want invalid control removed", meta.Title)
	}
}

func TestParseOPFLegacyOEBMetadata(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<metadata>
  <dc-metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:oebpackage="http://openebook.org/namespaces/oeb-package/1.0/">
    <dc:Title>Legacy Text Archive</dc:Title>
    <dc:Creator file-as="Writer, Legacy" role="aut">Legacy Writer</dc:Creator>
    <dc:Language>de</dc:Language>
    <dc:Publisher>Legacy Press</dc:Publisher>
    <dc:Date>2011-04-03</dc:Date>
    <dc:Identifier scheme="ISBN">9783406613685</dc:Identifier>
    <dc:Subject>Biography, Philosophy</dc:Subject>
  </dc-metadata>
  <x-metadata>
    <meta name="{http://calibre.kovidgoyal.net/2009/metadata}title_sort" content="Text Archive, Legacy"/>
    <meta name="{http://calibre.kovidgoyal.net/2009/metadata}series" content="Legacy Series"/>
    <meta name="{http://calibre.kovidgoyal.net/2009/metadata}series_index" content="2"/>
  </x-metadata>
</metadata>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Title != "Legacy Text Archive" {
		t.Fatalf("Title = %q; want legacy OEB title", meta.Title)
	}
	if meta.SortTitle != "Text Archive, Legacy" {
		t.Fatalf("SortTitle = %q; want legacy calibre title_sort", meta.SortTitle)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Legacy Writer" || meta.Authors[0].SortName != "Writer, Legacy" || meta.Authors[0].Role != "aut" {
		t.Fatalf("Authors = %+v; want Legacy Writer with sort/role", meta.Authors)
	}
	if meta.Language != "de" || meta.Publisher != "Legacy Press" || meta.Date != "2011-04-03" {
		t.Fatalf("simple fields = lang %q publisher %q date %q; want legacy fields", meta.Language, meta.Publisher, meta.Date)
	}
	if meta.Identifier != "isbn:9783406613685" {
		t.Fatalf("Identifier = %q; want legacy ISBN", meta.Identifier)
	}
	if meta.Series != "Legacy Series" || meta.SeriesIndex != 2 {
		t.Fatalf("Series = %q/%v; want legacy calibre series", meta.Series, meta.SeriesIndex)
	}
	wantTags := []string{"Biography", "Philosophy"}
	if len(meta.Tags) != len(wantTags) {
		t.Fatalf("Tags = %v; want %v", meta.Tags, wantTags)
	}
	for i, want := range wantTags {
		if meta.Tags[i] != want {
			t.Fatalf("Tags = %v; want %v", meta.Tags, wantTags)
		}
	}
}

// Some sidecars write the combined author-sort string into each creator's
// file-as. Polka must not apply that combined string as any single creator's
// sort_name; the per-creator sort derives from the name. Real case: an OPF
// listing the pen name "qntm" and the author's real name "Sam Hughes" as two
// creators, one person but two creator elements all the same.
func TestParseOPFCombinedAuthorSortNotApplied(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Ra</dc:title>
    <dc:creator opf:file-as="qntm &amp; Hughes, Sam" opf:role="aut">qntm</dc:creator>
    <dc:creator opf:file-as="qntm &amp; Hughes, Sam" opf:role="aut">Sam Hughes</dc:creator>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if len(meta.Authors) != 2 {
		t.Fatalf("authors = %+v; want 2", meta.Authors)
	}
	for _, a := range meta.Authors {
		if strings.Contains(a.SortName, "&") {
			t.Errorf("author %q kept combined sort %q; want it dropped", a.Name, a.SortName)
		}
	}
	// A single, genuine per-creator file-as is still honored.
	const single = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:creator opf:file-as="Hughes, Sam" opf:role="aut">Sam Hughes</dc:creator>
  </metadata>
</package>`
	m2, err := ParseOPF(strings.NewReader(single))
	if err != nil {
		t.Fatalf("ParseOPF single: %v", err)
	}
	if len(m2.Authors) != 1 || m2.Authors[0].SortName != "Hughes, Sam" {
		t.Errorf("single author sort = %+v; want Hughes, Sam preserved", m2.Authors)
	}
}

func TestParseOPFEPUB3Refinements(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Refined Book</dc:title>
    <dc:creator id="creator-b">Second Writer</dc:creator>
    <dc:creator id="creator-a">First Writer</dc:creator>
    <dc:identifier>https://example.org/books/refined</dc:identifier>
    <meta refines="#creator-a" property="file-as">Writer, First</meta>
    <meta refines="#creator-a" property="role">aut</meta>
    <meta refines="#creator-a" property="display-seq">1</meta>
    <meta refines="#creator-b" property="role">aut</meta>
    <meta refines="#creator-b" property="display-seq">2</meta>
    <meta property="belongs-to-collection" id="series-1">Refined Series</meta>
    <meta refines="#series-1" property="collection-type">series</meta>
    <meta refines="#series-1" property="group-position">4.5</meta>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if len(meta.Authors) != 2 {
		t.Fatalf("authors = %+v; want 2", meta.Authors)
	}
	if meta.Authors[0].Name != "First Writer" || meta.Authors[0].SortName != "Writer, First" || meta.Authors[0].Role != "aut" {
		t.Fatalf("author[0] = %+v; want refined First Writer", meta.Authors[0])
	}
	if meta.Authors[1].Name != "Second Writer" || meta.Authors[1].Role != "aut" {
		t.Fatalf("author[1] = %+v; want refined Second Writer", meta.Authors[1])
	}
	if meta.Series != "Refined Series" || meta.SeriesIndex != 4.5 {
		t.Fatalf("series = %q/%v; want Refined Series/4.5", meta.Series, meta.SeriesIndex)
	}
	if meta.Identifier != "url:https://example.org/books/refined" {
		t.Fatalf("identifier = %q; want URL identifier", meta.Identifier)
	}
}

func TestParseOPFEPUB3TitleRefinements(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title id="subtitle"> The Practical Subtitle </dc:title>
    <dc:title id="main-title"> Main Title </dc:title>
    <meta refines="#main-title" property="title-type">main</meta>
    <meta refines="#main-title" property="file-as">Title, Main</meta>
    <meta refines="#subtitle" property="title-type">subtitle</meta>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Title != "Main Title: The Practical Subtitle" {
		t.Fatalf("title = %q; want main title plus subtitle", meta.Title)
	}
	if meta.SortTitle != "Title, Main" {
		t.Fatalf("sort title = %q; want refined title file-as", meta.SortTitle)
	}
}

func TestParseOPFCreatorRoleFiltering(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Role Test</dc:title>
    <dc:creator id="translator">Helpful Translator</dc:creator>
    <dc:creator id="author">Actual Author</dc:creator>
    <dc:creator>Unroled Name</dc:creator>
    <meta refines="#translator" property="role">trl</meta>
    <meta refines="#translator" property="display-seq">1</meta>
    <meta refines="#author" property="role" scheme="marc:relators">aut</meta>
    <meta refines="#author" property="display-seq">2</meta>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Actual Author" || meta.Authors[0].Role != "aut" {
		t.Fatalf("authors = %+v; want only the explicit author", meta.Authors)
	}

	const editorOnly = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Edited Book</dc:title>
    <dc:creator opf:role="edt">Only Editor</dc:creator>
  </metadata>
</package>`
	m2, err := ParseOPF(strings.NewReader(editorOnly))
	if err != nil {
		t.Fatalf("ParseOPF editorOnly: %v", err)
	}
	if len(m2.Authors) != 1 || m2.Authors[0].Name != "Only Editor" || m2.Authors[0].Role != "edt" {
		t.Fatalf("editor fallback authors = %+v; want editor fallback", m2.Authors)
	}
}

func TestParseOPFMultipleRefinedRolesKeepsAuthorRole(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Dracula</dc:title>
    <dc:creator id="author">Bram Stoker</dc:creator>
    <meta property="file-as" refines="#author">Stoker, Bram</meta>
    <meta property="role" refines="#author" scheme="marc:relators">dto</meta>
    <meta property="role" refines="#author" scheme="marc:relators">aut</meta>
    <meta property="role" refines="#author" scheme="marc:relators">wpr</meta>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Bram Stoker" || meta.Authors[0].SortName != "Stoker, Bram" || meta.Authors[0].Role != "aut" {
		t.Fatalf("authors = %+v; want Bram Stoker with author role", meta.Authors)
	}
}

func TestParseOPFCommaSeparatedSubjects(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Subject Test</dc:title>
    <dc:subject>Science Fiction, Adventure</dc:subject>
    <dc:subject> adventure </dc:subject>
    <dc:subject>History,</dc:subject>
  </metadata>
</package>`

	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	want := []string{"Science Fiction", "Adventure", "History"}
	if len(meta.Tags) != len(want) {
		t.Fatalf("tags = %v; want %v", meta.Tags, want)
	}
	for i, tag := range want {
		if meta.Tags[i] != tag {
			t.Fatalf("tags = %v; want %v", meta.Tags, want)
		}
	}
}

func TestParseOPFMalformed(t *testing.T) {
	if _, err := ParseOPF(strings.NewReader("not xml <<<")); err == nil {
		t.Error("expected error on malformed OPF")
	}
}

// A garbage <dc:date> (litres ships "0101-01-01T...") is rejected to empty
// rather than leaked verbatim into the stored metadata.
func TestParseOPFRejectsMalformedDate(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Some Book</dc:title>
    <dc:date>0101-01-01T00:00:00+00:00</dc:date>
  </metadata>
</package>`
	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Date != "" {
		t.Errorf("date = %q; want empty (malformed date rejected)", meta.Date)
	}
}

func TestParseOPFUsesEarliestParseableDate(t *testing.T) {
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Multiple Dates</dc:title>
    <dc:date>2020-06-01</dc:date>
    <dc:date>0101-01-01T00:00:00+00:00</dc:date>
    <dc:date>1999</dc:date>
  </metadata>
</package>`
	meta, err := ParseOPF(strings.NewReader(opf))
	if err != nil {
		t.Fatalf("ParseOPF: %v", err)
	}
	if meta.Date != "1999" {
		t.Fatalf("date = %q; want earliest parseable date", meta.Date)
	}
}

func TestMetadataMerge(t *testing.T) {
	base := &Metadata{
		Title:       "Embedded Title",
		Authors:     []bookmeta.AuthorMeta{{Name: "Embedded Author"}},
		Language:    "en",
		SeriesIndex: 3,
		Tags:        []string{"old"},
	}
	override := &Metadata{
		Title:            "Sidecar Title",
		Authors:          []bookmeta.AuthorMeta{{Name: "Sidecar Author", SortName: "Author, Sidecar"}},
		CalibreTimestamp: "2019-02-03T04:05:06Z",
		// Language empty -> keep base.
		Publisher: "Sidecar Press",
		Tags:      []string{"new"},
	}
	base.Merge(override)

	if base.Title != "Sidecar Title" {
		t.Errorf("title = %q; want sidecar to win", base.Title)
	}
	if len(base.Authors) != 1 || base.Authors[0].Name != "Sidecar Author" {
		t.Errorf("authors = %+v; want sidecar author", base.Authors)
	}
	if base.Language != "en" {
		t.Errorf("language = %q; want base kept when override empty", base.Language)
	}
	if base.Publisher != "Sidecar Press" {
		t.Errorf("publisher = %q; want sidecar", base.Publisher)
	}
	if base.SeriesIndex != 3 {
		t.Errorf("series index = %v; want base kept (override 0)", base.SeriesIndex)
	}
	if len(base.Tags) != 1 || base.Tags[0] != "new" {
		t.Errorf("tags = %v; want sidecar [new]", base.Tags)
	}
	if base.CalibreTimestamp != override.CalibreTimestamp {
		t.Errorf("calibre timestamp = %q; want sidecar %q", base.CalibreTimestamp, override.CalibreTimestamp)
	}

	// Merging nil is a no-op.
	base.Merge(nil)
	if base.Title != "Sidecar Title" {
		t.Error("Merge(nil) mutated metadata")
	}
}
