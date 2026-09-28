package imagecodec

import (
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

// PrepareSVG recognizes UTF-8 SVG resources and compacts repeated glyph paths.
// Content that cannot be safely simplified is preserved.
func PrepareSVG(data []byte, name string) ([]byte, bool) {
	if !isSVGImageResource(data, name) || !utf8.Valid(data) {
		return nil, false
	}
	src := string(data)
	if strings.Contains(strings.ToLower(src), "<script") {
		return nil, false
	}
	return compactSVGPaths([]byte(stripSVGDoctype(src))), true
}

func isSVGImageResource(data []byte, name string) bool {
	if !strings.EqualFold(path.Ext(strings.TrimSpace(name)), ".svg") {
		return false
	}
	sample := bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	lower := bytes.ToLower(sample)
	return bytes.HasPrefix(lower, []byte("<svg")) ||
		(bytes.HasPrefix(lower, []byte("<?xml")) && bytes.Contains(lower, []byte("<svg")))
}

// Remove only a declaration in the prolog, preserving internal subsets that
// may define entities or attribute defaults used by the image.
func stripSVGDoctype(src string) string {
	dec := xml.NewDecoder(strings.NewReader(src))
	for {
		start := int(dec.InputOffset())
		token, err := dec.Token()
		if err != nil {
			return src
		}
		switch t := token.(type) {
		case xml.StartElement:
			return src
		case xml.Directive:
			if strings.HasPrefix(strings.ToUpper(string(t)), "DOCTYPE") {
				if bytes.Contains(t, []byte("[")) {
					return src
				}
				return src[:start] + src[int(dec.InputOffset()):]
			}
		}
	}
}

// compactSVGPaths removes only byte-identical leaf paths with the same ID
// directly inside the same defs element. All other bytes stay untouched.
// Styles, animation, foreign content or ambiguous IDs disable this optional
// optimization for the whole document; parsing is not an admission check.
func compactSVGPaths(data []byte) []byte {
	type element struct {
		name  string
		start int
		id    string
		empty bool
	}
	type definition struct {
		start, end int
		defs       int
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []element
	seen := make(map[string]definition)
	var cuts []definition
	removed := 0
	rootSeen := false
	for {
		start := int(dec.InputOffset())
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return data
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Space != "http://www.w3.org/2000/svg" || strings.HasPrefix(t.Name.Local, "animate") {
				return data
			}
			switch t.Name.Local {
			case "style", "script", "set", "discard", "foreignObject":
				return data
			}
			if len(stack) == 0 {
				if rootSeen || t.Name.Local != "svg" {
					return data
				}
				rootSeen = true
			} else {
				stack[len(stack)-1].empty = false
			}
			el := element{name: t.Name.Local, start: start, empty: true}
			for _, attr := range t.Attr {
				if attr.Name.Local == "style" || strings.HasPrefix(attr.Name.Local, "on") {
					return data
				}
				if attr.Name.Local == "id" {
					if attr.Name.Space != "" || el.id != "" {
						return data
					}
					el.id = attr.Value
				}
			}
			stack = append(stack, el)
		case xml.EndElement:
			el := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if el.id == "" {
				continue
			}
			def := definition{start: el.start, end: int(dec.InputOffset()), defs: -1}
			if el.empty && el.name == "path" && len(stack) > 0 && stack[len(stack)-1].name == "defs" {
				def.defs = stack[len(stack)-1].start
			}
			if first, exists := seen[el.id]; exists {
				if first.defs < 0 || first.defs != def.defs || !bytes.Equal(data[first.start:first.end], data[def.start:def.end]) {
					return data
				}
				cuts = append(cuts, def)
				removed += def.end - def.start
			} else {
				seen[el.id] = def
			}
		case xml.CharData, xml.Comment:
			if len(stack) > 0 {
				stack[len(stack)-1].empty = false
			}
		case xml.ProcInst:
			if t.Target != "xml" || len(stack) > 0 {
				return data
			}
		case xml.Directive:
			return data
		}
	}
	if len(cuts) == 0 {
		return data
	}
	out := make([]byte, 0, len(data)-removed)
	last := 0
	for _, cut := range cuts {
		out = append(out, data[last:cut.start]...)
		last = cut.end
	}
	return append(out, data[last:]...)
}
