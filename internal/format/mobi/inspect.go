package mobi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	ContainerMOBI    = "bookmobi"
	ContainerPalmDOC = "palmdoc"

	mobiCompressionNone     = 1
	mobiCompressionPalmDOC  = 2
	mobiCompressionHUFFCDIC = 17480
	mobiTypeDictionary      = 0x206
)

// Inspection holds header, resource and index information for diagnostics and
// content extraction. Text records are not decompressed during inspection.
type Inspection struct {
	SourceClass              string
	Container                string
	TypeCreator              string
	Kind                     Kind
	RecordCount              int
	Compression              uint16
	CompressionName          string
	TextLength               uint32
	TextRecords              uint16
	RecordSize               uint16
	Encryption               uint16
	Encrypted                bool
	HeaderLength             uint32
	Codepage                 uint32
	MOBIType                 uint32
	MOBIVersion              uint32
	TrailingFlags            uint16
	HasEXTH                  bool
	EXTHTypes                []uint32
	CDEType                  string
	PrimaryWritingMode       string
	PageProgressionDirection string
	Dictionary               bool
	FirstResourceIndex       uint32
	HUFFCDICIndex            uint32
	HUFFCDICRecordCount      uint32
	NCXIndex                 uint32
	FDSTIndex                uint32
	FDSTCount                uint32
	FragmentIndex            uint32
	SkeletonIndex            uint32
	GuideIndex               uint32
	BoundaryIndex            uint32
	ResourceCounts           ResourceCounts
	FDSTSections             []FDSTSection
	KF8Skeletons             []KF8Skeleton
	KF8Fragments             []KF8Fragment
	AZW4PDF                  bool
	UnsupportedFeatures      []string
}

type FDSTSection struct {
	Start uint32
	End   uint32
}

type KF8Skeleton struct {
	Index         int
	Name          string
	FragmentCount uint32
	Start         uint32
	Length        uint32
}

type KF8Fragment struct {
	InsertOffset uint32
	Selector     string
	FileNumber   uint32
	Sequence     uint32
	Start        uint32
	Length       uint32
}

type ResourceCounts struct {
	Images   int
	Fonts    int
	HUFF     int
	CDIC     int
	INDX     int
	RESC     int
	FDST     int
	FLIS     int
	FCIS     int
	DATP     int
	SRCS     int
	Audios   int
	Videos   int
	INFL     int
	ORTH     int
	Boundary int
	Other    int
}

// Inspect reads MOBI/PalmDOC headers, resource signatures and indexes.
// A nil result means the input is not a recognized PalmDB ebook container.
func Inspect(r io.ReaderAt, size int64) (*Inspection, error) {
	return inspect(r, size, false)
}

func inspect(r io.ReaderAt, size int64, printReplica bool) (*Inspection, error) {
	pdb, err := readPalmDB(r, size)
	if errors.Is(err, ErrUnsupportedSource) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	typeCreator := string(pdb.header[60:68])
	switch typeCreator {
	case string(palmDOCTypeCreator):
		if !printReplica {
			return inspectPalmDOC(r, size, pdb)
		}
	case "BOOKMOBI":
		return inspectBookMOBI(r, size, pdb, printReplica)
	}
	return nil, nil
}

func inspectPalmDOC(r io.ReaderAt, size int64, pdb palmDB) (*Inspection, error) {
	record0Offset, record0End, err := pdb.record0Bounds(r, size)
	if err != nil {
		return nil, err
	}
	if record0End-record0Offset < palmDOCHeader {
		return nil, fmt.Errorf("%w: invalid PalmDOC record table", ErrUnsupportedSource)
	}
	record0 := make([]byte, palmDOCHeader)
	if _, err := r.ReadAt(record0, record0Offset); err != nil {
		return nil, fmt.Errorf("read PalmDOC record 0: %w", err)
	}

	info := &Inspection{
		Container:   ContainerPalmDOC,
		TypeCreator: string(pdb.header[60:68]),
		Kind:        KindPalmDOC,
		RecordCount: pdb.records,
	}
	applyPalmDOCHeader(info, record0)
	info.SourceClass = kindleSourceClass(info, false)
	info.UnsupportedFeatures = kindleUnsupportedFeatures(info)
	return info, nil
}

func inspectBookMOBI(r io.ReaderAt, size int64, pdb palmDB, printReplica bool) (*Inspection, error) {
	ranges, err := pdb.recordRanges(r, size, 20)
	if err != nil {
		return nil, err
	}
	record0, err := mobiReadRecord(r, ranges[0], maxMOBIRecord0Bytes)
	if err != nil {
		return nil, fmt.Errorf("read MOBI record 0: %w", err)
	}

	info, err := parseMOBIHeader(record0, len(ranges), printReplica)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedSource, err)
	}
	combo, err := kindleHasComboBoundary(r, ranges, info.BoundaryIndex)
	if err != nil {
		return nil, err
	}
	if combo {
		info.Kind = KindCombo
	}
	applyKindleRecordSetDiagnostics(info, r, ranges)
	if printReplica {
		info.AZW4PDF = HasPDF(r, size)
	}
	info.SourceClass = kindleSourceClass(info, printReplica)
	info.UnsupportedFeatures = kindleUnsupportedFeatures(info)
	return info, nil
}

// parseMOBIHeader reads the fixed header and optional EXTH metadata for
// diagnostics and text extraction. Print Replica needs only the PalmDOC prefix.
func parseMOBIHeader(record0 []byte, records int, printReplica bool) (*Inspection, error) {
	info := &Inspection{Container: ContainerMOBI, TypeCreator: "BOOKMOBI", RecordCount: records}
	if len(record0) < palmDOCHeader {
		return info, fmt.Errorf("truncated PalmDOC header")
	}
	applyPalmDOCHeader(info, record0)
	if len(record0) < 24 || !bytes.Equal(record0[16:20], []byte("MOBI")) {
		if printReplica {
			return info, nil
		}
		return info, fmt.Errorf("invalid MOBI header signature")
	}
	applyMOBIHeader(info, record0)
	info.Dictionary = info.MOBIType == mobiTypeDictionary
	if len(record0) < 0x6c {
		if printReplica {
			return info, nil
		}
		return info, fmt.Errorf("truncated MOBI header")
	}
	info.Kind = KindMOBI6
	if info.MOBIVersion == 8 && info.HeaderLength >= 0x100 && info.FragmentIndex > 0 {
		info.Kind = KindKF8
	}
	return info, nil
}

func applyPalmDOCHeader(info *Inspection, record0 []byte) {
	if len(record0) < palmDOCHeader {
		return
	}
	info.Compression = binary.BigEndian.Uint16(record0[0:2])
	info.CompressionName = mobiCompressionName(info.Compression)
	info.TextLength = binary.BigEndian.Uint32(record0[4:8])
	info.TextRecords = binary.BigEndian.Uint16(record0[8:10])
	info.RecordSize = binary.BigEndian.Uint16(record0[10:12])
	info.Encryption = binary.BigEndian.Uint16(record0[12:14])
	info.Encrypted = info.Encryption != 0
}

func applyMOBIHeader(info *Inspection, record0 []byte) {
	info.HeaderLength = kindleUint32(record0, 20)
	info.MOBIType = kindleUint32(record0, 24)
	info.Codepage = mobiCodepage(record0)
	info.MOBIVersion = kindleUint32(record0, 0x68)
	info.TrailingFlags = kindleUint16(record0, 0xf2)
	info.FirstResourceIndex = kindleRecordIndex(record0, 0x6c)
	info.HUFFCDICIndex = kindleRecordIndex(record0, 0x70)
	info.HUFFCDICRecordCount = kindleUint32(record0, 0x74)
	info.HasEXTH = mobiHasEXTH(record0)
	if info.HasEXTH {
		info.EXTHTypes = mobiEXTHTypes(record0)
		info.CDEType = mobiEXTHString(record0, 501, info.Codepage)
		if boundary, ok := mobiEXTHUint32(record0, 121); ok && boundary != mobiNoImageIndex {
			info.BoundaryIndex = boundary
		}
		info.PrimaryWritingMode = mobiEXTHString(record0, 525, info.Codepage)
		info.PageProgressionDirection = mobiEXTHString(record0, 527, info.Codepage)
	}
	info.NCXIndex = kindleMOBIHeaderRecordIndex(record0, info.HeaderLength, 0xf4)
	info.FDSTIndex = kindleMOBIHeaderRecordIndex(record0, info.HeaderLength, 0xc0)
	info.FDSTCount = kindleMOBIHeaderUint32(record0, info.HeaderLength, 0xc4)
	info.FragmentIndex = kindleMOBIHeaderRecordIndex(record0, info.HeaderLength, 0xf8)
	info.SkeletonIndex = kindleMOBIHeaderRecordIndex(record0, info.HeaderLength, 0xfc)
	info.GuideIndex = kindleMOBIHeaderRecordIndex(record0, info.HeaderLength, 0x104)
}

func applyKindleRecordSetDiagnostics(info *Inspection, r io.ReaderAt, ranges []mobiRecordRange) {
	info.ResourceCounts = kindleCountResourceRecords(r, ranges, kindleFirstResourceRecord(info))
	info.Dictionary = info.MOBIType == mobiTypeDictionary || info.ResourceCounts.INFL > 0 || info.ResourceCounts.ORTH > 0
	if info.FDSTIndex > 0 && info.FDSTCount > 0 {
		if sections, err := readKindleFDSTSections(ranges, r, int(info.FDSTIndex), info.FDSTCount); err == nil {
			info.FDSTSections = sections
		}
	}
	if info.SkeletonIndex > 0 {
		if skeletons, err := readKindleKF8Skeletons(ranges, r, int(info.SkeletonIndex)); err == nil {
			info.KF8Skeletons = skeletons
		}
	}
	if info.FragmentIndex > 0 {
		if fragments, err := readKindleKF8Fragments(ranges, r, int(info.FragmentIndex)); err == nil {
			info.KF8Fragments = fragments
		}
	}
}

func mobiCompressionName(compression uint16) string {
	switch compression {
	case mobiCompressionNone:
		return "none"
	case mobiCompressionPalmDOC:
		return "palmdoc"
	case mobiCompressionHUFFCDIC:
		return "huff-cdic"
	case 0:
		return ""
	default:
		return "unknown"
	}
}

func mobiEXTHTypes(record0 []byte) []uint32 {
	var types []uint32
	mobiWalkEXTH(record0, func(recordType uint32, _ []byte) bool {
		types = append(types, recordType)
		return true
	})
	return types
}

func mobiEXTHString(record0 []byte, want uint32, codepage uint32) string {
	var value string
	mobiWalkEXTH(record0, func(recordType uint32, content []byte) bool {
		if recordType != want {
			return true
		}
		value = mobiCleanString(mobiDecode(content, codepage))
		return false
	})
	return value
}

func kindleSourceClass(info *Inspection, printReplica bool) string {
	if info.Container == ContainerPalmDOC {
		if info.Encrypted {
			return "encrypted-palmdoc"
		}
		return "palmdoc"
	}
	if info.Encrypted {
		return "encrypted"
	}
	if printReplica {
		if info.AZW4PDF {
			return "azw4-pdf-wrapper"
		}
		return "azw4-opaque"
	}
	if info.CDEType == "EBSP" {
		return "sample-book"
	}
	if info.Dictionary {
		return "dictionary"
	}
	switch info.Kind {
	case KindMOBI6:
		return string(KindMOBI6)
	case KindKF8:
		return string(KindKF8)
	case KindCombo:
		return string(KindCombo)
	default:
		return "unknown"
	}
}

func kindleUnsupportedFeatures(info *Inspection) []string {
	var out []string
	if info.Encrypted {
		out = append(out, "encrypted")
	}
	switch info.Compression {
	case mobiCompressionNone, mobiCompressionPalmDOC:
	case mobiCompressionHUFFCDIC:
		if !huffCDICAvailable(info) {
			out = append(out, "huff-cdic-compression")
		}
	default:
		out = append(out, "unknown-compression")
	}
	if info.Dictionary {
		out = append(out, "dictionary-indexes")
	}
	if info.AZW4PDF {
		out = append(out, "azw4-print-replica")
	}
	return out
}

func kindleFirstResourceRecord(info *Inspection) int {
	if info.FirstResourceIndex > 0 && info.FirstResourceIndex < uint32(info.RecordCount) {
		return int(info.FirstResourceIndex)
	}
	if info.TextRecords > 0 {
		return 1 + int(info.TextRecords)
	}
	return 1
}

func kindleCountResourceRecords(r io.ReaderAt, ranges []mobiRecordRange, start int) ResourceCounts {
	var counts ResourceCounts
	if start < 1 {
		start = 1
	}
	if start > len(ranges) {
		return counts
	}
	for i := start; i < len(ranges); i++ {
		prefix, ok := mobiReadRecordPrefix(r, ranges[i], 16)
		if !ok {
			continue
		}
		switch {
		case bytes.HasPrefix(prefix, []byte("HUFF")):
			counts.HUFF++
		case bytes.HasPrefix(prefix, []byte("CDIC")):
			counts.CDIC++
		case bytes.HasPrefix(prefix, []byte("INDX")):
			counts.INDX++
		case bytes.HasPrefix(prefix, []byte("RESC")):
			counts.RESC++
		case bytes.HasPrefix(prefix, []byte("FDST")):
			counts.FDST++
		case bytes.HasPrefix(prefix, []byte("FLIS")):
			counts.FLIS++
		case bytes.HasPrefix(prefix, []byte("FCIS")):
			counts.FCIS++
		case bytes.HasPrefix(prefix, []byte("DATP")):
			counts.DATP++
		case bytes.HasPrefix(prefix, []byte("SRCS")):
			counts.SRCS++
		case bytes.HasPrefix(prefix, []byte("AUDI")):
			counts.Audios++
		case bytes.HasPrefix(prefix, []byte("VIDE")):
			counts.Videos++
		case bytes.HasPrefix(prefix, []byte("INFL")):
			counts.INFL++
		case bytes.HasPrefix(prefix, []byte("ORTH")):
			counts.ORTH++
		case bytes.HasPrefix(prefix, []byte("BOUNDARY")):
			counts.Boundary++
		case kindleImageMagic(prefix):
			counts.Images++
		case kindleFontMagic(prefix):
			counts.Fonts++
		default:
			counts.Other++
		}
	}
	return counts
}

func mobiReadRecordPrefix(r io.ReaderAt, record mobiRecordRange, maxBytes int64) ([]byte, bool) {
	length := record.end - record.start
	if length <= 0 {
		return nil, false
	}
	if length > maxBytes {
		length = maxBytes
	}
	data := make([]byte, length)
	if _, err := r.ReadAt(data, record.start); err != nil {
		return nil, false
	}
	return data, true
}

func kindleImageMagic(prefix []byte) bool {
	return bytes.HasPrefix(prefix, []byte{0xff, 0xd8, 0xff}) ||
		bytes.HasPrefix(prefix, []byte("\x89PNG\r\n\x1a\n")) ||
		bytes.HasPrefix(prefix, []byte("GIF87a")) ||
		bytes.HasPrefix(prefix, []byte("GIF89a")) ||
		bytes.HasPrefix(prefix, []byte("RIFF")) && len(prefix) >= 12 && bytes.Equal(prefix[8:12], []byte("WEBP"))
}

func kindleFontMagic(prefix []byte) bool {
	return bytes.HasPrefix(prefix, []byte("FONT")) ||
		bytes.HasPrefix(prefix, []byte{0x00, 0x01, 0x00, 0x00}) ||
		bytes.HasPrefix(prefix, []byte("OTTO")) ||
		bytes.HasPrefix(prefix, []byte("ttcf")) ||
		bytes.HasPrefix(prefix, []byte("wOFF")) ||
		bytes.HasPrefix(prefix, []byte("wOF2"))
}

func kindleUint32(data []byte, offset int) uint32 {
	if offset < 0 || offset+4 > len(data) {
		return 0
	}
	return binary.BigEndian.Uint32(data[offset : offset+4])
}

func kindleUint16(data []byte, offset int) uint16 {
	if offset < 0 || offset+2 > len(data) {
		return 0
	}
	return binary.BigEndian.Uint16(data[offset : offset+2])
}

func kindleRecordIndex(data []byte, offset int) uint32 {
	value := kindleUint32(data, offset)
	if value == mobiNoImageIndex {
		return 0
	}
	return value
}

func kindleMOBIHeaderUint32(data []byte, headerLength uint32, offset int) uint32 {
	headerEnd := 16 + int(headerLength)
	if offset < 16 || offset+4 > headerEnd {
		return 0
	}
	return kindleUint32(data, offset)
}

func kindleMOBIHeaderRecordIndex(data []byte, headerLength uint32, offset int) uint32 {
	value := kindleMOBIHeaderUint32(data, headerLength, offset)
	if value == mobiNoImageIndex {
		return 0
	}
	return value
}
