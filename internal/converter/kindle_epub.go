package converter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/levmv/polka/internal/format"
)

const (
	kindleEmbedResourcePrefix = "kindle:embed:"
	kindleFlowResourcePrefix  = "kindle:flow:"
	kindleRecindexImagePrefix = "kindle-recindex:"
	kindleRecindexMediaPrefix = "kindle-mediarecindex:"
)

var (
	kindleCSSEmbedURLReferenceRE = regexp.MustCompile(`(?i)url\(\s*(['"]?)(kindle:embed:([0-9A-V]+)(?:\?[^'")\s]*)?)['"]?\s*\)`)
	kindleCSSFlowURLReferenceRE  = regexp.MustCompile(`(?i)url\(\s*(['"]?)(kindle:flow:([0-9A-V]+)\?mime=([a-z0-9.+-]+/[a-z0-9.+-]+)(?:[^'")\s]*)?)['"]?\s*\)`)
)

func convertKindleSourceToEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, from format.Format, size int64, opts ConversionOptions) error {
	doc, err := format.ExtractKindleDocument(src, size, from)
	if err != nil {
		if errors.Is(err, format.ErrKindleResourceLimit) {
			return fmt.Errorf("extract Kindle document: %v: %w", err, ErrResourceLimit)
		}
		return fmt.Errorf("extract Kindle document: %w", err)
	}
	return convertKindleDocumentToEPUB(ctx, w, doc, opts)
}

func convertKindleDocumentToEPUB(ctx context.Context, w io.Writer, doc *format.KindleDocument, opts ConversionOptions) error {
	if doc == nil {
		return fmt.Errorf("Kindle document is required")
	}
	if len(doc.Flows) != 1 {
		return fmt.Errorf("Kindle EPUB conversion supports one text flow, got %d", len(doc.Flows))
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	for _, flow := range doc.Flows {
		if err := claimConversionDecodedBytes(ctx, int64(len(flow.Data)), "Kindle text flow"); err != nil {
			return err
		}
	}

	flow := doc.Flows[0]
	body, assets, err := kindleEPUBBody(doc, flow)
	if err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	meta := epubMetadataWithFallback(toEPUBMetadata(doc.Metadata), opts)
	return writeSimpleEPUBWithNav(ctx, w, body, meta, kindleEPUBNav(doc.Navigation), assets...)
}

func kindleEPUBBody(doc *format.KindleDocument, flow format.KindleTextFlow) (string, []epubAsset, error) {
	switch strings.ToLower(strings.TrimSpace(flow.MediaType)) {
	case "", "text/html", "application/xhtml+xml":
		bodyRaw := insertKindleFileposAnchors(flow.Data, kindleReferencedFilepos(doc, flow.Data))
		bodyRaw = rewriteKindleFlowAttrs(bodyRaw)
		assets := kindleEPUBAssets(doc.Resources)
		resolvers := htmlEPUBResolvers{
			image: kindleImageResolver(doc.Resources),
			media: kindleMediaResolver(doc.Resources),
		}
		body, _, _, err := htmlBodyToEPUB(bodyRaw, "", resolvers)
		return body, assets, err
	case "text/plain":
		text := cleanTextForEPUB(string(flow.Data))
		return plainTextBody(text), nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported Kindle text flow media type %s", flow.MediaType)
	}
}

func kindleReferencedFilepos(doc *format.KindleDocument, flow []byte) []int {
	seen := map[int]bool{}
	var refs []int
	add := func(pos int) {
		if pos < 0 || pos > len(flow) || seen[pos] {
			return
		}
		seen[pos] = true
		refs = append(refs, pos)
	}
	var walkNav func([]format.KindleNavItem)
	walkNav = func(items []format.KindleNavItem) {
		for _, item := range items {
			if pos, ok := kindleFileposFromHref(item.Href); ok {
				add(pos)
			}
			walkNav(item.Children)
		}
	}
	walkNav(doc.Navigation)
	for _, ref := range doc.Guide {
		if pos, ok := kindleFileposFromHref(ref.Href); ok {
			add(pos)
		}
	}

	z := html.NewTokenizer(bytes.NewReader(flow))
	for {
		typ := z.Next()
		switch typ {
		case html.ErrorToken:
			sort.Ints(refs)
			return refs
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			if !strings.EqualFold(token.Data, "a") {
				continue
			}
			if pos, ok := kindleDecimalAttr(kindleTokenAttr(token.Attr, "filepos")); ok {
				add(int(pos))
			}
		}
	}
}

func insertKindleFileposAnchors(raw []byte, refs []int) []byte {
	if len(refs) == 0 {
		return raw
	}
	var out bytes.Buffer
	last := 0
	for _, pos := range refs {
		if pos < last || pos > len(raw) {
			continue
		}
		out.Write(raw[last:pos])
		if tag, tagNameEnd, ok := kindleStartTagNameEnd(raw, pos); ok {
			if kindleCanCarryRenderedID(tag) {
				out.Write(raw[pos:tagNameEnd])
				fmt.Fprintf(&out, ` id="filepos%d"`, pos)
				last = tagNameEnd
				continue
			}
			if tagEnd, ok := kindleStartTagEnd(raw, pos); ok {
				out.Write(raw[pos:tagEnd])
				fmt.Fprintf(&out, `<a id="filepos%d"></a>`, pos)
				last = tagEnd
				continue
			}
		}
		fmt.Fprintf(&out, `<a id="filepos%d"></a>`, pos)
		last = pos
	}
	out.Write(raw[last:])
	return out.Bytes()
}

func kindleCanCarryRenderedID(tag string) bool {
	tag = strings.ToLower(tag)
	if tag == "html" || tag == "body" || tag == "head" || shouldSkipHTMLElement(tag) {
		return false
	}
	_, ok := epubHTMLTag(tag)
	return ok
}

func kindleStartTagNameEnd(raw []byte, pos int) (string, int, bool) {
	if pos+1 >= len(raw) || raw[pos] != '<' {
		return "", 0, false
	}
	switch raw[pos+1] {
	case '/', '!', '?':
		return "", 0, false
	}
	i := pos + 1
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\n', '\r', '/', '>':
			if i <= pos+1 {
				return "", 0, false
			}
			return string(raw[pos+1 : i]), i, true
		}
		i++
	}
	return "", 0, false
}

func kindleStartTagEnd(raw []byte, pos int) (int, bool) {
	for i := pos; i < len(raw); i++ {
		if raw[i] == '>' {
			return i + 1, true
		}
	}
	return 0, false
}

func rewriteKindleFlowAttrs(raw []byte) []byte {
	z := html.NewTokenizer(bytes.NewReader(raw))
	var out bytes.Buffer
	for {
		typ := z.Next()
		switch typ {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				return out.Bytes()
			}
			return raw
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			if rewriteKindleTokenAttrs(&token) {
				out.WriteString(token.String())
				continue
			}
			out.Write(z.Raw())
		default:
			out.Write(z.Raw())
		}
	}
}

func rewriteKindleTokenAttrs(token *html.Token) bool {
	switch {
	case strings.EqualFold(token.Data, "a"):
		pos, ok := kindleDecimalAttr(kindleTokenAttr(token.Attr, "filepos"))
		if !ok {
			return false
		}
		token.Attr = kindleAttrsWithout(token.Attr, "filepos")
		if kindleTokenAttr(token.Attr, "href") == "" {
			token.Attr = append(token.Attr, html.Attribute{Key: "href", Val: fmt.Sprintf("#filepos%d", pos)})
		}
		return true
	case strings.EqualFold(token.Data, "img"):
		recindex, ok := kindleDecimalAttr(kindleTokenAttr(token.Attr, "recindex"))
		if !ok {
			return false
		}
		token.Attr = kindleAttrsWithout(token.Attr, "src", "recindex")
		token.Attr = append(token.Attr, html.Attribute{Key: "src", Val: fmt.Sprintf("%s%d", kindleRecindexImagePrefix, recindex)})
		return true
	case strings.EqualFold(token.Data, "audio"), strings.EqualFold(token.Data, "video"):
		recindex, ok := kindleDecimalAttr(kindleTokenAttr(token.Attr, "mediarecindex"))
		if !ok {
			return false
		}
		token.Attr = kindleAttrsWithout(token.Attr, "src", "mediarecindex")
		token.Attr = append(token.Attr, html.Attribute{Key: "src", Val: fmt.Sprintf("%s%d", kindleRecindexMediaPrefix, recindex)})
		return true
	default:
		return false
	}
}

func kindleAttrsWithout(attrs []html.Attribute, names ...string) []html.Attribute {
	out := attrs[:0]
	for _, attr := range attrs {
		drop := false
		for _, name := range names {
			if strings.EqualFold(attr.Key, name) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, attr)
		}
	}
	return out
}

func kindleImageResolver(resources []format.KindleResource) htmlImageResolver {
	return func(src string) (string, bool) {
		src = strings.TrimSpace(src)
		if raw, ok := strings.CutPrefix(src, kindleRecindexImagePrefix); ok {
			index, err := strconv.ParseUint(raw, 10, 32)
			if err != nil {
				return "", false
			}
			return kindleImageResourceHref(resources, index)
		}
		if raw, ok := cutKindlePrefix(src, kindleEmbedResourcePrefix); ok {
			if before, _, ok := strings.Cut(raw, "?"); ok {
				raw = before
			}
			index, err := strconv.ParseUint(strings.ToUpper(raw), 32, 32)
			if err != nil {
				return "", false
			}
			return kindleEmbedResourceHref(resources, index, "image/")
		}
		if raw, ok := cutKindlePrefix(src, kindleFlowResourcePrefix); ok {
			flow, mediaType, ok := kindleFlowResourceRef(raw)
			if !ok || !strings.EqualFold(mediaType, "image/svg+xml") {
				return "", false
			}
			return kindleFlowResourceHref(resources, flow, mediaType)
		}
		return "", false
	}
}

func kindleMediaResolver(resources []format.KindleResource) htmlMediaResolver {
	return func(src, mediaPrefix string) (string, bool) {
		src = strings.TrimSpace(src)
		if raw, ok := cutKindlePrefix(src, kindleRecindexMediaPrefix); ok {
			index, err := strconv.ParseUint(raw, 10, 32)
			if err != nil {
				return "", false
			}
			return kindleEmbedResourceHref(resources, index, mediaPrefix)
		}
		if raw, ok := cutKindlePrefix(src, kindleEmbedResourcePrefix); ok {
			if before, _, ok := strings.Cut(raw, "?"); ok {
				raw = before
			}
			index, err := strconv.ParseUint(strings.ToUpper(raw), 32, 32)
			if err != nil {
				return "", false
			}
			return kindleEmbedResourceHref(resources, index, mediaPrefix)
		}
		return "", false
	}
}

func cutKindlePrefix(src, prefix string) (string, bool) {
	if len(src) < len(prefix) || !strings.EqualFold(src[:len(prefix)], prefix) {
		return "", false
	}
	return src[len(prefix):], true
}

func kindleImageResourceHref(resources []format.KindleResource, index uint64) (string, bool) {
	if index == 0 {
		return "", false
	}
	var seen uint64
	for _, resource := range resources {
		if !kindleResourceHasMediaPrefix(resource, "image/") {
			continue
		}
		seen++
		if seen == index {
			return kindleResourceHref(resource)
		}
	}
	return "", false
}

func kindleEmbedResourceHref(resources []format.KindleResource, index uint64, mediaPrefix string) (string, bool) {
	if index == 0 {
		return "", false
	}
	hasEmbedIndex := false
	for _, resource := range resources {
		if resource.EmbedIndex > 0 {
			hasEmbedIndex = true
		}
		if resource.EmbedIndex == int(index) {
			return kindleResourceHrefWithMediaPrefix(resource, mediaPrefix)
		}
	}
	if !hasEmbedIndex && int(index) <= len(resources) {
		return kindleResourceHrefWithMediaPrefix(resources[index-1], mediaPrefix)
	}
	return "", false
}

func kindleResourceHrefWithMediaPrefix(resource format.KindleResource, mediaPrefix string) (string, bool) {
	if mediaPrefix != "" && !kindleResourceHasMediaPrefix(resource, mediaPrefix) {
		return "", false
	}
	return kindleResourceHref(resource)
}

func kindleResourceHasMediaPrefix(resource format.KindleResource, mediaPrefix string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(resource.MediaType)), mediaPrefix)
}

func kindleResourceHasMediaType(resource format.KindleResource, mediaType string) bool {
	return strings.EqualFold(strings.TrimSpace(resource.MediaType), strings.TrimSpace(mediaType))
}

func kindleResourceHref(resource format.KindleResource) (string, bool) {
	href := strings.TrimSpace(resource.Href)
	return href, href != ""
}

func kindleFlowResourceRef(raw string) (uint64, string, bool) {
	flowRaw, query, ok := strings.Cut(raw, "?")
	if !ok {
		return 0, "", false
	}
	flow, err := strconv.ParseUint(strings.ToUpper(flowRaw), 32, 32)
	if err != nil || flow == 0 {
		return 0, "", false
	}
	for part := range strings.SplitSeq(query, "&") {
		key, value, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "mime") && strings.TrimSpace(value) != "" {
			return flow, strings.ToLower(strings.TrimSpace(value)), true
		}
	}
	return 0, "", false
}

func kindleFlowResourceHref(resources []format.KindleResource, flow uint64, mediaType string) (string, bool) {
	if flow == 0 {
		return "", false
	}
	for _, resource := range resources {
		if resource.FlowIndex == int(flow) && kindleResourceHasMediaType(resource, mediaType) {
			return kindleResourceHref(resource)
		}
	}
	return "", false
}

func kindleEPUBAssets(resources []format.KindleResource) []epubAsset {
	assets := make([]epubAsset, 0, len(resources))
	for _, resource := range resources {
		if len(resource.Data) == 0 || strings.TrimSpace(resource.Href) == "" || strings.TrimSpace(resource.MediaType) == "" {
			continue
		}
		data := resource.Data
		if strings.EqualFold(strings.TrimSpace(resource.MediaType), "text/css") {
			data = rewriteKindleCSSResourceReferences(resource.Data, resource.Href, resources)
		}
		assets = append(assets, epubAsset{
			ID:        resource.ID,
			Href:      resource.Href,
			MediaType: resource.MediaType,
			Data:      data,
			Cover:     resource.Cover,
		})
	}
	return assets
}

func rewriteKindleCSSResourceReferences(css []byte, cssHref string, resources []format.KindleResource) []byte {
	css = kindleCSSEmbedURLReferenceRE.ReplaceAllFunc(css, func(match []byte) []byte {
		parts := kindleCSSEmbedURLReferenceRE.FindSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		index, err := strconv.ParseUint(strings.ToUpper(string(parts[3])), 32, 32)
		if err != nil {
			return match
		}
		href, ok := kindleEmbedResourceHref(resources, index, "")
		if !ok {
			return match
		}
		resource, ok := kindleEmbedResource(resources, index)
		if !ok || !kindleResourceHasMediaPrefix(resource, "image/") && !kindleResourceHasMediaPrefix(resource, "font/") {
			return match
		}
		quote := string(parts[1])
		relative := epubRelativeAssetHref(cssHref, href)
		if quote == "" {
			return []byte("url(" + relative + ")")
		}
		return []byte("url(" + quote + relative + quote + ")")
	})
	return kindleCSSFlowURLReferenceRE.ReplaceAllFunc(css, func(match []byte) []byte {
		parts := kindleCSSFlowURLReferenceRE.FindSubmatch(match)
		if len(parts) != 5 {
			return match
		}
		flow, err := strconv.ParseUint(strings.ToUpper(string(parts[3])), 32, 32)
		mediaType := strings.ToLower(string(parts[4]))
		if err != nil {
			return match
		}
		href, ok := kindleFlowResourceHref(resources, flow, mediaType)
		if !ok {
			return match
		}
		quote := string(parts[1])
		relative := epubRelativeAssetHref(cssHref, href)
		if quote == "" {
			return []byte("url(" + relative + ")")
		}
		return []byte("url(" + quote + relative + quote + ")")
	})
}

func kindleEmbedResource(resources []format.KindleResource, index uint64) (format.KindleResource, bool) {
	if index == 0 {
		return format.KindleResource{}, false
	}
	hasEmbedIndex := false
	for _, resource := range resources {
		if resource.EmbedIndex > 0 {
			hasEmbedIndex = true
		}
		if resource.EmbedIndex == int(index) {
			return resource, true
		}
	}
	if !hasEmbedIndex && int(index) <= len(resources) {
		return resources[index-1], true
	}
	return format.KindleResource{}, false
}

func epubRelativeAssetHref(fromHref, toHref string) string {
	fromHref = strings.TrimSpace(fromHref)
	toHref = strings.TrimLeft(strings.TrimSpace(toHref), "/")
	dir := path.Dir(strings.TrimLeft(fromHref, "/"))
	if dir == "." || dir == "/" {
		return toHref
	}
	depth := len(strings.Split(strings.Trim(dir, "/"), "/"))
	return strings.Repeat("../", depth) + toHref
}

func kindleEPUBNav(items []format.KindleNavItem) []epubNavItem {
	var nav []epubNavItem
	var walk func([]format.KindleNavItem)
	walk = func(items []format.KindleNavItem) {
		for _, item := range items {
			title := strings.TrimSpace(item.Label)
			href := kindleEPUBHref(item.Href)
			if title != "" && href != "" {
				nav = append(nav, epubNavItem{Title: title, Href: href})
			}
			walk(item.Children)
		}
	}
	walk(items)
	return nav
}

func kindleEPUBHref(href string) string {
	if _, fragment, ok := strings.Cut(strings.TrimSpace(href), "#"); ok && fragment != "" {
		return "text.xhtml#" + fragment
	}
	return ""
}

func kindleFileposFromHref(href string) (int, bool) {
	_, fragment, ok := strings.Cut(strings.TrimSpace(href), "#filepos")
	if !ok {
		return 0, false
	}
	pos, ok := kindleDecimalAttr(fragment)
	return int(pos), ok
}

func kindleTokenAttr(attrs []html.Attribute, name string) string {
	for _, attr := range attrs {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func kindleDecimalAttr(value string) (uint32, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(parsed), true
}
