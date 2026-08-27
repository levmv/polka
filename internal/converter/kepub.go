package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/levmv/polka/internal/format"
)

const (
	kepubKoboStyleID     = "kobostylehacks"
	kepubKoboSpanStyleID = "koboSpanStyle"
	kepubOuterDivID      = "book-columns"
	kepubInnerDivID      = "book-inner"
	kepubSpanClass       = "koboSpan"
	kepubXHTMLNamespace  = "http://www.w3.org/1999/xhtml"
)

var (
	kepubOPFMetaTagRe = regexp.MustCompile(`(?is)<\s*meta\b[^>]*>`)
	kepubOPFItemTagRe = regexp.MustCompile(`(?is)<\s*item\b[^>]*>`)
	kepubXMLAttrRe    = regexp.MustCompile(`(?is)([A-Za-z_:][-A-Za-z0-9_:.]*)\s*=\s*("([^"]*)"|'([^']*)')`)
)

type kepubContainerDoc struct {
	Rootfiles []kepubRootfile `xml:"rootfiles>rootfile"`
}

type kepubRootfile struct {
	FullPath  string `xml:"full-path,attr"`
	MediaType string `xml:"media-type,attr"`
}

type kepubPackage struct {
	opfPath        string
	opfBytes       []byte
	opfFile        *zip.File
	containerFile  *zip.File
	containerBytes []byte
}

type kepubOPFDoc struct {
	Manifest struct {
		Items []kepubManifestItem `xml:"item"`
	} `xml:"manifest"`
}

type kepubManifestItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type kepubXMLAttr struct {
	Name       string
	NameStart  int
	NameEnd    int
	Value      string
	ValueStart int
	ValueEnd   int
}

type kepubSpanState struct {
	paragraph int
	segment   int
	pending   bool
}

func convertEPUBToKEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, size int64) error {
	if size < 0 {
		return fmt.Errorf("source size is invalid")
	}
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return fmt.Errorf("open EPUB: %w", err)
	}
	if err := claimConversionResources(ctx, len(zr.File), "EPUB archive"); err != nil {
		return err
	}

	pkg, err := kepubReadPackage(ctx, zr)
	if err != nil {
		return err
	}
	contentDocs, err := kepubContentDocuments(pkg.opfPath, pkg.opfBytes)
	if err != nil {
		return err
	}
	manifestPaths, err := kepubManifestPaths(pkg.opfPath, pkg.opfBytes)
	if err != nil {
		return err
	}

	contentDocSet := make(map[*zip.File]bool, len(contentDocs))
	packageEntrySet := map[*zip.File]bool{
		pkg.containerFile: true,
		pkg.opfFile:       true,
	}
	for _, name := range contentDocs {
		file, err := kepubZipFile(zr, name)
		if err != nil {
			return fmt.Errorf("resolve EPUB content document %s: %w", name, err)
		}
		if file != nil {
			contentDocSet[file] = true
			packageEntrySet[file] = true
		}
	}
	for _, file := range zr.File {
		if manifestPaths[file.Name] {
			packageEntrySet[file] = true
		}
	}

	sourceMimetype := kepubMimetypeSource(zr)
	zw := zip.NewWriter(w)
	if err := addEPUBFile(zw, "mimetype", zip.Store, "application/epub+zip"); err != nil {
		zw.Close()
		return err
	}

	for _, f := range zr.File {
		if err := checkContext(ctx); err != nil {
			zw.Close()
			return err
		}
		if f == sourceMimetype || kepubFilterFile(f.Name) {
			continue
		}

		var data []byte
		var method uint16
		switch {
		case f == pkg.containerFile && pkg.containerBytes != nil:
			data = pkg.containerBytes
			method = zip.Deflate
		case f == pkg.opfFile:
			data, err = transformKEPUBOPF(pkg.opfBytes)
			method = zip.Deflate
		case contentDocSet[f]:
			raw, readErr := kepubReadZipFile(ctx, f, maxConverterDecodedInputBytes)
			if readErr != nil {
				err = readErr
				break
			}
			data, err = transformKEPUBContent(raw)
			method = zip.Deflate
		default:
			err = kepubWriteZipFile(ctx, zw, f, nil, f.Method, packageEntrySet[f])
			method = 0
		}
		if err != nil {
			zw.Close()
			return err
		}
		if method != 0 {
			if err := kepubWriteZipFile(ctx, zw, f, data, method, packageEntrySet[f]); err != nil {
				zw.Close()
				return err
			}
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("close KEPUB: %w", err)
	}
	return nil
}

func kepubReadPackage(ctx context.Context, zr *zip.Reader) (kepubPackage, error) {
	container, err := kepubZipFile(zr, "META-INF/container.xml")
	if err != nil {
		return kepubPackage{}, fmt.Errorf("resolve EPUB container.xml: %w", err)
	}
	if container == nil {
		return kepubPackage{}, fmt.Errorf("EPUB container.xml not found")
	}
	raw, err := kepubReadZipFile(ctx, container, maxConverterPackageBytes)
	if err != nil {
		return kepubPackage{}, fmt.Errorf("read EPUB container.xml: %w", err)
	}
	var doc kepubContainerDoc
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		return kepubPackage{}, fmt.Errorf("parse EPUB container.xml: %w", err)
	}
	var firstResolveErr error
	seen := make(map[string]bool)
	// Prefer standard OPF declarations, then tolerate another media type when
	// it resolves unambiguously. Import/metadata already follows this ordering;
	// conversion should not reject the same coherent package solely because a
	// producer misspelled the rootfile media type.
	for _, requireStandardMediaType := range []bool{true, false} {
		for _, rootfile := range doc.Rootfiles {
			standardMediaType := isKEPUBOPFMediaType(rootfile.MediaType)
			if requireStandardMediaType && !standardMediaType {
				continue
			}
			opfPath := cleanKEPUBHref("", rootfile.FullPath)
			if opfPath == "" || seen[opfPath] {
				continue
			}
			seen[opfPath] = true
			opf, resolveErr := kepubZipFile(zr, opfPath)
			if resolveErr != nil {
				if firstResolveErr == nil {
					firstResolveErr = fmt.Errorf("resolve EPUB OPF %s: %w", opfPath, resolveErr)
				}
				continue
			}
			if opf != nil {
				opfBytes, err := kepubReadZipFile(ctx, opf, maxConverterPackageBytes)
				if err != nil {
					return kepubPackage{}, fmt.Errorf("read EPUB OPF %s: %w", opfPath, err)
				}
				opfBytes, err = format.NormalizeOPFXML(opfBytes)
				if err != nil {
					return kepubPackage{}, fmt.Errorf("normalize EPUB OPF %s: %w", opfPath, err)
				}
				if int64(len(opfBytes)) > maxConverterPackageBytes {
					return kepubPackage{}, fmt.Errorf("decoded EPUB OPF %s exceeds %d bytes: %w", opfPath, maxConverterPackageBytes, ErrInputTooLarge)
				}
				pkg := kepubPackage{
					opfPath:       opf.Name,
					opfBytes:      opfBytes,
					opfFile:       opf,
					containerFile: container,
				}
				if !standardMediaType {
					pkg.containerBytes, err = normalizeKEPUBRootfileMediaType(raw, rootfile)
					if err != nil {
						return kepubPackage{}, err
					}
				}
				return pkg, nil
			}
		}
	}
	if firstResolveErr != nil {
		return kepubPackage{}, firstResolveErr
	}
	return kepubPackage{}, fmt.Errorf("EPUB OPF rootfile not found")
}

func isKEPUBOPFMediaType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType == "" || mediaType == "application/oebps-package+xml"
}

func normalizeKEPUBRootfileMediaType(raw []byte, selected kepubRootfile) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		start := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("normalize EPUB container.xml: %w", err)
		}
		rootfile, ok := token.(xml.StartElement)
		if !ok || rootfile.Name.Local != "rootfile" {
			continue
		}
		fullPath, mediaType := "", ""
		for _, attr := range rootfile.Attr {
			switch attr.Name.Local {
			case "full-path":
				fullPath = attr.Value
			case "media-type":
				mediaType = attr.Value
			}
		}
		if strings.TrimSpace(fullPath) != strings.TrimSpace(selected.FullPath) ||
			strings.TrimSpace(mediaType) != strings.TrimSpace(selected.MediaType) {
			continue
		}
		end := decoder.InputOffset()
		tag := string(raw[start:end])
		normalized, ok := kepubSetAttribute(tag, "media-type", "application/oebps-package+xml")
		if !ok {
			break
		}
		out := make([]byte, 0, len(raw)+len(normalized)-int(end-start))
		out = append(out, raw[:start]...)
		out = append(out, normalized...)
		out = append(out, raw[end:]...)
		return out, nil
	}
	return nil, fmt.Errorf("normalize EPUB container.xml: selected rootfile declaration not found")
}

func kepubContentDocuments(opfPath string, opfBytes []byte) ([]string, error) {
	doc, err := parseKEPUBOPF(opfPath, opfBytes)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := make(map[string]bool)
	for _, item := range doc.Manifest.Items {
		if !isKEPUBContentDocument(item) {
			continue
		}
		name := cleanKEPUBHref(opfPath, item.Href)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func kepubManifestPaths(opfPath string, opfBytes []byte) (map[string]bool, error) {
	doc, err := parseKEPUBOPF(opfPath, opfBytes)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(doc.Manifest.Items))
	for _, item := range doc.Manifest.Items {
		if name := cleanKEPUBHref(opfPath, item.Href); name != "" {
			out[name] = true
		}
	}
	return out, nil
}

func parseKEPUBOPF(opfPath string, opfBytes []byte) (kepubOPFDoc, error) {
	var doc kepubOPFDoc
	if err := format.DecodeOPFXML(opfBytes, &doc); err != nil {
		return kepubOPFDoc{}, fmt.Errorf("parse EPUB OPF %s: %w", opfPath, err)
	}
	return doc, nil
}

func isKEPUBContentDocument(item kepubManifestItem) bool {
	mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if mediaType == "application/xhtml+xml" || mediaType == "text/html" {
		return true
	}
	switch strings.ToLower(path.Ext(item.Href)) {
	case ".xhtml", ".xhtm", ".html", ".htm":
		return true
	default:
		return false
	}
}

func transformKEPUBOPF(raw []byte) ([]byte, error) {
	coverID := kepubCoverID(raw)
	out := append([]byte(nil), raw...)
	matches := kepubOPFItemTagRe.FindAllIndex(raw, -1)
	for _, match := range slices.Backward(matches) {
		start, end := match[0], match[1]
		tag := string(raw[start:end])
		attrs := kepubXMLAttrs(tag)
		mediaType := strings.ToLower(strings.TrimSpace(attrs["media-type"]))
		if attrs["id"] != coverID || !strings.HasPrefix(mediaType, "image/") {
			continue
		}
		next := kepubEnsureOPFProperty(tag, "cover-image")
		out = append(out[:start], append([]byte(next), out[end:]...)...)
	}
	return out, nil
}

func kepubCoverID(raw []byte) string {
	for _, tag := range kepubOPFMetaTagRe.FindAll(raw, -1) {
		attrs := kepubXMLAttrs(string(tag))
		if strings.EqualFold(strings.TrimSpace(attrs["name"]), "cover") {
			if content := strings.TrimSpace(attrs["content"]); content != "" {
				return content
			}
		}
	}
	return "cover"
}

func kepubEnsureOPFProperty(tag, property string) string {
	attrs := kepubXMLAttrsWithLocations(tag)
	for _, attr := range attrs {
		if !strings.EqualFold(attr.Name, "properties") {
			continue
		}
		if containsKEPUBToken(attr.Value, property) {
			return tag
		}
		value := strings.TrimSpace(attr.Value)
		if value == "" {
			value = property
		} else {
			value += " " + property
		}
		return tag[:attr.ValueStart] + value + tag[attr.ValueEnd:]
	}

	insertAt := strings.LastIndex(tag, ">")
	if insertAt < 0 {
		return tag
	}
	if insertAt > 0 && tag[insertAt-1] == '/' {
		insertAt--
	}
	return tag[:insertAt] + ` properties="` + property + `"` + tag[insertAt:]
}

func kepubSetAttribute(tag, name, value string) (string, bool) {
	for _, attr := range kepubXMLAttrsWithLocations(tag) {
		if strings.EqualFold(attr.Name, name) {
			return tag[:attr.ValueStart] + value + tag[attr.ValueEnd:], true
		}
	}
	return tag, false
}

func kepubXMLAttrs(tag string) map[string]string {
	out := make(map[string]string)
	for _, attr := range kepubXMLAttrsWithLocations(tag) {
		out[strings.ToLower(attr.Name)] = attr.Value
	}
	return out
}

func kepubXMLAttrsWithLocations(tag string) []kepubXMLAttr {
	matches := kepubXMLAttrRe.FindAllStringSubmatchIndex(tag, -1)
	out := make([]kepubXMLAttr, 0, len(matches))
	for _, match := range matches {
		valueStart, valueEnd := match[6], match[7]
		if valueStart < 0 {
			valueStart, valueEnd = match[8], match[9]
		}
		out = append(out, kepubXMLAttr{
			Name:       tag[match[2]:match[3]],
			NameStart:  match[2],
			NameEnd:    match[3],
			Value:      tag[valueStart:valueEnd],
			ValueStart: valueStart,
			ValueEnd:   valueEnd,
		})
	}
	return out
}

func transformKEPUBContent(raw []byte) ([]byte, error) {
	raw = stripXMLDeclaration(raw)
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse EPUB content document: %w", err)
	}
	// The HTML parser deliberately recovers common producer damage, but its tree
	// can still contain invalid UTF-8/XML characters or a junk attribute name
	// synthesized from a truncated start tag. Repair those local facts before
	// rendering. Structural problems that cannot produce coherent XHTML still
	// fail the strict validation below.
	sanitizeKEPUBXMLCompatibility(doc)
	// One generated anchor does not prove the rest of the document was marked.
	// Refuse both partial and repeated conversion instead of creating mixed
	// address semantics for Kobo progress and annotations.
	if hasKEPUBSpanMarker(doc) {
		return nil, fmt.Errorf("EPUB content document already contains Kobo span markup; refusing partial or repeated KEPUB conversion")
	}
	root := firstHTMLElement(doc, "html")
	if root != nil && !hasKEPUBAttr(root, "xmlns") {
		root.Attr = append(root.Attr, html.Attribute{Key: "xmlns", Val: "http://www.w3.org/1999/xhtml"})
	}
	normalizeKEPUBContentModels(doc)
	head := firstHTMLElement(doc, "head")
	if head != nil {
		ensureKEPUBStyle(head, kepubKoboStyleID, "div#book-inner { margin-top: 0; margin-bottom: 0; }")
		ensureKEPUBStyle(head, kepubKoboSpanStyleID, ".koboSpan { -webkit-text-combine: inherit; }")
	}
	body := firstHTMLElement(doc, "body")
	if body != nil {
		inner := ensureKEPUBBodyWrapper(body)
		addKEPUBSpans(inner)
	}

	var out bytes.Buffer
	out.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	if err := html.Render(&out, doc); err != nil {
		return nil, fmt.Errorf("render KEPUB content document: %w", err)
	}
	if err := validateKEPUBXHTML(out.Bytes()); err != nil {
		return nil, fmt.Errorf("rendered KEPUB content document is not XML-compatible: %w", err)
	}
	return out.Bytes(), nil
}

func sanitizeKEPUBXMLCompatibility(n *html.Node) {
	switch n.Type {
	case html.TextNode, html.CommentNode:
		n.Data = sanitizeKEPUBXMLString(n.Data)
	case html.ElementNode:
		attrs := n.Attr[:0]
		malformed := false
		for _, attr := range n.Attr {
			if malformed {
				continue
			}
			if !validKEPUBXMLName(attr.Key) || attr.Namespace != "" && !validKEPUBXMLName(attr.Namespace) {
				// The HTML tokenizer may split the rest of a truncated tag into
				// plausible-looking attributes. Once the first impossible name is
				// seen, none of that suffix is trustworthy.
				malformed = true
				continue
			}
			attr.Val = sanitizeKEPUBXMLString(attr.Val)
			attrs = append(attrs, attr)
		}
		n.Attr = attrs
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		sanitizeKEPUBXMLCompatibility(child)
	}
}

func sanitizeKEPUBXMLString(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	return strings.Map(func(r rune) rune {
		if validXML10Char(r) {
			return r
		}
		return '\uFFFD'
	}, value)
}

func validKEPUBXMLName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if r != ':' && r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != ':' && r != '_' && r != '-' && r != '.' && r != '\u00b7' && !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
			return false
		}
	}
	return true
}

// validateKEPUBXHTML prevents the tolerant HTML serializer from packaging an
// XHTML entry that a stricter EPUB/Kobo reader cannot parse. In particular,
// malformed raw-text elements and comments must fail conversion rather than
// silently producing a broken KEPUB.
func validateKEPUBXHTML(raw []byte) error {
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
				if value.Name.Space != kepubXHTMLNamespace || value.Name.Local != "html" {
					return fmt.Errorf("root element is {%s}%s, want XHTML html", value.Name.Space, value.Name.Local)
				}
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if roots != 1 {
		return fmt.Errorf("document has %d root elements, want 1", roots)
	}
	return nil
}

func stripXMLDeclaration(raw []byte) []byte {
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

func ensureKEPUBStyle(head *html.Node, id, css string) {
	for child := head.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || !strings.EqualFold(child.Data, "style") {
			continue
		}
		if attrValue(child, "id") == id || containsKEPUBToken(attrValue(child, "class"), id) {
			return
		}
	}
	style := &html.Node{
		Type: html.ElementNode,
		Data: "style",
		Attr: []html.Attribute{
			{Key: "type", Val: "text/css"},
			{Key: "id", Val: id},
		},
	}
	style.AppendChild(&html.Node{Type: html.TextNode, Data: css})
	head.AppendChild(style)
}

func ensureKEPUBBodyWrapper(body *html.Node) *html.Node {
	for child := body.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || attrValue(child, "id") != kepubOuterDivID {
			continue
		}
		for inner := child.FirstChild; inner != nil; inner = inner.NextSibling {
			if inner.Type == html.ElementNode && attrValue(inner, "id") == kepubInnerDivID {
				return inner
			}
		}
	}

	outer := &html.Node{
		Type: html.ElementNode,
		Data: "div",
		Attr: []html.Attribute{{Key: "id", Val: kepubOuterDivID}},
	}
	inner := &html.Node{
		Type: html.ElementNode,
		Data: "div",
		Attr: []html.Attribute{{Key: "id", Val: kepubInnerDivID}},
	}
	for child := body.FirstChild; child != nil; {
		next := child.NextSibling
		body.RemoveChild(child)
		inner.AppendChild(child)
		child = next
	}
	outer.AppendChild(inner)
	body.AppendChild(outer)
	return inner
}

func addKEPUBSpans(root *html.Node) {
	state := &kepubSpanState{}
	transformKEPUBChildren(root, state)
}

func transformKEPUBChildren(parent *html.Node, state *kepubSpanState) {
	for child := parent.FirstChild; child != nil; {
		next := child.NextSibling
		transformKEPUBNode(child, state)
		child = next
	}
}

func transformKEPUBNode(n *html.Node, state *kepubSpanState) {
	switch n.Type {
	case html.TextNode:
		wrapKEPUBTextNode(n, state)
	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		if isKEPUBSkippedElement(tag) {
			return
		}
		if tag == "img" {
			wrapKEPUBImageNode(n, state)
			return
		}
		if isKEPUBAtomicInlineElement(tag) {
			wrapKEPUBInlineElement(n, state)
			return
		}
		if isKEPUBBlockElement(tag) {
			state.markParagraph()
		}
		transformKEPUBChildren(n, state)
	}
}

func normalizeKEPUBContentModels(n *html.Node) {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, "hgroup") && kepubHGroupNeedsDiv(n) {
		n.Data = "div"
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		normalizeKEPUBContentModels(child)
	}
}

func kepubHGroupNeedsDiv(n *html.Node) bool {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		switch strings.ToLower(child.Data) {
		case "h1", "h2", "h3", "h4", "h5", "h6", "script", "template":
		default:
			return true
		}
	}
	return false
}

func wrapKEPUBTextNode(n *html.Node, state *kepubSpanState) {
	parent := n.Parent
	if parent == nil {
		return
	}
	wrapWhitespace := parent.Type == html.ElementNode && strings.EqualFold(parent.Data, "p")
	for _, part := range splitKEPUBSentences(n.Data) {
		if part == "" {
			continue
		}
		if strings.TrimSpace(part) == "" && !wrapWhitespace {
			parent.InsertBefore(&html.Node{Type: html.TextNode, Data: part}, n)
			continue
		}
		parent.InsertBefore(withKEPUBText(state.nextSpan(), part), n)
	}
	parent.RemoveChild(n)
}

func wrapKEPUBInlineElement(n *html.Node, state *kepubSpanState) {
	parent := n.Parent
	if parent == nil {
		return
	}
	span := state.nextSpan()
	parent.InsertBefore(span, n)
	parent.RemoveChild(n)
	span.AppendChild(n)
}

func wrapKEPUBImageNode(n *html.Node, state *kepubSpanState) {
	parent := n.Parent
	if parent == nil {
		return
	}
	state.forceParagraph()
	span := state.nextSpan()
	parent.InsertBefore(span, n)
	parent.RemoveChild(n)
	span.AppendChild(n)
}

func (s *kepubSpanState) markParagraph() {
	s.pending = true
}

func (s *kepubSpanState) forceParagraph() {
	s.paragraph++
	s.segment = 0
	s.pending = false
}

func (s *kepubSpanState) nextParagraph() {
	s.paragraph++
	s.segment = 0
	s.pending = false
}

func (s *kepubSpanState) nextSpan() *html.Node {
	if s.paragraph == 0 || s.pending {
		s.nextParagraph()
	}
	s.segment++
	return &html.Node{
		Type: html.ElementNode,
		Data: "span",
		Attr: []html.Attribute{
			{Key: "class", Val: kepubSpanClass},
			{Key: "id", Val: fmt.Sprintf("kobo.%d.%d", s.paragraph, s.segment)},
		},
	}
}

func withKEPUBText(n *html.Node, text string) *html.Node {
	n.AppendChild(&html.Node{Type: html.TextNode, Data: text})
	return n
}

func splitKEPUBSentences(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	start := 0
	afterTerminator := false
	for idx, r := range text {
		switch {
		case isKEPUBSentenceTerminator(r):
			afterTerminator = true
		case afterTerminator && isKEPUBClosingPunctuation(r):
		case afterTerminator && unicode.IsSpace(r):
			size := utf8.RuneLen(r)
			if size < 0 {
				size = 1
			}
			end := idx + size
			out = append(out, text[start:end])
			start = end
			afterTerminator = false
		case afterTerminator:
			out = append(out, text[start:idx])
			start = idx
			afterTerminator = false
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

func isKEPUBSentenceTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '…', '。', '！', '？':
		return true
	default:
		return false
	}
}

func isKEPUBClosingPunctuation(r rune) bool {
	switch r {
	case '\'', '"', '”', '’', '“', ')', ']', '}':
		return true
	default:
		return false
	}
}

func isKEPUBSkippedElement(tag string) bool {
	switch tag {
	case "script", "style", "pre", "audio", "video", "svg", "math":
		return true
	default:
		return false
	}
}

func isKEPUBAtomicInlineElement(tag string) bool {
	switch tag {
	case "time":
		return true
	default:
		return false
	}
}

func isKEPUBBlockElement(tag string) bool {
	switch tag {
	case "p", "ol", "ul", "table", "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	default:
		return false
	}
}

func hasKEPUBSpanMarker(n *html.Node) bool {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, "span") {
		if containsKEPUBToken(attrValue(n, "class"), kepubSpanClass) || isKEPUBSpanID(attrValue(n, "id")) {
			return true
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if hasKEPUBSpanMarker(child) {
			return true
		}
	}
	return false
}

func isKEPUBSpanID(id string) bool {
	parts := strings.Split(strings.TrimSpace(id), ".")
	if len(parts) != 3 || parts[0] != "kobo" {
		return false
	}
	paragraph, paragraphErr := strconv.Atoi(parts[1])
	segment, segmentErr := strconv.Atoi(parts[2])
	return paragraphErr == nil && segmentErr == nil && paragraph > 0 && segment > 0
}

func hasKEPUBAttr(n *html.Node, key string) bool {
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, key) {
			return true
		}
	}
	return false
}

func attrValue(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func containsKEPUBToken(value, token string) bool {
	for field := range strings.FieldsSeq(value) {
		if field == token {
			return true
		}
	}
	return false
}

func kepubFilterFile(name string) bool {
	base := path.Base(name)
	switch strings.ToLower(base) {
	case "calibre_bookmarks.txt", "itunesmetadata.plist", "itunesartwork.plist", ".ds_store", "thumbs.db":
		return true
	}
	clean := strings.TrimPrefix(path.Clean(name), "/")
	clean = strings.ToLower(clean)
	return clean == "__macosx" || strings.HasPrefix(clean, "__macosx/")
}

func cleanKEPUBHref(basePath, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if before, _, ok := strings.Cut(href, "#"); ok {
		href = before
	}
	if parsed, err := url.Parse(href); err == nil {
		if parsed.Scheme != "" || parsed.Host != "" {
			return ""
		}
		href = parsed.Path
	}
	if unescaped, err := url.PathUnescape(href); err == nil {
		href = unescaped
	}
	if basePath != "" && !strings.HasPrefix(href, "/") {
		href = path.Join(path.Dir(basePath), href)
	}
	href = path.Clean(strings.TrimPrefix(href, "/"))
	if href == "." || strings.HasPrefix(href, "../") {
		return ""
	}
	return href
}

func kepubZipFile(zr *zip.Reader, name string) (*zip.File, error) {
	file, ambiguous := format.ResolveZIPEntry(zr, name)
	if ambiguous {
		return nil, fmt.Errorf("entry %q has multiple matching archive members", name)
	}
	return file, nil
}

func kepubMimetypeSource(zr *zip.Reader) *zip.File {
	for _, file := range zr.File {
		if file.Name == "mimetype" {
			return file
		}
	}
	for _, file := range zr.File {
		if strings.EqualFold(file.Name, "mimetype") {
			return file
		}
	}
	return nil
}

func kepubReadZipFile(ctx context.Context, f *zip.File, maxBytes int64) ([]byte, error) {
	if err := kepubRejectEncryptedEntry(f); err != nil {
		return nil, err
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readAllContextLimited(ctx, rc, maxBytes, "EPUB entry "+f.Name)
}

func kepubWriteZipFile(ctx context.Context, zw *zip.Writer, f *zip.File, data []byte, method uint16, packageEntry bool) error {
	if err := kepubRejectEncryptedEntry(f); err != nil {
		return err
	}
	header := f.FileHeader
	header.Name = f.Name
	header.Method = method
	if packageEntry && utf8.ValidString(header.Name) && utf8.ValidString(header.Comment) {
		// Some producers write UTF-8 package paths without ZIP's language flag.
		// The OPF gives us unambiguous UTF-8 evidence for selected resources;
		// leave unrelated archive members' filename encoding flags unchanged.
		header.NonUTF8 = false
	}
	header.CRC32 = 0
	header.CompressedSize = 0
	header.CompressedSize64 = 0
	header.UncompressedSize = 0
	header.UncompressedSize64 = 0

	w, err := zw.CreateHeader(&header)
	if err != nil {
		return fmt.Errorf("create KEPUB entry %s: %w", f.Name, err)
	}
	if data != nil {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("write KEPUB entry %s: %w", f.Name, err)
		}
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open EPUB entry %s: %w", f.Name, err)
	}
	defer rc.Close()
	if err := copyContext(ctx, w, rc); err != nil {
		return fmt.Errorf("copy EPUB entry %s: %w", f.Name, err)
	}
	return nil
}

func kepubRejectEncryptedEntry(f *zip.File) error {
	if f.Flags&0x1 == 0 {
		return nil
	}
	return fmt.Errorf("EPUB entry %s is encrypted; KEPUB conversion cannot transform encrypted inputs", f.Name)
}
