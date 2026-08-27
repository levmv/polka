package format

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	palmDBNameBytes = 32
	palmDOCHeader   = 16
)

var palmDOCTypeCreator = []byte("TEXtREAd")

// isPalmDOC accepts only the PalmDOC/AportisDoc PDB shape. Palm databases are a
// broad container family, so extension-only .pdb files are not enough.
func isPalmDOC(r io.ReaderAt, size int64) bool {
	pdb, ok := readPalmDB(r, size)
	if !ok || !bytes.Equal(pdb.header[60:68], palmDOCTypeCreator) {
		return false
	}
	record0Offset, _, ok := pdb.record0Bounds(r, size)
	if !ok || record0Offset+palmDOCHeader > size {
		return false
	}
	record0 := make([]byte, palmDOCHeader)
	if _, err := r.ReadAt(record0, record0Offset); err != nil {
		return false
	}

	compression := binary.BigEndian.Uint16(record0[0:2])
	textLength := binary.BigEndian.Uint32(record0[4:8])
	textRecords := binary.BigEndian.Uint16(record0[8:10])
	recordSize := binary.BigEndian.Uint16(record0[10:12])
	encryption := binary.BigEndian.Uint16(record0[12:14])
	return (compression == 1 || compression == 2) &&
		textLength > 0 &&
		textRecords > 0 &&
		recordSize > 0 &&
		encryption == 0
}

// ExtractPDBMetadata reads PalmDOC/AportisDoc metadata. Plain documents usually
// carry only the Palm database name; older OEB-backed documents may also start
// with a bounded Dublin Core metadata block in their first text record.
func ExtractPDBMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	pdb, ok := readPalmDB(r, size)
	if !ok || !bytes.Equal(pdb.header[60:68], palmDOCTypeCreator) {
		return nil, fmt.Errorf("invalid PalmDOC PDB header")
	}
	meta := &Metadata{Title: palmDBHeaderName(pdb.header, 1252)}
	if embedded := palmDOCEmbeddedMetadata(r, size, pdb); embedded != nil {
		if !usefulMOBITextValue(embedded.Title) {
			embedded.Title = meta.Title
		}
		meta = embedded
	}
	return meta, nil
}

func palmDBHeaderName(header []byte, codepage uint32) string {
	if len(header) < palmDBNameBytes {
		return ""
	}
	raw := header[:palmDBNameBytes]
	if idx := bytes.IndexByte(raw, 0); idx >= 0 {
		raw = raw[:idx]
	}
	return mobiCleanString(mobiDecode(raw, codepage))
}

type palmDB struct {
	header     []byte
	records    int
	recordsEnd int64
}

func readPalmDB(r io.ReaderAt, size int64) (palmDB, bool) {
	if size < palmDBHeaderSize+palmDBRecordSize {
		return palmDB{}, false
	}
	header := make([]byte, palmDBHeaderSize)
	if _, err := r.ReadAt(header, 0); err != nil {
		return palmDB{}, false
	}
	records := int(binary.BigEndian.Uint16(header[76:78]))
	recordsEnd := int64(palmDBHeaderSize + records*palmDBRecordSize)
	if records < 1 || recordsEnd > size {
		return palmDB{}, false
	}
	return palmDB{header: header, records: records, recordsEnd: recordsEnd}, true
}

func (pdb palmDB) record0Bounds(r io.ReaderAt, size int64) (int64, int64, bool) {
	recordTableBytes := palmDBRecordSize
	if pdb.records > 1 {
		recordTableBytes = 2 * palmDBRecordSize
	}
	recordTable := make([]byte, recordTableBytes)
	if _, err := r.ReadAt(recordTable, palmDBHeaderSize); err != nil {
		return 0, 0, false
	}
	record0Offset := int64(binary.BigEndian.Uint32(recordTable[0:4]))
	if record0Offset < pdb.recordsEnd || record0Offset > size {
		return 0, 0, false
	}

	record0End := size
	if pdb.records > 1 {
		record0End = int64(binary.BigEndian.Uint32(recordTable[8:12]))
		if record0End <= record0Offset || record0End > size {
			return 0, 0, false
		}
	}
	return record0Offset, record0End, true
}

func palmDBRecordRanges(r io.ReaderAt, size int64, minRecord0Bytes int64) ([]mobiRecordRange, bool) {
	pdb, ok := readPalmDB(r, size)
	if !ok {
		return nil, false
	}
	return pdb.recordRanges(r, size, minRecord0Bytes)
}

func (pdb palmDB) recordRanges(r io.ReaderAt, size int64, minRecord0Bytes int64) ([]mobiRecordRange, bool) {
	recordTable := make([]byte, pdb.records*palmDBRecordSize)
	if _, err := r.ReadAt(recordTable, palmDBHeaderSize); err != nil {
		return nil, false
	}

	ranges := make([]mobiRecordRange, pdb.records)
	for i := range pdb.records {
		pos := i * palmDBRecordSize
		start := int64(binary.BigEndian.Uint32(recordTable[pos : pos+4]))
		end := size
		if i+1 < pdb.records {
			next := (i + 1) * palmDBRecordSize
			end = int64(binary.BigEndian.Uint32(recordTable[next : next+4]))
		}
		if start < pdb.recordsEnd || start > size || end < start || end > size {
			return nil, false
		}
		if i == 0 && minRecord0Bytes > 0 && start+minRecord0Bytes > end {
			return nil, false
		}
		ranges[i] = mobiRecordRange{start: start, end: end}
	}
	return ranges, true
}
