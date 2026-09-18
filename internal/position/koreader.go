package position

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// KOReaderKey normalizes optional first-child indices for comparison.
// Unknown address formats retain their literal spelling.
func KOReaderKey(address string) string {
	path, offset, err := parseXPointer(address)
	if err != nil {
		return address
	}
	return formatXPointer(path[1].index, path[2:], offset)
}

// KOReaderToCFI maps a KOReader XPointer to a CFI in the supplied EPUB.
// Text points retain character precision when independent of reader settings;
// otherwise they resume at the containing paragraph.
func KOReaderToCFI(ctx context.Context, src io.ReaderAt, size int64, xpointer string) (string, error) {
	path, offset, err := parseXPointer(xpointer)
	if err != nil {
		return "", err
	}
	book, err := readEPUB(ctx, src, size)
	if err != nil {
		return "", err
	}
	spine, err := koReaderSpine(book)
	if err != nil {
		return "", err
	}
	if path[1].index > len(spine) {
		return "", fmt.Errorf("XPointer does not identify an unambiguous XHTML spine resource")
	}
	ref := spine[path[1].index-1]
	raw, chapter, err := readChapter(ctx, book.files[ref])
	if err != nil {
		return "", err
	}
	elementPath := path[3:]
	if offset >= 0 {
		elementPath = elementPath[:len(elementPath)-1]
	}
	element, err := resolveXPointer(chapter.body, elementPath)
	if err != nil {
		return "", err
	}
	if offset < 0 {
		return formatCFI(ref, element, -1), nil
	}
	var result string
	err = walkKOReaderText(ctx, raw, func(text koReaderText) bool {
		if !slices.Equal(path[2:], text.path) {
			return false
		}
		offsets := text.sourceOffsets()
		if offset >= len(offsets) || offsets[offset] < 0 {
			return true
		}
		for _, run := range chapter.runs {
			if run.start <= text.start && text.start < run.end {
				result = formatCFI(ref, run, text.start+offsets[offset]-run.start)
				break
			}
		}
		return true
	})
	if err != nil {
		return "", err
	}
	if result != "" {
		return result, nil
	}
	if paragraph := koReaderParagraph(element); paragraph != nil {
		return formatCFI(ref, paragraph, -1), nil
	}
	return "", fmt.Errorf("XPointer has neither a stable text position nor a containing paragraph")
}

// CFIToKOReader maps an EPUB CFI to a KOReader XPointer. Ranges use their start;
// character positions that depend on reader settings use the containing paragraph.
func CFIToKOReader(ctx context.Context, src io.ReaderAt, size int64, cfi string) (string, error) {
	point, err := parseCFI(cfi)
	if err != nil {
		return "", err
	}
	book, err := readEPUB(ctx, src, size)
	if err != nil {
		return "", err
	}
	ref, err := resolveCFI(book.opf, point.packagePath)
	if err != nil {
		return "", err
	}
	spine, err := koReaderSpine(book)
	if err != nil {
		return "", err
	}
	chapterIndex := slices.Index(spine, ref) + 1
	if chapterIndex == 0 {
		return "", fmt.Errorf("CFI does not identify an unambiguous XHTML spine resource")
	}
	raw, chapter, err := readChapter(ctx, book.files[ref])
	if err != nil {
		return "", err
	}
	n, err := resolveCFI(chapter.root, point.contentPath)
	if err != nil || point.offset > n.end-n.start {
		return "", fmt.Errorf("CFI does not identify chapter content")
	}
	if n.name != "" {
		steps := nodeXPointer(n)
		if len(steps) == 0 || steps[0] != (xpointerStep{"body", 1}) {
			return "", fmt.Errorf("CFI does not identify chapter content")
		}
		return formatXPointer(chapterIndex, steps, -1), nil
	}
	var result string
	err = walkKOReaderText(ctx, raw, func(text koReaderText) bool {
		end := text.start + utf16Len(text.text)
		target := n.start + point.offset
		if text.start < n.start || text.start >= n.end || target < text.start || target > end {
			return false
		}
		if target == end && end < n.end {
			return false
		}
		if offset := slices.Index(text.sourceOffsets(), target-text.start); offset >= 0 {
			result = formatXPointer(chapterIndex, text.path, offset)
		}
		return true
	})
	if err != nil {
		return "", err
	}
	if result != "" {
		return result, nil
	}
	if paragraph := koReaderParagraph(n); paragraph != nil {
		return formatXPointer(chapterIndex, nodeXPointer(paragraph), -1), nil
	}
	return "", fmt.Errorf("CFI has neither a stable text position nor a containing paragraph")
}

// Use semantic text blocks, never an arbitrary div that may contain a chapter.
// Element anchors avoid CSS-dependent text-node numbering and whitespace.
func koReaderParagraph(n *cfiNode) *cfiNode {
	var paragraph *cfiNode
	for ; n != nil; n = n.parent {
		switch n.name {
		case "script", "style", "textarea", "svg", "math", "template":
			return nil
		case "p", "li", "dt", "dd", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "td", "th":
			if paragraph == nil {
				if n.end == n.start {
					return nil
				}
				paragraph = n
			}
		}
	}
	return paragraph
}

type xpointerStep struct {
	name  string
	index int
}

func resolveXPointer(root *cfiNode, steps []xpointerStep) (*cfiNode, error) {
	current := root
	for _, step := range steps {
		var next *cfiNode
		index := 0
		for _, child := range current.children {
			if child.name == step.name {
				index++
				if index == step.index {
					next = child
					break
				}
			}
		}
		if next == nil {
			return nil, fmt.Errorf("XPointer element does not resolve in this chapter")
		}
		current = next
	}
	return current, nil
}

func nodeXPointer(n *cfiNode) []xpointerStep {
	var steps []xpointerStep
	for ; n.parent != nil; n = n.parent {
		index := 1
		for _, sibling := range n.parent.children {
			if sibling == n {
				break
			}
			if sibling.name == n.name {
				index++
			}
		}
		steps = append(steps, xpointerStep{n.name, index})
	}
	slices.Reverse(steps)
	return steps
}

func formatXPointer(chapter int, steps []xpointerStep, offset int) string {
	var path strings.Builder
	fmt.Fprintf(&path, "/body[1]/DocFragment[%d]", chapter)
	for _, step := range steps {
		fmt.Fprintf(&path, "/%s[%d]", step.name, step.index)
	}
	if offset >= 0 {
		fmt.Fprintf(&path, ".%d", offset)
	}
	return path.String()
}

func parseXPointer(raw string) ([]xpointerStep, int, error) {
	invalid := fmt.Errorf("unsupported KOReader XPointer")
	if len(raw) > 4096 || !strings.HasPrefix(raw, "/") {
		return nil, 0, invalid
	}
	offset := -1
	var rest string
	var err error
	if point := strings.LastIndexByte(raw, '.'); point >= 0 {
		offset, rest, err = parseNumber(raw[point+1:])
		if err != nil || rest != "" {
			return nil, 0, invalid
		}
		raw = raw[:point]
	}
	var steps []xpointerStep
	for _, part := range strings.Split(raw, "/")[1:] {
		step := xpointerStep{name: part, index: 1}
		if name, index, ok := strings.Cut(part, "["); ok {
			step.name = name
			step.index, rest, err = parseNumber(index)
			if err != nil || rest != "]" || step.index == 0 {
				return nil, 0, invalid
			}
		}
		if step.name != "text()" {
			for i, c := range step.name {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '_')) {
					return nil, 0, invalid
				}
			}
		}
		if step.name == "" || len(steps) >= maxDepth+2 {
			return nil, 0, invalid
		}
		steps = append(steps, step)
	}
	if len(steps) < 3 || steps[0] != (xpointerStep{"body", 1}) || steps[1].name != "DocFragment" || steps[2] != (xpointerStep{"body", 1}) {
		return nil, 0, invalid
	}
	if steps[len(steps)-1].name == "text()" {
		if offset < 0 {
			return nil, 0, invalid
		}
	} else if offset > 0 {
		// Other element offsets count children, whose text nodes may vary
		// with rendering settings. Zero is the element's start.
		return nil, 0, invalid
	} else {
		offset = -1
	}
	return steps, offset, nil
}

// Older DOM versions omit non-XHTML spine items; newer ones keep placeholders.
// Do not infer a chapter number when either interpretation can shift it.
func koReaderSpine(book *epubBook) ([]*cfiNode, error) {
	xhtml := make(map[string]string)
	for _, item := range book.opf.child("manifest").children {
		if item.name == "item" && item.attr("media-type") == "application/xhtml+xml" {
			href, err := url.PathUnescape(item.attr("href"))
			if err == nil && href != "" && !strings.HasPrefix(href, "/") {
				xhtml[item.attr("id")] = path.Join(path.Dir(book.opfPath), href)
			}
		}
	}
	var spine []*cfiNode
	for _, ref := range book.opf.child("spine").children {
		if ref.name != "itemref" {
			continue
		}
		file := book.files[ref]
		// Converter recovery may find a differently named ZIP member. Native
		// KOReader may instead skip it or create an error chapter.
		if file == nil || xhtml[ref.attr("idref")] != file.Name {
			return nil, fmt.Errorf("KOReader mapping requires an unambiguous XHTML spine")
		}
		spine = append(spine, ref)
	}
	return spine, nil
}

type koReaderText struct {
	path         []xpointerStep
	start        int // UTF-16 offset in the source body.
	text         string
	uncertain    bool
	stripNewline bool
}

// crengine preserves balanced XHTML nesting, even where an HTML parser would
// repair it. Unlike CFI, it keeps separate text nodes across comments and CDATA.
// Returning true from visit stops the walk; its path is valid during the call.
func walkKOReaderText(ctx context.Context, raw []byte, visit func(koReaderText) bool) error {
	type frame struct {
		elements    map[string]int
		texts       int
		uncertain   bool
		unsupported bool
	}
	var frames []frame
	var path []xpointerStep
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Entity = xml.HTMLEntity
	depth, offset := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if len(frames) == 0 && !(depth == 2 && t.Name.Local == "body") {
				continue
			}
			index := 1
			unsupported := false
			if len(frames) > 0 {
				parent := &frames[len(frames)-1]
				parent.elements[t.Name.Local]++
				index = parent.elements[t.Name.Local]
				unsupported = parent.unsupported
			}
			switch t.Name.Local {
			case "script", "style", "textarea", "svg", "math", "template":
				unsupported = true
			}
			path = append(path, xpointerStep{t.Name.Local, index})
			frames = append(frames, frame{elements: make(map[string]int), unsupported: unsupported})
		case xml.EndElement:
			depth--
			if len(frames) > 0 {
				frames = frames[:len(frames)-1]
				path = path[:len(path)-1]
			}
		case xml.CharData:
			if len(frames) == 0 {
				continue
			}
			parent := &frames[len(frames)-1]
			parent.texts++
			text := string(t)
			// Whitespace-only nodes can disappear with display/white-space rules;
			// long raw runs can split at crengine's 8192-character input boundary.
			if strings.Trim(text, " \t\r\n") == "" || utf8.RuneCount(raw[start:decoder.InputOffset()]) >= 8192 {
				parent.uncertain = true
			}
			uncertain := parent.uncertain || parent.unsupported || strings.Contains(text, "\r") || bytes.Contains(raw[start:decoder.InputOffset()], []byte("\n\r"))
			stripNewline := path[len(path)-1].name == "pre" && parent.texts == 1 && len(parent.elements) == 0 && strings.HasPrefix(text, "\n")
			if visit(koReaderText{
				path:  append(path, xpointerStep{"text()", parent.texts}),
				start: offset, text: text, uncertain: uncertain, stripNewline: stripNewline,
			}) {
				return nil
			}
			offset += utf16Len(text)
		}
	}
}

// Map native character boundaries to source UTF-16 offsets. A -1 marks a
// boundary that depends on the reader's whitespace settings or DOM version.
func (t koReaderText) sourceOffsets() []int {
	if t.uncertain {
		return nil
	}
	var normal, pre []int
	offset, column := 0, 0
	space := false
	for _, r := range t.text {
		whitespace := r == ' ' || r == '\n' || r == '\t'
		if !whitespace || !space {
			normal = append(normal, offset)
		}
		space = whitespace
		count := 1
		if r == '\t' {
			count = 8 - column%8
		}
		for range count {
			pre = append(pre, offset)
		}
		column += count
		if r == '\n' {
			column = 0
		}
		offset += utf16.RuneLen(r)
	}
	normal, pre = append(normal, offset), append(pre, offset)
	for i, offset := range normal {
		if i >= len(pre) || pre[i] != offset ||
			t.stripNewline && (i+1 >= len(pre) || pre[i+1] != offset) {
			normal[i] = -1
		}
	}
	return normal
}
