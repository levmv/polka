package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/levmv/polka/internal/xmlutil"
	"golang.org/x/net/html"
)

type kf8Position struct{ fragment, offset, absolute int }
type kf8Skeleton struct{ start, length, fragments int }
type kf8Fragment struct {
	insert, file, start, length int
	selector                    string
}
type kf8Writer struct {
	*kindleEPUB
	text          []byte
	flows         [][]byte
	resources     [][]byte
	resourceRefs  map[*zip.File]string
	dataRefs      map[string]string
	anchors       map[string]string
	positions     map[string]kf8Position
	links         []string
	resourceCalls int
	skeletons     []kf8Skeleton
	fragments     []kf8Fragment
	cover         int
	images        *kindleImages
}

const kf8LinkPlaceholder = "kindle:pos:fid:0000:off:"

func convertEPUBToKF8(ctx context.Context, out io.Writer, src io.ReaderAt, size int64, opts ConversionOptions) error {
	publication, err := readKindleEPUB(ctx, src, size, opts)
	if err != nil {
		return err
	}
	w := &kf8Writer{kindleEPUB: publication, resourceRefs: map[*zip.File]string{}, dataRefs: map[string]string{}, anchors: map[string]string{}, positions: map[string]kf8Position{}, cover: -1}
	w.images = &kindleImages{kindleEPUB: publication, imageIDs: map[*zip.File]int{}}
	if err := w.prepare(); err != nil {
		return err
	}
	return w.write(ctx, out)
}

func kf8Base32(value, width int) string {
	s := strings.ToUpper(strconv.FormatInt(int64(value), 32))
	return strings.Repeat("0", max(0, width-len(s))) + s
}

func (w *kf8Writer) prepare() error {
	if w.coverHref != "" {
		id, err := w.images.image(w.opfPath, w.coverHref)
		if fatalConversionError(err) {
			return err
		}
		if err != nil {
			w.options.warn("Could not include the cover: %v", err)
		} else {
			w.cover = len(w.resources)
			w.resources = append(w.resources, w.images.images[id])
		}
	}
	if w.toc == nil && w.contents == "" && len(w.nav) > 0 {
		w.addContents()
	}
	aid := 0
	for _, doc := range w.documents {
		if err := walkKindleHTML(doc.root, func(n *html.Node) error {
			if n.Type != html.ElementNode {
				return nil
			}
			id := kf8Base32(aid, 1)
			aid++
			n.Attr = slices.DeleteFunc(n.Attr, func(a html.Attribute) bool { return a.Key == "aid" || a.Key == "cid" })
			n.Attr = append(n.Attr, html.Attribute{Key: "aid", Val: id})
			if n.Data == "body" {
				w.anchors[kindleKey(doc.name, "")] = id
			}
			for _, key := range []string{"id", "name"} {
				if value := attrValue(n, key); value != "" {
					key := kindleKey(doc.name, value)
					if _, ok := w.anchors[key]; !ok {
						w.anchors[key] = id
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for i := range w.documents {
		doc := &w.documents[i]
		if err := w.rewriteDocument(doc); err != nil {
			return err
		}
		if err := w.document(*doc, i); err != nil {
			return err
		}
	}
	if len(w.fragments) > 65535 || int64(len(w.text)) > maxConverterDecodedInputBytes {
		return ErrResourceLimit
	}
	// Resolve only link attributes. Examples, comments and CSS may contain
	// the same spelling as a placeholder and must keep their original text.
	z := html.NewTokenizer(bytes.NewReader(w.text))
	offset := 0
	for kind := z.Next(); kind != html.ErrorToken; kind = z.Next() {
		raw := z.Raw()
		start := offset
		offset += len(raw)
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		name, _ := z.TagName()
		if string(name) != "a" {
			continue
		}
		for _, attr := range xmlutil.AttributeSpans(string(raw)) {
			name := string(raw[attr.NameStart:attr.NameEnd])
			if name != "href" && name != "xlink:href" {
				continue
			}
			value := string(raw[attr.ValueStart:attr.ValueEnd])
			index, ok := strings.CutPrefix(value, kf8LinkPlaceholder)
			if !ok || len(index) != 10 {
				continue
			}
			i, err := strconv.ParseUint(index, 32, 32)
			if err != nil || i >= uint64(len(w.links)) {
				continue
			}
			pos := w.destination(w.links[i])
			// Fixed-width replacement keeps every indexed byte offset stable.
			copy(w.text[start+attr.ValueStart:], "kindle:pos:fid:"+kf8Base32(pos.fragment, 4)+":off:"+kf8Base32(pos.offset, 10))
		}
	}
	return nil
}

func (w *kf8Writer) destination(key string) kf8Position {
	if pos, ok := w.positions[w.anchors[key]]; ok {
		return pos
	}
	name, _, _ := strings.Cut(key, "\x00")
	return w.positions[w.anchors[kindleKey(name, "")]]
}

func (w *kf8Writer) addContents() {
	var content strings.Builder
	content.WriteString(`<html><head><title>Contents</title></head><body><nav id="toc"><h1>Contents</h1>`)
	var list func([]kindleNavItem)
	list = func(items []kindleNavItem) {
		content.WriteString("<ol>")
		for _, item := range items {
			content.WriteString("<li>")
			name, fragment, _ := strings.Cut(item.Href, "\x00")
			if name != "" {
				href := (&url.URL{Path: "/" + name, Fragment: fragment}).String()
				content.WriteString(`<a href="` + html.EscapeString(href) + `">`)
			}
			content.WriteString(html.EscapeString(item.Label))
			if name != "" {
				content.WriteString("</a>")
			}
			if len(item.Children) > 0 {
				list(item.Children)
			}
			content.WriteString("</li>")
		}
		content.WriteString("</ol>")
	}
	list(w.nav)
	content.WriteString("</nav></body></html>")
	root, _ := html.Parse(strings.NewReader(content.String()))
	name := "polka-contents.xhtml"
	for {
		found := false
		for _, doc := range w.documents {
			if doc.name == name {
				found = true
				break
			}
		}
		if !found {
			break
		}
		name = "_" + name
	}
	w.documents = append(w.documents, kindleDocument{name: name, root: root})
	w.contents = kindleKey(name, "toc")
	_ = walkKindleHTML(root, func(n *html.Node) error {
		if n.Data == "nav" {
			w.toc = n
		}
		return nil
	})
}

func (w *kf8Writer) linkReference(base, href string) (string, error) {
	file, fragment, external, err := w.reference(base, href)
	if err != nil {
		return "", err
	}
	if external {
		return safeHTMLLinkHref(href), nil
	}
	if file == nil {
		return "", fmt.Errorf("Kindle link document is missing: %.200s", href)
	}
	key := kindleKey(file.Name, fragment)
	if _, ok := w.anchors[key]; !ok {
		return "", fmt.Errorf("Kindle link target is missing: %.200s", href)
	}
	index := len(w.links)
	w.links = append(w.links, key)
	return kf8LinkPlaceholder + kf8Base32(index, 10), nil
}

func (w *kf8Writer) rewriteDocument(doc *kindleDocument) error {
	return walkKindleHTML(doc.root, func(n *html.Node) error {
		if err := checkContext(w.ctx); err != nil {
			return err
		}
		if n.Type != html.ElementNode {
			return nil
		}
		if n.Data == "script" {
			n.FirstChild = nil
			n.LastChild = nil
			n.Attr = nil
			n.Data = "span"
			return nil
		}
		for i := range n.Attr {
			a := &n.Attr[i]
			var value string
			var err error
			replace := false
			switch {
			case n.Data == "a" && a.Key == "href":
				value, err = w.linkReference(doc.name, a.Val)
				replace = true
			case n.Namespace == "svg":
				value, err = rewriteSVGAttribute(n.Data, a.Key, a.Val, func(element, href string) (string, error) {
					return w.svgReference(doc.name, element, href)
				})
				replace = true
			case n.Data == "link" && a.Key == "href" && containsToken(attrValue(n, "rel"), "stylesheet"):
				value, err = w.resource(doc.name, a.Val)
				replace = true
			case n.Data == "img" && a.Key == "src":
				value, err = w.resource(doc.name, a.Val)
				replace = true
			case n.Data == "object" && a.Key == "data":
				value, err = w.resource(doc.name, a.Val)
				replace = true
			case a.Key == "style":
				value, err = w.css(doc.name, []byte(a.Val))
				replace = true
			}
			if fatalConversionError(err) {
				return err
			}
			if err != nil {
				w.options.warn("Could not preserve %s %s: %v", n.Data, a.Key, err)
			}
			if replace {
				a.Val = value
			}
		}
		if n.Data == "style" {
			var raw strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					raw.WriteString(c.Data)
				}
			}
			css, err := w.css(doc.name, []byte(raw.String()))
			if err != nil {
				return err
			}
			n.FirstChild = nil
			n.LastChild = nil
			n.AppendChild(&html.Node{Type: html.TextNode, Data: css})
		}
		return nil
	})
}

func (w *kf8Writer) document(doc kindleDocument, file int) error {
	var body *html.Node
	_ = walkKindleHTML(doc.root, func(n *html.Node) error {
		if n.Type == html.ElementNode && n.Data == "body" {
			body = n
		}
		return nil
	})
	if body == nil {
		return fmt.Errorf("Kindle document has no body")
	}
	var content bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&content, c); err != nil {
			return err
		}
	}
	first, last := body.FirstChild, body.LastChild
	body.FirstChild = nil
	body.LastChild = nil
	var skeleton bytes.Buffer
	err := html.Render(&skeleton, doc.root)
	body.FirstChild, body.LastChild = first, last
	if err != nil {
		return err
	}
	if int64(len(w.text)+skeleton.Len()+content.Len()) > maxConverterDecodedInputBytes {
		return ErrResourceLimit
	}
	if err := claimConversionDecodedBytes(w.ctx, int64(skeleton.Len()+content.Len()), "KF8 text"); err != nil {
		return err
	}
	insert := 0
	z := html.NewTokenizer(bytes.NewReader(skeleton.Bytes()))
	for typ := z.Next(); typ != html.ErrorToken; typ = z.Next() {
		insert += len(z.Raw())
		if typ == html.StartTagToken && z.Token().Data == "body" {
			break
		}
	}
	chunks := kf8Chunks(content.Bytes())
	start := len(w.text)
	w.text = append(w.text, skeleton.Bytes()...)
	w.skeletons = append(w.skeletons, kf8Skeleton{start: start, length: skeleton.Len(), fragments: len(chunks)})
	firstFragment := len(w.fragments)
	offset := 0
	w.positions[attrValue(body, "aid")] = kf8Position{firstFragment, 0, start + insert}
	for _, chunk := range chunks {
		w.fragments = append(w.fragments, kf8Fragment{insert: start + insert + offset, file: file, start: offset, length: len(chunk), selector: `P-//*[@aid="` + attrValue(body, "aid") + `"]`})
		w.text = append(w.text, chunk...)
		offset += len(chunk)
	}
	// Read the continuous document so a chunk boundary inside raw style text
	// cannot turn a CSS string or an HTML comment into an indexed element.
	z = html.NewTokenizer(bytes.NewReader(content.Bytes()))
	offset, chunkStart, chunkIndex := 0, 0, 0
	for kind := z.Next(); kind != html.ErrorToken; kind = z.Next() {
		at := offset
		offset += len(z.Raw())
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		for at >= chunkStart+len(chunks[chunkIndex]) {
			chunkStart += len(chunks[chunkIndex])
			chunkIndex++
		}
		token := z.Token()
		for _, attr := range token.Attr {
			if attr.Key == "aid" {
				w.positions[attr.Val] = kf8Position{firstFragment + chunkIndex, at - chunkStart, start + insert + at}
			}
		}
	}
	return nil
}

// Split text at tokenizer boundaries, with UTF-8-safe splits for long text
// runs. Index positions refer to the same unchanged serialized byte stream.
func kf8Chunks(raw []byte) [][]byte {
	const size = 8192
	var out [][]byte
	var chunk []byte
	z := html.NewTokenizer(bytes.NewReader(raw))
	for typ := z.Next(); typ != html.ErrorToken; typ = z.Next() {
		token := bytes.Clone(z.Raw())
		for len(token) > 0 {
			if len(chunk) > 0 && len(chunk)+len(token) > size {
				out = append(out, chunk)
				chunk = nil
			}
			n := len(token)
			if typ == html.TextToken && n > size {
				n = size
				for n > 0 && !utf8.RuneStart(token[n]) {
					n--
				}
				// Keep character references intact as well as UTF-8 characters.
				if amp := bytes.LastIndexByte(token[:n], '&'); amp >= 0 && !bytes.ContainsRune(token[amp:n], ';') {
					n = amp
					if n == 0 {
						n = len(token)
					}
				}
			}
			chunk = append(chunk, token[:n]...)
			token = token[n:]
		}
	}
	if len(chunk) > 0 {
		out = append(out, chunk)
	}
	if len(out) == 0 {
		out = append(out, []byte("\n"))
	}
	return out
}
