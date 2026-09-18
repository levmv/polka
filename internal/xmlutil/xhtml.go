package xmlutil

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

var xhtmlNumericReferenceRE = regexp.MustCompile(`&#(?:x[0-9a-fA-F]+|[0-9]+);`)

// PrepareXHTMLForHTML preserves XML text and empty elements through HTML parsing.
// Raw-text elements retain their CDATA for XML serialization.
func PrepareXHTMLForHTML(raw []byte) ([]byte, error) {
	const cdataStart, cdataEnd = "<![CDATA[", "]]>"
	z := html.NewTokenizer(bytes.NewReader(raw))
	z.AllowCDATA(true)
	var out bytes.Buffer
	inRawText := false
	preserveLeadingNewline := false
	for {
		kind := z.Next()
		part := z.Raw()
		if kind == html.TextToken && !inRawText && bytes.HasPrefix(part, []byte(cdataStart)) {
			if !bytes.HasSuffix(part, []byte(cdataEnd)) {
				return nil, fmt.Errorf("unterminated XHTML CDATA section")
			}
			// Text() decodes entities; CDATA must keep them literal.
			part = []byte(html.EscapeString(string(part[len(cdataStart) : len(part)-len(cdataEnd)])))
		}
		if !inRawText && (kind == html.TextToken || kind == html.StartTagToken || kind == html.SelfClosingTagToken) && bytes.Contains(part, []byte("&#")) {
			// HTML remaps numeric references in U+0080–U+009F through
			// Windows-1252. Literal UTF-8 preserves their XML meaning.
			part = xhtmlNumericReferenceRE.ReplaceAllFunc(part, func(ref []byte) []byte {
				digits, base := ref[2:len(ref)-1], 10
				if digits[0] == 'x' {
					digits, base = digits[1:], 16
				}
				value, err := strconv.ParseUint(string(digits), base, 32)
				if err == nil && value >= 0x80 && value <= 0x9f {
					return []byte(string(rune(value)))
				}
				return ref
			})
		}
		if preserveLeadingNewline && kind == html.TextToken {
			text := html.UnescapeString(string(part))
			if strings.HasPrefix(text, "\n") || strings.HasPrefix(text, "\r") {
				// HTML parsing strips one initial newline; XHTML keeps it.
				out.WriteByte('\n')
			}
		}
		if kind == html.SelfClosingTagToken {
			name, _ := z.TagName()
			switch string(name) {
			case "area", "base", "br", "col", "embed", "hr", "img", "input", "keygen", "link", "meta", "param", "source", "track", "wbr":
				out.Write(part)
			default:
				out.Write(part[:len(part)-2])
				out.WriteString("></")
				out.Write(name)
				out.WriteByte('>')
			}
		} else {
			out.Write(part)
		}
		if kind != html.TextToken || len(part) > 0 {
			preserveLeadingNewline = false
		}
		switch kind {
		case html.SelfClosingTagToken:
			inRawText = false
			z.NextIsNotRawText()
		case html.StartTagToken:
			name, _ := z.TagName()
			preserveLeadingNewline = string(name) == "pre" || string(name) == "listing" || string(name) == "textarea"
			switch string(name) {
			case "script", "style", "xmp", "iframe", "noembed", "noframes", "noscript", "plaintext":
				inRawText = true
			default:
				inRawText = false
				z.NextIsNotRawText()
			}
		case html.EndTagToken:
			inRawText = false
		case html.ErrorToken:
			if err := z.Err(); err != io.EOF {
				return nil, fmt.Errorf("read XHTML content: %w", err)
			}
			return out.Bytes(), nil
		}
	}
}

// WalkXHTML decodes XML tokens and requires one XHTML html root. visit may be
// nil. Depth includes the current element for both start and end tokens.
func WalkXHTML(raw []byte, visit func(xml.Token, int) error) error {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	depth := 0
	roots := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if value.Name.Space != "http://www.w3.org/1999/xhtml" || value.Name.Local != "html" {
					return fmt.Errorf("root element is {%s}%s, want XHTML html", value.Name.Space, value.Name.Local)
				}
			}
			depth++
		}
		if visit != nil {
			if err := visit(token, depth); err != nil {
				return err
			}
		}
		if _, end := token.(xml.EndElement); end {
			depth--
		}
	}
	if roots != 1 {
		return fmt.Errorf("document has %d root elements, want 1", roots)
	}
	return nil
}

// StripXMLDeclaration removes the UTF-8 BOM and leading XML declaration.
func StripXMLDeclaration(raw []byte) []byte {
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	trimmed := bytes.TrimLeftFunc(raw, unicode.IsSpace)
	if !bytes.HasPrefix(bytes.ToLower(trimmed), []byte("<?xml")) {
		return raw
	}
	if _, after, ok := bytes.Cut(trimmed, []byte("?>")); ok {
		return after
	}
	return raw
}
