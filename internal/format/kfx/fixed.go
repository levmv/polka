package kfx

import (
	"fmt"
	"math"
	"strconv"
)

func (c *contentReader) renderFixedPages(root *element) (*Document, error) {
	// Source anchors are already resolved; assign their output page paths now.
	targets := make(map[string]string)
	var collect func(*element, string)
	collect = func(e *element, href string) {
		if id := e.attribute("id"); id != "" {
			targets["#"+id] = href + "#" + id
		}
		for _, child := range e.children {
			collect(child, href)
		}
	}
	for i, page := range root.children {
		collect(page, c.doc.FixedPages[i].Href)
	}
	var rewrite func(*element)
	rewrite = func(e *element) {
		for i, a := range e.attrs {
			if a.name == "href" && targets[a.value] != "" {
				e.attrs[i].value = targets[a.value]
			}
		}
		for _, child := range e.children {
			rewrite(child)
		}
	}
	rewrite(root)
	var nav func([]NavItem)
	nav = func(items []NavItem) {
		for i := range items {
			if href := targets[items[i].Href]; href != "" {
				items[i].Href = href
			}
			nav(items[i].Children)
		}
	}
	nav(c.doc.Navigation)
	nav(c.doc.Pages)
	for i, page := range root.children {
		body, err := renderBody(c.book.ctx, page)
		if err != nil {
			return nil, err
		}
		if err := c.claimGenerated(len(body)); err != nil {
			return nil, err
		}
		c.doc.FixedPages[i].Body = body
	}
	return c.doc, nil
}

func (c *contentReader) fixedTemplate(root *element, v value, spread string, depth int) error {
	if depth > 64 {
		return fmt.Errorf("%w: fixed-layout page structure is too deeply nested", ErrLimit)
	}
	if err := c.claimNodes(1); err != nil {
		return err
	}
	v, err := c.book.resolve(fragmentContent, v)
	if err != nil {
		return err
	}
	style, err := c.style(v, 0)
	if err != nil {
		return err
	}
	if v.get(fieldType).text != symbolContainer {
		return invalid("unsupported fixed-layout page type %s", v.get(fieldType).text)
	}
	group := style.layout == symbolSpread || style.layout == symbolFacing
	center := style.layout == symbolVertical && v.get(fieldPageSpread).integer == 1
	if group || center {
		if center && v.get(fieldConnectedPages).integer != 2 {
			return invalid("unsupported connected-page layout")
		}
		key := fragmentKey{fragmentStory, v.get(fieldStory).id()}
		if c.active[key] {
			return invalid("recursive page story %s", key.id)
		}
		c.active[key] = true
		defer delete(c.active, key)
		story, err := c.book.find(key.kind, key.id)
		if err != nil {
			return err
		}
		children := story.get(fieldChildren).list
		if len(children) == 0 || len(v.get(fieldChildren).list) != 0 {
			return invalid("invalid page-spread contents")
		}
		first := len(root.children)
		side := "center"
		if group {
			side = "left"
			if c.doc.Direction == "rtl" {
				side = "right"
			}
		}
		for _, child := range children {
			if err := c.fixedTemplate(root, child, side, depth+1); err != nil {
				return err
			}
			if side == "left" {
				side = "right"
			} else if side == "right" {
				side = "left"
			}
		}
		if err := c.registerLocation(v, root.children[first]); err != nil {
			return err
		}
		return c.registerLocation(story, root.children[first])
	}
	if style.layout != symbolScaleFit {
		return invalid("unsupported fixed-layout page layout %s", style.layout)
	}
	width, height, err := fixedDimensions(style)
	if err != nil {
		return err
	}
	if len(c.doc.FixedPages) >= 4096 {
		return fmt.Errorf("%w: too many fixed-layout pages", ErrLimit)
	}
	page, err := c.content(v, "section", depth+1)
	if err != nil {
		return err
	}
	root.children = append(root.children, page)
	c.doc.FixedPages = append(c.doc.FixedPages, FixedPage{
		Href: fmt.Sprintf("page-%d.xhtml", len(root.children)), Width: width, Height: height, Spread: spread,
	})
	return nil
}

func fixedDimensions(style contentStyle) (int, int, error) {
	valid := func(n float64) bool { return n > 0 && n <= 1e6 }
	if !valid(style.fixedWidth) || !valid(style.fixedHeight) {
		return 0, 0, invalid("fixed-layout page has no usable viewport")
	}
	return int(math.Ceil(style.fixedWidth)), int(math.Ceil(style.fixedHeight)), nil
}

func (c *contentReader) fixedContentStyle(e *element, style *contentStyle) error {
	position := "relative"
	for _, name := range []string{"top", "left", "bottom", "right"} {
		if style.get(name) != "" {
			position = "absolute"
			break
		}
	}
	if err := style.set("position", position); err != nil {
		return err
	}
	switch style.layout {
	case "", symbolVertical:
	case symbolFixed:
		for _, child := range e.children {
			css := child.attribute("style") + "position:absolute;"
			if err := c.claimGenerated(len("position:absolute;")); err != nil {
				return err
			}
			child.attr("style", css)
		}
	case symbolScaleFit:
		width, height, err := fixedDimensions(*style)
		if err != nil {
			return err
		}
		for _, a := range []attribute{{"width", strconv.Itoa(width) + "px"}, {"height", strconv.Itoa(height) + "px"}, {"overflow", "hidden"}} {
			if err := style.set(a.name, a.value); err != nil {
				return err
			}
		}
	default:
		return invalid("unsupported fixed-layout container %s", style.layout)
	}
	// Image-based pages have no inline baseline or reader typography margins.
	if e.tag == "img" {
		return style.set("display", "block")
	}
	return nil
}
