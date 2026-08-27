package testfixture

import "encoding/binary"

// MinimalMOBI returns the smallest PalmDB/MOBI structure used by format
// detection tests. It contains no book content or metadata.
func MinimalMOBI() []byte {
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

// MinimalDJVU returns a minimal IFF form with the requested DjVu form type.
func MinimalDJVU(formType string) []byte {
	data := []byte("AT&TFORM\x00\x00\x00\x10" + formType)
	return append(data, []byte("DJVU body")...)
}

// MinimalCHM returns a minimal CHM header with the requested format version.
func MinimalCHM(version uint32) []byte {
	data := make([]byte, 32)
	copy(data[:4], "ITSF")
	binary.LittleEndian.PutUint32(data[4:8], version)
	return data
}
