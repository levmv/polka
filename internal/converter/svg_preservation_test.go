package converter

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/format/mobi"
	"github.com/levmv/polka/internal/testfixture"
)

func TestAZW3SVGReferences(t *testing.T) {
	const artwork = `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><defs><linearGradient id="paint"><stop offset="0" stop-color="red"/><stop offset="1" stop-color="blue"/></linearGradient></defs><rect id="icon" width="20" height="20" fill="url(#paint)"/></svg>`
	encoded := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(artwork))
	for _, tc := range []struct{ name, content, fragment string }{
		{"inline use", `<svg xmlns="http://www.w3.org/2000/svg"><use href="art.svg#icon"/></svg>`, "icon"},
		{"inline xlink", `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="art.svg#icon"/></svg>`, "icon"},
		{"inline paint", `<svg xmlns="http://www.w3.org/2000/svg"><rect width="20" height="20" fill="url(art.svg#paint)"/></svg>`, "paint"},
		{"base64 image", `<img src="` + encoded + `"/>`, ""},
		{"percent encoded object", `<object data="data:image/svg+xml,` + url.PathEscape(artwork) + `" type="image/svg+xml"></object>`, ""},
		{"CSS data image", `<p style="background-image:url('` + encoded + `')">Background</p>`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packageXML := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Artwork</dc:title><dc:language>en</dc:language></metadata><manifest><item id="body" href="body.xhtml" media-type="application/xhtml+xml"/><item id="art" href="art.svg" media-type="image/svg+xml"/></manifest><spine><itemref idref="body"/></spine></package>`)
			source := testfixture.EPUB(t, packageXML, map[string][]byte{
				"OEBPS/body.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Artwork</title></head><body>` + tc.content + `</body></html>`),
				"OEBPS/art.svg":    []byte(artwork),
			})
			var output bytes.Buffer
			if err := ConvertContextWithOptions(t.Context(), &output, bytes.NewReader(source), format.FormatEPUB, int64(len(source)), TargetAZW3, ConversionOptions{OnWarning: func(message string) { t.Error(message) }}); err != nil {
				t.Fatal(err)
			}
			document, err := mobi.ExtractDocument(bytes.NewReader(output.Bytes()), int64(output.Len()))
			if err != nil {
				t.Fatal(err)
			}
			if len(document.Resources) != 1 || document.Resources[0].MediaType != "image/svg+xml" || string(document.Resources[0].Data) != artwork {
				t.Fatal("SVG artwork or its local paint reference changed")
			}
			matches := regexp.MustCompile(`kindle:flow:([0-9A-V]+)\?mime=image/svg\+xml(?:#([^\s)"']+))?`).FindAllSubmatch(document.Flows[0].Data, -1)
			if len(matches) == 0 {
				t.Fatalf("missing SVG reference: %s", document.Flows[0].Data)
			}
			for _, match := range matches {
				flow, err := strconv.ParseUint(string(match[1]), 32, 32)
				if err != nil || int(flow) != document.Resources[0].FlowIndex || string(match[2]) != tc.fragment {
					t.Fatalf("SVG reference targets wrong artwork or fragment: %s", match[0])
				}
			}
			if tc.fragment != "" {
				var epub bytes.Buffer
				if err := ConvertContextWithOptions(t.Context(), &epub, bytes.NewReader(output.Bytes()), format.FormatAZW3, int64(output.Len()), TargetEPUB, ConversionOptions{OnWarning: func(message string) { t.Error(message) }}); err != nil {
					t.Fatal(err)
				}
				chapter := zipEntry(t, epub.Bytes(), "OEBPS/text.xhtml")
				if !strings.Contains(chapter, document.Resources[0].Href+"#"+tc.fragment) || strings.Contains(chapter, "kindle:flow:") {
					t.Fatal("inline SVG lost its packaged artwork or paint on round trip")
				}
			}
		})
	}
}

func TestSVGDependenciesSurviveConversion(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(converterTinyPNG)
	svg := `<?xml version="1.0"?><?xml-stylesheet href="../styles/diagram.css" type="text/css"?><svg xmlns="http://www.w3.org/2000/svg" width="40" height="20" viewBox="0 0 40 20"><view id="view" viewBox="0 0 40 20"/><image href="pixel.png" width="10" height="10"/><image href="data:image/png;base64,` + encoded[:40] + "&#10;" + encoded[40:] + `" x="30" width="10" height="10"/><text x="12" y="15">Diagram label</text></svg>`
	const body = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Diagrams</title></head><body><p>Before <object type="image/svg+xml" data="art/diagram.svg#view" width="40" height="20">Diagram fallback</object> after.</p></body></html>`
	const css = `text { fill: green; font-size: 12px; }`
	for _, route := range []string{"htmlz", "epub", "azw3"} {
		t.Run(route, func(t *testing.T) {
			files := map[string][]byte{
				"OEBPS/index.html":         []byte(body),
				"OEBPS/art/diagram.svg":    []byte(svg),
				"OEBPS/art/pixel.png":      converterTinyPNG,
				"OEBPS/styles/diagram.css": []byte(css),
			}
			from := format.FormatEPUB
			opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Diagrams</dc:title><dc:language>en</dc:language></metadata><manifest><item id="body" href="index.html" media-type="application/xhtml+xml"/><item id="diagram" href="art/diagram.svg" media-type="image/svg+xml"/><item id="pixel" href="art/pixel.png" media-type="image/png"/><item id="css" href="styles/diagram.css" media-type="text/css"/></manifest><spine><itemref idref="body"/></spine></package>`)
			src := testfixture.EPUB(t, opf, files)
			if route == "htmlz" {
				flat := map[string][]byte{}
				for name, data := range files {
					flat[strings.TrimPrefix(name, "OEBPS/")] = data
				}
				src = testZip(t, flat)
				from = format.FormatHTMLZ
			}
			opts := ConversionOptions{OnWarning: func(message string) { t.Error(message) }}
			if route == "azw3" {
				var azw bytes.Buffer
				if err := ConvertContextWithOptions(t.Context(), &azw, bytes.NewReader(src), from, int64(len(src)), TargetAZW3, opts); err != nil {
					t.Fatal(err)
				}
				src, from = azw.Bytes(), format.FormatAZW3
			}
			var out bytes.Buffer
			if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), from, int64(len(src)), TargetEPUB, opts); err != nil {
				t.Fatal(err)
			}
			zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			var foundObject, foundSVG, foundCSS, foundPNG bool
			for _, file := range zr.File {
				r, err := file.Open()
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(r)
				r.Close()
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(file.Name, ".xhtml") || strings.HasSuffix(file.Name, ".html") {
					foundObject = foundObject || bytes.Contains(raw, []byte(`<object `)) && bytes.Contains(raw, []byte(`.svg#view"`)) && bytes.Contains(raw, []byte(`Diagram fallback</object> after.`))
				}
				if strings.HasSuffix(file.Name, ".svg") {
					var drawing struct {
						ViewBox string `xml:"viewBox,attr"`
						Text    string `xml:"text"`
						Images  []struct {
							Href string `xml:"href,attr"`
						} `xml:"image"`
					}
					if err := xml.Unmarshal(raw, &drawing); err != nil {
						t.Fatal(err)
					}
					foundSVG = drawing.ViewBox == "0 0 40 20" && drawing.Text == "Diagram label"
					// Keep both packaged dependencies. The common EPUB checker below
					// verifies their paths from the SVG's new location.
					if !bytes.Contains(raw, []byte(".css")) || len(drawing.Images) != 2 || !strings.HasSuffix(drawing.Images[0].Href, ".png") {
						t.Fatal("SVG lost its stylesheet or packaged image")
					}
					value := strings.TrimPrefix(drawing.Images[1].Href, "data:image/png;base64,")
					image, err := base64.StdEncoding.DecodeString(value)
					if err != nil || !bytes.Equal(image, converterTinyPNG) {
						t.Fatal("embedded SVG image changed")
					}
				}
				foundCSS = foundCSS || bytes.Equal(raw, []byte(css))
				foundPNG = foundPNG || bytes.Equal(raw, converterTinyPNG)
			}
			if !foundObject || !foundSVG || !foundCSS || !foundPNG {
				t.Fatalf("preserved object=%v SVG=%v CSS=%v PNG=%v", foundObject, foundSVG, foundCSS, foundPNG)
			}
			if problems, err := checkEPUBInternalLinks(out.Bytes()); err != nil || len(problems) != 0 {
				t.Fatalf("broken output references: %v, %v", problems, err)
			}
		})
	}
}

func TestSVGArtworkSurvivesResourceRewriting(t *testing.T) {
	const artwork = `<rect width="40" height="20" fill="green"/><text x="2" y="15">Diagram label</text>`
	for _, tc := range []struct {
		name, svg string
		warning   bool
	}{
		{"prefixed namespace", `<?xml-stylesheet href="diagram.css" type="text/css"?><s:svg xmlns:s="http://www.w3.org/2000/svg" width="40" height="20"><s:rect width="40" height="20" fill="green"/><s:text x="2" y="15">Diagram label</s:text></s:svg>`, false},
		{"missing image", `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20">` + artwork + `<image href="missing.png" width="10" height="10"/></svg>`, true},
		{"missing stylesheet", `<?xml-stylesheet href="missing.css" type="text/css"?><svg xmlns="http://www.w3.org/2000/svg" width="40" height="20">` + artwork + `</svg>`, true},
		{"quoted data URL", `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><style>svg { background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='1' height='1'/%3E"); }</style>` + artwork + `</svg>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Diagrams</dc:title><dc:language>en</dc:language></metadata><manifest><item id="body" href="body.xhtml" media-type="application/xhtml+xml"/><item id="diagram" href="diagram.svg" media-type="image/svg+xml"/></manifest><spine><itemref idref="body"/></spine></package>`)
			src := testfixture.EPUB(t, opf, map[string][]byte{
				"OEBPS/body.xhtml":  []byte(`<html><body><img src="diagram.svg"/></body></html>`),
				"OEBPS/diagram.svg": []byte(tc.svg),
				"OEBPS/diagram.css": []byte(`text { fill: black; }`),
			})
			var out bytes.Buffer
			var warnings []string
			if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetAZW3, ConversionOptions{OnWarning: func(s string) { warnings = append(warnings, s) }}); err != nil {
				t.Fatal(err)
			}
			if (len(warnings) > 0) != tc.warning {
				t.Errorf("warnings: %v", warnings)
			}
			doc, err := mobi.ExtractDocument(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			var svg []byte
			for _, resource := range doc.Resources {
				if resource.MediaType == "image/svg+xml" {
					svg = resource.Data
				}
			}
			if !bytes.Contains(svg, []byte("Diagram label")) {
				t.Fatalf("SVG artwork lost; warnings: %v", warnings)
			}
			dec := xml.NewDecoder(bytes.NewReader(svg))
			for {
				token, err := dec.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if element, ok := token.(xml.StartElement); ok && element.Name.Space != "http://www.w3.org/2000/svg" {
					t.Errorf("SVG element lost its namespace: %v", element.Name)
				}
			}
			if strings.Contains(tc.svg, `url("data:`) && !bytes.Contains(svg, []byte(`url("data:`)) {
				t.Errorf("embedded CSS URL lost required quotes: %s", svg)
			}
		})
	}
}
