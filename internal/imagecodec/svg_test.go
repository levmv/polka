package imagecodec

import (
	"bytes"
	"testing"
)

func TestPrepareSVGPreservesContent(t *testing.T) {
	const glyph = `<path id="glyph" d="M0 0L1 1"/>`
	const nested = "<path id=\"glyph\" d=\"M0 0L1 1\">\n<title>Glyph</title>\n</path>"
	for _, tt := range []struct {
		name   string
		prolog string
		body   string
	}{
		{
			name: "conflicting shape after identical paths",
			body: "<defs>" + glyph + glyph + `<path id="glyph" d="M5 5L6 6"/></defs>`,
		},
		{
			name: "different definition contexts",
			body: `<defs fill="red">` + glyph + `</defs><defs fill="blue">` + glyph + "</defs>",
		},
		{
			name: "paths with children",
			body: "<defs>" + nested + nested + "</defs>",
		},
		{
			name: "styles after definitions",
			body: "<defs>" + glyph + glyph + `</defs><style>path:nth-child(2) { fill: red }</style>`,
		},
		{
			name: "animation after definitions",
			body: "<defs>" + glyph + glyph + `</defs><animate href="#glyph" attributeName="opacity" from="0" to="1" dur="1s"/>`,
		},
		{
			name: "DOCTYPE spelling in visible text",
			body: `<text><![CDATA[<!DOCTYPE svg>]]></text>`,
		},
		{
			name:   "DOCTYPE defining image content",
			prolog: `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY shape "M0 0L1 1">]>`,
			body:   `<path d="&shape;"/>`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svg := []byte(tt.prolog + `<svg xmlns="http://www.w3.org/2000/svg">` + tt.body + "</svg>")
			got, ok := PrepareSVG(svg, "image.svg")
			if !ok || !bytes.Equal(got, svg) {
				t.Fatalf("SVG content changed:\ngot  %s\nwant %s (accepted: %v)", got, svg, ok)
			}
		})
	}
}
