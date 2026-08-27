package format

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"html"
	"image"
	"io"
	"strings"

	"golang.org/x/text/encoding/charmap"

	"github.com/levmv/polka/internal/bookmeta"
)

const (
	palmDBHeaderSize    = 78
	palmDBRecordSize    = 8
	maxMOBIRecord0Bytes = 4 << 20
	maxMOBICoverBytes   = 32 << 20
	mobiNoImageIndex    = 0xffffffff
)

type MOBIKind string

const (
	MOBIKindUnknown       MOBIKind = ""
	MOBIKindMOBI6         MOBIKind = "mobi6"
	MOBIKindKF8Standalone MOBIKind = "kf8-standalone"
	MOBIKindCombo         MOBIKind = "mobi6+kf8-combo"
	MOBIKindPalmDOC       MOBIKind = "palmdoc"
)

type mobiRecordRange struct {
	start int64
	end   int64
}

func isMOBI(r io.ReaderAt, size int64) bool {
	record0Offset, _, ok := mobiRecord0Bounds(r, size)
	if !ok {
		return false
	}
	mobiMagic := make([]byte, 4)
	if _, err := r.ReadAt(mobiMagic, record0Offset+16); err != nil {
		return false
	}
	return bytes.Equal(mobiMagic, []byte("MOBI"))
}

// DetectMOBIKind returns a cheap diagnostic subtype for PalmDB ebook containers.
// It intentionally stays at the header/record-table layer; it does not parse
// MOBI text records, KF8 flows, or resources.
func DetectMOBIKind(r io.ReaderAt, size int64) MOBIKind {
	if isPalmDOC(r, size) {
		return MOBIKindPalmDOC
	}
	ranges, ok := mobiRecordRanges(r, size)
	if !ok || len(ranges) == 0 {
		return MOBIKindUnknown
	}
	record0, ok := mobiReadRecord(r, ranges[0], maxMOBIRecord0Bytes)
	if !ok || len(record0) < 0x6c || !bytes.Equal(record0[16:20], []byte("MOBI")) {
		return MOBIKindUnknown
	}
	if kf8HeaderIndex, ok := mobiEXTHUint32(record0, 121); ok && kf8HeaderIndex > 0 && kf8HeaderIndex < uint32(len(ranges)) {
		boundary, ok := mobiReadRecord(r, ranges[kf8HeaderIndex-1], 64)
		if ok && bytes.Equal(boundary, []byte("BOUNDARY")) {
			return MOBIKindCombo
		}
	}
	version := binary.BigEndian.Uint32(record0[0x68:0x6c])
	headerLength := binary.BigEndian.Uint32(record0[20:24])
	if version == 8 && headerLength >= 0x100 && len(record0) >= 0xfc {
		skelIndex := binary.BigEndian.Uint32(record0[0xf8:0xfc])
		if skelIndex > 0 && skelIndex != mobiNoImageIndex {
			return MOBIKindKF8Standalone
		}
	}
	return MOBIKindMOBI6
}

// ExtractMOBIMetadata reads the lightweight metadata carried by the MOBI header
// and EXTH records. It does not parse text content, resources, or KF8 structure;
// those are separate parser layers.
func ExtractMOBIMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	record0, ok := mobiRecord0(r, size)
	if !ok {
		return nil, fmt.Errorf("invalid MOBI PalmDB header")
	}
	if len(record0) < 24 || !bytes.Equal(record0[16:20], []byte("MOBI")) {
		return &Metadata{}, nil
	}

	meta := &Metadata{}
	codepage := mobiCodepage(record0)
	if title := mobiHeaderTitle(record0, codepage); title != "" {
		meta.Title = title
	}
	if lang := mobiHeaderLanguage(record0); lang != "" {
		meta.Language = lang
	}
	if mobiHasEXTH(record0) {
		mobiApplyEXTH(meta, record0, codepage)
	}
	if meta.Title == "" {
		if pdb, ok := readPalmDB(r, size); ok {
			if title := palmDBHeaderName(pdb.header, codepage); usefulMOBIPalmDBTitle(title) {
				meta.Title = title
			}
		}
	}
	if meta.Title == "" || len(meta.Authors) == 0 {
		mobiApplyLegacyTitlePageMetadata(meta, r, size, record0, codepage)
	}
	return meta, nil
}

// ExtractMOBICover extracts the cover image referenced by MOBI EXTH record 201.
// If the explicit cover offset is absent or unusable, it falls back to the
// MOBI header's first image record. It intentionally supports only image types
// handled by the current cover pipeline.
func ExtractMOBICover(r io.ReaderAt, size int64) ([]byte, string, error) {
	ranges, ok := mobiRecordRanges(r, size)
	if !ok {
		return nil, "", fmt.Errorf("invalid MOBI PalmDB header")
	}
	record0, ok := mobiReadRecord(r, ranges[0], maxMOBIRecord0Bytes)
	if !ok {
		return nil, "", fmt.Errorf("invalid MOBI record 0")
	}
	if len(record0) < 0x70 || !bytes.Equal(record0[16:20], []byte("MOBI")) {
		return nil, "", nil
	}
	// EXTH 203 marks a generated placeholder cover. Treat it as "no cover"
	// instead of persisting a fake image as the book's real cover.
	if fakeCover, ok := mobiEXTHUint32(record0, 203); ok && fakeCover != 0 {
		return nil, "", nil
	}

	firstImageIndex := binary.BigEndian.Uint32(record0[0x6c:0x70])
	if !mobiValidRecordIndex(firstImageIndex, len(ranges)) {
		return nil, "", nil
	}

	candidates := make([]uint32, 0, 2)
	if coverOffset, ok := mobiEXTHUint32(record0, 201); ok {
		if coverOffset != mobiNoImageIndex && coverOffset <= mobiNoImageIndex-firstImageIndex {
			candidates = append(candidates, firstImageIndex+coverOffset)
		}
	}
	candidates = append(candidates, firstImageIndex)

	seen := make(map[uint32]bool, len(candidates))
	for _, index := range candidates {
		if seen[index] {
			continue
		}
		seen[index] = true
		if !mobiValidRecordIndex(index, len(ranges)) {
			continue
		}
		cover, ext, err := mobiReadImageRecord(r, ranges[index])
		if err != nil {
			return nil, "", err
		}
		if cover != nil {
			return cover, ext, nil
		}
	}
	return nil, "", nil
}

func mobiRecord0(r io.ReaderAt, size int64) ([]byte, bool) {
	record0Offset, record0End, ok := mobiRecord0Bounds(r, size)
	if !ok {
		return nil, false
	}
	return mobiReadRecord(r, mobiRecordRange{start: record0Offset, end: record0End}, maxMOBIRecord0Bytes)
}

func mobiReadRecord(r io.ReaderAt, record mobiRecordRange, maxBytes int64) ([]byte, bool) {
	length := record.end - record.start
	if length < 0 || length > maxBytes {
		return nil, false
	}
	data := make([]byte, length)
	if _, err := r.ReadAt(data, record.start); err != nil {
		return nil, false
	}
	return data, true
}

func mobiRecord0Bounds(r io.ReaderAt, size int64) (int64, int64, bool) {
	pdb, ok := readPalmDB(r, size)
	if !ok || !bytes.Equal(pdb.header[60:68], []byte("BOOKMOBI")) {
		return 0, 0, false
	}
	record0Offset, record0End, ok := pdb.record0Bounds(r, size)
	if !ok || record0Offset+20 > size {
		return 0, 0, false
	}
	return record0Offset, record0End, true
}

func mobiRecordRanges(r io.ReaderAt, size int64) ([]mobiRecordRange, bool) {
	pdb, ok := readPalmDB(r, size)
	if !ok || !bytes.Equal(pdb.header[60:68], []byte("BOOKMOBI")) {
		return nil, false
	}
	return pdb.recordRanges(r, size, 20)
}

func mobiValidRecordIndex(index uint32, records int) bool {
	return index != mobiNoImageIndex && index < uint32(records)
}

func mobiCodepage(record0 []byte) uint32 {
	if len(record0) < 32 {
		return 1252
	}
	codepage := binary.BigEndian.Uint32(record0[28:32])
	if codepage == 0 {
		return 1252
	}
	return codepage
}

func mobiHeaderTitle(record0 []byte, codepage uint32) string {
	if len(record0) < 0x5c {
		return ""
	}
	offset := int(binary.BigEndian.Uint32(record0[0x54:0x58]))
	length := int(binary.BigEndian.Uint32(record0[0x58:0x5c]))
	if length <= 0 || offset < 0 || offset > len(record0) || offset+length > len(record0) {
		return ""
	}
	return mobiCleanString(mobiDecode(record0[offset:offset+length], codepage))
}

func usefulMOBIPalmDBTitle(title string) bool {
	return usefulMOBITextValue(title)
}

func usefulMOBITextValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "unknown", "untitled":
		return false
	default:
		return true
	}
}

func mobiHeaderLanguage(record0 []byte) string {
	if len(record0) < 0x60 {
		return ""
	}
	langCode := binary.BigEndian.Uint32(record0[0x5c:0x60])
	primary := byte(langCode & 0xff)
	return bookmeta.NormalizeLanguage(mobiPrimaryLanguage(primary))
}

func mobiHasEXTH(record0 []byte) bool {
	if len(record0) < 0x84 {
		return false
	}
	return binary.BigEndian.Uint32(record0[0x80:0x84])&0x40 != 0
}

func mobiApplyEXTH(meta *Metadata, record0 []byte, codepage uint32) {
	var ids []bookmeta.Identifier
	if meta.Identifier != "" {
		ids = bookmeta.ParseIdentifiers(meta.Identifier)
	}
	seenIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		seenIDs[mobiIdentifierKey(id)] = true
	}
	addID := func(id bookmeta.Identifier) {
		if id.Value == "" || bookmeta.IsInternalIdentifier(id) {
			return
		}
		if id.Type == "isbn" && !bookmeta.ValidISBN(id.Value) {
			return
		}
		key := mobiIdentifierKey(id)
		if seenIDs[key] {
			return
		}
		seenIDs[key] = true
		ids = append(ids, id)
	}

	mobiWalkEXTH(record0, func(recordType uint32, content []byte) bool {
		value := mobiCleanString(mobiDecode(content, codepage))
		switch recordType {
		case 100:
			if author := mobiAuthor(value); author.Name != "" {
				meta.Authors = append(meta.Authors, author)
			}
		case 101:
			if usefulMOBITextValue(value) {
				meta.Publisher = value
			}
		case 103:
			meta.Description = value
		case 104:
			addID(bookmeta.IdentifierFromOPF("isbn", value))
		case 105:
			meta.Tags = append(meta.Tags, mobiSplitTags(value)...)
		case 106:
			if date := bookmeta.NormalizeMetadataDate(value); date != "" {
				meta.Date = date
			}
		case 112:
			id := bookmeta.IdentifierFromOPF("", value)
			if id.Type == "isbn" {
				addID(id)
			}
		case 113:
			addID(bookmeta.IdentifierFromOPF("amazon", value))
		case 503:
			if value != "" {
				meta.Title = value
			}
		case 524:
			if lang := bookmeta.NormalizeLanguage(value); lang != "" {
				meta.Language = lang
			}
		}
		return true
	})
	if len(ids) > 0 {
		meta.Identifier = bookmeta.FormatIdentifiers(ids)
	}
	meta.Tags = mobiUniqStrings(meta.Tags)
}

func mobiWalkEXTH(record0 []byte, fn func(recordType uint32, content []byte) bool) {
	headerLength := int(binary.BigEndian.Uint32(record0[20:24]))
	exthStart := 16 + headerLength
	if headerLength <= 0 || exthStart+12 > len(record0) || !bytes.Equal(record0[exthStart:exthStart+4], []byte("EXTH")) {
		return
	}
	exthLength := int(binary.BigEndian.Uint32(record0[exthStart+4 : exthStart+8]))
	itemCount := int(binary.BigEndian.Uint32(record0[exthStart+8 : exthStart+12]))
	if exthLength < 12 || exthStart+exthLength > len(record0) {
		return
	}

	pos := exthStart + 12
	end := exthStart + exthLength
	for i := 0; i < itemCount && pos+8 <= end; i++ {
		recordType := binary.BigEndian.Uint32(record0[pos : pos+4])
		recordSize := int(binary.BigEndian.Uint32(record0[pos+4 : pos+8]))
		if recordSize < 8 || pos+recordSize > end {
			break
		}
		content := record0[pos+8 : pos+recordSize]
		pos += recordSize
		if !fn(recordType, content) {
			return
		}
	}
}

func mobiEXTHUint32(record0 []byte, want uint32) (uint32, bool) {
	var got uint32
	found := false
	mobiWalkEXTH(record0, func(recordType uint32, content []byte) bool {
		if recordType != want {
			return true
		}
		if len(content) < 4 {
			return false
		}
		got = binary.BigEndian.Uint32(content[0:4])
		found = true
		return false
	})
	return got, found
}

func mobiReadImageRecord(r io.ReaderAt, record mobiRecordRange) ([]byte, string, error) {
	data, ok := mobiReadRecord(r, record, maxMOBICoverBytes)
	if !ok {
		return nil, "", nil
	}
	ext, ok := mobiImageExtension(data)
	if !ok {
		return nil, "", nil
	}
	return data, ext, nil
}

func mobiImageExtension(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	_, formatName, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", false
	}
	return coverImageExtensionFromFormatName(formatName)
}

func mobiAuthor(value string) bookmeta.AuthorMeta {
	name := mobiCleanString(value)
	if name == "" {
		return bookmeta.AuthorMeta{}
	}
	parts := strings.Split(name, ",")
	if len(parts) == 2 {
		last := strings.TrimSpace(parts[0])
		first := strings.TrimSpace(parts[1])
		if last != "" && first != "" {
			return bookmeta.AuthorMeta{Name: first + " " + last, SortName: last + ", " + first, Role: "aut"}
		}
	}
	return bookmeta.AuthorMeta{Name: name, Role: "aut"}
}

func mobiSplitTags(value string) []string {
	return splitTagFields(value, semicolonNewlineTabSeparator, mobiCleanString)
}

func mobiIdentifierKey(id bookmeta.Identifier) string {
	value := strings.ToLower(strings.TrimSpace(id.Value))
	if id.Type == "isbn" {
		value = strings.NewReplacer("-", "", " ", "").Replace(value)
	}
	return strings.ToLower(strings.TrimSpace(id.Type)) + "\x00" + value
}

func mobiDecode(raw []byte, codepage uint32) string {
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return ""
	}
	if codepage == 65001 {
		return string(raw)
	}
	return decodeCharmap(raw, mobiCharmap(codepage))
}

func mobiCleanString(s string) string {
	// EXTH strings are raw metadata bytes, not XML; decode common entities and
	// drop control characters before storing user-visible values.
	s = html.UnescapeString(strings.ToValidUTF8(s, ""))
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		default:
			return r
		}
	}, s)
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func mobiCharmap(codepage uint32) *charmap.Charmap {
	switch codepage {
	case 1250:
		return charmap.Windows1250
	case 1251:
		return charmap.Windows1251
	case 1252:
		return charmap.Windows1252
	case 1253:
		return charmap.Windows1253
	case 1254:
		return charmap.Windows1254
	case 1255:
		return charmap.Windows1255
	case 1256:
		return charmap.Windows1256
	case 1257:
		return charmap.Windows1257
	case 1258:
		return charmap.Windows1258
	default:
		return charmap.Windows1252
	}
}

func mobiPrimaryLanguage(primary byte) string {
	switch primary {
	case 0x01:
		return "ar"
	case 0x02:
		return "bg"
	case 0x03:
		return "ca"
	case 0x04:
		return "zh"
	case 0x05:
		return "cs"
	case 0x06:
		return "da"
	case 0x07:
		return "de"
	case 0x08:
		return "el"
	case 0x09:
		return "en"
	case 0x0a:
		return "es"
	case 0x0b:
		return "fi"
	case 0x0c:
		return "fr"
	case 0x0d:
		return "he"
	case 0x0e:
		return "hu"
	case 0x0f:
		return "is"
	case 0x10:
		return "it"
	case 0x11:
		return "ja"
	case 0x12:
		return "ko"
	case 0x13:
		return "nl"
	case 0x14:
		return "no"
	case 0x15:
		return "pl"
	case 0x16:
		return "pt"
	case 0x18:
		return "ro"
	case 0x19:
		return "ru"
	case 0x1a:
		return "hr"
	case 0x1b:
		return "sk"
	case 0x1d:
		return "sv"
	case 0x1e:
		return "th"
	case 0x1f:
		return "tr"
	case 0x21:
		return "id"
	case 0x22:
		return "uk"
	case 0x24:
		return "sl"
	case 0x25:
		return "et"
	case 0x26:
		return "lv"
	case 0x27:
		return "lt"
	case 0x29:
		return "fa"
	case 0x2a:
		return "vi"
	case 0x2d:
		return "eu"
	case 0x39:
		return "hi"
	case 0x3c:
		return "ga"
	case 0x3e:
		return "ms"
	case 0x45:
		return "bn"
	case 0x49:
		return "ta"
	default:
		return ""
	}
}

func mobiUniqStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := values[:0]
	for _, value := range values {
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}
