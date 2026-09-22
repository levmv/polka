package converter

import (
	"bytes"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

type mobi6Reference struct {
	at  int
	key string
}

type mobi6Renderer struct {
	source      *mobi6Source
	out         bytes.Buffer
	anchors     map[string]int
	references  []mobi6Reference
	err         error
	styleWork   int
	column      int
	tocRendered bool
}

const mobi6TOCAnchor = "\x00toc"

func (s *mobi6Source) render() (mobi6Book, error) {
	r := &mobi6Renderer{source: s, anchors: map[string]int{}}
	r.write(`<html><head><meta http-equiv="Content-Type" content="text/html; charset=utf-8"><title>` + html.EscapeString(s.meta.Title) + `</title><guide>`)
	if len(s.nav) > 0 {
		r.write(`<reference type="toc" title="Contents" filepos="`)
		r.reference(mobi6TOCAnchor)
		r.write(`">`)
	}
	r.write(`<reference type="text" title="Beginning" filepos="`)
	r.reference(s.start)
	r.write(`"></guide></head><body>`)
	for i, doc := range s.documents {
		if i > 0 {
			r.write(`<mbp:pagebreak/>`)
		}
		r.mark(mobi6Key(doc.name, ""))
		r.node(doc.root, doc, mobi6Style{}, 0)
		if r.err != nil {
			return mobi6Book{}, fmt.Errorf("convert %s to MOBI6: %w", doc.name, r.err)
		}
	}
	if len(s.nav) > 0 && !r.tocRendered {
		r.write(`<mbp:pagebreak/>`)
		r.mark(mobi6TOCAnchor)
		r.write(`<h1>Contents</h1>`)
		var toc func([]mobi6NavItem)
		toc = func(items []mobi6NavItem) {
			r.write(`<ul>`)
			for _, item := range items {
				r.write(`<li>`)
				if item.Href != "" {
					r.write(`<a filepos="`)
					r.reference(item.Href)
					r.write(`">`)
				}
				r.write(html.EscapeString(item.Label))
				if item.Href != "" {
					r.write(`</a>`)
				}
				if len(item.Children) > 0 {
					toc(item.Children)
				}
				r.write(`</li>`)
			}
			r.write(`</ul>`)
		}
		toc(s.nav)
	}
	r.write(`</body></html>`)
	if r.err != nil {
		return mobi6Book{}, r.err
	}
	data := r.out.Bytes()
	for _, ref := range r.references {
		pos, ok := r.anchors[ref.key]
		if !ok {
			return mobi6Book{}, fmt.Errorf("MOBI6 link target is missing: %s: %w", strings.ReplaceAll(ref.key, "\x00", "#"), ErrUnsupportedContent)
		}
		copy(data[ref.at:], fmt.Sprintf("%010d", pos))
	}
	var nav []mobi6Navigation
	var visitNav func([]mobi6NavItem)
	visitNav = func(items []mobi6NavItem) {
		for _, item := range items {
			if item.Href != "" {
				if pos, ok := r.anchors[item.Href]; ok {
					nav = append(nav, mobi6Navigation{label: item.Label, offset: pos})
				} else {
					r.err = fmt.Errorf("MOBI6 navigation target is missing: %s: %w", strings.ReplaceAll(item.Href, "\x00", "#"), ErrUnsupportedContent)
				}
			}
			visitNav(item.Children)
		}
	}
	visitNav(s.nav)
	if r.err != nil {
		return mobi6Book{}, r.err
	}
	if !utf8.Valid(data) {
		return mobi6Book{}, fmt.Errorf("MOBI6 text is not valid UTF-8")
	}
	return mobi6Book{text: data, images: s.images, cover: s.cover, meta: s.meta, nav: nav, start: r.anchors[s.start]}, nil
}

func (r *mobi6Renderer) write(s string) {
	if r.err != nil {
		return
	}
	if int64(r.out.Len())+int64(len(s)) > maxConverterDecodedInputBytes {
		r.err = fmt.Errorf("MOBI6 text exceeds limit: %w", ErrResourceLimit)
		return
	}
	r.out.WriteString(s)
}

func (r *mobi6Renderer) mark(key string) {
	// HTML resolves repeated IDs within one document to their first occurrence.
	if _, found := r.anchors[key]; !found {
		r.anchors[key] = r.out.Len()
	}
}

func (r *mobi6Renderer) reference(key string) {
	r.references = append(r.references, mobi6Reference{at: r.out.Len(), key: key})
	r.write("0000000000")
}

func (r *mobi6Renderer) node(n *html.Node, doc mobi6Document, inherited mobi6Style, depth int) {
	if r.err != nil {
		return
	}
	if depth > 256 {
		r.err = fmt.Errorf("MOBI6 content nesting exceeds limit: %w", ErrResourceLimit)
		return
	}
	if err := checkContext(r.source.ctx); err != nil {
		r.err = err
		return
	}
	if n.Type == html.TextNode {
		if !inherited.hidden {
			r.text(n.Data, inherited)
		}
		return
	}
	if n.Type != html.ElementNode && n.Type != html.DocumentNode {
		return
	}
	tag := n.Data
	if tag == "head" || tag == "script" || tag == "style" || tag == "template" {
		return
	}
	style := inherited
	if n.Type == html.ElementNode {
		var err error
		style, err = mobi6ComputedStyle(n, doc.css, inherited, &r.styleWork)
		if err != nil {
			r.err = err
			return
		}
		for _, key := range []string{"id", "name"} {
			if id := attrValue(n, key); id != "" {
				r.mark(mobi6Key(doc.name, id))
			}
		}
	}
	if style.hidden {
		return
	}
	if n == r.source.toc {
		r.mark(mobi6TOCAnchor)
		r.tocRendered = true
	}
	if tag == "svg" {
		r.svg(n, doc)
		return
	}
	if tag == "audio" || tag == "video" || tag == "math" || tag == "object" || tag == "embed" || tag == "iframe" {
		r.err = fmt.Errorf("MOBI6 cannot preserve <%s> content: %w", tag, ErrUnsupportedContent)
		return
	}
	if tag == "img" {
		r.image(doc.name, attrValue(n, "src"), attrValue(n, "alt"), n)
		return
	}
	if n.Type == html.DocumentNode || tag == "html" || tag == "body" {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			r.node(c, doc, style, depth+1)
		}
		return
	}
	if tag == "a" {
		r.write("<a")
		href := attrValue(n, "href")
		if href != "" {
			u, err := url.Parse(href)
			if err != nil {
				r.err = err
				return
			}
			if u.Scheme != "" || u.Host != "" {
				if safe := safeHTMLLinkHref(href); safe != "" {
					r.write(` href="` + html.EscapeString(safe) + `"`)
				}
			} else {
				key, err := r.source.link(doc.name, href)
				if err != nil {
					r.err = err
					return
				}
				r.write(` filepos="`)
				r.reference(key)
				r.write(`"`)
			}
		}
		r.write(">")
		r.text(style.before, style)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			r.node(c, doc, style, depth+1)
		}
		r.text(style.after, style)
		r.write("</a>")
		return
	}
	if tag == "pre" {
		tag = "p"
		r.column = 0
	}
	switch tag {
	case "section", "article", "aside", "nav", "main", "header", "footer", "figure", "figcaption", "hgroup":
		tag = "div"
	case "b", "strong", "i", "em", "u", "s", "strike", "del", "cite", "code", "tt", "kbd", "samp":
		tag = "span"
	}
	// Unknown wrappers keep their children. Only elements meaningful to the
	// legacy renderer are emitted; scripts and unsupported media were handled above.
	known := containsToken("p div span br hr h1 h2 h3 h4 h5 h6 blockquote ul ol li dl dt dd table caption thead tbody tfoot tr th td sup sub small big center", tag)
	if known {
		r.write("<" + tag)
		if containsToken("p div h1 h2 h3 h4 h5 h6 td th", tag) {
			align := style.align
			if align == "" {
				align = attrValue(n, "align")
			}
			if containsToken("left right center justify", align) {
				r.write(` align="` + align + `"`)
			}
			if tag == "p" && style.indent != "" {
				r.write(` width="` + html.EscapeString(style.indent) + `"`)
			}
		}
		for _, key := range []string{"colspan", "rowspan", "start", "value"} {
			if value, err := strconv.Atoi(attrValue(n, key)); err == nil && value > 0 {
				r.write(fmt.Sprintf(` %s="%d"`, key, value))
			}
		}
		if language := attrValue(n, "xml:lang"); language != "" {
			r.write(` lang="` + html.EscapeString(language) + `"`)
		}
		r.write(">")
	}
	r.text(style.before, style)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.node(c, doc, style, depth+1)
	}
	r.text(style.after, style)
	if known && tag != "br" && tag != "hr" {
		r.write("</" + tag + ">")
	}
}

func (r *mobi6Renderer) text(text string, style mobi6Style) {
	if text == "" {
		return
	}
	switch style.transform {
	case "uppercase":
		text = strings.ToUpper(text)
	case "lowercase":
		text = strings.ToLower(text)
	}
	var open, close string
	for _, wrapper := range []struct {
		enabled bool
		tag     string
	}{{style.bold, "b"}, {style.italic, "i"}, {style.underline, "u"}, {style.strike, "strike"}, {style.family == "monospace", "tt"}} {
		if wrapper.enabled {
			open += "<" + wrapper.tag + ">"
			close = "</" + wrapper.tag + ">" + close
		}
	}
	r.write(open)
	if style.pre {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
		for _, ch := range text {
			switch ch {
			case '\n':
				r.write("<br>")
				r.column = 0
			case '\t':
				spaces := 8 - r.column%8
				r.write(strings.Repeat("&#160;", spaces))
				r.column += spaces
			case ' ':
				r.write("&#160;")
				r.column++
			default:
				r.write(html.EscapeString(string(ch)))
				r.column++
			}
		}
	} else {
		r.write(html.EscapeString(collapseHTMLWhitespace(text)))
	}
	r.write(close)
}

func (r *mobi6Renderer) image(base, href, alt string, node *html.Node) {
	if href == "" {
		r.err = fmt.Errorf("MOBI6 image has no source: %w", ErrUnsupportedContent)
		return
	}
	id, err := r.source.image(base, href)
	if err != nil {
		r.err = err
		return
	}
	r.write(fmt.Sprintf(`<img recindex="%05d" alt="%s"`, id+1, html.EscapeString(alt)))
	for _, key := range []string{"width", "height"} {
		if value := attrValue(node, key); value != "" {
			r.write(` ` + key + `="` + html.EscapeString(value) + `"`)
		}
	}
	r.write(">")
}

func (r *mobi6Renderer) svg(n *html.Node, doc mobi6Document) {
	// A common EPUB cover is an SVG viewport containing just one raster image.
	// Unwrap only that unambiguous case; actual vector artwork needs a rasterizer.
	var img *html.Node
	err := mobi6Walk(n, func(c *html.Node) error {
		for _, key := range []string{"transform", "clip-path", "mask", "filter", "style"} {
			if attrValue(c, key) != "" {
				return ErrUnsupportedContent
			}
		}
		if c == n || c.Type != html.ElementNode || c.Data == "title" || c.Data == "desc" {
			return nil
		}
		if c.Data != "image" || img != nil {
			return ErrUnsupportedContent
		}
		img = c
		return nil
	})
	if err != nil || img == nil {
		r.err = fmt.Errorf("MOBI6 requires rasterization of SVG content: %w", ErrUnsupportedContent)
		return
	}
	// An offset or cropped viewport is artwork, not a transparent cover wrapper.
	for _, key := range []string{"x", "y"} {
		if value := attrValue(img, key); value != "" && value != "0" {
			r.err = fmt.Errorf("MOBI6 requires rasterization of positioned SVG content: %w", ErrUnsupportedContent)
			return
		}
	}
	if viewBox := attrValue(n, "viewBox"); viewBox != "" {
		parts := strings.Fields(strings.ReplaceAll(viewBox, ",", " "))
		if len(parts) != 4 || parts[0] != "0" || parts[1] != "0" || parts[2] != attrValue(img, "width") || parts[3] != attrValue(img, "height") {
			r.err = fmt.Errorf("MOBI6 requires rasterization of cropped SVG content: %w", ErrUnsupportedContent)
			return
		}
	} else if attrValue(n, "width") == "" || attrValue(n, "height") == "" || attrValue(n, "width") != attrValue(img, "width") || attrValue(n, "height") != attrValue(img, "height") {
		r.err = fmt.Errorf("MOBI6 requires rasterization of ambiguous SVG viewport: %w", ErrUnsupportedContent)
		return
	}
	href := attrValue(img, "href")
	if href == "" {
		href = attrValue(img, "xlink:href")
	}
	r.image(doc.name, href, "", img)
}
