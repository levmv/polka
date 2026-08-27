package format

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	maxKindleHUFFCDICRecordBytes   int64 = 32 << 20
	maxKindleHUFFDictionaryEntries       = 1 << 20
)

type kindleHUFFTable1Entry struct {
	found  bool
	length uint32
	value  uint32
}

type kindleHUFFTable2Entry struct {
	min   uint32
	value uint32
}

type kindleHUFFDictionaryEntry struct {
	data         []byte
	decompressed bool
	visiting     bool
}

type kindleHUFFCDICDecoder struct {
	table1     [256]kindleHUFFTable1Entry
	table2     [33]kindleHUFFTable2Entry
	dictionary []kindleHUFFDictionaryEntry
}

func kindleHUFFCDICAvailable(info *KindleInspection) bool {
	return info.HUFFCDICIndex > 0 && info.HUFFCDICRecordCount > 1
}

func newKindleHUFFCDICDecoder(r io.ReaderAt, ranges []mobiRecordRange, info *KindleInspection) (*kindleHUFFCDICDecoder, error) {
	if !kindleHUFFCDICAvailable(info) {
		return nil, fmt.Errorf("%w: missing HUFF/CDIC records", ErrUnsupportedKindleSource)
	}
	start := int(info.HUFFCDICIndex)
	count := int(info.HUFFCDICRecordCount)
	if start <= 0 || count < 2 || start+count > len(ranges) {
		return nil, fmt.Errorf("%w: invalid HUFF/CDIC record range", ErrUnsupportedKindleSource)
	}
	huff, ok := mobiReadRecord(r, ranges[start], maxKindleHUFFCDICRecordBytes)
	if !ok {
		return nil, fmt.Errorf("read HUFF record")
	}
	decoder, err := parseKindleHUFFRecord(huff)
	if err != nil {
		return nil, err
	}
	for i := 1; i < count; i++ {
		cdic, ok := mobiReadRecord(r, ranges[start+i], maxKindleHUFFCDICRecordBytes)
		if !ok {
			return nil, fmt.Errorf("read CDIC record %d", i)
		}
		if err := decoder.appendCDICRecord(cdic); err != nil {
			return nil, fmt.Errorf("parse CDIC record %d: %w", i, err)
		}
	}
	return decoder, nil
}

func parseKindleHUFFRecord(record []byte) (*kindleHUFFCDICDecoder, error) {
	if len(record) < 16 || string(record[0:4]) != "HUFF" {
		return nil, fmt.Errorf("invalid HUFF record")
	}
	offset1 := binary.BigEndian.Uint32(record[8:12])
	offset2 := binary.BigEndian.Uint32(record[12:16])
	if uint64(offset1)+256*4 > uint64(len(record)) || uint64(offset2)+32*8 > uint64(len(record)) {
		return nil, fmt.Errorf("invalid HUFF table bounds")
	}
	table1Offset := int(offset1)
	table2Offset := int(offset2)
	decoder := &kindleHUFFCDICDecoder{}
	for i := range 256 {
		offset := table1Offset + i*4
		value := binary.BigEndian.Uint32(record[offset : offset+4])
		decoder.table1[i] = kindleHUFFTable1Entry{
			found:  value&0x80 != 0,
			length: value & 0x1f,
			value:  value >> 8,
		}
	}
	for i := 1; i <= 32; i++ {
		offset := table2Offset + (i-1)*8
		decoder.table2[i] = kindleHUFFTable2Entry{
			min:   binary.BigEndian.Uint32(record[offset : offset+4]),
			value: binary.BigEndian.Uint32(record[offset+4 : offset+8]),
		}
	}
	return decoder, nil
}

func (d *kindleHUFFCDICDecoder) appendCDICRecord(record []byte) error {
	if len(record) < 16 || string(record[0:4]) != "CDIC" {
		return fmt.Errorf("invalid CDIC record")
	}
	headerLength := int(binary.BigEndian.Uint32(record[4:8]))
	numEntries := int(binary.BigEndian.Uint32(record[8:12]))
	codeLength := binary.BigEndian.Uint32(record[12:16])
	if headerLength < 16 || headerLength > len(record) || codeLength >= 31 {
		return fmt.Errorf("invalid CDIC header")
	}
	if numEntries < len(d.dictionary) || numEntries > maxKindleHUFFDictionaryEntries {
		return fmt.Errorf("invalid CDIC entry count")
	}
	perRecord := 1 << codeLength
	remaining := numEntries - len(d.dictionary)
	if remaining < perRecord {
		perRecord = remaining
	}
	buffer := record[headerLength:]
	if perRecord > len(buffer)/2 {
		return fmt.Errorf("invalid CDIC offset table")
	}
	for i := range perRecord {
		offset := int(binary.BigEndian.Uint16(buffer[i*2 : i*2+2]))
		if offset < perRecord*2 || offset+2 > len(buffer) {
			return fmt.Errorf("invalid CDIC entry offset")
		}
		prefix := binary.BigEndian.Uint16(buffer[offset : offset+2])
		length := int(prefix & 0x7fff)
		end := offset + 2 + length
		if end > len(buffer) {
			return fmt.Errorf("invalid CDIC entry length")
		}
		d.dictionary = append(d.dictionary, kindleHUFFDictionaryEntry{
			data:         append([]byte(nil), buffer[offset+2:end]...),
			decompressed: prefix&0x8000 != 0,
		})
	}
	return nil
}

func (d *kindleHUFFCDICDecoder) decompress(data []byte, maxBytes int64) ([]byte, error) {
	return d.decompressBytes(data, maxBytes)
}

func (d *kindleHUFFCDICDecoder) decompressBytes(data []byte, maxBytes int64) ([]byte, error) {
	bitLength := len(data) * 8
	out := make([]byte, 0, len(data))
	for bit := 0; bit < bitLength; {
		bits := kindleReadHUFF32Bits(data, bit)
		entry := d.table1[bits>>24]
		codeLength := entry.length
		value := entry.value
		if !entry.found {
			for codeLength <= 32 && bits>>(32-codeLength) < d.table2[codeLength].min {
				codeLength++
			}
			if codeLength == 0 || codeLength > 32 {
				return nil, fmt.Errorf("invalid HUFF code")
			}
			value = d.table2[codeLength].value
		}
		if codeLength == 0 || codeLength > 32 {
			return nil, fmt.Errorf("invalid HUFF code length")
		}
		bit += int(codeLength)
		if bit > bitLength {
			break
		}
		prefix := bits >> (32 - codeLength)
		if value < prefix {
			return nil, fmt.Errorf("invalid HUFF dictionary code")
		}
		code := int(value - prefix)
		chunk, err := d.dictionaryEntryData(code, maxBytes)
		if err != nil {
			return nil, err
		}
		if int64(len(out))+int64(len(chunk)) > maxBytes {
			return nil, fmt.Errorf("HUFF/CDIC text exceeds limit (%d bytes): %w", maxBytes, ErrTextTooLarge)
		}
		out = append(out, chunk...)
	}
	return out, nil
}

func (d *kindleHUFFCDICDecoder) dictionaryEntryData(code int, maxBytes int64) ([]byte, error) {
	if code < 0 || code >= len(d.dictionary) {
		return nil, fmt.Errorf("HUFF dictionary code %d outside dictionary", code)
	}
	entry := &d.dictionary[code]
	if entry.decompressed {
		return entry.data, nil
	}
	if entry.visiting {
		return nil, fmt.Errorf("recursive HUFF dictionary entry %d", code)
	}
	entry.visiting = true
	data, err := d.decompressBytes(entry.data, maxBytes)
	entry.visiting = false
	if err != nil {
		return nil, err
	}
	entry.data = data
	entry.decompressed = true
	return entry.data, nil
}

func kindleReadHUFF32Bits(data []byte, from int) uint32 {
	startByte := from >> 3
	end := from + 32
	endByte := end >> 3
	var bits uint64
	for i := startByte; i <= endByte; i++ {
		bits <<= 8
		if i >= 0 && i < len(data) {
			bits |= uint64(data[i])
		}
	}
	shift := uint(8 - (end & 7))
	return uint32((bits >> shift) & 0xffffffff)
}
