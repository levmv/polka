package format

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestKindleHUFFCDICDecoder(t *testing.T) {
	huff := testKindleHUFFRecord()
	cdic := testKindleCDICRecord([][]byte{[]byte("A"), []byte("B")})
	data, ranges := testKindleRecords([]byte("record0"), huff, cdic)
	info := &KindleInspection{
		HUFFCDICIndex:       1,
		HUFFCDICRecordCount: 2,
	}

	decoder, err := newKindleHUFFCDICDecoder(bytes.NewReader(data), ranges, info)
	if err != nil {
		t.Fatalf("newKindleHUFFCDICDecoder: %v", err)
	}
	got, err := decoder.decompress([]byte{0x00, 0xff}, 16)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(got) != "AB" {
		t.Fatalf("decompress = %q; want AB", got)
	}
}

func TestKindleUnsupportedFeaturesAllowsAvailableHUFFCDIC(t *testing.T) {
	info := &KindleInspection{
		Compression:         mobiCompressionHUFFCDIC,
		HUFFCDICIndex:       1,
		HUFFCDICRecordCount: 2,
	}
	if features := kindleUnsupportedFeatures(info); containsString(features, "huff-cdic-compression") {
		t.Fatalf("UnsupportedFeatures = %+v; want HUFF/CDIC accepted when dictionary records are present", features)
	}
}

func testKindleHUFFRecord() []byte {
	const (
		offset1 = 16
		offset2 = offset1 + 256*4
	)
	record := make([]byte, offset2+32*8)
	copy(record[0:4], "HUFF")
	binary.BigEndian.PutUint32(record[8:12], offset1)
	binary.BigEndian.PutUint32(record[12:16], offset2)
	for i := range 256 {
		value := uint32(i)
		if i == 0xff {
			value = 0x100
		}
		binary.BigEndian.PutUint32(record[offset1+i*4:offset1+i*4+4], value<<8|0x80|8)
	}
	return record
}

func testKindleCDICRecord(entries [][]byte) []byte {
	const headerLength = 16
	buffer := make([]byte, len(entries)*2)
	for i, entry := range entries {
		offset := len(buffer)
		binary.BigEndian.PutUint16(buffer[i*2:i*2+2], uint16(offset))
		body := make([]byte, 2+len(entry))
		binary.BigEndian.PutUint16(body[0:2], 0x8000|uint16(len(entry)))
		copy(body[2:], entry)
		buffer = append(buffer, body...)
	}
	record := make([]byte, headerLength)
	copy(record[0:4], "CDIC")
	binary.BigEndian.PutUint32(record[4:8], headerLength)
	binary.BigEndian.PutUint32(record[8:12], uint32(len(entries)))
	binary.BigEndian.PutUint32(record[12:16], 1)
	return append(record, buffer...)
}

func testKindleRecords(records ...[]byte) ([]byte, []mobiRecordRange) {
	var data []byte
	ranges := make([]mobiRecordRange, 0, len(records))
	for _, record := range records {
		start := int64(len(data))
		data = append(data, record...)
		ranges = append(ranges, mobiRecordRange{start: start, end: int64(len(data))})
	}
	return data, ranges
}
