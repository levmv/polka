package converter

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"slices"
)

// Standalone KF8: PalmDOC text records, document/fragment/navigation indexes,
// packaged resources and the FDST flow directory. There is no legacy rendition.
func (w *kf8Writer) write(ctx context.Context, out io.Writer) error {
	mainTextEnd := len(w.text)
	fdst := []byte("FDST")
	fdst = binary.BigEndian.AppendUint32(fdst, 12)
	fdst = binary.BigEndian.AppendUint32(fdst, uint32(len(w.flows)+1))
	fdst = binary.BigEndian.AppendUint32(fdst, 0)
	fdst = binary.BigEndian.AppendUint32(fdst, uint32(len(w.text)))
	for _, flow := range w.flows {
		if int64(len(w.text)+len(flow)) > maxConverterDecodedInputBytes {
			return fmt.Errorf("AZW3 text exceeds limit: %w", ErrResourceLimit)
		}
		fdst = binary.BigEndian.AppendUint32(fdst, uint32(len(w.text)))
		w.text = append(w.text, flow...)
		fdst = binary.BigEndian.AppendUint32(fdst, uint32(len(w.text)))
	}
	if len(w.text) == 0 || int64(len(w.text)) > maxConverterDecodedInputBytes {
		return fmt.Errorf("AZW3 text exceeds limit: %w", ErrResourceLimit)
	}
	records, err := kindleTextRecords(ctx, w.text)
	if err != nil {
		return err
	}
	textRecords := len(records) - 1
	indexes := map[int]uint32{}
	for _, index := range []struct {
		offset int
		build  func() ([][]byte, error)
	}{
		{244, func() ([][]byte, error) { return w.navigationIndex(mainTextEnd) }},
		{248, w.fragmentIndex}, {252, w.skeletonIndex}, {260, w.guideIndex},
	} {
		data, err := index.build()
		if err != nil {
			return err
		}
		indexes[index.offset] = 0xffffffff
		if len(data) > 0 {
			indexes[index.offset] = uint32(len(records))
			records = append(records, data...)
		}
	}
	firstResource := uint32(0xffffffff)
	if len(w.resources) > 0 {
		firstResource = uint32(len(records))
		records = append(records, w.resources...)
	}
	fdstIndex := len(records)
	records = append(records, fdst)
	flisIndex := len(records)
	fcisIndex := flisIndex + 1
	records = append(records, kindleTrailer(len(w.text))...)

	exth := kindleEXTH(w.meta, w.cover, w.destination(w.start).absolute, w.direction)
	if len(exth)+len(w.meta.Title) > int(maxConverterMetadataBytes) {
		return fmt.Errorf("AZW3 metadata exceeds limit: %w", ErrResourceLimit)
	}
	header := make([]byte, 280)
	u32 := func(offset int, value uint32) { binary.BigEndian.PutUint32(header[offset:], value) }
	binary.BigEndian.PutUint16(header, 2)
	u32(4, uint32(len(w.text)))
	binary.BigEndian.PutUint16(header[8:], uint16(textRecords))
	binary.BigEndian.PutUint16(header[10:], kindleRecordSize)
	copy(header[16:], "MOBI")
	u32(20, 264)
	u32(24, 2)
	u32(28, 65001)
	u32(32, crc32.ChecksumIEEE(w.text))
	u32(36, 8)
	for offset := 40; offset < 80; offset += 4 {
		u32(offset, 0xffffffff)
	}
	u32(80, uint32(textRecords+1))
	u32(84, uint32(len(header)+len(exth)))
	u32(88, uint32(len(w.meta.Title)))
	u32(92, kindleLanguage(w.meta.Language))
	u32(104, 8)
	u32(108, firstResource)
	exthFlags := uint32(0x50)
	for _, resource := range w.resources {
		if bytes.HasPrefix(resource, []byte("FONT")) {
			exthFlags |= 0x1000 // make embedded publisher fonts available to readers
			break
		}
	}
	u32(128, exthFlags)
	u32(164, 0xffffffff)
	u32(168, 0xffffffff)
	u32(192, uint32(fdstIndex))
	u32(196, uint32(len(w.flows)+1))
	u32(200, uint32(fcisIndex))
	u32(204, 1)
	u32(208, uint32(flisIndex))
	u32(212, 1)
	for _, offset := range []int{224, 232, 236, 256, 264, 272} {
		u32(offset, 0xffffffff)
	}
	u32(240, 1) // UTF-8 overlap
	for offset, value := range indexes {
		u32(offset, value)
	}
	header = append(header, exth...)
	header = append(header, w.meta.Title...)
	header = append(header, 0)
	records[0] = header
	return writeKindlePDB(ctx, out, w.meta.Title, records)
}

func (w *kf8Writer) skeletonIndex() ([][]byte, error) {
	var entries []kf8IndexEntry
	for i, s := range w.skeletons {
		entries = append(entries, kf8IndexEntry{fmt.Sprintf("SKEL%010d", i), map[byte][]int{
			1: {s.fragments, s.fragments}, 6: {s.start, s.length, s.start, s.length},
		}})
	}
	return kf8Index([]kf8Tag{{1, 1, 3}, {6, 2, 12}}, entries, kf8Strings{})
}

func (w *kf8Writer) fragmentIndex() ([][]byte, error) {
	var entries []kf8IndexEntry
	var strings kf8Strings
	selectors := map[string]int{}
	for i, f := range w.fragments {
		selector, ok := selectors[f.selector]
		if !ok {
			var err error
			selector, err = strings.add(f.selector)
			if err != nil {
				return nil, err
			}
			selectors[f.selector] = selector
		}
		entries = append(entries, kf8IndexEntry{fmt.Sprintf("%010d", f.insert), map[byte][]int{
			2: {selector}, 3: {f.file}, 4: {i}, 6: {f.start, f.length},
		}})
	}
	return kf8Index([]kf8Tag{{2, 1, 1}, {3, 1, 2}, {4, 1, 4}, {6, 2, 8}}, entries, strings)
}

func (w *kf8Writer) navigationIndex(textEnd int) ([][]byte, error) {
	type item struct {
		label         string
		pos           kf8Position
		depth, parent int
		children      []int
	}
	var items []item
	var destination func(kindleNavItem) (kf8Position, bool)
	destination = func(n kindleNavItem) (kf8Position, bool) {
		if _, ok := w.anchors[n.Href]; ok {
			return w.destination(n.Href), true
		}
		// A non-link group label navigates to its first available descendant.
		for _, child := range n.Children {
			if pos, ok := destination(child); ok {
				return pos, true
			}
		}
		return kf8Position{}, false
	}
	var visit func([]kindleNavItem, int, int)
	visit = func(nav []kindleNavItem, depth, parent int) {
		for _, n := range nav {
			pos, ok := destination(n)
			if !ok {
				visit(n.Children, depth, parent)
				continue
			}
			index := len(items)
			items = append(items, item{label: n.Label, pos: pos, depth: depth, parent: parent})
			if parent >= 0 {
				items[parent].children = append(items[parent].children, index)
			}
			visit(n.Children, depth+1, index)
		}
	}
	visit(w.nav, 0, -1)
	// Children occupy a contiguous range in the breadth-first NCX table.
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if d := items[a].depth - items[b].depth; d != 0 {
			return d
		}
		return a - b
	})
	indexes := make([]int, len(items))
	for index, original := range order {
		indexes[original] = index
	}
	var entries []kf8IndexEntry
	var strings kf8Strings
	for index, original := range order {
		n := items[original]
		label, err := strings.add(n.label)
		if err != nil {
			return nil, err
		}
		end := textEnd
		for j := original + 1; j < len(items); j++ {
			if items[j].depth <= n.depth && items[j].pos.absolute > n.pos.absolute {
				end = items[j].pos.absolute
				break
			}
		}
		values := map[byte][]int{1: {n.pos.absolute}, 2: {max(0, end-n.pos.absolute)}, 3: {label}, 4: {n.depth}, 6: {n.pos.fragment, n.pos.offset}}
		if n.parent >= 0 {
			values[21] = []int{indexes[n.parent]}
		}
		if len(n.children) > 0 {
			values[22] = []int{indexes[n.children[0]]}
			values[23] = []int{indexes[n.children[len(n.children)-1]]}
		}
		entries = append(entries, kf8IndexEntry{fmt.Sprintf("%010d", index), values})
	}
	return kf8Index([]kf8Tag{{1, 1, 1}, {2, 1, 2}, {3, 1, 4}, {4, 1, 8}, {21, 1, 16}, {22, 1, 32}, {23, 1, 64}, {6, 2, 128}}, entries, strings)
}

func (w *kf8Writer) guideIndex() ([][]byte, error) {
	var entries []kf8IndexEntry
	var strings kf8Strings
	add := func(kind, label string, pos kf8Position) error {
		offset, err := strings.add(label)
		if err != nil {
			return err
		}
		entries = append(entries, kf8IndexEntry{kind, map[byte][]int{1: {offset}, 6: {pos.fragment, pos.offset}}})
		return nil
	}
	if err := add("text", "Start", w.destination(w.start)); err != nil {
		return nil, err
	}
	if w.contents != "" {
		if pos, ok := w.positions[w.anchors[w.contents]]; ok {
			if err := add("toc", "Contents", pos); err != nil {
				return nil, err
			}
		}
	}
	return kf8Index([]kf8Tag{{1, 1, 1}, {6, 2, 2}}, entries, strings)
}
