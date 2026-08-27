package format

import (
	"encoding/binary"
	"fmt"
	"io"
)

const maxKindleFDSTRecordBytes int64 = 1 << 20

func readKindleFDSTSections(ranges []mobiRecordRange, r io.ReaderAt, index int, wantCount uint32) ([]KindleFDSTSection, error) {
	if index <= 0 || index >= len(ranges) {
		return nil, fmt.Errorf("FDST record %d outside record table", index)
	}
	record, ok := mobiReadRecord(r, ranges[index], maxKindleFDSTRecordBytes)
	if !ok {
		return nil, fmt.Errorf("read FDST record %d", index)
	}
	return parseKindleFDSTRecord(record, wantCount)
}

func parseKindleFDSTRecord(data []byte, wantCount uint32) ([]KindleFDSTSection, error) {
	if len(data) < 12 || string(data[:4]) != "FDST" {
		return nil, fmt.Errorf("missing FDST magic")
	}
	sectionOffset := binary.BigEndian.Uint32(data[4:8])
	if sectionOffset != 12 {
		return nil, fmt.Errorf("unsupported FDST section offset %d", sectionOffset)
	}
	count := binary.BigEndian.Uint32(data[8:12])
	if wantCount > 0 && count != wantCount {
		return nil, fmt.Errorf("FDST count %d does not match header count %d", count, wantCount)
	}
	if count > uint32((len(data)-12)/8) {
		return nil, fmt.Errorf("FDST section table overruns record")
	}

	sections := make([]KindleFDSTSection, 0, count)
	pos := int(sectionOffset)
	var prevEnd uint32
	for i := range count {
		start := binary.BigEndian.Uint32(data[pos : pos+4])
		end := binary.BigEndian.Uint32(data[pos+4 : pos+8])
		if end < start {
			return nil, fmt.Errorf("FDST section %d has inverted bounds %d..%d", i, start, end)
		}
		if i > 0 && start < prevEnd {
			return nil, fmt.Errorf("FDST section %d overlaps previous end %d", i, prevEnd)
		}
		sections = append(sections, KindleFDSTSection{Start: start, End: end})
		prevEnd = end
		pos += 8
	}
	for _, b := range data[pos:] {
		if b != 0 {
			return nil, fmt.Errorf("FDST record has trailing data")
		}
	}
	return sections, nil
}
