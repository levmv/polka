package mobi

import (
	"encoding/binary"
	"fmt"
	"io"
)

const maxKindleFDSTRecordBytes int64 = 1 << 20

func readKindleFDSTSections(ranges []mobiRecordRange, r io.ReaderAt, index int, wantCount uint32) ([]FDSTSection, error) {
	if index <= 0 || index >= len(ranges) {
		return nil, fmt.Errorf("%w: FDST record %d outside record table", ErrUnsupportedSource, index)
	}
	record, err := mobiReadRecord(r, ranges[index], maxKindleFDSTRecordBytes)
	if err != nil {
		return nil, fmt.Errorf("read FDST record %d: %w", index, err)
	}
	return parseKindleFDSTRecord(record, wantCount)
}

func parseKindleFDSTRecord(data []byte, wantCount uint32) ([]FDSTSection, error) {
	if len(data) < 12 || string(data[:4]) != "FDST" {
		return nil, fmt.Errorf("%w: missing FDST magic", ErrUnsupportedSource)
	}
	sectionOffset := binary.BigEndian.Uint32(data[4:8])
	if sectionOffset != 12 {
		return nil, fmt.Errorf("%w: unsupported FDST section offset %d", ErrUnsupportedSource, sectionOffset)
	}
	count := binary.BigEndian.Uint32(data[8:12])
	if wantCount > 0 && count != wantCount {
		return nil, fmt.Errorf("%w: FDST count %d does not match header count %d", ErrUnsupportedSource, count, wantCount)
	}
	if count > uint32((len(data)-12)/8) {
		return nil, fmt.Errorf("%w: FDST section table overruns record", ErrUnsupportedSource)
	}

	sections := make([]FDSTSection, 0, count)
	pos := int(sectionOffset)
	var prevEnd uint32
	for i := range count {
		start := binary.BigEndian.Uint32(data[pos : pos+4])
		end := binary.BigEndian.Uint32(data[pos+4 : pos+8])
		if end < start {
			return nil, fmt.Errorf("%w: FDST section %d has inverted bounds %d..%d", ErrUnsupportedSource, i, start, end)
		}
		if i > 0 && start < prevEnd {
			return nil, fmt.Errorf("%w: FDST section %d overlaps previous end %d", ErrUnsupportedSource, i, prevEnd)
		}
		sections = append(sections, FDSTSection{Start: start, End: end})
		prevEnd = end
		pos += 8
	}
	for _, b := range data[pos:] {
		if b != 0 {
			return nil, fmt.Errorf("%w: FDST record has trailing data", ErrUnsupportedSource)
		}
	}
	return sections, nil
}
