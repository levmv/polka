package format

import (
	"bytes"
	"image/color"
	"maps"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
)

func TestDetectFormatODTAndRTF(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want Format
	}{
		{name: "book.odt", data: testODTZip(t, odtFixture{}), want: FormatODT},
		{name: "book.rtf", data: []byte(`{\rtf1\ansi{\info{\title RTF Book}}Body}`), want: FormatRTF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFormatODTAndRTFRejectExtensionOnlyFiles(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "book.odt", data: writeTestZip(t, map[string][]byte{"mimetype": []byte("application/zip")})},
		{name: "book.odt", data: writeTestZip(t, map[string][]byte{"mimetype": []byte(odtMediaType)})},
		{name: "book.odt", data: writeTestZip(t, map[string][]byte{
			"mimetype":    []byte(odtMediaType),
			"content.xml": []byte(`<office:document-content xmlns:office="` + odtOfficeNamespace + `"><office:body><office:presentation/></office:body></office:document-content>`),
		})},
		{name: "book.odt", data: []byte("not a zip")},
		{name: "book.rtf", data: []byte("not rtf")},
		{name: "book.rtf", data: []byte(`{\foo1}`)},
	}
	for _, tt := range tests {
		t.Run(string(tt.data[:min(len(tt.data), 12)]), func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}

func TestDetectFormatODTWithoutMetadata(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"mimetype": []byte(odtMediaType),
		"content.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="` + odtOfficeNamespace + `">
  <office:body><office:text><text:p xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">Book</text:p></office:text></office:body>
</office:document-content>`),
	})
	r := bytes.NewReader(data)
	if got := DetectFormat("minimal.odt", r, r.Size()); got != FormatODT {
		t.Fatalf("DetectFormat = %v; want FormatODT", got)
	}
	meta, err := ExtractODTMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractODTMetadata: %v", err)
	}
	if meta.Title != "" || len(meta.Authors) != 0 || meta.Description != "" || len(meta.Tags) != 0 {
		t.Fatalf("Metadata = %+v; want empty metadata", meta)
	}
}

func TestExtractODTMetadata(t *testing.T) {
	data := testODTZip(t, odtFixture{meta: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-meta
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:dc="http://purl.org/dc/elements/1.1/"
  xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0">
  <office:meta>
    <dc:title>ODT Book</dc:title>
    <dc:creator>Fallback Author</dc:creator>
    <meta:initial-creator>Initial Author</meta:initial-creator>
    <dc:description> An ODT
      description. </dc:description>
    <dc:subject>Office, Saved</dc:subject>
    <meta:keyword>Documents, Office</meta:keyword>
    <dc:language>eng</dc:language>
    <dc:date>2024-02-03T12:00:00Z</dc:date>
  </office:meta>
</office:document-meta>`})
	r := bytes.NewReader(data)
	meta, err := ExtractODTMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractODTMetadata: %v", err)
	}
	if meta.Title != "ODT Book" {
		t.Fatalf("Title = %q; want ODT Book", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Initial Author" {
		t.Fatalf("Authors = %+v; want Initial Author", meta.Authors)
	}
	if meta.Description != "An ODT description." {
		t.Fatalf("Description = %q; want normalized description", meta.Description)
	}
	if meta.Language != "en" {
		t.Fatalf("Language = %q; want normalized en", meta.Language)
	}
	if meta.Date != "2024-02-03" {
		t.Fatalf("Date = %q; want 2024-02-03", meta.Date)
	}
	wantTags := []string{"Office", "Saved", "Documents"}
	if !equalStrings(meta.Tags, wantTags) {
		t.Fatalf("Tags = %+v; want %+v", meta.Tags, wantTags)
	}
}

func TestExtractODTUserDefinedOPFMetadata(t *testing.T) {
	data := testODTZip(t, odtFixture{meta: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-meta
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:dc="http://purl.org/dc/elements/1.1/"
  xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0">
  <office:meta>
    <dc:title>Original Title</dc:title>
    <meta:user-defined meta:name="opf.metadata" meta:value-type="boolean">true</meta:user-defined>
    <meta:user-defined meta:name="opf.titlesort">Title, Custom</meta:user-defined>
    <meta:user-defined meta:name="opf.authors">Custom Author</meta:user-defined>
    <meta:user-defined meta:name="opf.authorsort">Author, Custom</meta:user-defined>
    <meta:user-defined meta:name="opf.publisher">ODT Press</meta:user-defined>
    <meta:user-defined meta:name="opf.pubdate">2020-01-02T00:00:00Z</meta:user-defined>
    <meta:user-defined meta:name="opf.series">ODT Series</meta:user-defined>
    <meta:user-defined meta:name="opf.seriesindex">3.5</meta:user-defined>
    <meta:user-defined meta:name="opf.language">fr-FR</meta:user-defined>
    <meta:user-defined meta:name="opf.identifiers">{&quot;isbn&quot;:&quot;978-0-306-40615-7&quot;}</meta:user-defined>
  </office:meta>
</office:document-meta>`})
	r := bytes.NewReader(data)
	meta, err := ExtractODTMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractODTMetadata: %v", err)
	}
	if meta.SortTitle != "Title, Custom" {
		t.Fatalf("SortTitle = %q; want Title, Custom", meta.SortTitle)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Custom Author" || meta.Authors[0].SortName != "Author, Custom" {
		t.Fatalf("Authors = %+v; want Custom Author with explicit sort", meta.Authors)
	}
	if meta.Publisher != "ODT Press" || meta.Date != "2020-01-02" || meta.Language != "fr-FR" {
		t.Fatalf("Metadata = %+v; want OPF user metadata override", meta)
	}
	if meta.Series != "ODT Series" || meta.SeriesIndex != 3.5 {
		t.Fatalf("Series = %q/%v; want ODT Series/3.5", meta.Series, meta.SeriesIndex)
	}
	if meta.Identifier != "isbn:978-0-306-40615-7" {
		t.Fatalf("Identifier = %q; want ISBN from OPF identifiers", meta.Identifier)
	}
}

func TestIdentifiersFromODTJSONAllowsDuplicateNames(t *testing.T) {
	ids := identifiersFromODTJSON(`{"isbn":"978-0-306-40615-7","isbn":"978-1-4028-9462-6"}`)
	if got := bookmeta.FormatIdentifiers(ids); got != "isbn:978-1-4028-9462-6" {
		t.Fatalf("identifiers = %q; want the last duplicate value", got)
	}
}

func TestExtractODTCoverFromOPFCoverFrame(t *testing.T) {
	icon := testSizedPNG(t, 12, 12, color.NRGBA{R: 200, G: 40, B: 40, A: 255})
	cover := tinyPNG
	data := testODTZip(t, odtFixture{
		meta: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-meta
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0">
  <office:meta>
    <meta:user-defined meta:name="opf.metadata" meta:value-type="boolean">true</meta:user-defined>
  </office:meta>
</office:document-meta>`,
		content: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"
  xmlns:xlink="http://www.w3.org/1999/xlink">
  <office:body><office:text>
    <draw:frame draw:name="not-cover"><draw:image xlink:href="Pictures/icon.png"/></draw:frame>
    <draw:frame draw:name="opf.cover"><draw:image xlink:href="Pictures/cover.png"/></draw:frame>
  </office:text></office:body>
</office:document-content>`,
		extra: map[string][]byte{
			"Pictures/icon.png":  icon,
			"Pictures/cover.png": cover,
		},
	})
	r := bytes.NewReader(data)
	got, ext, err := ExtractODTCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractODTCover: %v", err)
	}
	if ext != ".png" || !bytes.Equal(got, cover) {
		t.Fatalf("cover ext=%q bytes=%d; want explicit ODT cover", ext, len(got))
	}
}

func TestExtractODTCoverFallsBackToFirstCoverLikeImage(t *testing.T) {
	icon := testSizedPNG(t, 12, 12, color.NRGBA{R: 200, G: 40, B: 40, A: 255})
	cover := testSizedPNG(t, 120, 160, color.NRGBA{R: 40, G: 90, B: 150, A: 255})
	data := testODTZip(t, odtFixture{
		content: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"
  xmlns:xlink="http://www.w3.org/1999/xlink">
  <office:body><office:text>
    <draw:frame draw:name="icon"><draw:image xlink:href="Pictures/icon.png"/></draw:frame>
    <draw:frame draw:name="page"><draw:image xlink:href="Pictures/cover.png"/></draw:frame>
  </office:text></office:body>
</office:document-content>`,
		extra: map[string][]byte{
			"Pictures/icon.png":  icon,
			"Pictures/cover.png": cover,
		},
	})
	r := bytes.NewReader(data)
	got, ext, err := ExtractODTCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractODTCover: %v", err)
	}
	if ext != ".png" || !bytes.Equal(got, cover) {
		t.Fatalf("cover ext=%q bytes=%d; want first cover-like ODT image", ext, len(got))
	}
}

func TestExtractODTCoverHonorsOPFNoCover(t *testing.T) {
	cover := testSizedPNG(t, 120, 160, color.NRGBA{R: 40, G: 90, B: 150, A: 255})
	data := testODTZip(t, odtFixture{
		meta: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-meta
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0">
  <office:meta>
    <meta:user-defined meta:name="opf.metadata" meta:value-type="boolean">true</meta:user-defined>
    <meta:user-defined meta:name="opf.nocover" meta:value-type="boolean">true</meta:user-defined>
  </office:meta>
</office:document-meta>`,
		content: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"
  xmlns:xlink="http://www.w3.org/1999/xlink">
  <office:body><office:text>
    <draw:frame draw:name="opf.cover"><draw:image xlink:href="Pictures/cover.png"/></draw:frame>
  </office:text></office:body>
</office:document-content>`,
		extra: map[string][]byte{"Pictures/cover.png": cover},
	})
	r := bytes.NewReader(data)
	got, ext, err := ExtractODTCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractODTCover: %v", err)
	}
	if got != nil || ext != "" {
		t.Fatalf("cover = %d bytes, ext = %q; want no cover", len(got), ext)
	}
}

func TestExtractRTFMetadata(t *testing.T) {
	data := []byte(`{\rtf1\ansi\ansicpg1251{\info{\title Unicode \u1046? title}{\author Ada Lovelace, Charles Babbage}{\subject RTF subject}{\category Math, Engines}{\manager RTF Press}}Body}`)
	r := bytes.NewReader(data)
	meta, err := ExtractRTFMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractRTFMetadata: %v", err)
	}
	if meta.Title != "Unicode Ж title" {
		t.Fatalf("Title = %q; want decoded unicode title", meta.Title)
	}
	if len(meta.Authors) != 2 || meta.Authors[0].Name != "Ada Lovelace" || meta.Authors[1].Name != "Charles Babbage" {
		t.Fatalf("Authors = %+v; want split RTF authors", meta.Authors)
	}
	if meta.Description != "RTF subject" || meta.Publisher != "RTF Press" {
		t.Fatalf("Metadata = %+v; want subject and publisher", meta)
	}
	wantTags := []string{"Math", "Engines"}
	if !equalStrings(meta.Tags, wantTags) {
		t.Fatalf("Tags = %+v; want %+v", meta.Tags, wantTags)
	}
}

func TestExtractRTFMetadataDecodesCodepageHex(t *testing.T) {
	data := []byte(`{\rtf1\ansi\ansicpg1251{\info{\title \'ca\'ed\'e8\'e3\'e0}}Body}`)
	r := bytes.NewReader(data)
	meta, err := ExtractRTFMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractRTFMetadata: %v", err)
	}
	if meta.Title != "Книга" {
		t.Fatalf("Title = %q; want cp1251 hex decoded title", meta.Title)
	}
}

func TestExtractRTFMetadataRejectsUnsupportedCodepage(t *testing.T) {
	data := []byte(`{\rtf1\ansi\ansicpg932{\info{\title \'82\'a0}}Body}`)
	r := bytes.NewReader(data)
	_, err := ExtractRTFMetadata(r, r.Size())
	if err == nil || !strings.Contains(err.Error(), "unsupported RTF code page 932") {
		t.Fatalf("ExtractRTFMetadata error = %v; want unsupported code page diagnostic", err)
	}
}

func TestExtractRTFMetadataSkipsIgnorableAndBinaryDestinations(t *testing.T) {
	data := []byte(`{\rtf1\ansi{\info{\*\ignored{\title Fake Title}}\bin5 a{b}c{\title Real {\*\generator Hidden}text\bin5 a{b}c done}{\author Actual Author}}Body}`)
	r := bytes.NewReader(data)
	meta, err := ExtractRTFMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractRTFMetadata: %v", err)
	}
	if meta.Title != "Real text done" {
		t.Fatalf("Title = %q; want direct field without ignored/binary text", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Actual Author" {
		t.Fatalf("Authors = %+v; want Actual Author", meta.Authors)
	}
}

func TestExtractRTFMetadataDoesNotRequireRootWithinScanLimit(t *testing.T) {
	data := append([]byte(`{\rtf1\ansi{\info{\title Prefix Metadata}}`), bytes.Repeat([]byte(" body"), maxRTFMetadataSize/5)...)
	data = append(data, '}')
	r := bytes.NewReader(data)
	meta, err := ExtractRTFMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractRTFMetadata: %v", err)
	}
	if meta.Title != "Prefix Metadata" {
		t.Fatalf("Title = %q; want metadata from bounded prefix", meta.Title)
	}
}

func TestExtractRTFMetadataCombinesUnicodeSurrogatePairs(t *testing.T) {
	data := []byte(`{\rtf1\ansi{\info{\title Smile \u-10179?\u-8704? done}}Body}`)
	r := bytes.NewReader(data)
	meta, err := ExtractRTFMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractRTFMetadata: %v", err)
	}
	if meta.Title != "Smile 😀 done" {
		t.Fatalf("Title = %q; want decoded non-BMP character", meta.Title)
	}
}

func TestExtractRTFMetadataRestoresGroupScopedUnicodeFallbackCount(t *testing.T) {
	data := []byte(`{\rtf1\ansi\uc1{\info{\title A {\uc0 \u1046} \u1047? C}}Body}`)
	r := bytes.NewReader(data)
	meta, err := ExtractRTFMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractRTFMetadata: %v", err)
	}
	if meta.Title != "A Ж З C" {
		t.Fatalf("Title = %q; want nested uc scope restored without literal braces or fallback", meta.Title)
	}
}

func TestExtractRTFMetadataCompanyAndKeywords(t *testing.T) {
	data := []byte(`{\rtf1\ansi{\info{\title RTF Book}{\category Fiction; Archive}{\keywords Archive, Scanned}{\manager Manager Field}{\company Company Field}}Body}`)
	r := bytes.NewReader(data)
	meta, err := ExtractRTFMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractRTFMetadata: %v", err)
	}
	if meta.Publisher != "Company Field" {
		t.Fatalf("Publisher = %q; want company before manager", meta.Publisher)
	}
	wantTags := []string{"Fiction", "Archive", "Scanned"}
	if !equalStrings(meta.Tags, wantTags) {
		t.Fatalf("Tags = %+v; want %+v", meta.Tags, wantTags)
	}
}

type odtFixture struct {
	meta    string
	content string
	extra   map[string][]byte
}

func testODTZip(t *testing.T, fixture odtFixture) []byte {
	t.Helper()
	meta := fixture.meta
	if meta == "" {
		meta = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-meta
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:dc="http://purl.org/dc/elements/1.1/">
  <office:meta><dc:title>ODT Book</dc:title></office:meta>
</office:document-meta>`
	}
	entries := map[string][]byte{
		"mimetype": []byte(odtMediaType),
		"meta.xml": []byte(meta),
	}
	content := fixture.content
	if content == "" {
		content = `<office:document-content xmlns:office="` + odtOfficeNamespace + `"><office:body><office:text/></office:body></office:document-content>`
	}
	entries["content.xml"] = []byte(content)
	maps.Copy(entries, fixture.extra)
	return writeTestZip(t, entries)
}
