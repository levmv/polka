package converter

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/levmv/polka/internal/xmlutil"
)

func validXML10Char(r rune) bool {
	return xmlutil.ValidXML10Char(r)
}

func removeInvalidXML10Chars(raw []byte) []byte {
	return xmlutil.RemoveInvalidXML10Chars(raw)
}

var xhtmlNumericReferenceRE = regexp.MustCompile(`&#(?:x[0-9a-fA-F]+|[0-9]+);`)

// Preserve XML-specific text and empty-element semantics through HTML parsing.
// Raw-text elements retain their CDATA for XML serialization.
func prepareXHTMLForHTML(raw []byte) ([]byte, error) {
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
