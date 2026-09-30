package converter

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

type kf8Tag struct{ id, values, mask byte }
type kf8IndexEntry struct {
	name   string
	values map[byte][]int
}

// CNCX offsets reserve 16 bits for the position within each string record.
type kf8Strings struct{ records [][]byte }

func (s *kf8Strings) add(value string) (int, error) {
	data := appendKindleVarint(nil, len(value))
	data = append(data, value...)
	if len(data) > 65532 {
		return 0, fmt.Errorf("Kindle index label exceeds limit: %w", ErrResourceLimit)
	}
	if len(s.records) == 0 || len(s.records[len(s.records)-1])+len(data) > 65532 {
		s.records = append(s.records, nil)
	}
	i := len(s.records) - 1
	offset := i*65536 + len(s.records[i])
	s.records[i] = append(s.records[i], data...)
	return offset, nil
}

func kf8Index(tags []kf8Tag, entries []kf8IndexEntry, strings kf8Strings) ([][]byte, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if len(entries) > 65535 {
		return nil, fmt.Errorf("Kindle index exceeds limit: %w", ErrResourceLimit)
	}
	header := func() []byte {
		b := make([]byte, 192)
		copy(b, "INDX")
		binary.BigEndian.PutUint32(b[4:], 192)
		return b
	}
	var records [][]byte
	var lastNames []string
	var counts []int
	block := header()
	var offsets []uint16
	last := ""
	flush := func() {
		block = append(block, make([]byte, (-len(block))&3)...)
		binary.BigEndian.PutUint32(block[12:], 1)
		binary.BigEndian.PutUint32(block[20:], uint32(len(block)))
		binary.BigEndian.PutUint32(block[24:], uint32(len(offsets)))
		binary.BigEndian.PutUint32(block[28:], 0xffffffff)
		binary.BigEndian.PutUint32(block[32:], 0xffffffff)
		block = append(block, "IDXT"...)
		for _, offset := range offsets {
			block = binary.BigEndian.AppendUint16(block, offset)
		}
		block = append(block, make([]byte, (-len(block))&3)...)
		records = append(records, block)
		lastNames = append(lastNames, last)
		counts = append(counts, len(offsets))
		block = header()
		offsets = nil
	}
	for _, entry := range entries {
		if len(entry.name) > 255 {
			return nil, fmt.Errorf("Kindle index key exceeds limit: %w", ErrResourceLimit)
		}
		data := append([]byte{byte(len(entry.name))}, entry.name...)
		controlAt := len(data)
		data = append(data, 0)
		var countBytes, values []byte
		for _, tag := range tags {
			v := entry.values[tag.id]
			if len(v) == 0 {
				continue
			}
			if len(v)%int(tag.values) != 0 {
				return nil, fmt.Errorf("invalid Kindle index value count")
			}
			n := len(v) / int(tag.values)
			shift := bits.TrailingZeros8(tag.mask)
			maskMax := int(tag.mask) >> shift
			var encoded []byte
			for _, value := range v {
				if value < 0 {
					return nil, fmt.Errorf("invalid negative Kindle index value")
				}
				encoded = appendKindleVarint(encoded, value)
			}
			if maskMax > 1 && n >= maskMax {
				data[controlAt] |= tag.mask
				countBytes = appendKindleVarint(countBytes, len(encoded))
			} else {
				if n > maskMax {
					return nil, fmt.Errorf("Kindle index value count exceeds mask")
				}
				data[controlAt] |= byte(n << shift)
			}
			values = append(values, encoded...)
		}
		data = append(data, countBytes...)
		data = append(data, values...)
		if len(block)+len(data)+2*(len(offsets)+1)+12 > 65532 {
			flush()
		}
		offsets = append(offsets, uint16(len(block)))
		block = append(block, data...)
		last = entry.name
	}
	flush()
	master := header()
	binary.BigEndian.PutUint32(master[16:], 2)
	binary.BigEndian.PutUint32(master[24:], uint32(len(records)))
	binary.BigEndian.PutUint32(master[28:], 65001)
	binary.BigEndian.PutUint32(master[32:], 0xffffffff)
	binary.BigEndian.PutUint32(master[36:], uint32(len(entries)))
	binary.BigEndian.PutUint32(master[52:], uint32(len(strings.records)))
	binary.BigEndian.PutUint32(master[180:], 192)
	master = append(master, "TAGX"...)
	master = binary.BigEndian.AppendUint32(master, uint32(12+4*(len(tags)+1)))
	master = binary.BigEndian.AppendUint32(master, 1)
	for _, tag := range tags {
		master = append(master, tag.id, tag.values, tag.mask, 0)
	}
	master = append(master, 0, 0, 0, 1)
	var geometry []uint16
	for i, name := range lastNames {
		if len(master) > 65535 {
			return nil, fmt.Errorf("Kindle index header exceeds limit: %w", ErrResourceLimit)
		}
		geometry = append(geometry, uint16(len(master)))
		master = append(master, byte(len(name)))
		master = append(master, name...)
		master = binary.BigEndian.AppendUint16(master, uint16(counts[i]))
	}
	master = append(master, make([]byte, (-len(master))&3)...)
	binary.BigEndian.PutUint32(master[20:], uint32(len(master)))
	master = append(master, "IDXT"...)
	for _, offset := range geometry {
		master = binary.BigEndian.AppendUint16(master, offset)
	}
	master = append(master, make([]byte, (-len(master))&3)...)
	out := append([][]byte{master}, records...)
	for _, record := range strings.records {
		out = append(out, append(record, make([]byte, (-len(record))&3)...))
	}
	return out, nil
}
