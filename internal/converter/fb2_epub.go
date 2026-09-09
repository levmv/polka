package converter

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	stdhtml "html"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html/charset"

	"github.com/levmv/polka/internal/format"
)

type fb2EPUBBinary struct {
	ID          string `xml:"id,attr"`
	ContentType string `xml:"content-type,attr"`
	Data        string `xml:",chardata"`
}

func convertFB2SourceToEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, size int64, opts ConversionOptions) error {
	raw, err := readFB2SourceBytes(ctx, src, size)
	if err != nil {
		return err
	}
	raw, err = format.NormalizeFB2XMLBytes(raw)
	if err != nil {
		return err
	}
	meta, coverRef, err := format.ExtractFB2Description(raw)
	if err != nil {
		return fmt.Errorf("extract FB2 metadata: %w", err)
	}
	assets, imageRefs, fallbackCoverHref, err := fb2EPUBImageAssets(raw)
	if err != nil {
		return err
	}
	coverHref := imageRefs[fb2RefID(coverRef)]
	if coverHref == "" {
		coverHref = fallbackCoverHref
	}
	fb2MarkEPUBCoverAsset(assets, coverHref)
	body, nav, err := fb2BodyToEPUB(raw, imageRefs)
	if err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	return writeSimpleEPUBWithNav(ctx, w, body, epubMetadataWithFallback(toEPUBMetadata(meta), opts), nav, assets...)
}

func readFB2SourceBytes(ctx context.Context, src io.ReaderAt, size int64) ([]byte, error) {
	source, err := format.OpenFB2Source(src, size, "")
	if err != nil {
		return nil, fmt.Errorf("open FB2 source: %w", err)
	}
	defer source.Reader.Close()
	raw, err := readAllContextLimited(ctx, source.Reader, maxConverterDecodedInputBytes, "FB2 source")
	if err != nil {
		return nil, fmt.Errorf("read FB2 source: %w", err)
	}
	return raw, nil
}

func fb2MarkEPUBCoverAsset(assets []epubAsset, coverHref string) {
	if coverHref == "" {
		return
	}
	for i := range assets {
		if assets[i].Href == coverHref {
			assets[i].Cover = true
			return
		}
	}
}

func fb2EPUBImageAssets(raw []byte) ([]epubAsset, map[string]string, string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	var assets []epubAsset
	refs := map[string]string{}
	fallbackCoverHref := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, "", fmt.Errorf("decode FB2 binaries: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "binary" {
			continue
		}
		var bin fb2EPUBBinary
		if err := decoder.DecodeElement(&bin, &start); err != nil {
			return nil, nil, "", fmt.Errorf("decode FB2 binary: %w", err)
		}
		data, mediaType, ext := decodeFB2EPUBImage(bin)
		if len(data) == 0 {
			continue
		}
		contentType := strings.ToLower(strings.TrimSpace(strings.Split(bin.ContentType, ";")[0]))
		isFallbackCover := fallbackCoverHref == "" && strings.HasPrefix(contentType, "image/")
		id := strings.TrimSpace(bin.ID)
		if (id == "" || refs[id] != "") && !isFallbackCover {
			continue
		}
		assetID := "img" + strconv.Itoa(len(assets)+1)
		href := "images/" + assetID + ext
		assets = append(assets, epubAsset{
			ID:        assetID,
			Href:      href,
			MediaType: mediaType,
			Data:      data,
		})
		if id != "" && refs[id] == "" {
			refs[id] = href
		}
		if isFallbackCover {
			fallbackCoverHref = href
		}
	}
	return assets, refs, fallbackCoverHref, nil
}

func decodeFB2EPUBImage(bin fb2EPUBBinary) ([]byte, string, string) {
	encoded := strings.Join(strings.Fields(bin.Data), "")
	if encoded == "" {
		return nil, "", ""
	}
	data, err := format.DecodeLenientBase64(encoded)
	if err != nil {
		return nil, "", ""
	}
	mediaType, ext, ok := format.EPUBImageTypeFromBytes(data)
	if !ok {
		return nil, "", ""
	}
	return data, mediaType, ext
}

func fb2BodyToEPUB(raw []byte, imageRefs map[string]string) (string, []epubNavItem, error) {
	index, err := indexFB2Body(raw)
	if err != nil {
		return "", nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	renderer := fb2EPUBRenderer{
		imageRefs:   imageRefs,
		usedIDs:     index.usedIDs,
		noteTargets: index.noteTargets,
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("decode FB2 body: %w", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Local == "description" || token.Name.Local == "binary" {
				if err := decoder.Skip(); err != nil {
					return "", nil, fmt.Errorf("skip FB2 %s: %w", token.Name.Local, err)
				}
				continue
			}
			renderer.start(token)
		case xml.EndElement:
			renderer.end()
		case xml.CharData:
			renderer.text(string(token))
		}
	}
	body := strings.TrimSpace(renderer.out.String())
	if body == "" {
		return "<p></p>\n", nil, nil
	}
	return body + "\n", renderer.nav, nil
}

type fb2BodyIndex struct {
	usedIDs     map[string]bool
	noteTargets map[string]bool
}

// Index before rendering: links can precede notes, and generated heading IDs
// must avoid existing IDs even in later bodies.
func indexFB2Body(raw []byte) (fb2BodyIndex, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	index := fb2BodyIndex{
		usedIDs:     map[string]bool{},
		noteTargets: map[string]bool{},
	}
	bodyDepth := 0
	skipIDsDepth := 0
	noteBodyDepth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return index, nil
		}
		if err != nil {
			return fb2BodyIndex{}, fmt.Errorf("index FB2 body: %w", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			name := token.Name.Local
			// Metadata and binaries outside bodies do not reserve heading IDs.
			// Track their depth without skipping the independent note scan.
			switch {
			case skipIDsDepth > 0:
				skipIDsDepth++
			case bodyDepth > 0 || name == "body":
				bodyDepth++
				if id := strings.TrimSpace(xmlAttr(token, "id")); id != "" {
					index.usedIDs[id] = true
				}
			case name == "description" || name == "binary":
				skipIDsDepth = 1
			}
			if name == "body" {
				role := strings.ToLower(strings.TrimSpace(xmlAttr(token, "name")))
				if role == "notes" || role == "comments" {
					noteBodyDepth = 1
				} else {
					noteBodyDepth = 0
				}
				continue
			}
			if noteBodyDepth == 0 {
				continue
			}
			noteBodyDepth++
			if name == "section" {
				if id := strings.TrimSpace(xmlAttr(token, "id")); id != "" {
					index.noteTargets[id] = true
				}
			}
		case xml.EndElement:
			if skipIDsDepth > 0 {
				skipIDsDepth--
			} else if bodyDepth > 0 {
				bodyDepth--
			}
			if noteBodyDepth > 0 {
				noteBodyDepth--
			}
		}
	}
}

type fb2EPUBRenderer struct {
	out           strings.Builder
	imageRefs     map[string]string
	noteTargets   map[string]bool
	nav           []epubNavItem
	stack         []fb2RenderFrame
	bodyDepth     int
	footnoteDepth int
	headingSeq    int
	usedIDs       map[string]bool
	heading       *fb2HeadingCapture
	titleNav      *fb2TitleNavCapture
	pendingSpace  bool
}

type fb2RenderFrame struct {
	name       string
	close      string
	hasContent bool
	footnote   bool
}

type fb2HeadingCapture struct {
	fb2Name       string
	id            string
	deferTitleNav bool
	title         strings.Builder
}

type fb2TitleNavCapture struct {
	href   string
	labels []string
}

func (r *fb2EPUBRenderer) start(el xml.StartElement) {
	name := el.Name.Local
	if name == "body" {
		r.bodyDepth++
		r.push(name, "")
		return
	}
	if r.bodyDepth == 0 {
		return
	}
	open, close := r.renderedElement(el)
	if r.pendingSpace {
		if fb2StartsInlineOutput(name) && open != "" {
			r.emitPendingSpace()
		} else {
			r.pendingSpace = false
		}
	}
	r.out.WriteString(open)
	r.push(name, close)
	if name == "section" && r.noteTargets[strings.TrimSpace(xmlAttr(el, "id"))] {
		r.footnoteDepth++
		r.stack[len(r.stack)-1].footnote = true
	}
	if name == "image" && open != "" {
		r.markContent()
	}
}

func (r *fb2EPUBRenderer) end() {
	if r.bodyDepth == 0 {
		return
	}
	frame := r.stack[len(r.stack)-1]
	r.stack = r.stack[:len(r.stack)-1]
	r.pendingSpace = false
	r.out.WriteString(frame.close)
	if r.heading != nil && frame.name == r.heading.fb2Name {
		r.finishHeading()
	}
	if frame.name == "title" {
		r.finishTitleNav()
	}
	if frame.name == "body" {
		r.bodyDepth--
	}
	if frame.footnote {
		r.footnoteDepth--
	}
}

func (r *fb2EPUBRenderer) text(text string) {
	if r.bodyDepth == 0 {
		return
	}
	if strings.TrimSpace(text) == "" {
		if r.inlineTextHasContent() {
			r.pendingSpace = true
		}
		return
	}
	r.emitPendingSpace()
	if r.heading != nil {
		r.heading.title.WriteString(text)
		r.heading.title.WriteByte(' ')
	}
	r.out.WriteString(stdhtml.EscapeString(text))
	r.markContent()
}

func (r *fb2EPUBRenderer) push(fb2Name, outputClose string) {
	r.stack = append(r.stack, fb2RenderFrame{name: fb2Name, close: outputClose})
}

func (r *fb2EPUBRenderer) emitPendingSpace() {
	if !r.pendingSpace {
		return
	}
	r.out.WriteByte(' ')
	r.markContent()
	r.pendingSpace = false
}

func (r *fb2EPUBRenderer) markContent() {
	for i := range r.stack {
		r.stack[i].hasContent = true
	}
}

func (r *fb2EPUBRenderer) inlineTextHasContent() bool {
	// A newly opened inline wrapper can start with whitespace after content in
	// its parent. Stop at a block boundary so indentation between paragraphs is
	// still ignored.
	for i := len(r.stack) - 1; i >= 0 && isFB2InlineText(r.stack[i].name); i-- {
		if r.stack[i].hasContent {
			return true
		}
	}
	return false
}

func (r *fb2EPUBRenderer) renderedElement(el xml.StartElement) (string, string) {
	name := el.Name.Local
	switch name {
	case "empty-line":
		return "<br/>\n", ""
	case "image":
		if href := r.imageHref(el); href != "" {
			return `<img src="` + stdhtml.EscapeString(href) + `" alt=""/>`, ""
		}
		return "", ""
	case "p":
		if r.inFB2("title") {
			return r.openHeading("h1", "p", el), "</h1>\n"
		}
		return fb2OpenTag("p", el), "</p>\n"
	case "subtitle":
		return r.openHeading("h2", "subtitle", el), "</h2>\n"
	case "title":
		return r.openTitle(el), "</header>\n"
	case "section":
		if id := strings.TrimSpace(xmlAttr(el, "id")); r.noteTargets[id] {
			return fb2OpenTagWithIDAndEPUBType("aside", el, id, "footnote"), "</aside>\n"
		}
		return fb2OpenTag("section", el), "</section>\n"
	case "annotation":
		return fb2OpenTag("aside", el), "</aside>\n"
	case "epigraph", "cite", "poem":
		return fb2OpenTag("blockquote", el), "</blockquote>\n"
	case "stanza":
		return fb2OpenTag("p", el), "</p>\n"
	case "v":
		return "", "<br/>\n"
	case "text-author", "date":
		return fb2OpenTag("p", el), "</p>\n"
	case "strong":
		return fb2OpenTag("strong", el), "</strong>"
	case "emphasis":
		return fb2OpenTag("em", el), "</em>"
	case "style":
		return fb2OpenTag("span", el), "</span>"
	case "strikethrough":
		return fb2OpenTag("s", el), "</s>"
	case "sub":
		return fb2OpenTag("sub", el), "</sub>"
	case "sup":
		return fb2OpenTag("sup", el), "</sup>"
	case "code":
		return fb2OpenTag("code", el), "</code>"
	case "a":
		if href := fb2SafeLinkHref(el); href != "" {
			open := `<a href="` + stdhtml.EscapeString(href) + `"`
			if r.noteTargets[fb2LocalRefID(href)] {
				open += ` epub:type="noteref"`
			}
			return open + ">", "</a>"
		}
		return "<span>", "</span>"
	case "table", "tr", "th", "td":
		return fb2OpenTag(name, el), "</" + name + ">\n"
	default:
		return "", ""
	}
}

// These inline classifiers mirror the inline cases in renderedElement. Keep
// them in sync when adding a rendered FB2 inline element, or whitespace between
// adjacent inline tags can disappear.
func isFB2InlineText(name string) bool {
	switch name {
	case "p", "subtitle", "text-author", "date", "strong", "emphasis", "style", "strikethrough", "sub", "sup", "code", "a":
		return true
	default:
		return false
	}
}

func fb2StartsInlineOutput(name string) bool {
	switch name {
	case "image", "strong", "emphasis", "style", "strikethrough", "sub", "sup", "code", "a":
		return true
	default:
		return false
	}
}

func (r *fb2EPUBRenderer) openHeading(tag, fb2Name string, el xml.StartElement) string {
	id := strings.TrimSpace(xmlAttr(el, "id"))
	if id == "" {
		id = r.nextHeadingID()
	} else if r.usedIDs != nil {
		r.usedIDs[id] = true
	}
	r.heading = &fb2HeadingCapture{fb2Name: fb2Name, id: id, deferTitleNav: fb2Name == "p" && r.inFB2("title")}
	return fb2OpenTagWithID(tag, el, id)
}

func (r *fb2EPUBRenderer) openTitle(el xml.StartElement) string {
	r.titleNav = &fb2TitleNavCapture{}
	return fb2OpenTag("header", el)
}

func (r *fb2EPUBRenderer) finishHeading() {
	title := strings.Join(strings.Fields(r.heading.title.String()), " ")
	id := r.heading.id
	deferTitleNav := r.heading.deferTitleNav
	r.heading = nil
	if title == "" {
		return
	}
	if r.footnoteDepth > 0 {
		return
	}
	if decorativeHeadingTitle(title) {
		return
	}
	if deferTitleNav {
		r.addTitleNavPart(title, id)
		return
	}
	r.nav = append(r.nav, epubNavItem{Title: title, Href: "text.xhtml#" + id})
}

func (r *fb2EPUBRenderer) addTitleNavPart(title, id string) {
	if r.titleNav == nil {
		r.nav = append(r.nav, epubNavItem{Title: title, Href: "text.xhtml#" + id})
		return
	}
	if r.titleNav.href == "" {
		r.titleNav.href = "text.xhtml#" + id
	}
	r.titleNav.labels = append(r.titleNav.labels, title)
}

func (r *fb2EPUBRenderer) finishTitleNav() {
	if r.titleNav == nil {
		return
	}
	capture := r.titleNav
	r.titleNav = nil
	title := strings.Join(capture.labels, " ")
	if title == "" || capture.href == "" {
		return
	}
	r.nav = append(r.nav, epubNavItem{Title: title, Href: capture.href})
}

func (r *fb2EPUBRenderer) nextHeadingID() string {
	for {
		r.headingSeq++
		id := fmt.Sprintf("fb2-heading-%d", r.headingSeq)
		if r.usedIDs == nil || !r.usedIDs[id] {
			if r.usedIDs != nil {
				r.usedIDs[id] = true
			}
			return id
		}
	}
}

func (r *fb2EPUBRenderer) inFB2(name string) bool {
	for _, frame := range slices.Backward(r.stack) {
		if frame.name == name {
			return true
		}
	}
	return false
}

func (r *fb2EPUBRenderer) imageHref(el xml.StartElement) string {
	id := fb2RefID(xmlAttr(el, "href"))
	if id == "" {
		return ""
	}
	return r.imageRefs[id]
}

func fb2OpenTag(tag string, el xml.StartElement) string {
	return fb2OpenTagWithID(tag, el, strings.TrimSpace(xmlAttr(el, "id")))
}

func fb2OpenTagWithID(tag string, el xml.StartElement, id string) string {
	return fb2OpenTagWithIDAndEPUBType(tag, el, id, "")
}

func fb2OpenTagWithIDAndEPUBType(tag string, el xml.StartElement, id, epubType string) string {
	var attrs strings.Builder
	if id != "" {
		attrs.WriteString(` id="`)
		attrs.WriteString(stdhtml.EscapeString(id))
		attrs.WriteByte('"')
	}
	if epubType != "" {
		attrs.WriteString(` epub:type="`)
		attrs.WriteString(stdhtml.EscapeString(epubType))
		attrs.WriteByte('"')
	}
	for _, name := range []string{"colspan", "rowspan"} {
		if value := strings.TrimSpace(xmlAttr(el, name)); value != "" {
			attrs.WriteByte(' ')
			attrs.WriteString(name)
			attrs.WriteString(`="`)
			attrs.WriteString(stdhtml.EscapeString(value))
			attrs.WriteByte('"')
		}
	}
	return "<" + tag + attrs.String() + ">"
}

func fb2SafeLinkHref(el xml.StartElement) string {
	return safeHTMLLinkHref(xmlAttr(el, "href"))
}

func fb2LocalRefID(href string) string {
	href = strings.TrimSpace(href)
	if !strings.HasPrefix(href, "#") {
		return ""
	}
	return strings.TrimPrefix(href, "#")
}

func decorativeHeadingTitle(title string) bool {
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func fb2RefID(href string) string {
	href = strings.TrimSpace(href)
	href = strings.TrimPrefix(href, "#")
	if href == "" {
		return ""
	}
	_, file := path.Split(href)
	if file != "" {
		return file
	}
	return href
}

func xmlAttr(el xml.StartElement, local string) string {
	for _, attr := range el.Attr {
		if attr.Name.Local == local {
			return attr.Value
		}
	}
	return ""
}
