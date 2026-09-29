package kfx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

const (
	maxGeneratedBytes = 64 << 20
	maxContentNodes   = 200000
)

// Document contains reading content ready for EPUB packaging.
type Document struct {
	Metadata   *bookmeta.Metadata
	Body       string
	Direction  string
	Navigation []NavItem
	Pages      []NavItem
	Resources  []Resource
	FixedPages []FixedPage
}

// FixedPage retains a page's viewport and its place in a spread. Hrefs are
// relative to the package, as are resource and navigation links.
type FixedPage struct {
	Href, Body, Spread string
	Width, Height      int
}

type NavItem struct {
	Label, Href string
	Children    []NavItem
}

type Resource struct {
	ID, Href, MediaType string
	Data                []byte
	Cover               bool
}

type contentReader struct {
	book          *book
	doc           *Document
	cover         string
	warn          func(string)
	warned        map[string]bool
	resources     map[string]int
	styles        map[string]contentStyle
	rubies        map[string]map[int64]value
	anchors       map[position]string
	anchorTargets map[string]position
	externalLinks map[string]string
	elements      map[string]*element
	active        map[fragmentKey]bool
	nodes         int
	styleFields   int
	navCount      int
	generatedLeft int
	fixed         bool
}

func newContentReader(b *book, doc *Document, cover string, warn func(string)) *contentReader {
	return &contentReader{book: b, doc: doc, cover: cover, warn: warn, warned: make(map[string]bool),
		resources: make(map[string]int), styles: make(map[string]contentStyle), rubies: make(map[string]map[int64]value), anchors: make(map[position]string),
		anchorTargets: make(map[string]position), externalLinks: make(map[string]string), elements: make(map[string]*element),
		active: make(map[fragmentKey]bool), generatedLeft: maxGeneratedBytes}
}

func (c *contentReader) claimGenerated(n int) error {
	if err := c.book.ctx.Err(); err != nil {
		return err
	}
	if n > c.generatedLeft {
		return fmt.Errorf("%w: generated content is too large", ErrLimit)
	}
	c.generatedLeft -= n
	return nil
}

func (c *contentReader) claimNodes(n int) error {
	if err := c.book.ctx.Err(); err != nil {
		return err
	}
	if n > maxContentNodes-c.nodes {
		return fmt.Errorf("%w: too many content nodes", ErrLimit)
	}
	c.nodes += n
	return nil
}

// Only local format damage is recoverable. IO errors, cancellation and aggregate
// limits still stop the operation, including when reading optional information.
func (c *contentReader) recover(err error, message string) bool {
	if !errors.Is(err, ErrUnsupported) {
		return false
	}
	c.warning(message)
	return true
}

func ExtractDocument(ctx context.Context, r io.ReaderAt, size int64, warn func(string)) (*Document, error) {
	b, err := openBook(ctx, r, size)
	if err != nil {
		return nil, err
	}
	meta, cover, err := b.metadata()
	if err != nil {
		return nil, err
	}
	doc := &Document{Metadata: meta}
	c := newContentReader(b, doc, cover, warn)
	metadata, err := b.singleton(fragmentMetadata)
	if err != nil {
		return nil, err
	}
	for _, category := range metadata.get(fieldCategories).list {
		if category.get(fieldCategory).text != "kindle_capability_metadata" {
			continue
		}
		for _, item := range category.get(fieldMetadataEntries).list {
			switch item.get(fieldMetadataKey).text {
			case "yj_fixed_layout":
				c.fixed = true
			case "yj_textbook":
				return nil, invalid("Print Replica KFX needs PDF export")
			}
		}
	}
	settings, err := b.singleton(fragmentSettings)
	if err != nil {
		return nil, err
	}
	if settings.get(fieldDirection).text == symbolRTL {
		doc.Direction = "rtl"
	}
	orders := settings.get(fieldReadingOrders).list
	if len(orders) == 0 {
		legacy, err := b.singleton(fragmentLegacyMetadata)
		if err != nil {
			return nil, err
		}
		orders = legacy.get(fieldReadingOrders).list
	}
	if len(orders) == 0 {
		return nil, invalid("missing reading order; supply the complete book bundle")
	}
	if err := c.navigation(); err != nil {
		return nil, err
	}
	root := &element{tag: "div"}
	if c.fixed {
		// Image coordinates may be LTR while the book's pages progress RTL.
		if settings.get(fieldWritingMode).text == "$559" {
			doc.Direction = "rtl"
		}
	} else {
		if err := c.fonts(); err != nil {
			return nil, err
		}
		style, err := c.style(settings, 0)
		if err != nil {
			return nil, err
		}
		if err := c.applyStyle(root, style.textStyle); err != nil {
			return nil, err
		}
		if mode := style.get("writing-mode"); mode == "vertical-rl" || mode == "vertical-lr" {
			// Readers discover the document's writing direction on html/body,
			// before laying out its sections and calculating page positions.
			doc.Resources = append(doc.Resources, Resource{ID: "layout", Href: "layout.css", MediaType: "text/css",
				Data: []byte("html,body{writing-mode:" + mode + ";}")})
			if mode == "vertical-rl" {
				doc.Direction = "rtl"
			}
		}
	}
	seen := make(map[string]bool)
	for _, order := range orders {
		for _, id := range order.get(fieldSections).list {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if seen[id.id()] {
				continue
			}
			seen[id.id()] = true
			section, err := b.find(fragmentSection, id.id())
			if err != nil {
				return nil, err
			}
			templates := section.get(fieldTemplates).list
			if len(templates) != 1 {
				return nil, invalid("section has %d page templates; conditional layouts are not supported", len(templates))
			}
			if c.fixed {
				if err := c.fixedTemplate(root, templates[0], "center", 0); err != nil {
					return nil, err
				}
				continue
			}
			part := &element{tag: "section"}
			part.attr("id", fmt.Sprintf("section-%d", len(seen)))
			if len(seen) > 1 {
				part.attr("style", "break-before:page")
			}
			node, err := c.content(templates[0], "section", 0)
			if err != nil {
				return nil, err
			}
			part.children = append(part.children, node)
			root.children = append(root.children, part)
		}
	}
	if len(root.children) == 0 {
		return nil, invalid("empty reading order")
	}
	// Preserve the declared cover even if it is not part of the reading order.
	if cover != "" {
		if _, err := c.resource(cover); err != nil {
			if !c.recover(err, "Could not include the declared KFX cover; kept the reading content.") {
				return nil, err
			}
		}
	}
	if err := c.placeAnchors(root); err != nil {
		return nil, err
	}
	if b.decoder.replacedControls {
		c.warning("Replaced control characters that EPUB cannot represent.")
	}
	if c.fixed {
		return c.renderFixedPages(root)
	}
	doc.Body, err = renderBody(ctx, root)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func (c *contentReader) warning(message string) {
	if c.warned[message] {
		return
	}
	c.warned[message] = true
	if c.warn != nil {
		c.warn(message)
	}
}

func (c *contentReader) resource(name string) (string, error) {
	if index, ok := c.resources[name]; ok {
		return c.doc.Resources[index].Href, nil
	}
	v, err := c.book.find(fragmentResource, name)
	if err != nil {
		return "", err
	}
	href, err := c.addResource(v, fragmentRawMedia)
	if err == nil {
		index := len(c.doc.Resources) - 1
		c.resources[name] = index
		c.doc.Resources[index].Cover = name == c.cover
	}
	return href, err
}

func (c *contentReader) addResource(v value, kind int) (string, error) {
	if len(c.doc.Resources) >= 4096 {
		return "", fmt.Errorf("%w: too many resources", ErrLimit)
	}
	data, err := c.book.resourceBytes(v, kind)
	if err != nil {
		return "", err
	}
	mediaType, ext := resourceType(data)
	mediaPrefix := "image/"
	if kind == fragmentRawFont {
		mediaPrefix = "font/"
	}
	if !strings.HasPrefix(mediaType, mediaPrefix) {
		return "", invalid("unsupported resource format %s; expected %s", v.get(fieldFormat).text, mediaPrefix)
	}
	id := fmt.Sprintf("resource-%d", len(c.doc.Resources)+1)
	r := Resource{ID: id, Href: "resources/" + id + ext, MediaType: mediaType, Data: data}
	c.doc.Resources = append(c.doc.Resources, r)
	return r.Href, nil
}

func (c *contentReader) fonts() error {
	var css strings.Builder
	used := make(map[string]string)
	for _, f := range c.book.ordered {
		if f.key.kind != fragmentFont {
			continue
		}
		if err := c.book.ctx.Err(); err != nil {
			return err
		}
		v, err := c.book.load(f)
		if err != nil {
			if !c.recover(err, "Ignored a damaged KFX font declaration.") {
				return err
			}
			continue
		}
		style, err := c.style(v, 0)
		if err != nil {
			if !c.recover(err, "Ignored a damaged KFX font style.") {
				return err
			}
			continue
		}
		location := v.get(fieldResourceLocation).text
		href := used[location]
		if href == "" {
			href, err = c.addResource(v, fragmentRawFont)
			if err != nil {
				if !c.recover(err, "Could not embed a KFX font; readers can use a fallback font.") {
					return err
				}
				continue
			}
			used[location] = href
		}
		if err := c.claimGenerated(style.bytes + len(href) + 32); err != nil {
			return err
		}
		fmt.Fprintf(&css, "@font-face{%ssrc:url(%s);}\n", style.css(), cssString(href))
	}
	if css.Len() > 0 {
		c.doc.Resources = append(c.doc.Resources, Resource{ID: "fonts", Href: "fonts.css", MediaType: "text/css", Data: []byte(css.String())})
	}
	return nil
}

func resourceType(data []byte) (string, string) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "image/jpeg", ".jpg"
	case "image/png":
		return "image/png", ".png"
	case "image/gif":
		return "image/gif", ".gif"
	}
	switch {
	case bytes.HasPrefix(data, []byte{0, 1, 0, 0}):
		return "font/ttf", ".ttf"
	case bytes.HasPrefix(data, []byte("OTTO")):
		return "font/otf", ".otf"
	case bytes.HasPrefix(data, []byte("wOFF")):
		return "font/woff", ".woff"
	case bytes.HasPrefix(data, []byte("wOF2")):
		return "font/woff2", ".woff2"
	}
	return "", ""
}
