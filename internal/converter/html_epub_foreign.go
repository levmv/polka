package converter

import (
	"bytes"
	"encoding/xml"
	"fmt"
	stdhtml "html"
	"io"
	"net/url"
	"strings"

	"github.com/levmv/polka/internal/css"
	"github.com/levmv/polka/internal/xmlutil"
	"golang.org/x/net/html"
)

// XML permits qualified XHTML, MathML and SVG tags, whereas the HTML parser
// needs their unprefixed names. Resolve namespaces before changing those names
// and known attribute prefixes; retain all other source bytes.
func normalizeEPUBForeignPrefixes(raw []byte, fallbacks map[string]string) []byte {
	if !bytes.Contains(raw, []byte("xmlns:")) && len(fallbacks) == 0 {
		return raw
	}
	if normalized, err := normalizeXMLForeignPrefixes(raw); err == nil {
		raw = normalized
		if len(fallbacks) == 0 {
			return raw
		}
	}
	// Legacy Kindle flows mix prefixed SVG with HTML void elements and other
	// non-XML markup. A prefix with one meaning throughout the document can
	// still be normalized without guessing at the malformed element nesting.
	namespaces := map[string]string{}
	ambiguous := map[string]bool{}
	z := html.NewTokenizer(bytes.NewReader(raw))
	for kind := z.Next(); kind != html.ErrorToken; kind = z.Next() {
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		for _, attr := range z.Token().Attr {
			prefix, ok := strings.CutPrefix(attr.Key, "xmlns:")
			if !ok {
				continue
			}
			if previous, exists := namespaces[prefix]; exists && previous != attr.Val {
				ambiguous[prefix] = true
			}
			if !ambiguous[prefix] || attr.Val == "http://www.w3.org/2000/svg" || attr.Val == "http://www.w3.org/1998/Math/MathML" {
				namespaces[prefix] = attr.Val
			}
		}
	}
	for prefix, namespace := range fallbacks {
		if _, declared := namespaces[prefix]; !declared {
			namespaces[prefix] = namespace
		}
	}
	z = html.NewTokenizer(bytes.NewReader(raw))
	var out bytes.Buffer
	for kind := z.Next(); kind != html.ErrorToken; kind = z.Next() {
		part := z.Raw()
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken || kind == html.EndTagToken {
			name, _ := z.TagName()
			prefix, _, prefixed := strings.Cut(string(name), ":")
			namespace := namespaces[prefix]
			if prefixed && !ambiguous[prefix] && (namespace == "http://www.w3.org/2000/svg" || namespace == "http://www.w3.org/1998/Math/MathML") {
				start := 1
				if kind == html.EndTagToken {
					start++
				}
				out.Write(part[:start])
				out.Write(part[start+len(prefix)+1:])
				continue
			}
		}
		out.Write(part)
	}
	return out.Bytes()
}

func normalizeXMLForeignPrefixes(raw []byte) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Entity = xml.HTMLEntity
	var edits []rebuildXMLEdit
	for {
		before := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		var name xml.Name
		switch t := token.(type) {
		case xml.StartElement:
			name = t.Name
		case xml.EndElement:
			name = t.Name
		}
		if name.Space != "http://www.w3.org/2000/svg" && name.Space != "http://www.w3.org/1998/Math/MathML" && name.Space != "http://www.w3.org/1999/xhtml" {
			continue
		}
		part := raw[before:decoder.InputOffset()]
		if len(part) < 2 { // Synthetic close of a self-closing tag.
			continue
		}
		start := 1
		if part[1] == '/' {
			start++
		}
		end := start
		for end < len(part) && !strings.ContainsRune(" \t\r\n/>", rune(part[end])) {
			end++
		}
		if bytes.ContainsRune(part[start:end], ':') {
			edits = append(edits, rebuildXMLEdit{before + start, before + end, []byte(name.Local)})
		}
		if element, ok := token.(xml.StartElement); ok {
			spans := xmlutil.AttributeSpans(string(part))
			for i, attr := range element.Attr {
				if i >= len(spans) {
					break
				}
				qualified := ""
				switch attr.Name.Space {
				case "http://www.w3.org/1999/xlink":
					qualified = "xlink:" + attr.Name.Local
				case "http://www.w3.org/XML/1998/namespace":
					qualified = "xml:" + attr.Name.Local
				}
				if qualified != "" && string(part[spans[i].NameStart:spans[i].NameEnd]) != qualified {
					edits = append(edits, rebuildXMLEdit{before + spans[i].NameStart, before + spans[i].NameEnd, []byte(qualified)})
				}
			}
		}
	}
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out, nil
}

func isPrefixedEPUBForeign(n *html.Node) bool {
	prefix, local, ok := strings.Cut(n.Data, ":")
	if !ok {
		return false
	}
	if local == "svg" || local == "math" {
		return true
	}
	for node := n; node != nil; node = node.Parent {
		if namespace := htmlAttr(node, "xmlns:"+prefix); namespace != "" {
			return namespace == "http://www.w3.org/2000/svg" || namespace == "http://www.w3.org/1998/Math/MathML"
		}
	}
	return false
}

func epubBodyProperties(body string, assets []epubAsset) string {
	svgImages := map[string]bool{}
	for _, asset := range assets {
		if asset.MediaType == "image/svg+xml" {
			svgImages[asset.Href] = true
		}
	}
	var svg, math bool
	z := html.NewTokenizer(strings.NewReader(body))
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := z.Token()
		switch token.Data {
		case "svg":
			svg = true
		case "math":
			math = true
		case "img", "object":
			for _, attr := range token.Attr {
				href, _, _ := strings.Cut(attr.Val, "#")
				if (attr.Key == "src" || attr.Key == "data") && svgImages[href] {
					svg = true
				}
			}
		}
	}
	var properties []string
	if svg {
		properties = append(properties, "svg")
	}
	if math {
		properties = append(properties, "mathml")
	}
	return strings.Join(properties, " ")
}

// Foreign content retains its vocabulary and whitespace. Recover at attribute
// or element boundaries, before writing an opening tag, so a missing resource
// never leaves half-written XML or discards the rest of the book.
func renderEPUBForeign(out *strings.Builder, n *html.Node, parentNamespace string, resolvers htmlEPUBResolvers, state *htmlEPUBRenderState, depth int) error {
	if depth > 128 {
		return fmt.Errorf("SVG/MathML nesting exceeds limit: %w", ErrResourceLimit)
	}
	if n.Type == html.TextNode {
		out.WriteString(stdhtml.EscapeString(n.Data))
		return nil
	}
	if n.Type != html.ElementNode {
		return nil
	}
	tag := strings.ToLower(n.Data)
	if tag == "script" {
		return nil
	}
	switch tag {
	case "animate", "animatecolor", "animatemotion", "animatetransform", "set", "discard":
		resolvers.options.warn("Skipped SVG animation <%s>", n.Data)
		return nil
	}
	hasBase := false
	for _, attr := range n.Attr {
		name, _ := epubForeignAttributeName(n, attr)
		if name == "xml:base" && strings.TrimSpace(attr.Val) != "" {
			hasBase = true
		}
	}
	if n.Namespace != "svg" && n.Namespace != "math" || strings.Contains(n.Data, ":") || safeHTMLAnchorID(n.Data) != n.Data || tag == "foreignobject" || hasBase {
		resolvers.options.warn("Could not preserve SVG/MathML <%s>; kept available text", n.Data)
		renderEPUBForeignFallback(out, n, parentNamespace)
		return nil
	}

	type attribute struct {
		name, value, namespace string
		refs                   []string
	}
	var attrs []attribute
	hasHref := false
	for _, attr := range n.Attr {
		if attr.Namespace == "" && attr.Key == "href" {
			hasHref = true
		}
	}
	seen := map[string]bool{}
	for _, attr := range n.Attr {
		name, namespace := epubForeignAttributeName(n, attr)
		if name == "" {
			continue
		}
		value := attr.Val
		lower := strings.ToLower(name)
		prefix, local, prefixed := strings.Cut(lower, ":")
		if !prefixed {
			local = lower
		}
		if lower == "xmlns" || prefix == "xmlns" || strings.HasPrefix(local, "on") || lower == "xml:base" {
			continue
		}
		var refs []string
		var err error
		switch lower {
		case "id", "xml:id":
			value = safeHTMLAnchorID(value)
			if value == "" {
				continue
			}
		case "href", "xlink:href":
			if lower == "xlink:href" && hasHref {
				continue
			}
			if tag == "a" || n.Namespace == "math" {
				value = safeHTMLLinkHref(value)
				if value == "" {
					continue
				}
				if id, ok := htmlTargetDocumentFragment(value, "", ""); ok {
					refs = append(refs, id)
				}
			} else {
				value, err = epubForeignResource(value, tag == "image" || tag == "feimage", resolvers, &refs)
			}
			if n.Namespace == "svg" {
				name = "xlink:href"
			}
		case "src", "altimg":
			// MathML images need a packaged image, not an element in this XHTML.
			value, err = epubForeignResource(value, true, resolvers, nil)
		case "style", "fill", "stroke", "filter", "clip-path", "mask", "marker", "marker-start", "marker-mid", "marker-end", "cursor", "color-profile":
			value, err = rewriteEPUBForeignCSS(value, resolvers, &refs)
		}
		if err != nil {
			if fatalConversionError(err) {
				return err
			}
			resolvers.options.warn("Could not preserve <%s> %s: %v", n.Data, name, err)
			if tag == "image" || tag == "feimage" || tag == "use" || tag == "mglyph" {
				renderEPUBForeignFallback(out, n, parentNamespace)
				return nil
			}
			continue
		}
		if !seen[name] {
			seen[name] = true
			attrs = append(attrs, attribute{name, value, namespace, refs})
		}
	}

	fmt.Fprintf(out, "<%s", n.Data)
	if n.Namespace != parentNamespace {
		namespace := "http://www.w3.org/2000/svg"
		if n.Namespace == "math" {
			namespace = "http://www.w3.org/1998/Math/MathML"
		}
		fmt.Fprintf(out, ` xmlns="%s" xmlns:xlink="http://www.w3.org/1999/xlink"`, namespace)
	}
	declared := map[string]bool{}
	nodeIDs := map[string]string{}
	for _, attr := range attrs {
		if attr.namespace != "" {
			prefix, _, _ := strings.Cut(attr.name, ":")
			if !declared[prefix] {
				fmt.Fprintf(out, ` xmlns:%s="%s"`, prefix, stdhtml.EscapeString(attr.namespace))
				declared[prefix] = true
			}
		}
		if attr.name == "id" || attr.name == "xml:id" {
			id, ok := nodeIDs[attr.value]
			if !ok {
				id = state.claimRenderedID(attr.value)
				nodeIDs[attr.value] = id
			}
			attr.value = id
		}
		start := out.Len()
		fmt.Fprintf(out, ` %s="%s"`, attr.name, stdhtml.EscapeString(attr.value))
		if len(attr.refs) > 0 {
			state.references = append(state.references, htmlEPUBReference{start, out.Len(), attr.refs})
		}
	}
	out.WriteByte('>')
	if tag == "style" {
		var css strings.Builder
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.TextNode {
				css.WriteString(child.Data)
			}
		}
		var refs []string
		if value, err := rewriteEPUBForeignCSS(css.String(), resolvers, &refs); err != nil {
			if fatalConversionError(err) {
				return err
			}
			resolvers.options.warn("Skipped SVG/MathML stylesheet: %v", err)
		} else {
			start := out.Len()
			out.WriteString(stdhtml.EscapeString(value))
			if len(refs) > 0 {
				state.references = append(state.references, htmlEPUBReference{start, out.Len(), refs})
			}
		}
	} else {
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := renderEPUBForeign(out, child, n.Namespace, resolvers, state, depth+1); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(out, "</%s>", n.Data)
	return nil
}

func renderEPUBForeignFallback(out *strings.Builder, n *html.Node, namespace string) {
	text := htmlAttr(n, "alt")
	if text == "" {
		text = htmlNodeText(n)
	}
	if text == "" {
		return
	}
	tag := "span"
	if namespace == "svg" {
		tag = "text"
	} else if namespace == "math" {
		if n.Parent != nil && containsToken("mi mn mo ms mtext", n.Parent.Data) {
			out.WriteString(stdhtml.EscapeString(text))
			return
		}
		tag = "mtext"
	}
	fmt.Fprintf(out, "<%s>%s</%s>", tag, stdhtml.EscapeString(text), tag)
}

// HTML parsing recognizes the usual xml/xlink prefixes, but XML sources may
// declare aliases. Resolve those as well so an aliased XLink cannot bypass the
// resource policy. Other declared attributes (for example editor metadata) keep
// their namespace, even when its declaration was on a removed HTML ancestor.
// Invalid names and undeclared attributes have no usable XML meaning; omit
// those attributes without discarding the element or refusing the whole book.
func epubForeignAttributeName(n *html.Node, attr html.Attribute) (name, namespace string) {
	name = attr.Key
	if attr.Namespace != "" {
		name = attr.Namespace + ":" + name
	}
	prefix, local, prefixed := strings.Cut(name, ":")
	if !prefixed {
		local = name
	}
	if local == "" || safeHTMLAnchorID(local) != local || strings.Contains(local, ":") || prefixed && (prefix == "" || safeHTMLAnchorID(prefix) != prefix) {
		return "", ""
	}
	if !prefixed || prefix == "xmlns" {
		return name, ""
	}
	for node := n; node != nil; node = node.Parent {
		if namespace = htmlAttr(node, "xmlns:"+prefix); namespace != "" {
			break
		}
	}
	switch {
	case prefix == "xml" || namespace == "http://www.w3.org/XML/1998/namespace":
		return "xml:" + local, ""
	case prefix == "xlink" || namespace == "http://www.w3.org/1999/xlink":
		return "xlink:" + local, ""
	case namespace != "":
		return name, namespace
	default:
		return "", ""
	}
}

func epubForeignResource(raw string, image bool, resolvers htmlEPUBResolvers, refs *[]string) (string, error) {
	ref, err := url.Parse(strings.TrimSpace(raw))
	if refs != nil && err == nil && ref.Scheme == "" && ref.Host == "" && ref.Path == "" && ref.RawQuery == "" && ref.Fragment != "" {
		*refs = append(*refs, ref.Fragment)
		return "#" + ref.EscapedFragment(), nil
	}
	if image && resolvers.image != nil {
		resource := raw
		if err == nil && ref.Fragment != "" {
			resource, _, _ = strings.Cut(raw, "#")
		}
		if href, ok := resolvers.image(resource); ok {
			if err == nil && ref.Fragment != "" {
				href += "#" + ref.EscapedFragment()
			}
			return href, nil
		}
	}
	if !image && resolvers.resource != nil {
		if href, ok := resolvers.resource(raw); ok {
			return href, nil
		}
	}
	return "", fmt.Errorf("SVG/MathML resource %.200q cannot be packaged: %w", raw, ErrUnsupportedContent)
}

// Validate the supported static CSS and package its dependencies. The syntax
// reader owns strings, escapes and nesting; this is the foreign-content policy.
func rewriteEPUBForeignCSS(raw string, resolvers htmlEPUBResolvers, refs *[]string) (string, error) {
	values := css.Parse(raw)
	var edits []css.Edit
	err := values.Walk(func(c css.Component) error {
		if !c.Closed || c.Kind == css.BadString || c.Kind == css.BadURL {
			return fmt.Errorf("invalid SVG/MathML CSS: %w", ErrUnsupportedContent)
		}
		if c.Kind == css.AtKeyword || c.Is('@') {
			return fmt.Errorf("unsupported SVG/MathML CSS at-rule: %w", ErrUnsupportedContent)
		}
		if ref, ok := c.Reference(); ok {
			href, err := epubForeignResource(ref.URL, false, resolvers, refs)
			if err != nil {
				return err
			}
			if href != ref.URL {
				edits = append(edits, css.Edit{Span: ref.Span, Text: ref.WithURL(href)})
			}
			return nil
		}
		if c.Kind != css.Function {
			return nil
		}
		function := strings.ToLower(c.Text())
		switch function {
		case "rgb", "rgba", "hsl", "hsla", "hwb", "lab", "lch", "oklab", "oklch", "color", "color-mix",
			"calc", "min", "max", "clamp", "var", "matrix", "matrix3d", "translate", "translatex", "translatey", "translate3d",
			"scale", "scalex", "scaley", "scale3d", "rotate", "rotatex", "rotatey", "rotatez", "rotate3d", "skew", "skewx", "skewy",
			"blur", "brightness", "contrast", "drop-shadow", "grayscale", "hue-rotate", "invert", "opacity", "saturate", "sepia",
			"not", "is", "where", "has", "nth-child", "nth-last-child", "nth-of-type", "nth-last-of-type":
			return nil
		default:
			return fmt.Errorf("unsupported SVG/MathML CSS function %q: %w", function, ErrUnsupportedContent)
		}
	})
	if err != nil {
		return "", err
	}
	return values.Apply(edits)
}
