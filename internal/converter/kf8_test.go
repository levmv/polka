package converter

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/format/mobi"
	"github.com/levmv/polka/internal/testfixture"
	"golang.org/x/image/font/gofont/goregular"
)

func TestAZW3NavigationAndContents(t *testing.T) {
	for _, tc := range []struct {
		name, attributes, style, landmark, guide string
		ncx                                      bool
		wantContents                             string
	}{
		{name: "hidden navigation", attributes: ` hidden="hidden"`},
		{name: "CSS hidden navigation", style: `nav {display:none}`},
		{name: "landmark", attributes: ` hidden="hidden"`, landmark: `<nav epub:type="landmarks" hidden="hidden"><ol><li><a epub:type="toc" href="contents.xhtml#contents">Contents</a></li></ol></nav>`, wantContents: "Visible contents"},
		{name: "guide", attributes: ` hidden="hidden"`, guide: `<guide><reference type="toc" href="contents.xhtml#contents"/></guide>`, wantContents: "Visible contents"},
		{name: "NCX with contents page", ncx: true, guide: `<guide><reference type="toc" href="contents.xhtml#contents"/></guide>`, wantContents: "Visible contents"},
		{name: "NCX fallback", ncx: true, wantContents: "Contents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{
				"OEBPS/chapter.xhtml": []byte(`<html><body><h1 id="chapter">Chapter one</h1></body></html>`),
				"OEBPS/nav.xhtml":     []byte(`<html xmlns:epub="http://www.idpf.org/2007/ops"><head><style>` + tc.style + `</style></head><body><nav epub:type="toc"` + tc.attributes + `><ol><li><a href="chapter.xhtml#chapter">Chapter one</a></li></ol></nav>` + tc.landmark + `</body></html>`),
			}
			manifest := `<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>`
			spine := `<spine>`
			if tc.ncx {
				manifest += `<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>`
				spine = `<spine toc="ncx">`
				files["OEBPS/toc.ncx"] = []byte(`<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><navMap><navPoint><navLabel><text>Chapter one</text></navLabel><content src="chapter.xhtml#chapter"/></navPoint></navMap></ncx>`)
			} else {
				manifest += `<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`
			}
			if tc.wantContents == "Visible contents" {
				manifest += `<item id="contents" href="contents.xhtml" media-type="application/xhtml+xml"/>`
				files["OEBPS/contents.xhtml"] = []byte(`<html><body><h1 id="contents">Visible contents</h1><a href="chapter.xhtml#chapter">Chapter one</a></body></html>`)
			}
			packageXML := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Navigation</dc:title><dc:language>en</dc:language></metadata><manifest>` + manifest + `</manifest>` + spine + `<itemref idref="chapter"/></spine>` + tc.guide + `</package>`)
			source := testfixture.EPUB(t, packageXML, files)
			var output bytes.Buffer
			if err := ConvertContextWithOptions(t.Context(), &output, bytes.NewReader(source), format.FormatEPUB, int64(len(source)), TargetAZW3, ConversionOptions{OnWarning: func(message string) { t.Error(message) }}); err != nil {
				t.Fatal(err)
			}
			reader := bytes.NewReader(output.Bytes())
			info, err := mobi.Inspect(reader, reader.Size())
			if err != nil {
				t.Fatal(err)
			}
			document, err := mobi.ExtractDocument(reader, reader.Size())
			if err != nil {
				t.Fatal(err)
			}
			if len(document.Navigation) != 1 || document.Navigation[0].Label != "Chapter one" {
				t.Fatalf("chapter menu changed: %+v", document.Navigation)
			}
			guide := azw3TestGuide(output.Bytes(), info.GuideIndex)
			position, hasContents := guide["toc"]
			if hasContents != (tc.wantContents != "") {
				t.Fatalf("contents page guide = %v; want %q", hasContents, tc.wantContents)
			}
			if hasContents {
				start := int(info.KF8Fragments[position[0]].InsertOffset) + position[1]
				if !bytes.Contains(document.Flows[0].Data[start:min(start+150, len(document.Flows[0].Data))], []byte(tc.wantContents)) {
					t.Fatal("contents guide points at the wrong passage")
				}
			}
			if !hasContents && len(info.KF8Skeletons) != 2 {
				t.Fatal("hidden navigation gained an extra reading-order document")
			}
		})
	}
}

func azw3TestGuide(raw []byte, index uint32) map[string][2]int {
	offset := binary.BigEndian.Uint32(raw[78+8*(index+1):])
	record := raw[offset:]
	indexOffset := binary.BigEndian.Uint32(record[20:])
	count := binary.BigEndian.Uint32(record[24:])
	guide := map[string][2]int{}
	for entry := range count {
		start := int(binary.BigEndian.Uint16(record[indexOffset+4+entry*2:]))
		length := int(record[start])
		name := string(record[start+1 : start+1+length])
		cursor := start + length + 2
		var values [3]int
		for field := range values {
			for {
				current := record[cursor]
				cursor++
				values[field] = values[field]<<7 | int(current&0x7f)
				if current&0x80 != 0 {
					break
				}
			}
		}
		guide[name] = [2]int{values[1], values[2]}
	}
	return guide
}

func TestAZW3PreservesPublication(t *testing.T) {
	for _, ncx := range []bool{false, true} {
		src := kindleTestEPUB(t, ncx, func(files map[string][]byte) {
			files["OEBPS/font.ttf"] = goregular.TTF
			files["OEBPS/style.css"] = append(files["OEBPS/style.css"], []byte(`@font-face {font-family:Book;src:url("font.ttf")} body {font-family:Book} table {border:1px solid black}`)...)
			if !ncx {
				// EPUB navigation also permits a non-link label for a group.
				files["OEBPS/nav.xhtml"] = bytes.Replace(files["OEBPS/nav.xhtml"], []byte(`<a href="two.xhtml#same">Second</a>`), []byte(`<span>Second</span>`), 1)
			}
		})
		var out bytes.Buffer
		var warnings []string
		if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetAZW3, ConversionOptions{OnWarning: func(s string) { warnings = append(warnings, s) }}); err != nil {
			t.Fatal(err)
		}
		if len(warnings) > 0 {
			t.Fatalf("warnings: %v", warnings)
		}
		r := bytes.NewReader(out.Bytes())
		info, err := mobi.Inspect(r, r.Size())
		if err != nil {
			t.Fatal(err)
		}
		if info.Kind != mobi.KindKF8 || len(info.KF8Skeletons) != 4 || len(info.KF8Fragments) < 6 {
			t.Fatalf("structure: %+v", info)
		}
		doc, err := mobi.ExtractDocument(r, r.Size())
		if err != nil {
			t.Fatal(err)
		}
		if doc.Metadata.Title != "Metadata Проба" || doc.Metadata.Language != "ru" {
			t.Fatalf("metadata: %+v", doc.Metadata)
		}
		text := string(doc.Flows[0].Data)
		for _, want := range []string{"First chapter", "Second chapter", "Note body.", "Привет α 中 café.", strings.Repeat("абв α中é🙂 bytes across records. ", 400), `class="emphasis"`, `font-weight:normal`, `<table`, `https://example.org/`} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing text/markup: %.100s", want)
			}
		}
		var font, css, cover bool
		for _, r := range doc.Resources {
			font = font || bytes.Equal(r.Data, goregular.TTF)
			css = css || (r.MediaType == "text/css" && bytes.Contains(r.Data, []byte(`border:1px solid black`)))
			cover = cover || r.Cover
		}
		if !font || !css || !cover {
			t.Fatalf("resources: font=%v css=%v cover=%v", font, css, cover)
		}
		if len(doc.Navigation) != 3 || len(doc.Navigation[1].Children) != 1 || doc.Navigation[1].Children[0].Label != "Child" {
			t.Fatalf("navigation: %+v", doc.Navigation)
		}
		if info.GuideIndex == 0 {
			t.Fatal("missing guide index")
		}
		for label, want := range map[string]string{"Next chapter": "Second chapter", "Read note": "Note body.", "Return": "Привет α 中 café."} {
			m := regexp.MustCompile(`<a[^>]*href="kindle:pos:fid:([0-9A-V]+):off:([0-9A-V]+)"[^>]*>` + label + `</a>`).FindStringSubmatch(text)
			if len(m) != 3 {
				t.Fatalf("missing %s link", label)
			}
			fragment, _ := strconv.ParseInt(m[1], 32, 32)
			offset, _ := strconv.ParseInt(m[2], 32, 32)
			if fragment >= int64(len(info.KF8Fragments)) {
				t.Fatalf("invalid fragment %d", fragment)
			}
			position := int(info.KF8Fragments[fragment].InsertOffset) + int(offset)
			if position >= len(text) || !strings.Contains(text[position:min(len(text), position+160)], want) {
				t.Fatalf("%s jumps to wrong position %d", label, position)
			}
		}
		var again bytes.Buffer
		if err := ConvertContext(t.Context(), &again, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetAZW3); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), again.Bytes()) {
			t.Fatal("AZW3 output is nondeterministic")
		}
	}
}

func TestAZW3LinkPositionsPreserveLiteralMarkup(t *testing.T) {
	const literal = "kindle:pos:fid:0000:off:0000000000"
	// The comment and long CSS string contain markup resembling a generated
	// anchor. Neither may become a navigation destination, even across chunks.
	style := `p::before {content:'` + strings.Repeat(" ", 9000) + `<p aid="4">CSS example</p> ` + literal + `'}`
	body := `<html><body><p>Introduction.</p><p id="target">Destination.</p>` +
		`<!-- <p aid="4">Comment example</p> ` + literal + ` -->` +
		`<style><![CDATA[` + style + `]]></style><p data-example="` + literal + `">` + literal +
		`</p><a href="#target">Link</a></body></html>`
	opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Links</dc:title></metadata><manifest><item id="body" href="body.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="body"/></spine></package>`)
	src := testfixture.EPUB(t, opf, map[string][]byte{"OEBPS/body.xhtml": []byte(body)})
	var out bytes.Buffer
	if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetAZW3, ConversionOptions{OnWarning: func(s string) { t.Error(s) }}); err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(out.Bytes())
	doc, err := mobi.ExtractDocument(r, r.Size())
	if err != nil {
		t.Fatal(err)
	}
	info, err := mobi.Inspect(r, r.Size())
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc.Flows[0].Data)
	if strings.Count(text, literal) != 4 || !strings.Contains(text, style) {
		t.Fatal("link fixup changed literal text, attributes, comments or CSS")
	}
	link := regexp.MustCompile(`<a href="kindle:pos:fid:([0-9A-V]+):off:([0-9A-V]+)"`).FindStringSubmatch(text)
	if len(link) != 3 {
		t.Fatal("missing internal link")
	}
	fragment, _ := strconv.ParseUint(link[1], 32, 32)
	offset, _ := strconv.ParseUint(link[2], 32, 32)
	if fragment >= uint64(len(info.KF8Fragments)) {
		t.Fatalf("invalid link fragment: %d", fragment)
	}
	position := uint64(info.KF8Fragments[fragment].InsertOffset) + offset
	if position >= uint64(len(text)) || !strings.HasPrefix(text[position:], `<p id="target"`) {
		t.Fatal("link points to example markup instead of the destination element")
	}
}

func TestAZW3PreservesObfuscatedFont(t *testing.T) {
	identifier := "urn:uuid:00112233-4455-6677-8899-aabbccddeeff"
	for _, algorithm := range []string{idpfFontObfuscation, adobeFontObfuscation} {
		font := bytes.Clone(goregular.TTF)
		sum := sha1.Sum([]byte(identifier))
		key := sum[:]
		count := 1040
		if algorithm == adobeFontObfuscation {
			key = []byte{0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
			count = 1024
		}
		for i := range count {
			font[i] ^= key[i%len(key)]
		}
		opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Embedded font</dc:title><dc:identifier id="uid">` + identifier + `</dc:identifier><dc:language>en</dc:language></metadata><manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/><item id="font" href="font.ttf" media-type="font/ttf"/></manifest><spine><itemref idref="text"/></spine></package>`)
		src := testfixture.EPUB(t, opf, map[string][]byte{
			"OEBPS/text.xhtml":        []byte(`<html><head><style>@font-face{font-family:Book;src:url(font.ttf)} body{font-family:Book}</style></head><body><p>Font preserved.</p></body></html>`),
			"OEBPS/font.ttf":          font,
			"META-INF/encryption.xml": []byte(`<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><EncryptedData xmlns="http://www.w3.org/2001/04/xmlenc#"><EncryptionMethod Algorithm="` + algorithm + `"/><CipherData><CipherReference URI="OEBPS/font.ttf"/></CipherData></EncryptedData></encryption>`),
		})
		var out bytes.Buffer
		if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetAZW3, ConversionOptions{OnWarning: func(s string) { t.Error(s) }}); err != nil {
			t.Fatal(err)
		}
		doc, err := mobi.ExtractDocument(bytes.NewReader(out.Bytes()), int64(out.Len()))
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Resources) != 1 || !bytes.Equal(doc.Resources[0].Data, goregular.TTF) {
			t.Fatalf("font not recovered for %s", algorithm)
		}
	}
}

func TestAZW3CSSResources(t *testing.T) {
	for _, name := range []string{"style.css", "styles"} {
		t.Run(name, func(t *testing.T) {
			opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Styles</dc:title><dc:language>en</dc:language></metadata><manifest><item id="body" href="body.xhtml" media-type="application/xhtml+xml"/><item id="css" href="` + name + `" media-type="text/css"/></manifest><spine><itemref idref="body"/></spine></package>`)
			src := testfixture.EPUB(t, opf, map[string][]byte{
				"OEBPS/body.xhtml": []byte(`<html><head><link rel="stylesheet" href="` + name + `"/></head><body><p>Styles stay intact.</p></body></html>`),
				"OEBPS/" + name: []byte(`/* url(missing.png) */
@import /* shared */ "shared.css";
@namespace svg url("http://www.w3.org/2000/svg");
p::after {content:'url(missing.png) and @import "missing.css"'}
p {background-image:url("art\20 work.png"); mask:url("art work.png"); --art:image-set("alternate.png" 1x, url("art work.png") 2x);
   --label:@import "missing.css"; --future:@brand url("art work.png")}`),
				"OEBPS/shared.css":    []byte(`p {color:red}`),
				"OEBPS/art work.png":  converterTinyPNG,
				"OEBPS/alternate.png": converterTinyPNG,
			})
			var out bytes.Buffer
			if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetAZW3, ConversionOptions{OnWarning: func(s string) { t.Error(s) }}); err != nil {
				t.Fatal(err)
			}
			doc, err := mobi.ExtractDocument(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			var css strings.Builder
			for _, r := range doc.Resources {
				if r.MediaType == "text/css" {
					css.Write(r.Data)
				}
			}
			for _, want := range []string{
				`/* url(missing.png) */`, `content:'url(missing.png) and @import "missing.css"'`,
				`p {color:red}`, `@namespace svg url("http://www.w3.org/2000/svg")`,
				`@import /* shared */ url(kindle:flow:`, `background-image:url("kindle:embed:`,
				`mask:url("kindle:embed:`, `image-set("kindle:embed:`,
				`--label:@import "missing.css"`, `--future:@brand url("kindle:embed:`,
			} {
				if !strings.Contains(css.String(), want) {
					t.Errorf("missing %q in CSS: %s", want, css.String())
				}
			}
			var epub bytes.Buffer
			if err := ConvertContext(t.Context(), &epub, bytes.NewReader(out.Bytes()), format.FormatAZW3, int64(out.Len()), TargetEPUB); err != nil {
				t.Fatal(err)
			}
			styles := zipEntry(t, epub.Bytes(), "OEBPS/styles/flow-0001.css")
			if strings.Contains(styles, "kindle:") {
				t.Fatalf("Kindle reference remains after round trip: %s", styles)
			}
			for _, want := range []string{
				`image-set("../images/`, `--future:@brand url("../images/`,
				`--label:@import "missing.css"`, `@namespace svg url("http://www.w3.org/2000/svg")`,
			} {
				if !strings.Contains(styles, want) {
					t.Errorf("missing %q after round trip: %s", want, styles)
				}
			}
		})
	}
}

func TestAZW3PackagesSVGFilterImages(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><defs><filter id="f"><feImage href="image.png"/></filter></defs><rect width="10" height="10" filter="url(#f)"/></svg>`
	src := kindleTestEPUB(t, false, func(files map[string][]byte) {
		files["OEBPS/diagram.svg"] = []byte(svg)
		files["OEBPS/image.png"] = converterTinyPNG
		files["OEBPS/one.xhtml"] = bytes.Replace(files["OEBPS/one.xhtml"], []byte(`</body>`), []byte(`<img src="diagram.svg"/>`+svg+`</body>`), 1)
	})
	var out bytes.Buffer
	if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetAZW3, ConversionOptions{OnWarning: func(s string) { t.Error(s) }}); err != nil {
		t.Fatal(err)
	}
	doc, err := mobi.ExtractDocument(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var resources int
	for _, data := range append([]mobi.Resource{{MediaType: "image/svg+xml", Data: doc.Flows[0].Data}}, doc.Resources...) {
		if data.MediaType != "image/svg+xml" {
			continue
		}
		resources++
		if !bytes.Contains(data.Data, []byte(`href="data:image/png;base64,`)) || bytes.Contains(data.Data, []byte(`href="image.png"`)) {
			t.Errorf("SVG filter image not packaged: %s", data.Data)
		}
	}
	if resources != 2 {
		t.Fatalf("SVG resources: %d", resources)
	}
}
