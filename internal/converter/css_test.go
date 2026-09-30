package converter

import (
	"bytes"
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/format/mobi"
	"golang.org/x/net/html"
)

func TestCSSConversionLimits(t *testing.T) {
	src := kindleTestEPUB(t, false, func(files map[string][]byte) {
		files["OEBPS/style.css"] = []byte(strings.Repeat("@media screen {", 256) + `p{background:url(red.png)}` + strings.Repeat("}", 256))
	})
	for _, target := range []Target{TargetAZW3, TargetMOBI6} {
		t.Run(string(target), func(t *testing.T) {
			var out bytes.Buffer
			err := ConvertContext(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), target)
			if !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("CSS complexity must stop conversion, got %v", err)
			}
		})
	}
}

func TestMOBI6ComputedStyle(t *testing.T) {
	for _, tc := range []struct {
		name, body, stylesheet string
		want                   mobi6Style
	}{
		{"literal generated text", `<p>text</p>`, `p::before{content:"/* !important */ "}`, mobi6Style{before: "/* !important */ "}},
		{"quoted selector", `<p data-kind="one, &gt; two">chosen</p>`, `[data-kind="one, > two"]::before{content:"Start: "}`, mobi6Style{before: "Start: "}},
		{"absent attribute", `<p>text</p>`, `[data-kind=""]{display:none}`, mobi6Style{}},
		{"important declaration", `<p>text</p>`, `p{display:block !important; display:none}`, mobi6Style{}},
		{"important beats inline", `<p style="display:none">text</p>`, `p{display:block !important}`, mobi6Style{}},
		{"inherited emphasis", `<b><i style="font-style:inherit">text</i></b>`, "", mobi6Style{bold: true}},
		{"unused artwork", `<p>text</p>`, `.absent{background-image:url(missing.svg)}`, mobi6Style{}},
		{"nested selector", `<div class="outer"><div class="middle"><div class="middle"><p class="leaf">text</p></div></div></div>`, `.outer > .middle .leaf{font-weight:bold}`, mobi6Style{bold: true}},
		{"invalid selector", `<p class="1bad">text</p>`, `.1bad{display:none}`, mobi6Style{}},
		{"screen stylesheet", `<p>text</p>`, `@media screen{p::before{content:"Start: "}} @media print{p{display:none}}`, mobi6Style{before: "Start: "}},
		{"invalid hiding value", `<p>text</p>`, `p{display:"none"; display:none,}`, mobi6Style{}},
		{"display fallback", `<p>text</p>`, `p{display:none; display:inline flow-root}`, mobi6Style{}},
		{"selector priority", `<p id="priority" class="a">text</p>`, `#priority{display:block} .a.a.a.a.a.a.a.a.a.a.a{display:none}`, mobi6Style{}},
		{"inline priority", `<p id="priority" style="display:block">text</p>`, `#priority#priority#priority#priority#priority#priority#priority#priority#priority#priority#priority{display:none}`, mobi6Style{}},
		{"declaration recovery", `<p>text</p>`, `p{a:hover{font-style:normal} font-weight:600; font-style:italic; font-style:100}`, mobi6Style{bold: true, italic: true}},
		{"decoration shorthand", `<p>text</p>`, `p{text-decoration:underline solid red}`, mobi6Style{underline: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := ConversionOptions{OnWarning: func(message string) { t.Errorf("unexpected warning: %s", message) }}
			rules, err := mobi6ParseCSS(tc.stylesheet, nil, opts)
			if err != nil {
				t.Fatal(err)
			}
			root, err := html.Parse(strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			work, texts := 0, 0
			var visit func(*html.Node, mobi6Style)
			visit = func(n *html.Node, style mobi6Style) {
				if n.Type == html.ElementNode {
					style, err = mobi6ComputedStyle(n, rules, style, &work, opts)
					if err != nil {
						t.Fatal(err)
					}
				}
				if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
					texts++
					if style != tc.want {
						t.Errorf("style = %+v, want %+v", style, tc.want)
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					visit(c, style)
				}
			}
			visit(root, mobi6Style{})
			if texts != 1 {
				t.Fatalf("expected one styled text, got %d", texts)
			}
		})
	}
}

func TestMOBI6StylesheetImports(t *testing.T) {
	for _, tc := range []struct {
		name, stylesheet, linked string
	}{
		{"late import", `.visible{display:block} @import "hide.css";`, ""},
		{"nested import", `@media screen {@import "hide.css";}`, ""},
		{"print import", `@import "hide.css" print;`, ""},
		{"print link", "", `<link rel="stylesheet" href="hide.css" media="print"/>`},
		{"alternate link", "", `<link rel="alternate stylesheet" href="hide.css" title="Other"/>`},
		{"cyclic imports", `@import "shared.css"; .visible{font-style:italic}`, `<link rel="stylesheet" href="shared.css"/>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := kindleTestEPUB(t, false, func(files map[string][]byte) {
				files["OEBPS/style.css"] = []byte(tc.stylesheet)
				files["OEBPS/hide.css"] = []byte(`.visible{display:none}`)
				files["OEBPS/shared.css"] = []byte(`@import "style.css"; .visible{font-weight:bold}`)
				files["OEBPS/one.xhtml"] = []byte(`<html><head><link rel="stylesheet" href="style.css"/></head><body><p class="visible">First words</p></body></html>`)
				files["OEBPS/two.xhtml"] = []byte(`<html><head>` + tc.linked + `</head><body><p class="visible">Second words</p></body></html>`)
			})
			var out bytes.Buffer
			if err := ConvertContext(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetMOBI6); err != nil {
				t.Fatal(err)
			}
			doc, err := mobi.ExtractDocument(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			for _, words := range []string{"First words", "Second words"} {
				if tc.name == "cyclic imports" {
					words = "<b><i>" + words + "</i></b>"
				}
				if !bytes.Contains(doc.Flows[0].Data, []byte(words)) {
					t.Errorf("stylesheet selection lost %q", words)
				}
			}
		})
	}
}

func TestSVGStylesUseCompleteXMLText(t *testing.T) {
	const artwork = `<rect id="drawing" width="20" height="20"/><text>Diagram</text>`
	source := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><style>` +
		`/*<![CDATA[url(fake.svg)]]>*/ rect{fill:url(paint.<!-- keep --><![CDATA[svg#p]]>);}` +
		`</style><style><![CDATA[text{fill:url(paint.svg#p);}]]></style>` + artwork + `</svg>`)
	unchanged, err := rewriteSVGResources(t.Context(), source, func(_, href string) (string, error) { return href, nil })
	if err != nil || !bytes.Equal(unchanged, source) {
		t.Fatalf("no-op changed SVG: %s, %v", unchanged, err)
	}
	calls := 0
	got, err := rewriteSVGResources(t.Context(), source, func(_, href string) (string, error) {
		calls++
		if href != "paint.svg" {
			t.Errorf("unexpected resource %q", href)
		}
		return "new.svg", nil
	})
	if err != nil || calls != 2 || !bytes.Contains(got, []byte(artwork)) || !bytes.Contains(got, []byte("<!-- keep -->")) {
		t.Fatalf("SVG resources/artwork lost: %s, calls=%d, error=%v", got, calls, err)
	}
	var document struct {
		Styles []string `xml:"style"`
	}
	if err := xml.Unmarshal(got, &document); err != nil || len(document.Styles) != 2 {
		t.Fatalf("invalid rewritten SVG: %s, %v", got, err)
	}
	for i, want := range []string{`/*url(fake.svg)*/ rect{fill:url(new.svg#p);}`, `text{fill:url(new.svg#p);}`} {
		if document.Styles[i] != want {
			t.Errorf("CSS = %q, want %q", document.Styles[i], want)
		}
	}
}

func TestFontCleanupPreservesDeclarationValues(t *testing.T) {
	const custom = `p{--template:{@font-face{src:url(font.ttf)}}; color:red}`
	const unknown = `@future { @font-face{src:url(font.ttf)} }`
	const font = `@font-face{font-family:Book;src:url(font.ttf),local("Fallback")}`
	source := custom + unknown + `@media screen {` + font + `}`
	r := epubRecovery{omitted: map[string]bool{"OEBPS/font.ttf": true}}
	got, err := r.cleanFontCSS("OEBPS/style.css", []byte(source))
	want := custom + unknown + `@media screen {@font-face{font-family:Book;src:local("Fallback")}}`
	if err != nil || string(got) != want {
		t.Fatalf("font cleanup changed unrelated CSS: %s, %v", got, err)
	}
}
