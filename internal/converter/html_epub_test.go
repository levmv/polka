package converter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/format/mobi"
)

func TestHTMLPublicationPreservesDiagramsEquationsAndStructure(t *testing.T) {
	const body = `<h1>Chapter</h1><p>Before <svg xmlns="http://www.w3.org/2000/svg" xml:id="heading-1" width="120" height="80" viewBox="0 0 120 80" onload="evil()">
<title>Diagram</title><defs><linearGradient id="paint"><stop offset="0" stop-color="green"/><stop offset="1" stop-color="blue"/></linearGradient><path id="shape" d="M 0 0 L 30 30 L 0 30 Z"/></defs>
<style>.shape { fill: url('#paint'); transform: translate3d(4px, 0, 0); }</style><use href="#shape" class="shape"/><image href="IMAGE" x="40" y="0" width="24" height="16"/><text x="0" y="60" xml:space="preserve">A  &amp; B</text><script>evil()</script></svg> after.</p>
<p>Fraction: <math xmlns="http://www.w3.org/1998/Math/MathML" id="equation"><semantics><mfrac><mn>1</mn><mn>2</mn></mfrac><annotation encoding="application/x-tex">\frac{1}{2}</annotation></semantics></math>.</p>
<ol start="7" reversed="reversed" type="I"><li>Seven</li><li value="12">Twelve</li></ol>
<table><colgroup span="2"></colgroup><tbody><tr><th id="name" scope="row" rowspan="2">Shared</th><td headers="name" colspan="2">First</td></tr><tr><td>Second</td><td>Third</td></tr></tbody></table>`
	for _, source := range []string{"html", "prefixed-xhtml", "htmlz", "kindle"} {
		t.Run(source, func(t *testing.T) {
			href := "data:image/png;base64," + base64.StdEncoding.EncodeToString(converterTinyPNG)
			if source == "htmlz" {
				href = "drawing.svg#crop"
			} else if source == "kindle" {
				href = "kindle:embed:0001?mime=image/png"
			}
			raw := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Publication</title></head><body>` + strings.ReplaceAll(body, "IMAGE", href) + `</body></html>`)
			if source == "html" {
				// A saved HTML page may have damaged attributes. Its reading
				// content and relationships must survive just like the clean XML.
				raw = []byte(strings.NewReplacer(
					`<svg `, `<svg broken"="ignored" editor:label="drawing" id="" `,
					`start="7"`, `start=" +007.0 "`, `scope="row"`, `scope="ROW"`,
					`<td>Second`, `<td colspan="">Second`,
				).Replace(string(raw)))
			}
			if source == "kindle" {
				raw = []byte(strings.NewReplacer(`<svg `, `<svg:svg `, `</svg>`, `</svg:svg>`, `<path `, `<svg:path `).Replace(string(raw)))
			}
			if source == "prefixed-xhtml" {
				raw = []byte(strings.NewReplacer(
					`<svg `, `<s:svg xmlns:s="http://www.w3.org/2000/svg" xmlns:q="http://www.w3.org/1999/xlink" `, `</svg>`, `</s:svg>`,
					`<linearGradient `, `<s:linearGradient `, `</linearGradient>`, `</s:linearGradient>`,
					`<math `, `<m:math xmlns:m="http://www.w3.org/1998/Math/MathML" `, `</math>`, `</m:math>`,
					`<mfrac>`, `<m:mfrac>`, `</mfrac>`, `</m:mfrac>`,
					`<mn>`, `<m:mn>`, `</mn>`, `</m:mn>`, `href="#shape"`, `q:href="#shape"`,
				).Replace(string(raw)))
			}
			var out bytes.Buffer
			var err error
			opts := ConversionOptions{OnWarning: func(message string) { t.Errorf("unexpected warning: %s", message) }}
			if source == "kindle" {
				err = convertMOBIDocumentToEPUB(context.Background(), &out, &mobi.Document{
					Metadata:  &format.Metadata{Title: "Publication", Language: "en"},
					Flows:     []mobi.TextFlow{{MediaType: "text/html", Data: raw}},
					Resources: []mobi.Resource{{ID: "picture", Href: "images/picture.png", MediaType: "image/png", Data: converterTinyPNG, EmbedIndex: 1}},
				}, opts)
			} else {
				kind := format.FormatHTML
				if source == "prefixed-xhtml" {
					kind = format.FormatXHTML
				} else if source == "htmlz" {
					kind = format.FormatHTMLZ
					raw = testZip(t, map[string][]byte{
						"index.html":  raw,
						"drawing.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100"><view id="crop" viewBox="100 0 100 100"/><rect x="100" width="100" height="100"/></svg>`),
					})
				}
				err = ConvertContextWithOptions(context.Background(), &out, bytes.NewReader(raw), kind, int64(len(raw)), TargetEPUB, opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
			for _, want := range []string{
				`viewBox="0 0 120 80"`, `<linearGradient id="paint">`, `xlink:href="#shape"`,
				`xml:id="heading-1"`, `transform: translate3d(4px, 0, 0)`,
				`fill: url(&#39;#paint&#39;)`, `xml:space="preserve">A  &amp; B</text>`, `</svg> after.`,
				`<mfrac><mn>1</mn><mn>2</mn></mfrac>`, `>\frac{1}{2}</annotation>`,
				`<ol start="7" reversed="reversed" type="I">`, `<li value="12">`,
				`<colgroup span="2">`, `rowspan="2"`, `scope="row"`, `headers="name"`, `colspan="2"`, `<td>Second</td>`,
			} {
				if !strings.Contains(xhtml, want) {
					t.Errorf("missing %q in %s", want, xhtml)
				}
			}
			if source == "htmlz" && !strings.Contains(xhtml, `xlink:href="images/image1.svg#crop"`) {
				t.Fatalf("lost SVG view fragment: %s", xhtml)
			}
			if strings.Contains(xhtml, "evil()") || strings.Contains(xhtml, "onload") {
				t.Fatalf("retained executable content: %s", xhtml)
			}
			// An independent XML reader checks the actual namespaces, not only
			// names which an HTML reader might repair after a broken serialization.
			decoder := xml.NewDecoder(strings.NewReader(xhtml))
			namespaces := map[string]string{}
			ids := map[string]bool{}
			for {
				token, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if start, ok := token.(xml.StartElement); ok {
					namespaces[start.Name.Local] = start.Name.Space
					for _, attr := range start.Attr {
						if attr.Name.Local == "id" {
							if ids[attr.Value] {
								t.Fatalf("duplicate publication anchor %q", attr.Value)
							}
							ids[attr.Value] = true
						}
					}
				}
			}
			if namespaces["linearGradient"] != "http://www.w3.org/2000/svg" || namespaces["mfrac"] != "http://www.w3.org/1998/Math/MathML" {
				t.Fatalf("wrong namespaces: %v", namespaces)
			}
			if opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf"); !strings.Contains(opf, `properties="svg mathml"`) {
				t.Fatalf("missing foreign content declarations: %s", opf)
			}
			if problems, err := checkEPUBInternalLinks(out.Bytes()); err != nil || len(problems) != 0 {
				t.Fatalf("broken publication references: %v, %v", problems, err)
			}
		})
	}
}

func TestHTMLForeignContentRecoversWithWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantText   []string
	}{
		{"unavailable artwork", `<p><a href="#lost">Diagram</a></p><svg xmlns:q="http://www.w3.org/1999/xlink"><image id="lost" q:href="https://example.org/image.png"/><use href="#lost"/><use href="other.svg#shape"/></svg>`, nil},
		{"unsupported styling", `<svg><style>@import "https://example.org/style.css";</style><path fill="url(https://example.org/paint.svg#paint)"/><path style="fill:u\72l(https://example.org/paint)"/><path style="background:image-set('https://example.org/image.png' 1x)"/></svg>`, nil},
		{"animation", `<svg><set attributeName="href" to="https://example.org/image.png"/></svg>`, nil},
		{"fallback text", `<svg><foreignObject><div>Required HTML drawing</div></foreignObject></svg><math><mi><mglyph src="missing.png" alt="Required glyph"/></mi></math>`, []string{"Required HTML drawing", "Required glyph"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte("<html><body>Before" + tc.body + "after</body></html>")
			var out bytes.Buffer
			warnings := 0
			err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(raw), format.FormatHTML, int64(len(raw)), TargetKEPUB, ConversionOptions{OnWarning: func(string) { warnings++ }})
			if err != nil || warnings == 0 {
				t.Fatalf("conversion = %v, warnings = %d", err, warnings)
			}
			xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
			for _, want := range append(tc.wantText, "Before", "after") {
				if !strings.Contains(xhtml, want) {
					t.Fatalf("lost readable content %q: %s", want, xhtml)
				}
			}
			if strings.Contains(xhtml, "https://example.org/") {
				t.Fatalf("retained unpackaged dependency: %s", xhtml)
			}
			if problems, err := checkEPUBInternalLinks(out.Bytes()); err != nil || len(problems) != 0 {
				t.Fatalf("broken publication references: %v, %v", problems, err)
			}
		})
	}
}
