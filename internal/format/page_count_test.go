package format

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"
)

func TestDeclaredPageCountsAndWriteback(t *testing.T) {
	for _, tt := range []struct {
		name, fields string
		want         int
	}{
		{"priority", `<meta name="bookorbit:page_count" content="9"/><meta name="calibre:user_metadata:#pages" content='{ "#value#": 20 }'/><meta property="schema:numberOfPages">30</meta>`, 30},
		{"calibre combined", `<meta property="calibre:user_metadata">{"#page_count":{"#value#":31},"#pages":{"#value#":25},"#other":{"#value#":900}}</meta>`, 25},
		{"malformed fallback", `<meta name="schema:numberOfPages" content="2.5"/><meta name="calibre:user_metadata:#pages" content='{"#value#":false}'/><meta name="calibre:user_metadata:#pagecount" content='{"#value#":"19"}'/>`, 19},
		{"invalid", `<meta property="schema:numberOfPages">-5</meta><meta name="calibre:user_metadata:#pages" content='{"#value#":1e12}'/><meta refines="#chapter" property="schema:numberOfPages">12</meta>`, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Example</dc:title>` + tt.fields + `</metadata></package>`)
			meta, err := ParseOPF(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if meta.PageCount != tt.want {
				t.Fatalf("pages = %d; want %d", meta.PageCount, tt.want)
			}
		})
	}
	for _, version := range []string{"2.0", "3.0"} {
		t.Run("round trip "+version, func(t *testing.T) {
			columns := `{"#pagecount":{"#value#":42},"#review":{"#value#":"Keep &amp; preserve"}}`
			calibre := `<meta property="calibre:user_metadata"><![CDATA[` + strings.ReplaceAll(columns, "&amp;", "&") + `]]></meta>`
			if version == "2.0" {
				calibre = `<meta name="calibre:user_metadata" content='` + columns + `'/>`
			}
			foreign := []string{
				`<meta name="calibre:user_metadata:#pages" content='{ "#value#": 20 }'/>`,
				`<meta name="bookorbit:page_count" content="9"/>`,
				calibre,
			}
			raw := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="` + version + `"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Example</dc:title><meta name="schema:numberOfPages" content="55"/>` + strings.Join(foreign, "") + `</metadata><manifest/><spine/></package>`)
			meta := Metadata{Title: "Example", PageCount: 123}
			out, err := rewriteOPFMetadata(raw, meta, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseOPF(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.PageCount != 123 || bytes.Count(out, []byte("schema:numberOfPages")) != 1 {
				t.Fatalf("canonical count was not replaced: %s", out)
			}
			for _, field := range foreign {
				if !bytes.Contains(out, []byte(field)) {
					t.Fatalf("writeback changed foreign field %s: %s", field, out)
				}
			}
			repeated, err := rewriteOPFMetadata(out, meta, time.Time{})
			if err != nil || !bytes.Equal(out, repeated) {
				t.Fatalf("repeated writeback changed the OPF: %s, %v", repeated, err)
			}
			normalized, err := NormalizeEPUBPageCountMetadata(&zip.Reader{}, "book.opf", out)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err = ParseOPF(bytes.NewReader(normalized))
			if err != nil || parsed.PageCount != 123 || bytes.Contains(normalized, []byte("#pages")) || bytes.Contains(normalized, []byte("#pagecount")) || bytes.Contains(normalized, []byte("bookorbit:page_count")) || !bytes.Contains(normalized, []byte("Keep &amp; preserve")) {
				t.Fatalf("conversion did not consolidate counts and preserve other columns: %s, %v", normalized, err)
			}
			repeated, err = NormalizeEPUBPageCountMetadata(&zip.Reader{}, "book.opf", normalized)
			if err != nil || !bytes.Equal(normalized, repeated) {
				t.Fatalf("repeated normalization changed the OPF: %s, %v", repeated, err)
			}
			meta.PageCount = 0
			unknown, err := rewriteOPFMetadata(raw, meta, time.Time{})
			if err != nil || !bytes.Contains(unknown, []byte("#pages")) || !bytes.Contains(unknown, []byte("#pagecount")) {
				t.Fatalf("unknown count erased source declarations: %s, %v", unknown, err)
			}
		})
	}
}

func pageTestEPUB(t *testing.T, bodies []string, metadata, nav string) []byte {
	t.Helper()
	entries := map[string][]byte{"mimetype": []byte("application/epub+zip"), "META-INF/container.xml": []byte(`<container><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)}
	manifest, spine := "", ""
	for i, body := range bodies {
		name := fmt.Sprintf("part%d.xhtml", i)
		manifest += fmt.Sprintf(`<item id="p%d" href="%s" media-type="application/xhtml+xml"/>`, i, name)
		spine += fmt.Sprintf(`<itemref idref="p%d"/>`, i)
		entries[name] = []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title/></head><body>` + body + `</body></html>`)
	}
	if nav != "" {
		manifest += `<item id="nav" href="nav.xhtml" properties="nav" media-type="application/xhtml+xml"/>`
		entries["nav.xhtml"] = []byte(`<html><head><title/></head><body>` + nav + `</body></html>`)
	}
	entries["book.opf"] = []byte(`<package version="3.0"><metadata>` + metadata + `</metadata><manifest>` + manifest + `</manifest><spine>` + spine + `</spine></package>`)
	return writeTestZip(t, entries)
}

func TestPageCountContentInvariants(t *testing.T) {
	paragraph := `<p>` + strings.Repeat("A useful word and another. ", 35) + `</p>`
	plain := strings.Repeat(paragraph, 30)
	count := func(raw []byte, kind Format) int {
		t.Helper()
		n, err := CountPages(t.Context(), bytes.NewReader(raw), int64(len(raw)), kind)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	base := count(pageTestEPUB(t, []string{plain}, "", ""), FormatEPUB)
	if base <= 1 {
		t.Fatalf("reference content has %d pages; want more than one", base)
	}
	for _, bodies := range [][]string{
		{strings.ReplaceAll(plain, "word", `<span><em>wo</em>rd</span>`)},
		{strings.Repeat(paragraph, 10), strings.Repeat(paragraph, 10), strings.Repeat(paragraph, 10)},
		{plain + `<div hidden>` + plain + `</div><script>` + plain + `</script>`},
	} {
		if got := count(pageTestEPUB(t, bodies, "", ""), FormatEPUB); got != base {
			t.Fatalf("markup or chapter split changed %d pages to %d", base, got)
		}
	}
	tiny := count(pageTestEPUB(t, []string{plain + strings.Repeat(`<img width="8" height="9"/>`, 51)}, "", ""), FormatEPUB)
	large := count(pageTestEPUB(t, []string{plain + strings.Repeat(`<img width="480" height="720"/>`, 51)}, "", ""), FormatEPUB)
	if large <= 2*tiny {
		t.Fatalf("image sizes: base %d, tiny %d, large %d", base, tiny, large)
	}
	for _, wrapper := range []string{
		`<p><svg width="480" height="720"><foreignObject>%s</foreignObject></svg></p>`,
		`<p><math><annotation-xml>%s</annotation-xml></math></p>`,
		`<p><template>%s</template></p>`,
	} {
		without := count(pageTestEPUB(t, []string{plain + fmt.Sprintf(wrapper, "")}, "", ""), FormatEPUB)
		with := count(pageTestEPUB(t, []string{plain + fmt.Sprintf(wrapper, plain)}, "", ""), FormatEPUB)
		if with != without {
			t.Fatalf("alternative content changed %d pages to %d: %s", without, with, wrapper)
		}
	}
	composed := count(pageTestEPUB(t, []string{strings.ReplaceAll(plain, "word", "caf\u00e9")}, "", ""), FormatEPUB)
	decomposed := count(pageTestEPUB(t, []string{strings.ReplaceAll(plain, "word", "cafe\u0301")}, "", ""), FormatEPUB)
	if composed != decomposed {
		t.Fatal("Unicode normalization changed page count")
	}
}

func TestEPUBReadyPageCounts(t *testing.T) {
	for _, tt := range []struct {
		name, meta, nav string
		ready, counted  int
	}{
		{"fixed", `<meta property="rendition:layout">pre-paginated</meta><meta property="schema:numberOfPages">99</meta>`, "", 2, 2},
		{"map requires estimate", "", `<nav epub:type="page-list"><a href="part0.xhtml#p169">169</a><a href="part1.xhtml#p170">170</a></nav>`, 0, 2},
		{"declared count already available", `<meta property="schema:numberOfPages">99</meta>`, `<nav epub:type="page-list"><a href="part0.xhtml#p169">169</a><a href="part1.xhtml#p170">170</a></nav>`, 99, 99},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := pageTestEPUB(t, []string{`<p id="p169">First</p>`, `<p id="p170">Second</p>`}, tt.meta, tt.nav)
			meta, err := ExtractEPUBMetadata(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			counted, err := CountPages(t.Context(), bytes.NewReader(raw), int64(len(raw)), FormatEPUB)
			if err != nil || meta.PageCount != tt.ready || counted != tt.counted {
				t.Fatalf("ready = %d, counted = %d, err = %v; want %d, %d", meta.PageCount, counted, err, tt.ready, tt.counted)
			}
		})
	}
}

func TestKindlePageCountUsesNativeImageReferences(t *testing.T) {
	for _, reference := range []string{`recindex="1"`, `src="kindle:embed:0002?mime=image/png"`} {
		t.Run(reference, func(t *testing.T) {
			doc := &KindleDocument{
				Flows: []KindleTextFlow{{MediaType: "text/html", Data: []byte(strings.Repeat(`<p>A short paragraph.</p>`, 300) + strings.Repeat(`<img `+reference+`>`, 51))}},
				Resources: []KindleResource{
					{MediaType: "font/ttf", EmbedIndex: 1},
					{MediaType: "image/png", EmbedIndex: 2, Href: "images/formula.png", Data: testPNGSize(t, 8, 9, color.Black)},
				},
			}
			tiny, err := kindlePageCount(t.Context(), doc)
			if err != nil {
				t.Fatal(err)
			}
			doc.Resources[1].Data = testPNGSize(t, 480, 720, color.Black)
			large, err := kindlePageCount(t.Context(), doc)
			if err != nil {
				t.Fatal(err)
			}
			if tiny <= 0 || large <= 2*tiny {
				t.Fatalf("native image dimensions lost: tiny %d, large %d", tiny, large)
			}
		})
	}
}

func TestPageCountOptionalHTMLEndTags(t *testing.T) {
	for _, tt := range []struct{ name, body, compact string }{
		{"paragraphs", strings.Repeat(`<p>A line of text.</p>`, 300), strings.Repeat(`<p>A line of text.`, 300)},
		{"list", `<ul>` + strings.Repeat(`<li>A line of text.</li>`, 300) + `</ul>`, `<ul>` + strings.Repeat(`<li>A line of text.`, 300) + `</ul>`},
		{"table", `<table>` + strings.Repeat(`<tr><td>First</td><td>Second</td></tr>`, 300) + `</table>`, `<table>` + strings.Repeat(`<tr><td>First<td>Second`, 300) + `</table>`},
		{"hidden paragraph", `<p hidden>Hidden</p><p>Visible</p>`, `<p hidden>Hidden<p>Visible`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var want, got pageExtent
			if err := htmlPageExtent(t.Context(), []byte(`<html><head><title>Example</title></head><body>`+tt.body+`</body></html>`), &want, nil); err != nil {
				t.Fatal(err)
			}
			if err := htmlPageExtent(t.Context(), []byte(`<html><head><title>Example</title><body>`+tt.compact+`</body></html>`), &got, nil); err != nil {
				t.Fatal(err)
			}
			if got.Pages() == 0 || got.Pages() != want.Pages() {
				t.Fatalf("optional end tags changed %d pages to %d", want.Pages(), got.Pages())
			}
		})
	}
}

func TestFB2EagerCountMatchesOnDemand(t *testing.T) {
	body := `<body><section>` + strings.Repeat(`<p>`+strings.Repeat("Text and dialogue. ", 15)+`</p>`, 100) + strings.Repeat(`<image href="#formula"/>`, 100) + `</section></body>`
	picture := testPNGSize(t, 8, 9, color.Black)
	raw := []byte(`<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0"><description><title-info><book-title>Example</book-title></title-info></description>` + body + `<binary id="formula" content-type="image/png">` + base64.StdEncoding.EncodeToString(picture) + `</binary></FictionBook>`)
	meta, _, _, err := ExtractFB2MetadataAndCover(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	n, err := CountPages(t.Context(), bytes.NewReader(raw), int64(len(raw)), FormatFB2)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 1 || meta.PageCount != n {
		t.Fatalf("eager %d, lazy %d", meta.PageCount, n)
	}
	encoded := base64.StdEncoding.EncodeToString(picture)
	spaced := bytes.Replace(raw, []byte(encoded), []byte(strings.Join(strings.Split(encoded, ""), " \t")), 1)
	other, _, _, err := ExtractFB2MetadataAndCover(bytes.NewReader(spaced), int64(len(spaced)))
	if err != nil || other.PageCount != n {
		t.Fatalf("base64 whitespace changed the image weight: %v, %v", other, err)
	}
	split := bytes.Replace(raw, []byte("</p>"), []byte(`</p></section></body><body name="notes"><section>`), 1)
	other, _, _, err = ExtractFB2MetadataAndCover(bytes.NewReader(split), int64(len(split)))
	if err != nil || other.PageCount != n {
		t.Fatalf("splitting FB2 into bodies changed its count: %v, %v", other, err)
	}
}

func TestDjVuPagesExcludeSharedResources(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
		want int
	}{
		{"single", testDJVUForm("DJVU", testDJVUChunk("INFO", []byte{0})), 1},
		{"bundled", testDJVUForm("DJVM", testDJVUFormChunk("DJVI", testDJVUChunk("Djbz", []byte{1})), testDJVUFormChunk("DJVU"), testDJVUFormChunk("DJVU")), 2},
		{"indirect", testDJVUForm("DJVM", testDJVUChunk("DIRM", []byte{0, 0, 3})), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CountPages(t.Context(), bytes.NewReader(tt.raw), int64(len(tt.raw)), FormatDJVU)
			if err != nil || got != tt.want {
				t.Fatalf("pages = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

func TestRTFCountExcludesMetadataAndBinaryDestinations(t *testing.T) {
	body := strings.Repeat(`A paragraph of text.\par `, 100)
	var base int
	for _, extra := range []string{"", `{\fonttbl{\f0 ` + strings.Repeat("Font name ", 1000) + `;}}{\pict ` + strings.Repeat("00", 10000) + `}`} {
		raw := []byte(`{\rtf1\ansi ` + extra + body + `}`)
		n, err := CountPages(t.Context(), bytes.NewReader(raw), int64(len(raw)), FormatRTF)
		if err != nil || n <= 0 {
			t.Fatalf("RTF count = %d, %v", n, err)
		}
		if base != 0 && n != base {
			t.Fatalf("RTF metadata or binary content changed %d pages to %d", base, n)
		}
		base = n
	}
}

func TestOfficePageCountKeepsLineBreaks(t *testing.T) {
	for _, tt := range []struct {
		kind Format
		raw  []byte
	}{
		{FormatDOCX, testDOCXZip(t, docxFixture{document: `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p>` + strings.Repeat(`<w:r><w:t>Line</w:t><w:br/></w:r>`, 360) + `</w:p></w:body></w:document>`})},
		{FormatODT, testODTZip(t, odtFixture{content: `<office:document-content xmlns:office="` + odtOfficeNamespace + `" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><office:text><text:p>` + strings.Repeat(`Line<text:line-break/>`, 360) + `</text:p></office:text></office:body></office:document-content>`})},
	} {
		t.Run(FormatKey(tt.kind), func(t *testing.T) {
			// A poem or dialogue can use line breaks within a single paragraph.
			// Losing those breaks collapses these ten reference pages into one.
			got, err := CountPages(t.Context(), bytes.NewReader(tt.raw), int64(len(tt.raw)), tt.kind)
			if err != nil || got != 10 {
				t.Fatalf("360 short lines = %d pages, %v; want 10", got, err)
			}
		})
	}
}
