package position

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/levmv/polka/internal/converter"
)

const (
	maxDocumentBytes = 8 << 20
	maxDepth         = 128
	maxNodes         = 100000
)

type cfiNode struct {
	name     string // Empty for a CFI character chunk, including adjacent CDATA.
	attrs    []xml.Attr
	text     string
	parent   *cfiNode
	children []*cfiNode
	step     int
	start    int // UTF-16 offsets in the chapter body; never persisted.
	end      int
}

func (n *cfiNode) attr(name string) string {
	for _, attr := range n.attrs {
		if attr.Name.Space == "" && attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func (n *cfiNode) child(name string) *cfiNode {
	for _, child := range n.children {
		if child.name == name {
			return child
		}
	}
	return nil
}

// CFI steps count XML elements and character chunks, not an HTML parser's
// repaired tree. Comments and processing instructions do not split a chunk.
func readCFIDocument(ctx context.Context, raw []byte) (*cfiNode, error) {
	if len(raw) > maxDocumentBytes {
		return nil, fmt.Errorf("CFI document exceeds %d bytes", maxDocumentBytes)
	}
	root := &cfiNode{name: "#document"}
	stack := []*cfiNode{root}
	decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})))
	// XHTML's named entities are built in to readers; Go does not load its DTD.
	decoder.Entity = xml.HTMLEntity
	count := 0
	var pending strings.Builder
	flushText := func(parent *cfiNode) {
		if pending.Len() > 0 {
			parent.children = append(parent.children, &cfiNode{parent: parent, text: pending.String()})
			pending.Reset()
			count++
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			flushText(root)
			break
		}
		if err != nil {
			return nil, fmt.Errorf("CFI requires well-formed XML: %w", err)
		}
		parent := stack[len(stack)-1]
		switch t := token.(type) {
		case xml.StartElement:
			flushText(parent)
			if len(stack) > maxDepth {
				return nil, fmt.Errorf("CFI document exceeds depth limit")
			}
			n := &cfiNode{name: t.Name.Local, attrs: t.Attr, parent: parent}
			parent.children = append(parent.children, n)
			stack = append(stack, n)
			count++
		case xml.EndElement:
			flushText(parent)
			stack = stack[:len(stack)-1]
		case xml.CharData:
			pending.Write(t)
		}
		if count > maxNodes {
			return nil, fmt.Errorf("CFI document exceeds node limit")
		}
	}
	var document *cfiNode
	for _, child := range root.children {
		if child.name == "" {
			if strings.TrimSpace(child.text) != "" {
				return nil, fmt.Errorf("text outside XML root")
			}
		} else {
			if document != nil {
				return nil, fmt.Errorf("multiple XML roots")
			}
			document = child
		}
	}
	if document == nil {
		return nil, fmt.Errorf("missing XML root")
	}
	document.parent = nil
	var index func(*cfiNode)
	index = func(parent *cfiNode) {
		elements := 0
		for _, child := range parent.children {
			if child.name == "" {
				child.step = 2*elements + 1
			} else {
				elements++
				child.step = 2 * elements
				index(child)
			}
		}
	}
	index(document)
	return document, nil
}

type cfiStep struct {
	index int
	id    string
}

type cfiPoint struct {
	packagePath []cfiStep
	contentPath []cfiStep
	offset      int
}

func parseCFI(raw string) (cfiPoint, error) {
	if len(raw) > 8192 || !strings.HasPrefix(raw, "epubcfi(") || !strings.HasSuffix(raw, ")") {
		return cfiPoint{}, fmt.Errorf("invalid CFI envelope")
	}
	parts, err := splitCFI(raw[8:len(raw)-1], ',')
	if err != nil {
		return cfiPoint{}, err
	}
	if len(parts) == 1 {
		return parseCFIPoint(parts[0])
	}
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return cfiPoint{}, fmt.Errorf("invalid CFI range")
	}
	if _, err := parseCFIPoint(parts[0] + parts[2]); err != nil {
		return cfiPoint{}, err
	}
	// Resume at the start of a range emitted by the web reader.
	return parseCFIPoint(parts[0] + parts[1])
}

func splitCFI(s string, delimiter byte) ([]string, error) {
	var parts []string
	start, assertion := 0, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '^':
			if !assertion || i+1 == len(s) || !strings.ContainsRune("^[](),;=", rune(s[i+1])) {
				return nil, fmt.Errorf("invalid CFI escape")
			}
			i++
		case '[':
			if assertion {
				return nil, fmt.Errorf("nested CFI assertion")
			}
			assertion = true
		case ']':
			if !assertion {
				return nil, fmt.Errorf("unexpected CFI assertion end")
			}
			assertion = false
		default:
			if s[i] == delimiter && !assertion {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	if assertion {
		return nil, fmt.Errorf("unclosed CFI assertion")
	}
	return append(parts, s[start:]), nil
}

func parseCFIPoint(s string) (cfiPoint, error) {
	parts, err := splitCFI(s, '!')
	if err != nil {
		return cfiPoint{}, err
	}
	if len(parts) != 2 {
		return cfiPoint{}, fmt.Errorf("only package-to-chapter CFI indirection is supported")
	}
	pkg, offset, err := parseCFIPath(parts[0])
	if err != nil || offset >= 0 {
		return cfiPoint{}, fmt.Errorf("unsupported CFI package path")
	}
	content, offset, err := parseCFIPath(parts[1])
	if err != nil {
		return cfiPoint{}, err
	}
	return cfiPoint{pkg, content, max(offset, 0)}, nil
}

func parseCFIPath(s string) ([]cfiStep, int, error) {
	var steps []cfiStep
	for len(s) > 0 && s[0] == '/' {
		n, rest, err := parseNumber(s[1:])
		if err != nil || n == 0 || len(steps) >= maxDepth {
			return nil, 0, fmt.Errorf("invalid or unsupported CFI step")
		}
		step := cfiStep{index: n}
		s = rest
		if strings.HasPrefix(s, "[") {
			if n%2 != 0 {
				return nil, 0, fmt.Errorf("CFI text assertions are not supported")
			}
			var id strings.Builder
			i := 1
			for ; i < len(s) && s[i] != ']'; i++ {
				if s[i] == '^' {
					i++ // splitCFI has already validated assertion boundaries and escaping.
				} else if strings.ContainsRune("[(),;=", rune(s[i])) {
					return nil, 0, fmt.Errorf("unsupported CFI assertion")
				}
				id.WriteByte(s[i])
			}
			if id.Len() == 0 {
				return nil, 0, fmt.Errorf("empty CFI ID assertion")
			}
			step.id = id.String()
			s = s[i+1:]
		}
		steps = append(steps, step)
		if n%2 != 0 {
			break
		}
	}
	if len(steps) == 0 {
		return nil, 0, fmt.Errorf("empty CFI path")
	}
	offset := -1
	if strings.HasPrefix(s, ":") {
		var err error
		offset, s, err = parseNumber(s[1:])
		if err != nil || steps[len(steps)-1].index%2 == 0 {
			return nil, 0, fmt.Errorf("invalid CFI text offset")
		}
	}
	if s != "" {
		return nil, 0, fmt.Errorf("unsupported CFI terminus %q", s)
	}
	return steps, offset, nil
}

func parseNumber(s string) (int, string, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i > 1 && s[0] == '0' {
		return 0, "", fmt.Errorf("invalid CFI integer")
	}
	n, err := strconv.Atoi(s[:i])
	return n, s[i:], err
}

func resolveCFI(root *cfiNode, steps []cfiStep) (*cfiNode, error) {
	current := root
	for _, step := range steps {
		var next *cfiNode
		for _, child := range current.children {
			if child.step == step.index {
				next = child
				break
			}
		}
		if next == nil || step.id != "" && next.attr("id") != step.id {
			return nil, fmt.Errorf("CFI does not resolve in this document")
		}
		current = next
	}
	return current, nil
}

var escapeCFIID = strings.NewReplacer("^", "^^", "[", "^[", "]", "^]", "(", "^(", ")", "^)", ",", "^,", ";", "^;", "=", "^=")

func nodeCFI(n *cfiNode) string {
	if n.parent == nil {
		return ""
	}
	step := "/" + strconv.Itoa(n.step)
	if id := n.attr("id"); id != "" {
		step += "[" + escapeCFIID.Replace(id) + "]"
	}
	return nodeCFI(n.parent) + step
}

// A negative offset addresses the element itself rather than a text boundary.
func formatCFI(ref, node *cfiNode, offset int) string {
	point := nodeCFI(ref) + "!" + nodeCFI(node)
	if offset >= 0 {
		point += ":" + strconv.Itoa(offset)
	}
	return "epubcfi(" + point + ")"
}

type epubBook struct {
	opf         *cfiNode
	opfPath     string
	files       map[*cfiNode]*zip.File
	spineByPath map[string]*cfiNode
}

func readEPUB(ctx context.Context, src io.ReaderAt, size int64) (*epubBook, error) {
	pkg, err := converter.OpenEPUBSource(ctx, src, size)
	if err != nil {
		return nil, err
	}
	opf, err := readCFIDocument(ctx, pkg.OPF)
	if err != nil {
		return nil, err
	}
	manifest, spine := opf.child("manifest"), opf.child("spine")
	if opf.name != "package" || manifest == nil || spine == nil {
		return nil, fmt.Errorf("missing EPUB manifest/spine")
	}
	items := make(map[string]*cfiNode)
	for _, item := range manifest.children {
		if item.name == "item" {
			id := item.attr("id")
			if _, duplicate := items[id]; duplicate {
				items[id] = nil
			} else {
				items[id] = item
			}
		}
	}
	book := &epubBook{
		opf: opf, opfPath: pkg.OPFPath,
		files:       make(map[*cfiNode]*zip.File),
		spineByPath: make(map[string]*cfiNode),
	}
	for _, ref := range spine.children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ref.name != "itemref" {
			continue
		}
		item := items[ref.attr("idref")]
		if item == nil {
			continue
		}
		file, err := pkg.ContentDocument(item.attr("href"), item.attr("media-type"))
		if err != nil || file == nil {
			continue
		}
		book.files[ref] = file
		if _, duplicate := book.spineByPath[file.Name]; duplicate {
			book.spineByPath[file.Name] = nil
		} else {
			book.spineByPath[file.Name] = ref
		}
	}
	return book, nil
}

func readChapter(ctx context.Context, file *zip.File) ([]byte, *epubChapter, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if file.Flags&1 != 0 {
		return nil, nil, fmt.Errorf("EPUB entry %s is encrypted", file.Name)
	}
	r, err := file.Open()
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, Reader: r}, maxDocumentBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > maxDocumentBytes {
		return nil, nil, fmt.Errorf("EPUB chapter exceeds %d bytes", maxDocumentBytes)
	}
	chapter, err := indexChapter(ctx, raw)
	return raw, chapter, err
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

type epubChapter struct {
	root *cfiNode
	body *cfiNode
	runs []*cfiNode
}

func indexChapter(ctx context.Context, raw []byte) (*epubChapter, error) {
	root, err := readCFIDocument(ctx, raw)
	if err != nil {
		return nil, err
	}
	body := root.child("body")
	if root.name != "html" || body == nil {
		return nil, fmt.Errorf("position requires an XHTML chapter with a body")
	}
	chapter := &epubChapter{root: root, body: body}
	offset := 0
	var visit func(*cfiNode)
	visit = func(n *cfiNode) {
		n.start = offset
		if n.name == "" {
			offset += utf16Len(n.text)
			chapter.runs = append(chapter.runs, n)
		} else {
			for _, child := range n.children {
				visit(child)
			}
		}
		n.end = offset
	}
	visit(body)
	return chapter, nil
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
