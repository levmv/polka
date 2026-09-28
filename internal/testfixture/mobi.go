package testfixture

import (
	"encoding/binary"
	"testing"
)

const (
	palmDBHeaderSize    = 78
	palmDBRecordSize    = 8
	palmDBNameBytes     = 32
	palmDOCHeader       = 16
	mobiNoImageIndex    = 0xffffffff
	mobiCompressionNone = 1
)

type MOBIEXTHRecord struct {
	Type  uint32
	Value []byte
}

func MOBIWithMetadata(codepage uint32, title string, records []MOBIEXTHRecord) []byte {
	return BuildMOBI(MOBIOptions{
		Codepage: codepage,
		Title:    title,
		EXTH:     records,
	})
}

type MOBIOptions struct {
	Codepage        uint32
	PalmDBName      string
	Title           string
	Compression     uint16
	Encryption      uint16
	HeaderLength    uint32
	MOBIVersion     uint32
	TrailingFlags   uint16
	TextLength      uint32
	TextRecords     [][]byte
	EXTH            []MOBIEXTHRecord
	FirstImageIndex uint32
	ExtraRecords    [][]byte
}

// BuildMOBI defaults to an uncompressed UTF-8 MOBI6 book. KF8 fixtures opt in
// with MOBIVersion 8; malformed-header tests should mutate the resulting bytes.
func BuildMOBI(opts MOBIOptions) []byte {
	mobiVersion := opts.MOBIVersion
	if mobiVersion == 0 {
		mobiVersion = 6
	}
	mobiHeaderLength := opts.HeaderLength
	if mobiHeaderLength == 0 {
		mobiHeaderLength = 0xe8
		if mobiVersion >= 8 {
			mobiHeaderLength = 0x108
		}
	}
	codepage := opts.Codepage
	if codepage == 0 {
		codepage = 65001
	}
	exth := mobiEXTH(opts.EXTH)
	titleOffset := 16 + int(mobiHeaderLength) + len(exth)
	titleBytes := []byte(opts.Title)
	firstImageIndex := opts.FirstImageIndex
	if firstImageIndex == 0 {
		firstImageIndex = mobiNoImageIndex
	}
	compression := opts.Compression
	if compression == 0 {
		compression = mobiCompressionNone
	}
	textRecords := opts.TextRecords
	if len(textRecords) == 0 {
		textRecords = [][]byte{[]byte("<p>A synthetic book.</p>")}
	}
	textLength := opts.TextLength
	if textLength == 0 {
		for _, record := range textRecords {
			textLength += uint32(len(record))
		}
	}
	record0 := make([]byte, titleOffset+len(titleBytes))
	binary.BigEndian.PutUint16(record0[0:2], compression)
	binary.BigEndian.PutUint32(record0[4:8], textLength)
	binary.BigEndian.PutUint16(record0[8:10], uint16(len(textRecords)))
	binary.BigEndian.PutUint16(record0[10:12], 4096)
	binary.BigEndian.PutUint16(record0[12:14], opts.Encryption)
	copy(record0[16:20], "MOBI")
	binary.BigEndian.PutUint32(record0[20:24], mobiHeaderLength)
	binary.BigEndian.PutUint32(record0[24:28], 2) // Ordinary book.
	binary.BigEndian.PutUint32(record0[28:32], codepage)
	binary.BigEndian.PutUint32(record0[36:40], mobiVersion)
	binary.BigEndian.PutUint32(record0[0x54:0x58], uint32(titleOffset))
	binary.BigEndian.PutUint32(record0[0x58:0x5c], uint32(len(titleBytes)))
	binary.BigEndian.PutUint32(record0[0x5c:0x60], 0x09) // English primary language id.
	binary.BigEndian.PutUint32(record0[0x68:0x6c], mobiVersion)
	binary.BigEndian.PutUint32(record0[0x6c:0x70], firstImageIndex)
	binary.BigEndian.PutUint16(record0[0xf2:0xf4], opts.TrailingFlags)
	if len(opts.EXTH) > 0 {
		binary.BigEndian.PutUint32(record0[0x80:0x84], 0x40)
		copy(record0[16+mobiHeaderLength:], exth)
	}
	copy(record0[titleOffset:], titleBytes)

	recordBodies := append([][]byte{record0}, textRecords...)
	recordBodies = append(recordBodies, opts.ExtraRecords...)
	return PalmDBWithRecords(opts.PalmDBName, "BOOKMOBI", recordBodies)
}

// MOBIHeaderOnly has a PalmDB/MOBI signature but no complete header or content.
// Use MOBI when the fixture needs to represent a readable book.
func MOBIHeaderOnly() []byte {
	const (
		palmDBHeaderSize = 78
		palmDBRecordSize = 8
		record0Offset    = palmDBHeaderSize + palmDBRecordSize
	)

	data := make([]byte, record0Offset+32)
	copy(data[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(data[76:78], 1)
	binary.BigEndian.PutUint32(data[78:82], record0Offset)
	copy(data[record0Offset+16:record0Offset+20], "MOBI")
	return data
}

// MOBI contains an ordinary MOBI6 header and one tiny text record. Encryption
// sets the header flag only; encrypted fixtures are intentionally not decrypted.
func MOBI(encryption uint16) []byte {
	return BuildMOBI(MOBIOptions{Encryption: encryption})
}

func PalmDBRecordBodies(t *testing.T, data []byte) [][]byte {
	t.Helper()
	if len(data) < palmDBHeaderSize {
		t.Fatalf("PalmDB fixture too short")
	}
	count := int(binary.BigEndian.Uint16(data[76:78]))
	if count < 1 || len(data) < palmDBHeaderSize+count*palmDBRecordSize {
		t.Fatalf("invalid PalmDB record table")
	}
	records := make([][]byte, 0, count)
	for i := range count {
		start := int(binary.BigEndian.Uint32(data[palmDBHeaderSize+i*palmDBRecordSize : palmDBHeaderSize+i*palmDBRecordSize+4]))
		end := len(data)
		if i+1 < count {
			end = int(binary.BigEndian.Uint32(data[palmDBHeaderSize+(i+1)*palmDBRecordSize : palmDBHeaderSize+(i+1)*palmDBRecordSize+4]))
		}
		if start < palmDBHeaderSize+count*palmDBRecordSize || start > end || end > len(data) {
			t.Fatalf("invalid PalmDB record %d bounds %d..%d", i, start, end)
		}
		records = append(records, append([]byte(nil), data[start:end]...))
	}
	return records
}

func MOBIUint32(value uint32) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, value)
	return buf
}

func mobiEXTH(records []MOBIEXTHRecord) []byte {
	if len(records) == 0 {
		return nil
	}
	var body []byte
	for _, rec := range records {
		buf := make([]byte, 8+len(rec.Value))
		binary.BigEndian.PutUint32(buf[0:4], rec.Type)
		binary.BigEndian.PutUint32(buf[4:8], uint32(len(buf)))
		copy(buf[8:], rec.Value)
		body = append(body, buf...)
	}
	length := 12 + len(body)
	exth := make([]byte, length)
	copy(exth[0:4], "EXTH")
	binary.BigEndian.PutUint32(exth[4:8], uint32(length))
	binary.BigEndian.PutUint32(exth[8:12], uint32(len(records)))
	copy(exth[12:], body)
	for len(exth)%4 != 0 {
		exth = append(exth, 0)
	}
	return exth
}

func SetMOBIRecord0Uint32(t *testing.T, data []byte, offset int, value uint32) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	if len(out) < palmDBHeaderSize+palmDBRecordSize {
		t.Fatalf("MOBI fixture too short")
	}
	record0Offset := int(binary.BigEndian.Uint32(out[palmDBHeaderSize : palmDBHeaderSize+4]))
	if record0Offset+offset+4 > len(out) {
		t.Fatalf("record0 offset %x outside fixture length %d", offset, len(out))
	}
	binary.BigEndian.PutUint32(out[record0Offset+offset:record0Offset+offset+4], value)
	return out
}

func PalmDOC(title string) []byte {
	return PalmDB(title, "TEXtREAd", PalmDOCHeader(2))
}

func PalmDOCWithText(title string, compression uint16, textRecords [][]byte, textLength uint32) []byte {
	return PalmDOCWithResources(title, compression, textRecords, textLength, nil)
}

func PalmDOCWithResources(title string, compression uint16, textRecords [][]byte, textLength uint32, resources [][]byte) []byte {
	record0 := PalmDOCHeader(compression)
	binary.BigEndian.PutUint32(record0[4:8], textLength)
	binary.BigEndian.PutUint16(record0[8:10], uint16(len(textRecords)))
	records := append([][]byte{record0}, textRecords...)
	records = append(records, resources...)
	return PalmDBWithRecords(title, "TEXtREAd", records)
}

func PalmDOCHeader(compression uint16) []byte {
	record0 := make([]byte, palmDOCHeader)
	binary.BigEndian.PutUint16(record0[0:2], compression)
	binary.BigEndian.PutUint32(record0[4:8], 1024)
	binary.BigEndian.PutUint16(record0[8:10], 1)
	binary.BigEndian.PutUint16(record0[10:12], 4096)
	return record0
}

func PalmDB(name, typeCreator string, record0 []byte) []byte {
	return PalmDBWithRecords(name, typeCreator, [][]byte{record0})
}

func PalmDBWithRecords(name, typeCreator string, records [][]byte) []byte {
	tableSize := len(records) * palmDBRecordSize
	data := make([]byte, palmDBHeaderSize+tableSize)
	copy(data[:palmDBNameBytes], []byte(name))
	copy(data[60:68], []byte(typeCreator))
	binary.BigEndian.PutUint16(data[76:78], uint16(len(records)))
	offset := palmDBHeaderSize + tableSize
	for i, record := range records {
		binary.BigEndian.PutUint32(data[palmDBHeaderSize+i*palmDBRecordSize:palmDBHeaderSize+i*palmDBRecordSize+4], uint32(offset))
		offset += len(record)
	}
	for _, record := range records {
		data = append(data, record...)
	}
	return data
}
