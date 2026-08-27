package format

import (
	"encoding/binary"
	"io"
	"strings"

	"golang.org/x/net/html"

	"github.com/levmv/polka/internal/bookmeta"
)

const maxMOBIMetadataText = 64 << 10

// Some old Mobipocket producers wrote Unknown into every header field but
// emitted a regular title page at the start of the HTML stream. Keep this
// fallback deliberately narrow: read only the first bounded text record and
// accept only the complete producer shape, including its placeholder block and
// copyright line. Ordinary prose must never become catalog metadata merely
// because three nearby paragraphs happen to look like a title and author.
func mobiApplyLegacyTitlePageMetadata(meta *Metadata, r io.ReaderAt, size int64, record0 []byte, codepage uint32) {
	if meta == nil || len(record0) < palmDOCHeader || binary.BigEndian.Uint16(record0[12:14]) != 0 {
		return
	}
	ranges, ok := mobiRecordRanges(r, size)
	if !ok || len(ranges) < 2 || binary.BigEndian.Uint16(record0[8:10]) == 0 {
		return
	}
	raw, ok := mobiReadRecord(r, ranges[1], maxMOBIMetadataText)
	if !ok {
		return
	}
	trailingFlags := uint16(0)
	if len(record0) >= 0xf4 {
		trailingFlags = binary.BigEndian.Uint16(record0[0xf2:0xf4])
	}
	raw, err := mobiTrimTrailingEntries(raw, trailingFlags)
	if err != nil {
		return
	}
	switch binary.BigEndian.Uint16(record0[0:2]) {
	case mobiCompressionNone:
	case mobiCompressionPalmDOC:
		raw, err = palmDOCDecompress(raw, maxMOBIMetadataText)
		if err != nil {
			return
		}
	default:
		return
	}

	title, author, ok := mobiLegacyTitlePageMetadata(mobiDecode(raw, codepage))
	if !ok {
		return
	}
	if meta.Title == "" {
		meta.Title = title
	}
	if len(meta.Authors) == 0 {
		meta.Authors = []bookmeta.AuthorMeta{author}
	}
}

func mobiLegacyTitlePageMetadata(src string) (string, bookmeta.AuthorMeta, bool) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", bookmeta.AuthorMeta{}, false
	}
	body := mobiFirstHTMLElement(doc, "body")
	headTitle := mobiFirstHTMLElement(doc, "title")
	if body == nil || headTitle == nil || !mobiUnknownTextValue(mobiHTMLText(headTitle)) {
		return "", bookmeta.AuthorMeta{}, false
	}
	divs := mobiDirectHTMLElements(body, "div")
	if len(divs) < 2 || !mobiLegacyUnknownPlaceholder(divs[0]) {
		return "", bookmeta.AuthorMeta{}, false
	}
	paragraphs := mobiDirectHTMLElements(divs[1], "p")
	if len(paragraphs) < 4 {
		return "", bookmeta.AuthorMeta{}, false
	}
	title := mobiCleanString(mobiHTMLText(paragraphs[0]))
	by := mobiCleanString(mobiHTMLText(paragraphs[1]))
	authorText := mobiCleanString(mobiHTMLText(paragraphs[2]))
	copyright := strings.ToLower(mobiCleanString(mobiHTMLText(paragraphs[3])))
	if !usefulMOBITextValue(title) || len([]rune(title)) > 512 || !strings.EqualFold(by, "by") ||
		!usefulMOBITextValue(authorText) || len([]rune(authorText)) > 256 || !strings.HasPrefix(copyright, "copyright") {
		return "", bookmeta.AuthorMeta{}, false
	}
	author := mobiAuthor(authorText)
	if author.Name == "" {
		return "", bookmeta.AuthorMeta{}, false
	}
	return title, author, true
}

func mobiLegacyUnknownPlaceholder(node *html.Node) bool {
	h1 := mobiFirstHTMLElement(node, "h1")
	h3 := mobiFirstHTMLElement(node, "h3")
	if h1 == nil || h3 == nil || !mobiUnknownTextValue(mobiHTMLText(h1)) {
		return false
	}
	byline := strings.ToLower(mobiCleanString(mobiHTMLText(h3)))
	return byline == "by unknown" || byline == "by untitled"
}

func mobiUnknownTextValue(value string) bool {
	switch strings.ToLower(mobiCleanString(value)) {
	case "unknown", "untitled":
		return true
	default:
		return false
	}
}

func mobiFirstHTMLElement(node *html.Node, name string) *html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.ElementNode && strings.EqualFold(node.Data, name) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := mobiFirstHTMLElement(child, name); found != nil {
			return found
		}
	}
	return nil
}

func mobiDirectHTMLElements(node *html.Node, name string) []*html.Node {
	var out []*html.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, name) {
			out = append(out, child)
		}
	}
	return out
}

func mobiHTMLText(node *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			parts = append(parts, current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(parts, " ")
}
