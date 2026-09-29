package kfx

import "slices"

type rubyRange struct {
	start, end int
	annotation *element
	style      textStyle
}

func (c *contentReader) rubyContent(name string, id int64, depth int) (*element, error) {
	entries, ok := c.rubies[name]
	if !ok {
		fragment, err := c.book.find(fragmentRuby, name)
		if err != nil {
			return nil, err
		}
		values := fragment.get(fieldChildren).list
		if err := c.claimNodes(len(values)); err != nil {
			return nil, err
		}
		entries = make(map[int64]value, len(values))
		for _, v := range values {
			v, err = c.book.resolve(fragmentContent, v)
			if err != nil {
				return nil, err
			}
			id := v.get(fieldRubyID).integer
			if _, duplicate := entries[id]; duplicate || v.get(fieldRubyID).kind != ionPositiveInt {
				return nil, invalid("invalid or duplicate ruby annotation ID")
			}
			entries[id] = v
		}
		c.rubies[name] = entries
	}
	v, ok := entries[id]
	if !ok {
		return nil, invalid("missing ruby annotation %s/%d", name, id)
	}
	annotation, err := c.content(v, "rt", depth+1)
	if err != nil {
		return nil, err
	}
	if annotation.tag != "p" && annotation.tag != "span" {
		return nil, invalid("unsupported ruby annotation content")
	}
	for _, child := range annotation.children {
		if !child.phrasing() {
			return nil, invalid("block content in ruby annotation")
		}
	}
	annotation.tag, annotation.inlineObject = "rt", false
	return annotation, nil
}

// Attach annotations before splitting text into style runs. This keeps group
// ruby intact when emphasis or links divide its base into several spans.
func (c *contentReader) rubyAnnotations(root *element, events []value, depth int) error {
	var ranges []rubyRange
	for _, event := range events {
		name := event.get(fieldRuby).id()
		if name == "" {
			continue
		}
		start, length := event.get(fieldOffset).integer, event.get(fieldLength).integer
		style, err := c.style(event, 0)
		if err != nil {
			return err
		}
		parts := event.get(fieldRubyRanges).list
		single := event.get(fieldRubyID).kind != ionPadding
		if single {
			parts = []value{event}
		}
		if err := c.claimNodes(len(parts)); err != nil {
			return err
		}
		offset := int64(0)
		for _, part := range parts {
			partOffset := part.get(fieldOffset).integer
			if single {
				partOffset = 0
			}
			n := part.get(fieldLength).integer
			if partOffset != offset || n <= 0 || n > length-offset {
				return invalid("invalid ruby annotation range")
			}
			annotation, err := c.rubyContent(name, part.get(fieldRubyID).integer, depth)
			if err != nil {
				return err
			}
			ranges = append(ranges, rubyRange{int(start + offset), int(start + offset + n), annotation, style.textStyle})
			offset += n
		}
		if offset != length || length == 0 {
			return invalid("incomplete ruby annotation range")
		}
	}
	if len(ranges) == 0 {
		return nil
	}
	slices.SortFunc(ranges, func(a, b rubyRange) int { return a.start - b.start })
	for i := 1; i < len(ranges); i++ {
		if ranges[i].start < ranges[i-1].end {
			return invalid("overlapping ruby annotations")
		}
	}
	runs, _, err := c.textRuns(root)
	if err != nil {
		return err
	}
	index := 0
	for _, run := range runs {
		if index == len(ranges) || ranges[index].start >= run.end {
			continue
		}
		var children []*element
		begin, offset := 0, run.start
		for index < len(ranges) && ranges[index].start < run.end {
			r := ranges[index]
			if run.element.tag != "" || r.start < run.start || r.end > run.end {
				return invalid("ruby base spans separate content nodes")
			}
			if err := c.claimNodes(4); err != nil {
				return err
			}
			from := begin + textByteOffset(run.element.text[begin:], r.start-offset)
			to := from + textByteOffset(run.element.text[from:], r.end-r.start)
			if from > begin {
				children = append(children, &element{text: run.element.text[begin:from]})
			}
			ruby := &element{tag: "ruby", children: []*element{{text: run.element.text[from:to]}, r.annotation}}
			if err := c.applyStyle(ruby, r.style); err != nil {
				return err
			}
			children = append(children, ruby)
			begin, offset = to, r.end
			index++
		}
		if begin < len(run.element.text) {
			children = append(children, &element{text: run.element.text[begin:]})
		}
		wrapper := positionWrapper(run.element)
		wrapper.children = children
		*run.element = wrapper
	}
	if index != len(ranges) {
		return invalid("ruby annotation exceeds its text")
	}
	return nil
}
