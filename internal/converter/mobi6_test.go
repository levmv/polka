package converter

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/testfixture"
)

func mobi6TestEPUB(t *testing.T, ncx bool, edit func(map[string][]byte)) []byte {
	t.Helper()
	var pngBytes bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 20, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 20; x++ {
			img.Set(x, y, color.RGBA{R: 220, G: 40, B: 20, A: 255})
		}
	}
	if err := png.Encode(&pngBytes, img); err != nil {
		t.Fatal(err)
	}
	header := `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><link rel="stylesheet" href="style.css"/></head><body>`
	files := map[string][]byte{
		"OEBPS/one.xhtml": []byte(header + `<h1 id="same">First chapter</h1><p id="return">Привет α 中 café. <a href="two.xhtml#same">Next chapter</a> <a href="notes.xhtml#note">Read note</a></p><p><span class="emphasis">Styled emphasis</span> <b style="font-weight:normal">plain</b> <span epub:type="stage">Aside</span>.</p><pre>code
    x &lt; 2 &amp;&amp; y
</pre><p>Before image<img src="red.png"/>after image.</p><table><tr><td>left cell</td><td>right cell</td></tr></table><p>` + strings.Repeat("абв α中é🙂 bytes across records. ", 400) + `</p></body></html>`),
		"OEBPS/two.xhtml":   []byte(header + `<h1 id="same">Second chapter</h1><h2 id="sub">Child section</h2><p>Body two. <a href="https://example.org/">Web</a></p></body></html>`),
		"OEBPS/notes.xhtml": []byte(header + `<p id="note">Note body. <a href="one.xhtml#return">Return</a></p></body></html>`),
		"OEBPS/nav.xhtml":   []byte(header + `<nav epub:type="toc" id="toc"><ol><li><a href="one.xhtml#same">First</a></li><li><a href="two.xhtml#same">Second</a><ol><li><a href="two.xhtml#sub">Child</a></li></ol></li><li><a href="notes.xhtml#note">Notes</a></li></ol></nav></body></html>`),
		"OEBPS/style.css":   []byte(`.emphasis {font-weight:bold; font-style:italic} [epub|type~="stage"]::before {content:"("} [epub|type~="stage"]::after {content:")"} pre {white-space:pre} p {text-indent:1em}`),
		"OEBPS/red.png":     pngBytes.Bytes(),
	}
	navItem := `<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`
	spine := `<spine>`
	if ncx {
		// Classic EPUB covers often use a full-image SVG viewport. It must
		// preserve the raster in place; cropped artwork gets a warning below.
		files["OEBPS/one.xhtml"] = bytes.Replace(files["OEBPS/one.xhtml"], []byte(`<img src="red.png"/>`), []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 12"><image href="red.png" width="20" height="12"/></svg>`), 1)
		delete(files, "OEBPS/nav.xhtml")
		files["OEBPS/toc.ncx"] = []byte(`<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><navMap><navPoint><navLabel><text>First</text></navLabel><content src="one.xhtml#same"/></navPoint><navPoint><navLabel><text>Second</text></navLabel><content src="two.xhtml#same"/><navPoint><navLabel><text>Child</text></navLabel><content src="two.xhtml#sub"/></navPoint></navPoint><navPoint><navLabel><text>Notes</text></navLabel><content src="notes.xhtml#note"/></navPoint></navMap></ncx>`)
		navItem = `<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>`
		spine = `<spine toc="ncx">`
	}
	opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Metadata Проба</dc:title><dc:creator>Ada Author</dc:creator><dc:language>ru</dc:language><dc:publisher>Press</dc:publisher><dc:description>Description.</dc:description><dc:date>2020-01-02</dc:date></metadata><manifest><item id="two" href="two.xhtml" media-type="application/xhtml+xml"/><item id="one" href="one.xhtml" media-type="application/xhtml+xml"/><item id="notes" href="notes.xhtml" media-type="application/xhtml+xml"/><item id="css" href="style.css" media-type="text/css"/><item id="cover" href="red.png" media-type="image/png" properties="cover-image"/>` + navItem + `</manifest>` + spine + `<itemref idref="one"/><itemref idref="two"/></spine></package>`)
	if edit != nil {
		edit(files)
	}
	return testfixture.EPUB(t, opf, files)
}

func TestMOBI6PreservesPublication(t *testing.T) {
	for _, tc := range []struct {
		name           string
		ncx, hiddenNav bool
	}{{"EPUB3", false, false}, {"NCX", true, false}, {"hidden EPUB navigation", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			src := mobi6TestEPUB(t, tc.ncx, func(files map[string][]byte) {
				if tc.hiddenNav {
					files["OEBPS/style.css"] = append(files["OEBPS/style.css"], []byte(`nav {display:none}`)...)
				}
			})
			var out bytes.Buffer
			if err := ConvertContext(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetMOBI6); err != nil {
				t.Fatal(err)
			}
			r := bytes.NewReader(out.Bytes())
			inspection, err := format.InspectKindle(r, r.Size(), format.FormatMOBI)
			if err != nil {
				t.Fatal(err)
			}
			if inspection.MOBIKind != format.MOBIKindMOBI6 || inspection.Encrypted || inspection.Compression != 2 || inspection.TextRecords < 3 {
				t.Fatalf("header: %+v", inspection)
			}
			doc, err := format.ExtractKindleDocument(r, r.Size(), format.FormatMOBI)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Metadata.Title != "Metadata Проба" || doc.Metadata.Language != "ru" || len(doc.Metadata.Authors) != 1 || doc.Metadata.Authors[0].Name != "Ada Author" || doc.Metadata.Publisher != "Press" || doc.Metadata.Description != "Description." || doc.Metadata.Date != "2020-01-02" {
				t.Fatalf("metadata: %+v", doc.Metadata)
			}
			text := string(doc.Flows[0].Data)
			for _, want := range []string{"Привет α 中 café.", strings.Repeat("абв α中é🙂 bytes across records. ", 400), "<b><i>Styled emphasis</i></b>", "&#160;&#160;&#160;&#160;x", "(Aside)", "<td>left cell</td><td>right cell</td>", `href="https://example.org/"`} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing preserved content %q", want[:min(len(want), 120)])
				}
			}
			if strings.Contains(text, "<b>plain</b>") {
				t.Fatal("CSS font-weight reset lost")
			}
			previous := -1
			for _, passage := range []string{"First chapter", "Second chapter", "Note body."} {
				pos := strings.Index(text, passage)
				if pos <= previous {
					t.Fatalf("passage %q missing or out of reading order", passage)
				}
				previous = pos
			}
			if !regexp.MustCompile(`Before image<img recindex="00001"[^>]*>after image\.`).MatchString(text) {
				t.Fatal("image placement changed")
			}
			for label, destination := range map[string]string{"Next chapter": "Second chapter", "Read note": "Note body.", "Return": "Привет α 中 café."} {
				match := regexp.MustCompile(`<a filepos="(\d+)">` + label + `</a>`).FindStringSubmatch(text)
				if len(match) != 2 {
					t.Fatalf("missing link %s", label)
				}
				pos, _ := strconv.Atoi(match[1])
				if pos < 0 || pos >= len(text) || !strings.Contains(text[pos:min(len(text), pos+130)], destination) {
					t.Fatalf("%s jumps to wrong location %d", label, pos)
				}
			}
			if len(doc.Navigation) != 4 {
				t.Fatalf("navigation: %+v", doc.Navigation)
			}
			if !regexp.MustCompile(`Second</a><(?:ol|ul)><li><a filepos="\d+">Child</a>`).MatchString(text) {
				t.Fatal("inline contents lost hierarchy")
			}
			for i, label := range []string{"First", "Second", "Child", "Notes"} {
				if doc.Navigation[i].Label != label {
					t.Fatalf("navigation %d: %+v", i, doc.Navigation[i])
				}
			}
			cover, _, err := format.ExtractMOBICover(r, r.Size())
			if err != nil {
				t.Fatal(err)
			}
			decoded, _, err := image.Decode(bytes.NewReader(cover))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Bounds().Dx() != 20 || decoded.Bounds().Dy() != 12 {
				t.Fatalf("image resized: %v", decoded.Bounds())
			}
			red, green, blue, _ := decoded.At(10, 6).RGBA()
			if red < 200*257 || green > 60*257 || blue > 40*257 {
				t.Fatal("cover pixels changed substantially")
			}
		})
	}
}

func TestMOBI6StableDownload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := mobi6TestEPUB(t, false, nil)
		convert := func() []byte {
			var out bytes.Buffer
			if err := ConvertContext(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetMOBI6); err != nil {
				t.Fatal(err)
			}
			return out.Bytes()
		}
		first := convert()
		time.Sleep(time.Hour)
		if !bytes.Equal(first, convert()) {
			t.Fatal("unchanged book produces a different download and sync identity")
		}
	})
}

func TestMOBI6Presentation(t *testing.T) {
	for _, tc := range []struct{ name, body, css, want, absent string }{
		{"literal comment markers", `<p class="literal">text</p>`, `.literal::before {content:"/* keep */ "}`, `/* keep */ text`, ""},
		{"quoted selector", `<p data-kind="one, &gt; two">chosen</p>`, `[data-kind="one, > two"]::before {content:"Start: "}`, `Start: chosen`, ""},
		{"absent attribute", `<p>Keep unmarked text</p>`, `[data-kind=""] {display:none}`, `Keep unmarked text`, ""},
		{"important declaration", `<p class="priority">Visible text</p>`, `.priority {display:block !important; display:none}`, `Visible text`, ""},
		{"inline comment", `<b style="font-weight:/* comment */ normal">Normal text</b>`, "", `Normal text`, `<b>Normal text</b>`},
		{"inherited emphasis", `<i style="font-style:inherit">Inherited text</i>`, "", `Inherited text`, `<i>Inherited text</i>`},
		{"unused artwork", `<p>Ordinary text</p>`, `.absent {background-image:url(missing.svg)}`, `Ordinary text`, ""},
		{"nested selector", `<div class="outer"><div class="middle"><div class="middle"><p class="leaf">Nested text</p></div></div></div>`, `.outer > .middle .leaf {font-weight:bold}`, `<b>Nested text</b>`, ""},
		{"invalid selector", `<p class="1bad">Keep this text</p>`, `.1bad {display:none}`, `Keep this text`, ""},
		{"screen stylesheet", `<p class="screen">Reading text</p>`, `@media screen {.screen::before {content:"Start: "}} @media print {.screen {display:none}}`, `Start: Reading text`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := mobi6TestEPUB(t, false, func(files map[string][]byte) {
				files["OEBPS/one.xhtml"] = bytes.Replace(files["OEBPS/one.xhtml"], []byte("</body>"), []byte(tc.body+"</body>"), 1)
				files["OEBPS/style.css"] = append(files["OEBPS/style.css"], []byte(tc.css)...)
			})
			var out bytes.Buffer
			if err := ConvertContext(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetMOBI6); err != nil {
				t.Fatal(err)
			}
			doc, err := format.ExtractKindleDocument(bytes.NewReader(out.Bytes()), int64(out.Len()), format.FormatMOBI)
			if err != nil {
				t.Fatal(err)
			}
			text := string(doc.Flows[0].Data)
			if !strings.Contains(text, tc.want) || tc.absent != "" && strings.Contains(text, tc.absent) {
				t.Fatalf("presentation lost: want %q, exclude %q", tc.want, tc.absent)
			}
		})
	}
}

func TestMOBI6RecoversContentWithWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, body, warning string
		wantText            []string
	}{
		{"missing image", `<img src="absent.png" alt="Image description"/>`, "image is not packaged", []string{"Image description"}},
		{"SVG", `<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0L1 1"/><text>Diagram label</text></svg>`, "rasterization", []string{"Diagram label"}},
		{"cropped SVG image", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="1 0 19 12"><image href="red.png" width="20" height="12"/></svg>`, "rasterization", nil},
		{"media and equations", `<audio src="voice.mp3">Transcript</audio><math><mtext>Equation description</mtext></math><object data="other.bin">Object description</object>`, "cannot preserve", []string{"Transcript", "Equation description", "Object description"}},
		{"missing fragment", `<a href="two.xhtml#absent">Jump</a>`, "link target is missing", []string{"Jump"}},
		{"CSS background", `<p style="background-image:url(red.png)">Picture</p>`, "background images", []string{"Picture"}},
		{"unsupported generated text", `<style>.a + .b::before {content:"Required words"}</style>`, "unsupported CSS rule", nil},
		{"conditional artwork", `<style>@media (min-width:30em) {p {background-image:url(red.png)}}</style>`, "unsupported CSS rule", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := mobi6TestEPUB(t, false, func(files map[string][]byte) {
				files["OEBPS/one.xhtml"] = bytes.Replace(files["OEBPS/one.xhtml"], []byte("</body>"), []byte(tc.body+"</body>"), 1)
			})
			var out bytes.Buffer
			var warnings []string
			err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetMOBI6, ConversionOptions{OnWarning: func(message string) { warnings = append(warnings, message) }})
			if err != nil || !strings.Contains(strings.Join(warnings, "\n"), tc.warning) {
				t.Fatalf("error=%v warnings=%v", err, warnings)
			}
			doc, err := format.ExtractKindleDocument(bytes.NewReader(out.Bytes()), int64(out.Len()), format.FormatMOBI)
			if err != nil || len(doc.Flows) != 1 {
				t.Fatalf("read converted book: %v", err)
			}
			for _, want := range append(tc.wantText, "Note body.", "Body two.") {
				if !bytes.Contains(doc.Flows[0].Data, []byte(want)) {
					t.Fatalf("lost readable content %q: %s", want, doc.Flows[0].Data)
				}
			}
		})
	}
}

func TestMOBI6KeepsReadableChapters(t *testing.T) {
	src := mobi6TestEPUB(t, false, func(files map[string][]byte) {
		delete(files, "OEBPS/two.xhtml")
		delete(files, "OEBPS/nav.xhtml")
	})
	var out bytes.Buffer
	hasWarnings := false
	err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetMOBI6, ConversionOptions{
		OnWarning: func(string) { hasWarnings = true },
	})
	if err != nil || !hasWarnings {
		t.Fatalf("conversion = %v, warnings = %v", err, hasWarnings)
	}
	doc, err := format.ExtractKindleDocument(bytes.NewReader(out.Bytes()), int64(out.Len()), format.FormatMOBI)
	if err != nil || len(doc.Flows) != 1 {
		t.Fatalf("read converted book: %v", err)
	}
	for _, want := range []string{"First chapter", "Note body."} {
		if !bytes.Contains(doc.Flows[0].Data, []byte(want)) {
			t.Fatalf("lost readable chapter %q", want)
		}
	}
}
