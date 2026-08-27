package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/levmv/polka/internal/format"
)

func convertHTMLSourceToEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, size int64, opts ConversionOptions) error {
	if size < 0 {
		return fmt.Errorf("source size is invalid")
	}
	raw, err := readSectionContextLimited(ctx, src, size, maxConverterDecodedInputBytes, "HTML source")
	if err != nil {
		return fmt.Errorf("read HTML source: %w", err)
	}
	decoded, err := format.DecodeHTMLToUTF8(raw)
	if err != nil {
		return fmt.Errorf("decode HTML source: %w", err)
	}
	var assets []epubAsset
	imageResolver := htmlDataImageResolver(&assets)
	body, nav, _, err := htmlBodyToEPUBWithImages(decoded, "", imageResolver)
	if err != nil {
		return err
	}
	addHTMLDocumentCoverAsset(&assets, raw)
	if err := checkContext(ctx); err != nil {
		return err
	}
	meta := epubMetadataWithFallback(toEPUBMetadata(format.MetadataFromHTML(raw)), opts)
	return writeSimpleEPUBWithNav(ctx, w, body, meta, nav, assets...)
}

func convertHTMLZSourceToEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, size int64, opts ConversionOptions) error {
	if size < 0 {
		return fmt.Errorf("source size is invalid")
	}
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return fmt.Errorf("open HTMLZ source: %w", err)
	}
	entry := format.HTMLZIndexEntry(zr)
	if entry == nil {
		return fmt.Errorf("HTMLZ archive contains no top-level HTML document")
	}
	raw, err := readZipFileContextLimited(ctx, entry, maxConverterDecodedInputBytes, "HTMLZ document")
	if err != nil {
		return fmt.Errorf("read HTMLZ entry %s: %w", entry.Name, err)
	}
	decoded, err := format.DecodeHTMLToUTF8(raw)
	if err != nil {
		return fmt.Errorf("decode HTMLZ entry %s: %w", entry.Name, err)
	}
	meta := format.MetadataFromHTML(raw)
	extracted, err := htmlZMetadataForEPUB(ctx, zr)
	if err != nil {
		return err
	}
	if extracted != nil {
		meta = extracted
	}

	var assets []epubAsset
	seenImages := make(map[string]string)
	imageResolver := func(src string) (string, bool) {
		name := cleanHTMLZResourceHref(entry.Name, src)
		if name == "" {
			return "", false
		}
		if href, ok := seenImages[name]; ok {
			return href, true
		}
		img := htmlZOptionalZIPEntry(zr, name)
		if img == nil || img.FileInfo().IsDir() {
			return "", false
		}
		raw, err := readZipFileContextLimited(ctx, img, maxConverterResourceBytes, "HTMLZ image")
		if err != nil {
			return "", false
		}
		data, mediaType, ext, ok := format.EPUBImageResource(raw, name)
		if !ok {
			return "", false
		}
		href := fmt.Sprintf("images/image%d%s", len(assets)+1, ext)
		seenImages[name] = href
		assets = append(assets, epubAsset{
			ID:        fmt.Sprintf("img%d", len(assets)+1),
			Href:      href,
			MediaType: mediaType,
			Data:      data,
		})
		return href, true
	}
	sourcePath := format.NormalizeZipName(entry.Name)
	body, nav, ids, err := htmlBodyToEPUBWithImages(decoded, sourcePath, imageResolver)
	if err != nil {
		return err
	}
	if len(nav) == 0 {
		packageNav, err := htmlZPackageNavForEPUB(ctx, zr, sourcePath, ids)
		if err != nil {
			return err
		}
		if len(packageNav) > 0 {
			nav = packageNav
		}
	}
	addHTMLZCoverAsset(&assets, src, size)
	if err := checkContext(ctx); err != nil {
		return err
	}
	return writeSimpleEPUBWithNav(ctx, w, body, epubMetadataWithFallback(toEPUBMetadata(meta), opts), nav, assets...)
}

type (
	htmlImageResolver func(src string) (href string, ok bool)
	htmlMediaResolver func(src, mediaPrefix string) (href string, ok bool)
)

type htmlEPUBResolvers struct {
	image htmlImageResolver
	media htmlMediaResolver
}

func htmlDataImageResolver(assets *[]epubAsset) htmlImageResolver {
	seen := make(map[string]string)
	return func(src string) (string, bool) {
		src = strings.TrimSpace(src)
		if src == "" {
			return "", false
		}
		if href, ok := seen[src]; ok {
			return href, true
		}
		data, mediaType, ext := decodeHTMLDataImage(src)
		if len(data) == 0 {
			return "", false
		}
		id := fmt.Sprintf("img%d", len(*assets)+1)
		href := "images/" + id + ext
		seen[src] = href
		*assets = append(*assets, epubAsset{
			ID:        id,
			Href:      href,
			MediaType: mediaType,
			Data:      data,
		})
		return href, true
	}
}

func decodeHTMLDataImage(src string) ([]byte, string, string) {
	if !strings.HasPrefix(strings.ToLower(src), "data:") {
		return nil, "", ""
	}
	meta, encoded, ok := strings.Cut(src[5:], ",")
	if !ok || !strings.Contains(strings.ToLower(meta), ";base64") {
		return nil, "", ""
	}
	data, err := format.DecodeLenientBase64(strings.Join(strings.Fields(encoded), ""))
	if err != nil {
		return nil, "", ""
	}
	mediaType, ext, ok := format.EPUBImageTypeFromBytes(data)
	if !ok {
		return nil, "", ""
	}
	return data, mediaType, ext
}

func addHTMLDocumentCoverAsset(assets *[]epubAsset, raw []byte) {
	cover, ext, err := format.ExtractHTMLCover(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return
	}
	addEPUBCoverAsset(assets, cover, ext)
}

func addHTMLZCoverAsset(assets *[]epubAsset, src io.ReaderAt, size int64) {
	cover, ext, err := format.ExtractHTMLZCover(src, size)
	if err != nil {
		return
	}
	addEPUBCoverAsset(assets, cover, ext)
}

func addEPUBCoverAsset(assets *[]epubAsset, cover []byte, ext string) {
	if len(cover) == 0 || strings.TrimSpace(ext) == "" {
		return
	}
	mediaType, ok := format.EPUBImageMediaTypeForExtension(ext)
	if !ok {
		return
	}
	for i := range *assets {
		if bytes.Equal((*assets)[i].Data, cover) {
			(*assets)[i].Cover = true
			return
		}
	}
	*assets = append(*assets, epubAsset{
		ID:        "cover-image",
		Href:      "images/cover" + ext,
		MediaType: mediaType,
		Data:      cover,
		Cover:     true,
	})
}

func htmlBodyToEPUBWithImages(raw []byte, sourcePath string, imageResolver htmlImageResolver) (string, []epubNavItem, map[string]bool, error) {
	return htmlBodyToEPUB(raw, sourcePath, htmlEPUBResolvers{image: imageResolver})
}

func htmlBodyToEPUB(raw []byte, sourcePath string, resolvers htmlEPUBResolvers) (string, []epubNavItem, map[string]bool, error) {
	raw = removeInvalidXML10Chars(raw)
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse HTML source: %w", err)
	}
	root := firstHTMLElement(doc, "body")
	if root == nil {
		root = doc
	}

	var out strings.Builder
	ids := collectHTMLIDs(root)
	state := &htmlEPUBRenderState{
		usedIDs:     ids,
		renderedIDs: map[string]bool{},
	}
	renderHTMLChildren(&out, root, false, resolvers, state)
	body := strings.TrimSpace(out.String())
	if body == "" {
		return "<p></p>\n", nil, ids, nil
	}
	if len(state.nav) == 0 {
		state.nav = htmlLinkNavFallback(root, sourcePath, ids)
	}
	return body + "\n", state.nav, ids, nil
}

type htmlEPUBRenderState struct {
	nav         []epubNavItem
	headingSeq  int
	usedIDs     map[string]bool
	renderedIDs map[string]bool
}

func renderHTMLChildren(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		renderHTMLNode(out, child, inPre, resolvers, state)
	}
}

func renderHTMLNode(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	switch n.Type {
	case html.TextNode:
		text := n.Data
		if !inPre {
			text = collapseHTMLWhitespace(text)
		}
		if text != "" {
			out.WriteString(stdhtml.EscapeString(text))
		}
	case html.DocumentNode:
		renderHTMLChildren(out, n, inPre, resolvers, state)
	case html.ElementNode:
		renderHTMLElement(out, n, inPre, resolvers, state)
	}
}

func renderHTMLElement(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	tag := strings.ToLower(n.Data)
	if tag == "audio" || tag == "video" {
		renderHTMLMedia(out, n, inPre, resolvers, state)
		return
	}
	if shouldSkipHTMLElement(tag) {
		return
	}
	if tag == "html" || tag == "body" {
		renderHTMLChildren(out, n, inPre, resolvers, state)
		return
	}
	if tag == "head" {
		return
	}
	if tag == "br" {
		out.WriteString("<br/>\n")
		return
	}
	if tag == "hr" {
		out.WriteString("<hr/>\n")
		return
	}
	if tag == "img" {
		renderHTMLImage(out, n, resolvers.image)
		return
	}
	if tag == "a" {
		renderHTMLAnchor(out, n, inPre, resolvers, state)
		return
	}

	mapped, ok := epubHTMLTag(tag)
	if !ok {
		renderHTMLChildren(out, n, inPre, resolvers, state)
		return
	}
	if htmlHeadingLevel(mapped) > 0 {
		renderHTMLHeading(out, n, mapped, inPre, resolvers, state)
		return
	}
	nextInPre := inPre || mapped == "pre"
	fmt.Fprintf(out, "<%s%s>", mapped, htmlElementAttributes(n, state))
	if isEPUBBlockHTMLTag(mapped) {
		renderHTMLChildren(out, n, nextInPre, resolvers, state)
	} else {
		// EPUB's XHTML content model does not allow flow/block descendants
		// inside phrasing elements such as span, strong, or i. Real HTML/KF8
		// producers still emit that shape, and the tolerant HTML parser keeps
		// it in the DOM. Render the descendants through the phrasing boundary
		// so generated EPUB is valid without discarding their reading text.
		renderHTMLPhrasingChildren(out, n, nextInPre, resolvers, state)
	}
	fmt.Fprintf(out, "</%s>", mapped)
	if isEPUBBlockHTMLTag(mapped) {
		out.WriteByte('\n')
	}
}

func renderHTMLHeading(out *strings.Builder, n *html.Node, tag string, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	id := htmlAnchorID(n)
	if id == "" {
		id = state.nextHeadingID()
	}
	id = state.claimRenderedID(id)
	title := htmlNodeText(n)
	if title != "" {
		state.nav = append(state.nav, epubNavItem{
			Title: title,
			Href:  "text.xhtml#" + id,
		})
	}
	nextInPre := inPre || tag == "pre"
	fmt.Fprintf(out, `<%s id="%s"%s>`, tag, stdhtml.EscapeString(id), htmlSharedAttributes(n))
	renderHTMLPhrasingChildren(out, n, nextInPre, resolvers, state)
	fmt.Fprintf(out, "</%s>\n", tag)
}

func (s *htmlEPUBRenderState) nextHeadingID() string {
	for {
		s.headingSeq++
		id := fmt.Sprintf("heading-%d", s.headingSeq)
		if !s.usedIDs[id] {
			s.usedIDs[id] = true
			return id
		}
	}
}

func (s *htmlEPUBRenderState) claimRenderedID(id string) string {
	id = safeHTMLAnchorID(id)
	if id == "" {
		return ""
	}
	if !s.renderedIDs[id] {
		s.renderedIDs[id] = true
		s.usedIDs[id] = true
		return id
	}
	for seq := 2; ; seq++ {
		candidate := fmt.Sprintf("%s-%d", id, seq)
		if !s.usedIDs[candidate] && !s.renderedIDs[candidate] {
			s.usedIDs[candidate] = true
			s.renderedIDs[candidate] = true
			return candidate
		}
	}
}

func renderHTMLAnchor(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	attrs := htmlAnchorAttributes(n, state)
	if attrs == "" {
		renderHTMLChildren(out, n, inPre, resolvers, state)
		return
	}
	out.WriteString("<a")
	out.WriteString(attrs)
	out.WriteString(">")
	renderHTMLChildren(out, n, inPre, resolvers, state)
	out.WriteString("</a>")
}

func renderHTMLImage(out *strings.Builder, n *html.Node, imageResolver htmlImageResolver) {
	if imageResolver == nil {
		return
	}
	src := strings.TrimSpace(htmlAttr(n, "src"))
	if src == "" {
		return
	}
	href, ok := imageResolver(src)
	if !ok {
		return
	}
	alt := htmlAttr(n, "alt")
	fmt.Fprintf(out, `<img src="%s" alt="%s"%s/>`, stdhtml.EscapeString(href), stdhtml.EscapeString(alt), htmlSharedAttributes(n))
}

func renderHTMLMedia(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	tag := strings.ToLower(n.Data)
	if resolvers.media == nil {
		renderHTMLChildren(out, n, inPre, resolvers, state)
		return
	}
	href, ok := resolvers.media(strings.TrimSpace(htmlAttr(n, "src")), tag+"/")
	if !ok {
		renderHTMLChildren(out, n, inPre, resolvers, state)
		return
	}
	fmt.Fprintf(out, `<%s src="%s" controls="controls"%s`, tag, stdhtml.EscapeString(href), htmlElementAttributes(n, state))
	if title := strings.TrimSpace(htmlAttr(n, "title")); title != "" {
		fmt.Fprintf(out, ` title="%s"`, stdhtml.EscapeString(title))
	}
	out.WriteString(">")
	renderHTMLChildren(out, n, inPre, resolvers, state)
	fmt.Fprintf(out, "</%s>\n", tag)
}

func renderHTMLPhrasingChildren(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		renderHTMLPhrasingNode(out, child, inPre, resolvers, state)
	}
}

func renderHTMLPhrasingNode(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	switch n.Type {
	case html.TextNode:
		text := n.Data
		if !inPre {
			text = collapseHTMLWhitespace(text)
		}
		if text != "" {
			out.WriteString(stdhtml.EscapeString(text))
		}
	case html.DocumentNode:
		renderHTMLPhrasingChildren(out, n, inPre, resolvers, state)
	case html.ElementNode:
		renderHTMLPhrasingElement(out, n, inPre, resolvers, state)
	}
}

func renderHTMLPhrasingElement(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	tag := strings.ToLower(n.Data)
	if tag == "audio" || tag == "video" {
		renderHTMLMedia(out, n, inPre, resolvers, state)
		return
	}
	if shouldSkipHTMLElement(tag) {
		return
	}
	if tag == "html" || tag == "body" || tag == "head" {
		renderHTMLPhrasingChildren(out, n, inPre, resolvers, state)
		return
	}
	if tag == "br" {
		out.WriteString("<br/>")
		return
	}
	if tag == "img" {
		renderHTMLImage(out, n, resolvers.image)
		return
	}
	if tag == "a" {
		renderHTMLPhrasingAnchor(out, n, inPre, resolvers, state)
		return
	}
	mapped, ok := epubHTMLTag(tag)
	if !ok {
		renderHTMLPhrasingChildren(out, n, inPre, resolvers, state)
		return
	}
	if isEPUBBlockHTMLTag(mapped) {
		attrs := htmlElementAttributes(n, state)
		if attrs == "" {
			renderHTMLPhrasingChildren(out, n, inPre, resolvers, state)
			return
		}
		// Keep safe IDs (including Kindle filepos targets) and semantic
		// attributes even though the invalid block wrapper itself cannot remain
		// inside phrasing content.
		fmt.Fprintf(out, "<span%s>", attrs)
		renderHTMLPhrasingChildren(out, n, inPre, resolvers, state)
		out.WriteString("</span>")
		return
	}
	nextInPre := inPre || mapped == "pre"
	fmt.Fprintf(out, "<%s%s>", mapped, htmlElementAttributes(n, state))
	renderHTMLPhrasingChildren(out, n, nextInPre, resolvers, state)
	fmt.Fprintf(out, "</%s>", mapped)
}

func renderHTMLPhrasingAnchor(out *strings.Builder, n *html.Node, inPre bool, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState) {
	attrs := htmlAnchorAttributes(n, state)
	if attrs == "" {
		renderHTMLPhrasingChildren(out, n, inPre, resolvers, state)
		return
	}
	out.WriteString("<a")
	out.WriteString(attrs)
	out.WriteString(">")
	renderHTMLPhrasingChildren(out, n, inPre, resolvers, state)
	out.WriteString("</a>")
}

func htmlAnchorAttributes(n *html.Node, state *htmlEPUBRenderState) string {
	var attrs strings.Builder
	if id := htmlAnchorID(n); id != "" {
		if state != nil {
			id = state.claimRenderedID(id)
		}
		if id != "" {
			fmt.Fprintf(&attrs, ` id="%s"`, stdhtml.EscapeString(id))
		}
	}
	if href := safeHTMLLinkHref(htmlAttr(n, "href")); href != "" {
		fmt.Fprintf(&attrs, ` href="%s"`, stdhtml.EscapeString(href))
	}
	attrs.WriteString(htmlSharedAttributes(n))
	return attrs.String()
}

func htmlElementAttributes(n *html.Node, state *htmlEPUBRenderState) string {
	var attrs strings.Builder
	id := htmlAnchorID(n)
	if id != "" {
		if state != nil {
			id = state.claimRenderedID(id)
		}
		if id != "" {
			fmt.Fprintf(&attrs, ` id="%s"`, stdhtml.EscapeString(id))
		}
	}
	attrs.WriteString(htmlSharedAttributes(n))
	return attrs.String()
}

func htmlSharedAttributes(n *html.Node) string {
	var attrs strings.Builder
	if class := safeHTMLSpaceTokens(htmlAttr(n, "class"), true); class != "" {
		fmt.Fprintf(&attrs, ` class="%s"`, stdhtml.EscapeString(class))
	}
	if role := safeHTMLSpaceTokens(htmlAttr(n, "role"), false); role != "" {
		fmt.Fprintf(&attrs, ` role="%s"`, stdhtml.EscapeString(role))
	}
	if dir := safeHTMLDirection(htmlAttr(n, "dir")); dir != "" {
		fmt.Fprintf(&attrs, ` dir="%s"`, dir)
	}
	if lang := safeHTMLLanguage(htmlAttr(n, "lang")); lang != "" {
		fmt.Fprintf(&attrs, ` lang="%s"`, stdhtml.EscapeString(lang))
	}
	if lang := safeHTMLLanguage(htmlAttr(n, "xml:lang")); lang != "" {
		fmt.Fprintf(&attrs, ` xml:lang="%s"`, stdhtml.EscapeString(lang))
	}
	return attrs.String()
}

func htmlAnchorID(n *html.Node) string {
	if id := safeHTMLAnchorID(htmlAttr(n, "id")); id != "" {
		return id
	}
	if strings.EqualFold(n.Data, "a") {
		return safeHTMLAnchorID(htmlAttr(n, "name"))
	}
	return ""
}

func collectHTMLIDs(n *html.Node) map[string]bool {
	ids := map[string]bool{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if id := htmlAnchorID(node); id != "" {
				ids[id] = true
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return ids
}

func htmlHeadingLevel(tag string) int {
	if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
		return int(tag[1] - '0')
	}
	return 0
}

func htmlNodeText(n *html.Node) string {
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			text.WriteString(node.Data)
			text.WriteByte(' ')
		case html.ElementNode:
			if shouldSkipHTMLElement(strings.ToLower(node.Data)) {
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(text.String()), " ")
}

const (
	htmlLinkNavMaxItems  = 50
	htmlLinkNavMaxRunes  = 100
	htmlLinkNavTextXHTML = "text.xhtml#"
)

func htmlLinkNavFallback(root *html.Node, sourcePath string, ids map[string]bool) []epubNavItem {
	var nav []epubNavItem
	seenHrefs := map[string]bool{}
	seenLabels := map[string]bool{}
	targetOrder := htmlAnchorDocumentOrder(root, ids)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if len(nav) >= htmlLinkNavMaxItems {
			return
		}
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "a") {
			if fragment, ok := htmlTargetDocumentFragment(htmlAttr(node, "href"), sourcePath, sourcePath); ok && ids[fragment] {
				title := htmlLinkNavLabel(node)
				href := htmlLinkNavTextXHTML + fragment
				appendHTMLNavItem(&nav, seenHrefs, seenLabels, title, href)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
			if len(nav) >= htmlLinkNavMaxItems {
				return
			}
		}
	}
	walk(root)
	sort.SliceStable(nav, func(i, j int) bool {
		left := strings.TrimPrefix(nav[i].Href, htmlLinkNavTextXHTML)
		right := strings.TrimPrefix(nav[j].Href, htmlLinkNavTextXHTML)
		leftOrder, leftKnown := targetOrder[left]
		rightOrder, rightKnown := targetOrder[right]
		if leftKnown != rightKnown {
			return leftKnown
		}
		return leftKnown && leftOrder < rightOrder
	})
	return nav
}

func htmlAnchorDocumentOrder(root *html.Node, ids map[string]bool) map[string]int {
	order := map[string]int{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if id := htmlAnchorID(node); ids[id] {
				if _, exists := order[id]; !exists {
					order[id] = len(order)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return order
}

func appendHTMLNavItem(nav *[]epubNavItem, seenHrefs, seenLabels map[string]bool, title, href string) {
	if len(*nav) >= htmlLinkNavMaxItems {
		return
	}
	title = truncateHTMLLinkNavLabel(strings.Join(strings.Fields(title), " "))
	if title == "" {
		title = "Unnamed"
	}
	labelKey := strings.ToLower(title)
	if seenHrefs[href] || seenLabels[labelKey] {
		return
	}
	seenHrefs[href] = true
	seenLabels[labelKey] = true
	*nav = append(*nav, epubNavItem{Title: title, Href: href})
}

func htmlTargetDocumentFragment(rawHref, linkBasePath, targetPath string) (string, bool) {
	rawHref = strings.TrimSpace(rawHref)
	if rawHref == "" {
		return "", false
	}
	parsed, err := url.Parse(rawHref)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return "", false
	}
	fragment := safeHTMLAnchorID(parsed.Fragment)
	if fragment == "" {
		return "", false
	}
	hrefPath := parsed.Path
	if unescaped, err := url.PathUnescape(hrefPath); err == nil {
		hrefPath = unescaped
	}
	if strings.TrimSpace(hrefPath) == "" && linkBasePath == "" && targetPath == "" {
		return fragment, true
	}
	if strings.TrimSpace(hrefPath) == "" {
		hrefPath = linkBasePath
	}
	if targetPath == "" {
		return "", false
	}
	if !strings.HasPrefix(hrefPath, "/") {
		hrefPath = path.Join(path.Dir(linkBasePath), hrefPath)
	}
	cleanHref := format.NormalizeZipName(hrefPath)
	if cleanHref == "" {
		return "", false
	}
	return fragment, strings.EqualFold(cleanHref, targetPath)
}

func htmlLinkNavLabel(n *html.Node) string {
	label := htmlNodeText(n)
	if label == "" {
		label = strings.TrimSpace(htmlAttr(n, "title"))
	}
	if label == "" {
		label = firstHTMLImageAlt(n)
	}
	if label == "" {
		label = "Unnamed"
	}
	return truncateHTMLLinkNavLabel(strings.Join(strings.Fields(label), " "))
}

func firstHTMLImageAlt(n *html.Node) string {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, "img") {
		if alt := strings.TrimSpace(htmlAttr(n, "alt")); alt != "" {
			return alt
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if alt := firstHTMLImageAlt(child); alt != "" {
			return alt
		}
	}
	return ""
}

func truncateHTMLLinkNavLabel(label string) string {
	if utf8.RuneCountInString(label) <= htmlLinkNavMaxRunes {
		return label
	}
	runes := []rune(label)
	return string(runes[:htmlLinkNavMaxRunes])
}

func safeHTMLLinkHref(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Host != "" {
			return raw
		}
		return ""
	case "":
	default:
		return ""
	}
	if parsed.Host != "" {
		return ""
	}
	if parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment != "" && safeHTMLAnchorID(parsed.Fragment) != "" {
		return "#" + parsed.EscapedFragment()
	}
	return ""
}

func safeHTMLAnchorID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for i, r := range raw {
		if unicode.IsSpace(r) {
			return ""
		}
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return ""
			}
			continue
		}
		if r != '_' && r != '-' && r != '.' && r != ':' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ""
		}
	}
	return raw
}

func safeHTMLSpaceTokens(raw string, allowColon bool) string {
	var out []string
	for token := range strings.FieldsSeq(raw) {
		if safeHTMLToken(token, allowColon) {
			out = append(out, token)
		}
	}
	return strings.Join(out, " ")
}

func safeHTMLToken(token string, allowColon bool) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		switch {
		case r == '_' || r == '-' || r == '.':
		case allowColon && r == ':':
		case unicode.IsLetter(r) || unicode.IsDigit(r):
		default:
			return false
		}
	}
	return true
}

func safeHTMLDirection(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ltr", "rtl", "auto":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func safeHTMLLanguage(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 {
		return ""
	}
	for _, r := range raw {
		if r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ""
		}
	}
	return raw
}

func htmlAttr(n *html.Node, name string) string {
	for _, attr := range n.Attr {
		if attr.Namespace == "" && strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
		if attr.Namespace != "" && strings.EqualFold(attr.Namespace+":"+attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func firstHTMLElement(n *html.Node, names ...string) *html.Node {
	if n.Type == html.ElementNode {
		for _, name := range names {
			if strings.EqualFold(n.Data, name) {
				return n
			}
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := firstHTMLElement(child, names...); found != nil {
			return found
		}
	}
	return nil
}

type htmlZOPFNavigation struct {
	NavHrefs []string
	NCXHrefs []string
}

type htmlZOPFPackage struct {
	Manifest []htmlZOPFItem `xml:"manifest>item"`
	Spine    struct {
		Toc string `xml:"toc,attr"`
	} `xml:"spine"`
}

type htmlZOPFItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

func htmlZPackageNavForEPUB(ctx context.Context, zr *zip.Reader, sourcePath string, ids map[string]bool) ([]epubNavItem, error) {
	opf := format.FirstRootOPF(zr)
	if opf == nil {
		return nil, nil
	}
	raw, err := readZipFileContextLimited(ctx, opf, maxConverterMetadataBytes, "HTMLZ package metadata")
	if err != nil {
		return nil, err
	}
	refs, err := htmlZOPFNavigationRefs(raw, format.NormalizeZipName(opf.Name))
	if err != nil {
		return nil, nil
	}

	nav, err := htmlZPackageNavFromRefs(ctx, zr, refs.NavHrefs, sourcePath, ids, htmlZNavDocumentItems)
	if err != nil {
		return nil, err
	}
	if len(nav) > 0 {
		return nav, nil
	}
	return htmlZPackageNavFromRefs(ctx, zr, refs.NCXHrefs, sourcePath, ids, htmlZNCXNavItems)
}

func htmlZPackageNavFromRefs(ctx context.Context, zr *zip.Reader, hrefs []string, sourcePath string, ids map[string]bool, extract func([]byte, string, string, map[string]bool) []epubNavItem) ([]epubNavItem, error) {
	var nav []epubNavItem
	seenHrefs := map[string]bool{}
	seenLabels := map[string]bool{}
	for _, href := range hrefs {
		if len(nav) >= htmlLinkNavMaxItems {
			break
		}
		entry := htmlZOptionalZIPEntry(zr, href)
		if entry == nil || entry.FileInfo().IsDir() {
			continue
		}
		raw, err := readZipFileContextLimited(ctx, entry, maxConverterMetadataBytes, "HTMLZ navigation")
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			continue
		}
		for _, item := range extract(raw, format.NormalizeZipName(entry.Name), sourcePath, ids) {
			appendHTMLNavItem(&nav, seenHrefs, seenLabels, item.Title, item.Href)
			if len(nav) >= htmlLinkNavMaxItems {
				break
			}
		}
	}
	return nav, nil
}

func htmlZOPFNavigationRefs(raw []byte, opfPath string) (htmlZOPFNavigation, error) {
	var pkg htmlZOPFPackage
	if err := format.DecodeOPFXML(raw, &pkg); err != nil {
		return htmlZOPFNavigation{}, err
	}
	itemsByID := map[string]htmlZOPFItem{}
	var refs htmlZOPFNavigation
	for _, item := range pkg.Manifest {
		href := cleanHTMLZResourceHref(opfPath, item.Href)
		if href == "" {
			continue
		}
		itemsByID[strings.TrimSpace(item.ID)] = item
		if htmlZOPFHasProperty(item.Properties, "nav") {
			refs.NavHrefs = appendUniqueString(refs.NavHrefs, href)
		}
		if strings.EqualFold(strings.TrimSpace(item.MediaType), "application/x-dtbncx+xml") || strings.EqualFold(path.Ext(href), ".ncx") {
			refs.NCXHrefs = appendUniqueString(refs.NCXHrefs, href)
		}
	}
	if tocID := strings.TrimSpace(pkg.Spine.Toc); tocID != "" {
		if item, ok := itemsByID[tocID]; ok {
			if href := cleanHTMLZResourceHref(opfPath, item.Href); href != "" {
				refs.NCXHrefs = appendUniqueString([]string{href}, refs.NCXHrefs...)
			}
		}
	}
	return refs, nil
}

func htmlZOPFHasProperty(properties, want string) bool {
	for property := range strings.FieldsSeq(properties) {
		if strings.EqualFold(property, want) {
			return true
		}
	}
	return false
}

func appendUniqueString(items []string, values ...string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		found := false
		for _, existing := range items {
			if strings.EqualFold(existing, value) {
				found = true
				break
			}
		}
		if !found {
			items = append(items, value)
		}
	}
	return items
}

func htmlZNavDocumentItems(raw []byte, navPath string, sourcePath string, ids map[string]bool) []epubNavItem {
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	root := htmlTOCNavElement(doc)
	if root == nil {
		root = doc
	}
	var nav []epubNavItem
	seenHrefs := map[string]bool{}
	seenLabels := map[string]bool{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if len(nav) >= htmlLinkNavMaxItems {
			return
		}
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "a") {
			if fragment, ok := htmlTargetDocumentFragment(htmlAttr(node, "href"), navPath, sourcePath); ok && ids[fragment] {
				appendHTMLNavItem(&nav, seenHrefs, seenLabels, htmlLinkNavLabel(node), htmlLinkNavTextXHTML+fragment)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
			if len(nav) >= htmlLinkNavMaxItems {
				return
			}
		}
	}
	walk(root)
	return nav
}

func htmlTOCNavElement(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, "nav") {
		for _, attr := range n.Attr {
			if strings.EqualFold(attr.Key, "epub:type") || strings.EqualFold(attr.Key, "type") {
				for token := range strings.FieldsSeq(attr.Val) {
					if strings.EqualFold(token, "toc") {
						return n
					}
				}
			}
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := htmlTOCNavElement(child); found != nil {
			return found
		}
	}
	return nil
}

type htmlZNCXDocument struct {
	NavMap struct {
		Points []htmlZNCXPoint `xml:"navPoint"`
	} `xml:"navMap"`
}

type htmlZNCXPoint struct {
	PlayOrder string `xml:"playOrder,attr"`
	Label     struct {
		Text string `xml:"text"`
	} `xml:"navLabel"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Points []htmlZNCXPoint `xml:"navPoint"`
}

func htmlZNCXNavItems(raw []byte, ncxPath string, sourcePath string, ids map[string]bool) []epubNavItem {
	var doc htmlZNCXDocument
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		return nil
	}
	var nav []epubNavItem
	seenHrefs := map[string]bool{}
	seenLabels := map[string]bool{}
	for _, point := range htmlZNCXPointsInReadingOrder(doc.NavMap.Points) {
		if len(nav) >= htmlLinkNavMaxItems {
			break
		}
		if fragment, ok := htmlTargetDocumentFragment(point.Content.Src, ncxPath, sourcePath); ok && ids[fragment] {
			appendHTMLNavItem(&nav, seenHrefs, seenLabels, point.Label.Text, htmlLinkNavTextXHTML+fragment)
		}
	}
	return nav
}

func htmlZNCXPointsInReadingOrder(points []htmlZNCXPoint) []htmlZNCXPoint {
	type orderedPoint struct {
		point htmlZNCXPoint
		order int
	}
	var flat []orderedPoint
	completePlayOrder := true
	seenOrders := map[int]bool{}
	var walk func([]htmlZNCXPoint)
	walk = func(items []htmlZNCXPoint) {
		for _, point := range items {
			order, err := strconv.Atoi(strings.TrimSpace(point.PlayOrder))
			if err != nil || order <= 0 || seenOrders[order] {
				completePlayOrder = false
			} else {
				seenOrders[order] = true
			}
			flat = append(flat, orderedPoint{point: point, order: order})
			walk(point.Points)
		}
	}
	walk(points)
	if completePlayOrder {
		sort.SliceStable(flat, func(i, j int) bool { return flat[i].order < flat[j].order })
	}
	out := make([]htmlZNCXPoint, 0, len(flat))
	for _, item := range flat {
		out = append(out, item.point)
	}
	return out
}

func htmlZMetadataForEPUB(ctx context.Context, zr *zip.Reader) (*format.Metadata, error) {
	opf := format.FirstRootOPF(zr)
	if opf == nil {
		return nil, nil
	}
	raw, err := readZipFileContextLimited(ctx, opf, maxConverterMetadataBytes, "HTMLZ metadata")
	if err != nil {
		return nil, err
	}
	meta, err := format.ParseOPF(bytes.NewReader(raw))
	if err != nil {
		return nil, nil
	}
	return meta, nil
}

func cleanHTMLZResourceHref(basePath, href string) string {
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
	return format.NormalizeZipName(href)
}

// HTMLZ image and navigation members are optional. Missing and ambiguous paths
// both fall back to the document-only conversion; choosing one of several
// plausible archive members would be worse than omitting the enhancement.
func htmlZOptionalZIPEntry(zr *zip.Reader, name string) *zip.File {
	file, ambiguous := format.ResolveZIPEntry(zr, name)
	if ambiguous {
		return nil
	}
	return file
}

func readZipFileContextLimited(ctx context.Context, f *zip.File, maxBytes int64, label string) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readAllContextLimited(ctx, rc, maxBytes, label)
}

func epubHTMLTag(tag string) (string, bool) {
	switch tag {
	case "article", "aside", "div", "figcaption", "figure", "footer", "header", "main", "nav", "section":
		return "div", true
	case "b":
		return "strong", true
	case "address", "blockquote", "caption", "code", "dd", "del", "dfn", "dl", "dt",
		"em", "h1", "h2", "h3", "h4", "h5", "h6", "i", "ins", "kbd", "li",
		"mark", "ol", "p", "pre", "q", "s", "samp", "small", "span", "strong",
		"sub", "sup", "table", "tbody", "td", "tfoot", "th", "thead", "tr", "u",
		"ul", "var":
		return tag, true
	default:
		return "", false
	}
}

func shouldSkipHTMLElement(tag string) bool {
	switch tag {
	case "applet", "canvas", "embed", "iframe", "math", "noscript", "object",
		"script", "style", "svg", "template":
		return true
	default:
		return false
	}
}

func isEPUBBlockHTMLTag(tag string) bool {
	switch tag {
	case "address", "blockquote", "caption", "dd", "div", "dl", "dt", "figcaption",
		"figure", "h1", "h2", "h3", "h4", "h5", "h6", "li", "ol", "p", "pre",
		"table", "tbody", "td", "tfoot", "th", "thead", "tr", "ul":
		return true
	default:
		return false
	}
}

func collapseHTMLWhitespace(text string) string {
	if text == "" {
		return ""
	}
	var out strings.Builder
	lastSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !lastSpace {
				out.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		out.WriteRune(r)
		lastSpace = false
	}
	return out.String()
}
