package format

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	maxKindleIndexRecordBytes int64 = 8 << 20
	maxKindleNavigationItems        = 4096
)

type kindleIndexData struct {
	Entries []kindleIndexEntry
	CNCX    map[uint32]string
}

type kindleIndexEntry struct {
	Name string
	Tags map[uint32][]uint32
}

type kindleTAGXEntry struct {
	Tag            uint32
	ValuesPerEntry uint32
	Mask           byte
	EndFlag        byte
}

type kindleNCXEntry struct {
	Index        int
	Label        string
	Href         string
	HeadingLevel int
	FirstChild   int
	LastChild    int
}

// extractKindleNCXNavigation reads the MOBI/KF8 NCX (INDX/TAGX/CNCX) table into
// navigation items. It is format-neutral: MOBI6 and KF8 share the same NCX
// structure, so both source classes use it against their active record set.
func extractKindleNCXNavigation(ranges []mobiRecordRange, r io.ReaderAt, info *KindleInspection, flowHref string) ([]KindleNavItem, error) {
	if info.NCXIndex == 0 {
		return nil, nil
	}
	data, err := readKindleIndex(ranges, r, int(info.NCXIndex))
	if err != nil {
		return nil, err
	}

	entries := make([]kindleNCXEntry, len(data.Entries))
	for i, entry := range data.Entries {
		label := ""
		if labelOffset, ok := kindleIndexTagValue(entry.Tags, 3, 0); ok {
			label = data.CNCX[labelOffset]
		}
		if label == "" {
			label = entry.Name
		}
		label = mobiCleanString(label)
		href := ""
		if pos, ok := kindleIndexTagValue(entry.Tags, 1, 0); ok {
			href = kindleFileposHref(flowHref, pos)
		}
		level := 0
		if value, ok := kindleIndexTagValue(entry.Tags, 4, 0); ok {
			level = int(value)
		}
		firstChild := -1
		if value, ok := kindleIndexTagValue(entry.Tags, 22, 0); ok {
			firstChild = int(value)
		}
		lastChild := -1
		if value, ok := kindleIndexTagValue(entry.Tags, 23, 0); ok {
			lastChild = int(value)
		}
		entries[i] = kindleNCXEntry{
			Index:        i,
			Label:        label,
			Href:         href,
			HeadingLevel: level,
			FirstChild:   firstChild,
			LastChild:    lastChild,
		}
	}
	return kindleNCXNavigationFromEntries(entries), nil
}

func readKindleIndex(ranges []mobiRecordRange, r io.ReaderAt, index int) (kindleIndexData, error) {
	if index <= 0 || index >= len(ranges) {
		return kindleIndexData{}, fmt.Errorf("INDX record %d outside record table", index)
	}
	master, ok := mobiReadRecord(r, ranges[index], maxKindleIndexRecordBytes)
	if !ok {
		return kindleIndexData{}, fmt.Errorf("read INDX record %d", index)
	}
	header, err := parseKindleINDXHeader(master)
	if err != nil {
		return kindleIndexData{}, fmt.Errorf("parse INDX record %d: %w", index, err)
	}
	tags, controlBytes, err := parseKindleTAGX(master, header.Length)
	if err != nil {
		return kindleIndexData{}, fmt.Errorf("parse TAGX record %d: %w", index, err)
	}

	out := kindleIndexData{CNCX: map[uint32]string{}}
	cncxOffset := uint32(0)
	for i := range int(header.NumCNCX) {
		recordIndex := index + int(header.NumRecords) + i + 1
		if recordIndex >= len(ranges) {
			return kindleIndexData{}, fmt.Errorf("CNCX record %d outside record table", recordIndex)
		}
		record, ok := mobiReadRecord(r, ranges[recordIndex], maxKindleIndexRecordBytes)
		if !ok {
			return kindleIndexData{}, fmt.Errorf("read CNCX record %d", recordIndex)
		}
		if err := readKindleCNCXRecord(out.CNCX, cncxOffset, record, header.Encoding); err != nil {
			return kindleIndexData{}, fmt.Errorf("parse CNCX record %d: %w", recordIndex, err)
		}
		cncxOffset += 0x10000
	}

	for i := range int(header.NumRecords) {
		recordIndex := index + i + 1
		if recordIndex >= len(ranges) {
			return kindleIndexData{}, fmt.Errorf("index entry record %d outside record table", recordIndex)
		}
		record, ok := mobiReadRecord(r, ranges[recordIndex], maxKindleIndexRecordBytes)
		if !ok {
			return kindleIndexData{}, fmt.Errorf("read index entry record %d", recordIndex)
		}
		entries, err := readKindleIndexEntryRecord(record, tags, controlBytes, header.Encoding)
		if err != nil {
			return kindleIndexData{}, fmt.Errorf("parse index entry record %d: %w", recordIndex, err)
		}
		out.Entries = append(out.Entries, entries...)
	}
	return out, nil
}

type kindleINDXHeader struct {
	Length     uint32
	IDXTOffset uint32
	NumRecords uint32
	Encoding   uint32
	NumCNCX    uint32
}

func parseKindleINDXHeader(data []byte) (kindleINDXHeader, error) {
	if len(data) < 56 || string(data[:4]) != "INDX" {
		return kindleINDXHeader{}, fmt.Errorf("missing INDX magic")
	}
	header := kindleINDXHeader{
		Length:     binary.BigEndian.Uint32(data[4:8]),
		IDXTOffset: binary.BigEndian.Uint32(data[20:24]),
		NumRecords: binary.BigEndian.Uint32(data[24:28]),
		Encoding:   binary.BigEndian.Uint32(data[28:32]),
		NumCNCX:    binary.BigEndian.Uint32(data[52:56]),
	}
	if header.Length > uint32(len(data)) {
		return kindleINDXHeader{}, fmt.Errorf("INDX header length %d exceeds record length %d", header.Length, len(data))
	}
	return header, nil
}

func parseKindleTAGX(data []byte, offset uint32) ([]kindleTAGXEntry, int, error) {
	start := int(offset)
	if start == 0 {
		return nil, 0, fmt.Errorf("missing TAGX offset")
	}
	if start+12 > len(data) || string(data[start:start+4]) != "TAGX" {
		return nil, 0, fmt.Errorf("missing TAGX magic")
	}
	length := int(binary.BigEndian.Uint32(data[start+4 : start+8]))
	if length < 12 || start+length > len(data) || (length-12)%4 != 0 {
		return nil, 0, fmt.Errorf("invalid TAGX length %d", length)
	}
	controlBytes := int(binary.BigEndian.Uint32(data[start+8 : start+12]))
	if controlBytes > 16 {
		return nil, 0, fmt.Errorf("invalid TAGX control byte count %d", controlBytes)
	}
	entries := make([]kindleTAGXEntry, 0, (length-12)/4)
	for pos := start + 12; pos < start+length; pos += 4 {
		entries = append(entries, kindleTAGXEntry{
			Tag:            uint32(data[pos]),
			ValuesPerEntry: uint32(data[pos+1]),
			Mask:           data[pos+2],
			EndFlag:        data[pos+3],
		})
	}
	return entries, controlBytes, nil
}

func readKindleCNCXRecord(out map[uint32]string, base uint32, data []byte, codepage uint32) error {
	for pos := 0; pos < len(data); {
		if data[pos] == 0 {
			break
		}
		index := pos
		length, consumed, ok := kindleVarLen(data, pos)
		if !ok {
			return fmt.Errorf("invalid CNCX length at %d", pos)
		}
		pos += consumed
		end := pos + int(length)
		if end < pos || end > len(data) {
			return fmt.Errorf("CNCX string at %d overruns record", index)
		}
		out[base+uint32(index)] = mobiCleanString(mobiDecode(data[pos:end], codepage))
		pos = end
	}
	return nil
}

func readKindleIndexEntryRecord(data []byte, tags []kindleTAGXEntry, controlBytes int, codepage uint32) ([]kindleIndexEntry, error) {
	header, err := parseKindleINDXHeader(data)
	if err != nil {
		return nil, err
	}
	idxt := int(header.IDXTOffset)
	if idxt <= 0 || idxt+4+int(header.NumRecords)*2 > len(data) {
		return nil, fmt.Errorf("invalid IDXT offset %d", idxt)
	}

	var entries []kindleIndexEntry
	for i := range int(header.NumRecords) {
		offsetPos := idxt + 4 + 2*i
		start := int(binary.BigEndian.Uint16(data[offsetPos : offsetPos+2]))
		end := idxt
		if i+1 < int(header.NumRecords) {
			nextOffsetPos := idxt + 4 + 2*(i+1)
			end = int(binary.BigEndian.Uint16(data[nextOffsetPos : nextOffsetPos+2]))
		}
		if start < 0 || start >= end || end > len(data) {
			return nil, fmt.Errorf("invalid index entry bounds %d..%d", start, end)
		}
		nameLength := int(data[start])
		nameEnd := start + 1 + nameLength
		if nameEnd+controlBytes > end {
			return nil, fmt.Errorf("index entry %d text/control bytes overrun entry", i)
		}
		entry := kindleIndexEntry{
			Name: mobiCleanString(mobiDecode(data[start+1:nameEnd], codepage)),
			Tags: map[uint32][]uint32{},
		}
		tagMap, err := readKindleIndexTags(data, nameEnd, end, tags, controlBytes)
		if err != nil {
			return nil, fmt.Errorf("index entry %d tags: %w", i, err)
		}
		entry.Tags = tagMap
		entries = append(entries, entry)
	}
	return entries, nil
}

func readKindleIndexTags(data []byte, start, end int, tags []kindleTAGXEntry, controlBytes int) (map[uint32][]uint32, error) {
	type pendingTag struct {
		tag        uint32
		valueCount int
		valueBytes int
	}
	var pending []pendingTag
	controlByteIndex := 0
	pos := start + controlBytes
	if pos > end {
		return nil, fmt.Errorf("control bytes overrun entry")
	}

	for _, tag := range tags {
		if tag.EndFlag&1 != 0 {
			controlByteIndex++
			continue
		}
		if controlByteIndex >= controlBytes {
			return nil, fmt.Errorf("TAGX control byte index %d outside %d bytes", controlByteIndex, controlBytes)
		}
		if tag.Mask == 0 {
			continue
		}
		value := int(data[start+controlByteIndex] & tag.Mask)
		if value == 0 {
			continue
		}
		valuesPerEntry := int(tag.ValuesPerEntry)
		if valuesPerEntry <= 0 {
			valuesPerEntry = 1
		}
		switch {
		case value == int(tag.Mask) && countKindleBitsSet(tag.Mask) > 1:
			valueBytes, consumed, ok := kindleVarLen(data, pos)
			if !ok || pos+consumed > end {
				return nil, fmt.Errorf("invalid variable-width tag byte count")
			}
			pos += consumed
			pending = append(pending, pendingTag{tag: tag.Tag, valueCount: -1, valueBytes: int(valueBytes)})
		case value == int(tag.Mask):
			pending = append(pending, pendingTag{tag: tag.Tag, valueCount: valuesPerEntry})
		default:
			value >>= countKindleUnsetEnd(tag.Mask)
			pending = append(pending, pendingTag{tag: tag.Tag, valueCount: value * valuesPerEntry})
		}
	}

	out := map[uint32][]uint32{}
	for _, tag := range pending {
		var values []uint32
		if tag.valueCount >= 0 {
			for range tag.valueCount {
				value, consumed, ok := kindleVarLen(data, pos)
				if !ok || pos+consumed > end {
					return nil, fmt.Errorf("invalid variable-width tag value")
				}
				pos += consumed
				values = append(values, value)
			}
		} else {
			consumedBytes := 0
			for consumedBytes < tag.valueBytes {
				value, consumed, ok := kindleVarLen(data, pos)
				if !ok || pos+consumed > end {
					return nil, fmt.Errorf("invalid variable-width counted tag value")
				}
				pos += consumed
				consumedBytes += consumed
				values = append(values, value)
			}
			if consumedBytes != tag.valueBytes {
				return nil, fmt.Errorf("tag byte count mismatch")
			}
		}
		out[tag.tag] = values
	}

	for _, b := range data[pos:end] {
		if b != 0 {
			return nil, fmt.Errorf("unprocessed non-zero index bytes")
		}
	}
	return out, nil
}

func kindleNCXNavigationFromEntries(entries []kindleNCXEntry) []KindleNavItem {
	visited := map[int]bool{}
	nav := kindleNCXChildren(entries, 0, len(entries), 0, visited)
	if len(nav) == 0 && len(entries) > 0 {
		nav = kindleNCXFlatNavigation(entries)
	}
	return nav
}

func kindleNCXChildren(entries []kindleNCXEntry, start, end, level int, visited map[int]bool) []KindleNavItem {
	if start < 0 {
		start = 0
	}
	if end <= 0 || end > len(entries) {
		end = len(entries)
	}
	if start > end {
		return nil
	}
	var out []KindleNavItem
	for i := start; i < end && len(out) < maxKindleNavigationItems; i++ {
		entry := entries[i]
		if visited[entry.Index] || entry.HeadingLevel != level {
			continue
		}
		visited[entry.Index] = true
		if entry.Label == "" || entry.Href == "" {
			continue
		}
		item := KindleNavItem{Label: entry.Label, Href: entry.Href}
		if entry.FirstChild >= 0 {
			childEnd := entry.LastChild + 1
			if childEnd <= entry.FirstChild {
				childEnd = len(entries)
			}
			item.Children = kindleNCXChildren(entries, entry.FirstChild, childEnd, level+1, visited)
		}
		out = append(out, item)
	}
	return out
}

func kindleNCXFlatNavigation(entries []kindleNCXEntry) []KindleNavItem {
	var out []KindleNavItem
	for _, entry := range entries {
		if entry.Label == "" || entry.Href == "" {
			continue
		}
		out = append(out, KindleNavItem{Label: entry.Label, Href: entry.Href})
		if len(out) >= maxKindleNavigationItems {
			break
		}
	}
	return out
}

func kindleIndexTagValue(tags map[uint32][]uint32, tag uint32, offset int) (uint32, bool) {
	values := tags[tag]
	if offset < 0 || offset >= len(values) {
		return 0, false
	}
	return values[offset], true
}

func kindleFileposHref(flowHref string, pos uint32) string {
	return fmt.Sprintf("%s#filepos%d", flowHref, pos)
}

func kindleVarLen(data []byte, pos int) (uint32, int, bool) {
	var value uint32
	for i := 0; i < 4 && pos+i < len(data); i++ {
		b := data[pos+i]
		value = (value << 7) | uint32(b&0x7f)
		if b&0x80 != 0 {
			return value, i + 1, true
		}
	}
	return 0, 0, false
}

func countKindleBitsSet(value byte) int {
	count := 0
	for value > 0 {
		if value&1 != 0 {
			count++
		}
		value >>= 1
	}
	return count
}

func countKindleUnsetEnd(value byte) int {
	count := 0
	for value&1 == 0 {
		count++
		value >>= 1
	}
	return count
}
