package kfx

import (
	"fmt"
	"slices"
	"sort"
	"unicode/utf8"
)

type textRun struct {
	element    *element
	start, end int
	linked     bool
}

// Both style ranges and navigation use Unicode code points, including one
// position per image or inline object. Each inline object has an independent
// coordinate space inside it. Generated wrappers/anchors occupy no positions,
// and whitespace remains source text until final XHTML serialization.
func (c *contentReader) textRuns(root *element) ([]textRun, int, error) {
	var runs []textRun
	offset := 0
	var walk func(*element, bool) error
	walk = func(e *element, linked bool) error {
		if err := c.book.ctx.Err(); err != nil {
			return err
		}
		start := offset
		linked = linked || e.tag == "a"
		switch {
		case e != root && e.tag == "rt":
			return nil // Ruby annotations have their own locations, outside the base text.
		case e.tag == "":
			offset += utf8.RuneCountInString(e.text)
		case e.tag == "img" || e.tag == "hr" || e != root && e.inlineObject:
			offset++
		default:
			for _, child := range e.children {
				if err := walk(child, linked); err != nil {
					return err
				}
			}
			return nil
		}
		if offset > start {
			runs = append(runs, textRun{e, start, offset, linked})
		}
		return nil
	}
	err := walk(root, false)
	return runs, offset, err
}

type textMarker struct {
	offset int // Code point offset within one run; an image occupies one unit.
	id     string
}

func (c *contentReader) placeAnchors(root *element) error {
	positions := make([]position, 0, len(c.anchors))
	for p := range c.anchors {
		positions = append(positions, p)
	}
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].id != positions[j].id {
			return positions[i].id < positions[j].id
		}
		return positions[i].offset < positions[j].offset
	})
	markers := make(map[*element][]textMarker)
	rewrites := make(map[string]string)
	var marked []*element
	var indexed *element
	var runs []textRun
	var length int
	for _, p := range positions {
		if err := c.book.ctx.Err(); err != nil {
			return err
		}
		id := c.anchors[p]
		e := c.elements[p.id]
		if e == nil {
			rewrites["#"+id] = ""
			continue
		}
		if p.offset == 0 {
			// A story and its container can name the same position. Keep one
			// physical ID, and point every alias at it. An ID works on tables
			// and lists too, without introducing an illegal span child.
			if existing := e.attribute("id"); existing != "" {
				rewrites["#"+id] = "#" + existing
			} else {
				e.attr("id", id)
			}
			continue
		}
		if indexed != e {
			var err error
			runs, length, err = c.textRuns(e)
			if err != nil {
				return err
			}
			indexed = e
		}
		if p.offset > length {
			rewrites["#"+id] = ""
			continue
		}
		i := sort.Search(len(runs), func(i int) bool { return runs[i].end >= p.offset })
		if i == len(runs) {
			rewrites["#"+id] = ""
			continue
		}
		run := runs[i]
		if len(markers[run.element]) == 0 {
			marked = append(marked, run.element)
		}
		markers[run.element] = append(markers[run.element], textMarker{p.offset - run.start, id})
	}
	// Split each text run once, keeping a flat list even when thousands of page
	// or link positions land in one paragraph. Collect all positions first so
	// inserting a parent anchor cannot shift an inline object's local offsets.
	for _, e := range marked {
		list := markers[e]
		if err := c.claimNodes(2*len(list) + 2); err != nil {
			return err
		}
		slices.SortStableFunc(list, func(a, b textMarker) int { return a.offset - b.offset })
		wrapper := positionWrapper(e)
		var children []*element
		begin, units := 0, 0
		for _, m := range list {
			end := m.offset
			if e.tag == "" {
				length := textByteOffset(e.text[begin:], m.offset-units)
				if length < 0 {
					rewrites["#"+m.id] = ""
					continue
				}
				end = begin + length
			}
			if end > begin {
				if e.tag != "" {
					original := *e
					original.inlineObject = false
					children = append(children, &original)
				} else {
					children = append(children, &element{text: e.text[begin:end]})
				}
			}
			children = append(children, &element{tag: "span", attrs: []attribute{{"id", m.id}}})
			begin, units = end, m.offset
		}
		if e.tag != "" && begin == 0 {
			original := *e
			original.inlineObject = false
			children = append(children, &original)
		} else if e.tag == "" && begin < len(e.text) {
			children = append(children, &element{text: e.text[begin:]})
		}
		wrapper.children = children
		*e = wrapper
	}
	for _, href := range rewrites {
		if href == "" {
			c.warning("A KFX destination is outside the reading content; kept its text without a link.")
			break
		}
	}
	var rewrite func(*element)
	rewrite = func(e *element) {
		for i, attr := range e.attrs {
			if attr.name != "href" {
				continue
			}
			if href, ok := rewrites[attr.value]; ok {
				if href == "" {
					// An anchor without href keeps its content model, including
					// block children that a span cannot contain.
					e.attrs = slices.Delete(e.attrs, i, i+1)
				} else {
					e.attrs[i].value = href
				}
				break
			}
		}
		for _, child := range e.children {
			rewrite(child)
		}
	}
	rewrite(root)
	var nav func([]NavItem) []NavItem
	nav = func(items []NavItem) []NavItem {
		var result []NavItem
		for _, item := range items {
			item.Children = nav(item.Children)
			if href, ok := rewrites[item.Href]; ok {
				if href == "" {
					result = append(result, item.Children...)
					continue
				}
				item.Href = href
			}
			result = append(result, item)
		}
		return result
	}
	c.doc.Navigation, c.doc.Pages = nav(c.doc.Navigation), nav(c.doc.Pages)
	return c.book.ctx.Err()
}

type styleEvent struct {
	start, end int
	style      textStyle
	href       string
}

func (c *contentReader) styleEvents(e *element, values []value, depth int, initial ...styleEvent) error {
	if len(values) == 0 && len(initial) == 0 {
		return nil
	}
	if len(values) > 1024 {
		return fmt.Errorf("%w: too many style ranges in one element", ErrLimit)
	}
	events := slices.Clone(initial)
	for _, v := range values {
		start, length := v.get(fieldOffset).integer, v.get(fieldLength).integer
		if start < 0 || length < 0 || start > maxGeneratedBytes || length > maxGeneratedBytes {
			return invalid("invalid style range")
		}
		style, err := c.style(v, 0)
		if err != nil {
			return err
		}
		href := ""
		if style.link != "" {
			href = c.link(style.link)
		}
		events = append(events, styleEvent{int(start), int(start + length), style.textStyle, href})
	}
	if err := c.rubyAnnotations(e, values, depth); err != nil {
		return err
	}
	runs, length, err := c.textRuns(e)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.end > length {
			return invalid("style range exceeds its text")
		}
	}
	for _, run := range runs {
		if err := c.book.ctx.Err(); err != nil {
			return err
		}
		cuts := []int{0, run.end - run.start}
		for _, event := range events {
			if event.start > run.start && event.start < run.end {
				cuts = append(cuts, event.start-run.start)
			}
			if event.end > run.start && event.end < run.end {
				cuts = append(cuts, event.end-run.start)
			}
		}
		slices.Sort(cuts)
		cuts = slices.Compact(cuts)
		var children []*element
		wrapper := positionWrapper(run.element)
		begin := 0
		for i := 1; i < len(cuts); i++ {
			if err := c.claimNodes(2); err != nil {
				return err
			}
			var node *element
			if run.element.tag != "" {
				original := *run.element
				original.inlineObject = false
				node = &original
			} else {
				length := textByteOffset(run.element.text[begin:], cuts[i]-cuts[i-1])
				if length < 0 {
					return invalid("style range exceeds its text")
				}
				end := begin + length
				node = &element{text: run.element.text[begin:end]}
				begin = end
			}
			var style textStyle
			href := ""
			for _, event := range events {
				if event.start <= run.start+cuts[i-1] && event.end >= run.start+cuts[i] {
					if err := style.merge(event.style); err != nil {
						return err
					}
					if event.href != "" {
						href = event.href
					}
				}
			}
			if run.linked {
				href = ""
			}
			if style.bytes > 0 || style.direction != "" || style.language != "" || href != "" {
				styled := positionWrapper(node)
				styled.children = []*element{node}
				node = &styled
				if err := c.applyStyle(node, style); err != nil {
					return err
				}
				if href != "" {
					if err := c.linkContent(node, href); err != nil {
						return err
					}
				}
			}
			children = append(children, node)
		}
		wrapper.children = children
		*run.element = wrapper
	}
	return nil
}

func positionWrapper(e *element) element {
	wrapper := element{tag: "span", inlineObject: e.inlineObject}
	if !e.phrasing() {
		wrapper.tag = "div"
		if e.inlineObject {
			wrapper.attr("style", "display:inline-block;")
		}
	}
	return wrapper
}

func textByteOffset(s string, units int) int {
	for i := range s {
		if units == 0 {
			return i
		}
		units--
	}
	if units == 0 {
		return len(s)
	}
	return -1
}
