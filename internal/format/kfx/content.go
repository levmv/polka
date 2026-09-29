package kfx

import (
	"fmt"
	"strconv"
	"strings"
)

func (c *contentReader) content(v value, parent string, depth int) (*element, error) {
	if err := c.book.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > 64 {
		return nil, fmt.Errorf("%w: content structure is too large or deeply nested", ErrLimit)
	}
	if err := c.claimNodes(1); err != nil {
		return nil, err
	}
	if v.kind == ionString {
		if c.fixed && strings.TrimSpace(v.text) != "" {
			return nil, invalid("fixed-layout KFX currently requires image-based pages")
		}
		if err := c.claimGenerated(len(v.text)); err != nil {
			return nil, err
		}
		return &element{text: v.text}, nil
	}
	if v.kind == ionSymbol {
		resolved, err := c.book.resolve(fragmentContent, v)
		if err != nil {
			return nil, err
		}
		return c.content(resolved, parent, depth+1)
	}
	if v.kind != ionStruct {
		return nil, invalid("invalid content node")
	}
	style, err := c.style(v, 0)
	if err != nil {
		return nil, err
	}
	tag := "div"
	kind := v.get(fieldType).text
	if c.fixed && kind != symbolContainer && kind != symbolImage {
		return nil, invalid("fixed-layout KFX currently requires image-based pages")
	}
	switch kind {
	case symbolText:
		if v.get(fieldText).kind != ionPadding {
			tag = "p"
		}
	case symbolContainer, "":
	case symbolImage:
		tag = "img"
	case symbolList:
		tag = "ul"
		switch style.get("list-style-type") {
		case "decimal", "decimal-leading-zero", "lower-roman", "upper-roman", "lower-alpha", "upper-alpha":
			tag = "ol"
		}
	case symbolListItem:
		tag = "li"
	case symbolTable:
		tag = "table"
	case symbolRow:
		tag = "tr"
	case symbolTableBody:
		tag = "tbody"
	case symbolTableHead:
		tag = "thead"
	case symbolTableFoot:
		tag = "tfoot"
	case symbolRule:
		tag = "hr"
	default:
		return nil, invalid("unsupported content type %s", kind)
	}
	if style.render == symbolInline && (tag == "p" || tag == "div") {
		tag = "span"
	}
	if heading := style.headingLevel; heading > 0 && heading <= 6 && tag != "img" {
		tag = "h" + strconv.FormatInt(heading, 10)
	}
	if parent == "tr" && tag == "div" {
		tag = "td"
	}
	e := &element{tag: tag, inlineObject: style.render == symbolInline}
	if err := c.registerLocation(v, e); err != nil {
		return nil, err
	}
	if style.listStart != "" && (tag == "ol" || tag == "li") {
		name := "start"
		if tag == "li" {
			name = "value"
		}
		e.attr(name, style.listStart)
	}
	if tag == "img" {
		href, err := c.resource(v.get(fieldResource).id())
		if err != nil {
			return nil, err
		}
		e.attr("src", href)
		e.attrs = append(e.attrs, attribute{"alt", v.get(fieldAltText).text})
	}
	if text := v.get(fieldText); text.kind != ionPadding {
		text, err := c.text(text)
		if err != nil {
			return nil, err
		}
		if c.fixed && strings.TrimSpace(text) != "" {
			return nil, invalid("fixed-layout KFX currently requires image-based pages")
		}
		if err := c.claimGenerated(len(text)); err != nil {
			return nil, err
		}
		e.children = append(e.children, &element{text: text})
	}
	if err := c.appendContent(e, v.get(fieldChildren).list, depth); err != nil {
		return nil, err
	}
	if storyID := v.get(fieldStory).id(); storyID != "" {
		key := fragmentKey{fragmentStory, storyID}
		if c.active[key] {
			return nil, invalid("recursive story %s", storyID)
		}
		c.active[key] = true
		story, err := c.book.find(fragmentStory, storyID)
		if err != nil {
			return nil, err
		}
		if err := c.registerLocation(story, e); err != nil {
			return nil, err
		}
		if err := c.appendContent(e, story.get(fieldChildren).list, depth); err != nil {
			return nil, err
		}
		delete(c.active, key)
	}
	if e.inlineObject {
		display := ""
		switch e.tag {
		case "table":
			display = "inline-table"
		case "div", "ol", "ul":
			display = "inline-block"
		}
		if err := style.set("display", display); err != nil {
			return nil, err
		}
	}
	if c.fixed {
		if err := c.fixedContentStyle(e, &style); err != nil {
			return nil, err
		}
	} else if !e.inlineObject && style.boxAlign != "" && (tag == "table" || tag != "img" && style.get("width") != "") {
		// Align the block itself without changing text alignment within it.
		for _, side := range []string{"left", "right"} {
			if style.boxAlign != side {
				if err := style.set("margin-"+side, "auto"); err != nil {
					return nil, err
				}
			}
		}
	}
	if tag == "td" {
		if err := style.set("vertical-align", style.tableVerticalAlign); err != nil {
			return nil, err
		}
	}
	if err := c.applyStyle(e, style.textStyle); err != nil {
		return nil, err
	}
	var initial []styleEvent
	if style.dropcapLines > 0 && style.dropcapChars > 0 {
		if style.dropcapLines > 1000 || style.dropcapChars > maxGeneratedBytes {
			return nil, invalid("invalid drop-cap dimensions")
		}
		var capStyle textStyle
		for _, a := range []attribute{{"float", "left"}, {"font-size", strconv.FormatInt(style.dropcapLines, 10) + "em"}, {"line-height", "1"}, {"margin", "0 0.1em 0 0"}} {
			if err := capStyle.set(a.name, a.value); err != nil {
				return nil, err
			}
		}
		initial = append(initial, styleEvent{start: 0, end: int(style.dropcapChars), style: capStyle})
	}
	if err := c.styleEvents(e, v.get(fieldStyleEvents).list, depth, initial...); err != nil {
		return nil, err
	}
	if style.link != "" {
		href := c.link(style.link)
		if href != "" {
			if err := c.linkContent(e, href); err != nil {
				return nil, err
			}
		}
	}
	if !c.fixed && tag == "img" && style.render != symbolInline && style.boxAlign != "" {
		// A block image needs an alignment container; text-align on img has
		// no effect. Keep its source location and link on the image inside.
		if err := c.claimNodes(1); err != nil {
			return nil, err
		}
		var alignment textStyle
		if err := alignment.set("text-align", style.boxAlign); err != nil {
			return nil, err
		}
		e = &element{tag: "div", children: []*element{e}}
		if err := c.applyStyle(e, alignment); err != nil {
			return nil, err
		}
	}
	if parent == "tr" && e.tag != "td" {
		// A cell can contain an image, a paragraph or a nested table directly.
		// Keep that content node intact inside its required XHTML cell wrapper.
		e = &element{tag: "td", children: []*element{e}}
		var cellStyle textStyle
		if err := cellStyle.set("vertical-align", style.tableVerticalAlign); err != nil {
			return nil, err
		}
		if err := c.applyStyle(e, cellStyle); err != nil {
			return nil, err
		}
	}
	if e.tag == "td" {
		if span := style.columnSpan; span > 1 {
			e.attr("colspan", strconv.FormatInt(span, 10))
		}
		if span := style.rowSpan; span > 1 {
			e.attr("rowspan", strconv.FormatInt(span, 10))
		}
	}
	return e, nil
}

func (c *contentReader) appendContent(parent *element, values []value, depth int) error {
	for _, v := range values {
		child, err := c.content(v, parent.tag, depth+1)
		if err != nil {
			return err
		}
		if (parent.tag == "p" || parent.tag == "span") && !child.phrasing() {
			parent.tag = "div"
		}
		parent.children = append(parent.children, child)
	}
	return nil
}

// Table and list containers require structural children. Put links inside their
// cells/items, and preserve more specific links already present in the content.
func (c *contentReader) linkContent(e *element, href string) error {
	if e.tag == "a" {
		return nil
	}
	if e.structured() || e.hasLink() {
		for _, child := range e.children {
			if err := c.linkContent(child, href); err != nil {
				return err
			}
		}
		return nil
	}
	if err := c.claimNodes(1); err != nil {
		return err
	}
	link := &element{tag: "a", attrs: []attribute{{"href", href}}}
	if e.void() || e.tag == "" {
		original := *e
		link.children = []*element{&original}
		*e = *link
	} else {
		link.children, e.children = e.children, []*element{link}
	}
	return nil
}

func (c *contentReader) registerLocation(v value, e *element) error {
	if id := locationID(v); id != "" {
		if previous := c.elements[id]; previous != nil && previous != e {
			return invalid("duplicate content location %s", id)
		}
		c.elements[id] = e
	}
	return nil
}

func (c *contentReader) text(v value) (string, error) {
	if v.kind == ionString {
		return v.text, nil
	}
	if v.kind != ionStruct {
		return "", invalid("invalid text reference")
	}
	fragment, err := c.book.find(fragmentText, v.named("name").id())
	if err != nil {
		return "", err
	}
	index := v.get(fieldTextIndex).integer
	texts := fragment.get(fieldChildren).list
	if index < 0 || index >= int64(len(texts)) || texts[index].kind != ionString {
		return "", invalid("text index is out of range")
	}
	return texts[index].text, nil
}

func locationID(v value) string {
	if id := v.get(fieldLocation).id(); id != "" {
		return id
	}
	return v.get(fieldLocalLocation).id()
}
