package format

import (
	"bytes"
	"path"
	"strings"
	"unicode/utf8"
)

// Saved-web SVGs often carry external SVG DTDs and duplicate glyph IDs. EPUB 3
// readers may display them, but EPUBCheck rejects them, so generated EPUBs keep
// only SVG resources that this small sanitizer can make package-valid.
func epubSafeSVGResource(data []byte, name string) ([]byte, bool) {
	if !isSVGImageResource(data, name) || !utf8.Valid(data) {
		return nil, false
	}
	src := string(data)
	if strings.Contains(strings.ToLower(src), "<script") {
		return nil, false
	}
	src, ok := stripSVGDoctype(src)
	if !ok {
		return nil, false
	}
	src = dropDuplicateSVGIDDefinitions(src)
	if svgHasDuplicateIDs(src) {
		return nil, false
	}
	return []byte(src), true
}

func isSVGImageResource(data []byte, name string) bool {
	if !strings.EqualFold(path.Ext(strings.TrimSpace(name)), ".svg") {
		return false
	}
	sample := bytes.TrimSpace(trimUTF8BOM(data))
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	lower := bytes.ToLower(sample)
	return bytes.HasPrefix(lower, []byte("<svg")) ||
		(bytes.HasPrefix(lower, []byte("<?xml")) && bytes.Contains(lower, []byte("<svg")))
}

func stripSVGDoctype(src string) (string, bool) {
	for {
		idx := strings.Index(strings.ToLower(src), "<!doctype")
		if idx < 0 {
			return src, true
		}
		end := svgDoctypeEnd(src, idx+len("<!doctype"))
		if end < 0 {
			return "", false
		}
		src = src[:idx] + src[end:]
	}
}

func svgDoctypeEnd(src string, start int) int {
	var quote byte
	brackets := 0
	for i := start; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '[':
			brackets++
		case ']':
			if brackets > 0 {
				brackets--
			}
		case '>':
			if brackets == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func dropDuplicateSVGIDDefinitions(src string) string {
	var out strings.Builder
	lines := strings.SplitAfter(src, "\n")
	seen := make(map[string]bool)
	inDefs := false
	for _, line := range lines {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(trimmed, "<defs") {
			inDefs = true
		}
		if inDefs {
			if id, ok := firstXMLIDAttr(line); ok {
				if seen[id] {
					if strings.Contains(trimmed, "</defs") {
						inDefs = false
					}
					continue
				}
				seen[id] = true
			}
		}
		out.WriteString(line)
		if strings.Contains(trimmed, "</defs") {
			inDefs = false
		}
	}
	return out.String()
}

func svgHasDuplicateIDs(src string) bool {
	seen := make(map[string]bool)
	for {
		id, next, ok := nextXMLIDAttr(src)
		if !ok {
			return false
		}
		if seen[id] {
			return true
		}
		seen[id] = true
		src = src[next:]
	}
}

func firstXMLIDAttr(src string) (string, bool) {
	id, _, ok := nextXMLIDAttr(src)
	return id, ok
}

func nextXMLIDAttr(src string) (string, int, bool) {
	lower := strings.ToLower(src)
	offset := 0
	for {
		idx := strings.Index(lower[offset:], "id")
		if idx < 0 {
			return "", 0, false
		}
		idx += offset
		if idx > 0 && isXMLNameByte(lower[idx-1]) {
			offset = idx + len("id")
			continue
		}
		pos := idx + len("id")
		if pos < len(lower) && isXMLNameByte(lower[pos]) {
			offset = pos
			continue
		}
		for pos < len(src) && isXMLSpace(src[pos]) {
			pos++
		}
		if pos >= len(src) || src[pos] != '=' {
			offset = pos
			continue
		}
		pos++
		for pos < len(src) && isXMLSpace(src[pos]) {
			pos++
		}
		if pos >= len(src) || (src[pos] != '"' && src[pos] != '\'') {
			offset = pos
			continue
		}
		quote := src[pos]
		valueStart := pos + 1
		valueEnd := strings.IndexByte(src[valueStart:], quote)
		if valueEnd < 0 {
			return "", 0, false
		}
		valueEnd += valueStart
		return src[valueStart:valueEnd], valueEnd + 1, true
	}
}

func isXMLNameByte(b byte) bool {
	return b == ':' || b == '_' || b == '-' || b == '.' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}
