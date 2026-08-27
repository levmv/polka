package format

import (
	"fmt"
	"io"
	"strconv"
)

func readKindleKF8Skeletons(ranges []mobiRecordRange, r io.ReaderAt, index int) ([]KindleKF8Skeleton, error) {
	data, err := readKindleIndex(ranges, r, index)
	if err != nil {
		return nil, err
	}
	out := make([]KindleKF8Skeleton, 0, len(data.Entries))
	for i, entry := range data.Entries {
		fragmentCount, ok := kindleIndexTagValue(entry.Tags, 1, 0)
		if !ok {
			return nil, fmt.Errorf("SKEL entry %d missing fragment count", i)
		}
		start, length, ok := kindleIndexTagPair(entry.Tags, 6)
		if !ok {
			return nil, fmt.Errorf("SKEL entry %d missing bounds", i)
		}
		out = append(out, KindleKF8Skeleton{
			Index:         i,
			Name:          entry.Name,
			FragmentCount: fragmentCount,
			Start:         start,
			Length:        length,
		})
	}
	return out, nil
}

func readKindleKF8Fragments(ranges []mobiRecordRange, r io.ReaderAt, index int) ([]KindleKF8Fragment, error) {
	data, err := readKindleIndex(ranges, r, index)
	if err != nil {
		return nil, err
	}
	out := make([]KindleKF8Fragment, 0, len(data.Entries))
	for i, entry := range data.Entries {
		insertOffset, err := strconv.ParseUint(entry.Name, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("fragment entry %d has invalid insert offset %q", i, entry.Name)
		}
		selectorOffset, ok := kindleIndexTagValue(entry.Tags, 2, 0)
		if !ok {
			return nil, fmt.Errorf("fragment entry %d missing selector offset", i)
		}
		fileNumber, ok := kindleIndexTagValue(entry.Tags, 3, 0)
		if !ok {
			return nil, fmt.Errorf("fragment entry %d missing file number", i)
		}
		sequence, ok := kindleIndexTagValue(entry.Tags, 4, 0)
		if !ok {
			return nil, fmt.Errorf("fragment entry %d missing sequence", i)
		}
		start, length, ok := kindleIndexTagPair(entry.Tags, 6)
		if !ok {
			return nil, fmt.Errorf("fragment entry %d missing bounds", i)
		}
		out = append(out, KindleKF8Fragment{
			InsertOffset: uint32(insertOffset),
			Selector:     data.CNCX[selectorOffset],
			FileNumber:   fileNumber,
			Sequence:     sequence,
			Start:        start,
			Length:       length,
		})
	}
	return out, nil
}

func kindleIndexTagPair(tags map[uint32][]uint32, tag uint32) (uint32, uint32, bool) {
	values := tags[tag]
	if len(values) < 2 {
		return 0, 0, false
	}
	return values[0], values[1], true
}
