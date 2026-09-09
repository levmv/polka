package format

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type pageImageSize func(attrs map[string]string) (int, int)

func pageImageHref(attrs map[string]string) string {
	for _, key := range []string{"src", "href", "xlink:href"} {
		if attrs[key] != "" {
			return attrs[key]
		}
	}
	return ""
}

func htmlLocalName(name string) string {
	if _, local, ok := strings.Cut(name, ":"); ok {
		return local
	}
	return name
}

func pageBlock(tag string) bool {
	switch tag {
	case "p", "div", "blockquote", "section", "article", "ul", "ol", "dl", "table", "li", "dt", "dd", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "pre", "address", "figcaption":
		return true
	}
	return false
}

func htmlPageExtent(ctx context.Context, raw []byte, extent *pageExtent, imageSize pageImageSize) error {
	return htmlPageExtentAndAnchors(ctx, raw, extent, imageSize, nil)
}

// Anchors in visually hidden page-break spans still locate pages. Inert
// content (head, scripts, templates and alternative SVG/MathML content) does
// not supply reading locations.
func htmlPageExtentAndAnchors(ctx context.Context, raw []byte, extent *pageExtent, imageSize pageImageSize, anchor func(string, map[string]string)) error {
	decoded, err := DecodeHTMLToUTF8(raw)
	if err != nil {
		return err
	}
	z := html.NewTokenizer(bytes.NewReader(decoded))
	z.SetMaxBuf(8 << 20)
	type frame struct {
		tag    string
		hidden bool
		pre    bool
		inert  bool
	}
	stack := make([]frame, 0, 16)
	hidden, pre := false, false
	inert := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind := z.Next()
		switch kind {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				return nil
			}
			return z.Err()
		case html.StartTagToken, html.SelfClosingTagToken:
			if kind == html.SelfClosingTagToken {
				z.NextIsNotRawText()
			}
			token := z.Token()
			tag := htmlLocalName(token.Data)
			// HTML permits omitted paragraph, list-item and table-cell endings.
			// Restore their state before a sibling; otherwise hidden content can
			// hide the rest of the book, or flat text can exhaust the depth limit.
			if pageBlock(tag) || tag == "td" || tag == "th" || tag == "body" {
			ancestors:
				for i := len(stack) - 1; i >= 0; i-- {
					open := stack[i].tag
					if pageImpliedEnd(open, tag) {
						hidden, pre, inert = stack[i].hidden, stack[i].pre, stack[i].inert
						stack = stack[:i]
					}
					switch open {
					case "table", "ul", "ol", "dl", "button", "svg", "math", "template":
						break ancestors
					case "td", "th":
						if tag != "tr" {
							break ancestors
						}
					}
				}
			}
			attrs := make(map[string]string, len(token.Attr))
			for _, a := range token.Attr {
				attrs[a.Key] = a.Val
			}
			nextHidden, nextInert := hidden, inert
			switch tag {
			case "head", "script", "style", "template", "noscript":
				nextHidden, nextInert = true, true
			}
			_, hasHidden := attrs["hidden"]
			style := strings.ToLower(strings.ReplaceAll(attrs["style"], " ", ""))
			if hasHidden || attrs["aria-hidden"] == "true" || strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") {
				nextHidden = true
			}
			if !nextHidden && (pageBlock(tag) || tag == "br" || tag == "hr") {
				extent.Break()
			}
			if anchor != nil && !nextInert {
				anchor(tag, attrs)
			}
			if !nextHidden {
				switch tag {
				case "img", "image", "svg":
					w, h := pageDimension(attrs["width"]), pageDimension(attrs["height"])
					if tag == "svg" && (w == 0 || h == 0) {
						box := strings.Fields(strings.ReplaceAll(attrs["viewbox"], ",", " "))
						if len(box) == 4 {
							w, h = pageDimension(box[2]), pageDimension(box[3])
						}
					}
					if (w == 0 || h == 0) && imageSize != nil {
						if iw, ih := imageSize(attrs); iw > 0 && ih > 0 {
							switch {
							case w > 0:
								h = max(1, int(float64(ih)*float64(w)/float64(iw)))
							case h > 0:
								w = max(1, int(float64(iw)*float64(h)/float64(ih)))
							default:
								w, h = iw, ih
							}
						}
					}
					extent.Image(w, h)
					if tag == "svg" {
						nextHidden = true
					}
				case "math":
					// MathML commonly contains both presentation and source forms.
					// Reserve one inline formula, never sum its alternatives.
					extent.Text("formula")
					nextHidden = true
				case "td", "th":
					extent.Text(" ")
				}
			}
			if tag == "svg" || tag == "math" {
				nextInert = true
			}
			if kind == html.SelfClosingTagToken || htmlVoidElement(tag) {
				continue
			}
			if len(stack) >= 256 {
				return fmt.Errorf("page count: HTML nesting exceeds 256")
			}
			stack = append(stack, frame{tag: tag, hidden: hidden, pre: pre, inert: inert})
			hidden, pre, inert = nextHidden, pre || tag == "pre", nextInert
		case html.EndTagToken:
			tag := htmlLocalName(z.Token().Data)
			if !hidden && pageBlock(tag) {
				extent.Break()
			}
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == tag {
					hidden, pre, inert = stack[i].hidden, stack[i].pre, stack[i].inert
					stack = stack[:i]
					break
				}
			}
		case html.TextToken:
			if hidden {
				continue
			}
			text := string(z.Text())
			if pre {
				lines := strings.Split(text, "\n")
				for i, line := range lines {
					if i > 0 {
						extent.Break()
					}
					extent.Text(line)
				}
			} else {
				extent.Text(text)
			}
		}
	}
}

func htmlVoidElement(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}

func pageImpliedEnd(open, next string) bool {
	switch open {
	case "head":
		return next == "body"
	case "p":
		return pageBlock(next)
	case "li", "tr":
		return next == open
	case "dt", "dd":
		return next == "dt" || next == "dd"
	case "td", "th":
		return next == "td" || next == "th"
	}
	return false
}

func pageDimension(raw string) int {
	n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(raw), "px"), 64)
	if err != nil || n <= 0 || n > 1_000_000 {
		return 0
	}
	return int(n)
}
