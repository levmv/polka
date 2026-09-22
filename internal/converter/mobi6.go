package converter

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

const mobi6RecordSize = 4096

// This is the result of projecting a publication into legacy Mobipocket HTML.
// Target names and any future default/profile selection belong to converter.go.
type mobi6Book struct {
	text   []byte
	images [][]byte
	cover  int // zero based image index, or -1
	meta   epubMetadata
	nav    []mobi6Navigation
	start  int
}

type mobi6Navigation struct {
	label  string
	offset int
}

func writeMOBI6(ctx context.Context, w io.Writer, book mobi6Book) error {
	if len(book.text) == 0 || int64(len(book.text)) > maxConverterDecodedInputBytes {
		return fmt.Errorf("MOBI6 text length exceeds limit: %w", ErrResourceLimit)
	}
	records := [][]byte{nil}
	for start := 0; start < len(book.text); start += mobi6RecordSize {
		if err := checkContext(ctx); err != nil {
			return err
		}
		end := min(start+mobi6RecordSize, len(book.text))
		record := compressMOBI6Text(book.text[start:end])
		// Readers may render individual records. Complete a UTF-8 character at
		// the boundary in the trailing area; it is not part of the text length.
		overlap := end
		for overlap < len(book.text) && book.text[overlap]&0xc0 == 0x80 {
			overlap++
		}
		record = append(record, book.text[end:overlap]...)
		records = append(records, append(record, byte(overlap-end)))
	}
	textRecords := len(records) - 1
	ncx := uint32(0xffffffff)
	if len(book.nav) > 0 {
		index, err := mobi6Index(book.nav, len(book.text))
		if err != nil {
			return err
		}
		ncx = uint32(len(records))
		records = append(records, index...)
	}
	firstImage := uint32(0xffffffff)
	if len(book.images) > 0 {
		firstImage = uint32(len(records))
		records = append(records, book.images...)
	}
	lastContent := len(records) - 1
	flis := len(records)
	flisRecord := make([]byte, 36)
	copy(flisRecord, "FLIS")
	for offset, value := range map[int]uint32{4: 8, 8: 0x410000, 16: 0xffffffff, 20: 0x10003, 24: 3, 28: 1, 32: 0xffffffff} {
		binary.BigEndian.PutUint32(flisRecord[offset:], value)
	}
	records = append(records, flisRecord)
	fcis := len(records)
	fcisRecord := make([]byte, 44)
	copy(fcisRecord, "FCIS")
	for offset, value := range map[int]uint32{4: 20, 8: 16, 12: 1, 20: uint32(len(book.text)), 28: 32, 32: 8, 36: 0x10001} {
		binary.BigEndian.PutUint32(fcisRecord[offset:], value)
	}
	records = append(records, fcisRecord, []byte{0xe9, 0x8e, 0x0d, 0x0a})
	if len(records) > 65535 {
		return fmt.Errorf("too many MOBI6 records: %w", ErrResourceLimit)
	}

	exth := mobi6EXTH(book)
	if len(exth)+len(book.meta.Title) > int(maxConverterMetadataBytes) {
		return fmt.Errorf("MOBI6 metadata exceeds limit: %w", ErrResourceLimit)
	}
	header := make([]byte, 16+0xe8)
	u32 := func(offset int, value uint32) { binary.BigEndian.PutUint32(header[offset:], value) }
	binary.BigEndian.PutUint16(header, 2) // PalmDOC compression
	u32(4, uint32(len(book.text)))
	binary.BigEndian.PutUint16(header[8:], uint16(textRecords))
	binary.BigEndian.PutUint16(header[10:], mobi6RecordSize)
	copy(header[16:], "MOBI")
	u32(20, 0xe8)
	u32(24, 2) // ordinary book
	u32(28, 65001)
	u32(32, crc32.ChecksumIEEE(book.text))
	u32(36, 6)
	for offset := 40; offset < 80; offset += 4 {
		u32(offset, 0xffffffff)
	}
	u32(80, uint32(textRecords+1))
	u32(84, uint32(len(header)+len(exth)))
	u32(88, uint32(len(book.meta.Title)))
	u32(92, mobi6Language(book.meta.Language))
	u32(104, 6)
	u32(108, firstImage)
	u32(128, 0x50)       // EXTH present
	u32(164, 0xffffffff) // no DRM
	u32(168, 0xffffffff)
	binary.BigEndian.PutUint16(header[192:], 1)
	binary.BigEndian.PutUint16(header[194:], uint16(lastContent))
	u32(196, 1)
	u32(200, uint32(fcis))
	u32(204, 1)
	u32(208, uint32(flis))
	u32(212, 1)
	u32(224, 0xffffffff)
	u32(232, 0xffffffff)
	u32(236, 0xffffffff)
	u32(240, 1) // UTF-8 overlap, no text-record indexing trailer
	u32(244, ncx)
	header = append(header, exth...)
	header = append(header, book.meta.Title...)
	header = append(header, 0)
	records[0] = header

	pdb := make([]byte, 78+8*len(records)+2)
	// The full Unicode title lives in record 0. PalmDB's short name is ASCII.
	name := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return '_'
		}
		return r
	}, book.meta.Title)
	copy(pdb[:31], name)
	// Leave PalmDB bookkeeping dates unset. The publication date is in EXTH;
	// a wall-clock timestamp here changes the identity of every repeated download.
	copy(pdb[60:], "BOOKMOBI")
	binary.BigEndian.PutUint32(pdb[68:], uint32(len(records)*2))
	binary.BigEndian.PutUint16(pdb[76:], uint16(len(records)))
	offset := uint64(len(pdb))
	for i, record := range records {
		binary.BigEndian.PutUint32(pdb[78+8*i:], uint32(offset))
		binary.BigEndian.PutUint32(pdb[82+8*i:], uint32(i*2))
		offset += uint64(len(record))
	}
	if offset > uint64(maxConversionOutputBytes) {
		return fmt.Errorf("MOBI6 exceeds output limit: %w", ErrResourceLimit)
	}
	if _, err := w.Write(pdb); err != nil {
		return err
	}
	for _, record := range records {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if _, err := w.Write(record); err != nil {
			return err
		}
	}
	return nil
}

func mobi6EXTH(book mobi6Book) []byte {
	out := make([]byte, 12)
	copy(out, "EXTH")
	count := uint32(0)
	add := func(tag uint32, value []byte) {
		if len(value) == 0 {
			return
		}
		out = binary.BigEndian.AppendUint32(out, tag)
		out = binary.BigEndian.AppendUint32(out, uint32(len(value)+8))
		out = append(out, value...)
		count++
	}
	for _, author := range book.meta.Authors {
		add(100, []byte(author))
	}
	add(101, []byte(book.meta.Publisher))
	add(103, []byte(book.meta.Description))
	for _, tag := range book.meta.Tags {
		add(105, []byte(tag))
	}
	add(106, []byte(book.meta.Date))
	for _, value := range append([]string{book.meta.Identifier}, book.meta.ExtraIdentifiers...) {
		for _, id := range bookmeta.ParseIdentifiers(value) {
			switch id.Type {
			case "isbn":
				add(104, []byte(id.Value))
			case "amazon":
				add(113, []byte(id.Value))
			}
		}
	}
	add(108, []byte("polka"))
	add(116, binary.BigEndian.AppendUint32(nil, uint32(book.start)))
	if book.cover >= 0 {
		add(201, binary.BigEndian.AppendUint32(nil, uint32(book.cover)))
	}
	add(501, []byte("EBOK"))
	add(503, []byte(book.meta.Title))
	add(524, []byte(book.meta.Language))
	binary.BigEndian.PutUint32(out[4:], uint32(len(out)))
	binary.BigEndian.PutUint32(out[8:], count)
	return append(out, make([]byte, (4-len(out)%4)%4)...)
}

func mobi6Language(value string) uint32 {
	base, _, _ := strings.Cut(strings.ToLower(value), "-")
	// EXTH 524 retains the complete language tag, including less common locales.
	return map[string]uint32{"ar": 1, "zh": 4, "cs": 5, "da": 6, "de": 7, "el": 8, "en": 9, "es": 10, "fi": 11, "fr": 12, "he": 13, "hu": 14, "is": 15, "it": 16, "ja": 17, "ko": 18, "nl": 19, "no": 20, "pl": 21, "pt": 22, "ro": 24, "ru": 25, "hr": 26, "sk": 27, "sv": 29, "th": 30, "tr": 31, "uk": 34}[base]
}

// PalmDOC's back-reference window is 2047 bytes and match length is at most
// ten. A single previous occurrence per three-byte key bounds encoder work.
func compressMOBI6Text(src []byte) []byte {
	out := make([]byte, 0, len(src))
	previous := make(map[uint32]int)
	key := func(i int) uint32 { return uint32(src[i])<<16 | uint32(src[i+1])<<8 | uint32(src[i+2]) }
	for i := 0; i < len(src); {
		n := 0
		if i+3 <= len(src) {
			if pos, ok := previous[key(i)]; ok && i-pos <= 2047 {
				for n < 10 && i+n < len(src) && src[pos+n] == src[i+n] {
					n++
				}
				if n >= 3 {
					out = binary.BigEndian.AppendUint16(out, 0x8000|uint16(i-pos)<<3|uint16(n-3))
				}
			}
		}
		if n < 3 {
			n = 1
			switch {
			case src[i] == ' ' && i+1 < len(src) && src[i+1] >= 0x40 && src[i+1] <= 0x7f:
				out = append(out, src[i+1]^0x80)
				n = 2
			case src[i] == 0 || src[i] >= 9 && src[i] <= 0x7f:
				out = append(out, src[i])
			default:
				for n < 8 && i+n < len(src) && (src[i+n] >= 0x80 || src[i+n] > 0 && src[i+n] < 9) {
					n++
				}
				out = append(out, byte(n))
				out = append(out, src[i:i+n]...)
			}
		}
		for j := i; j < i+n && j+3 <= len(src); j++ {
			previous[key(j)] = j
		}
		i += n
	}
	return out
}

func mobi6VWI(out []byte, value int) []byte {
	var buf [5]byte
	pos := len(buf) - 1
	buf[pos] = byte(value&127) | 128
	for value >>= 7; value > 0; value >>= 7 {
		pos--
		buf[pos] = byte(value & 127)
	}
	return append(out, buf[pos:]...)
}

// The classic book index is flat and ordered by text position. Hierarchy is
// retained in the inline contents, independently of chapter-to-chapter jumps.
func mobi6Index(nav []mobi6Navigation, textEnd int) ([][]byte, error) {
	nav = slices.Clone(nav)
	slices.SortStableFunc(nav, func(a, b mobi6Navigation) int { return a.offset - b.offset })
	nav = slices.CompactFunc(nav, func(a, b mobi6Navigation) bool { return a.offset == b.offset })
	if len(nav) > maxConversionResourceCount {
		return nil, fmt.Errorf("MOBI6 navigation exceeds limit: %w", ErrResourceLimit)
	}
	newHeader := func() []byte {
		out := make([]byte, 192)
		copy(out, "INDX")
		binary.BigEndian.PutUint32(out[4:], 192)
		binary.BigEndian.PutUint32(out[8:], 0)
		binary.BigEndian.PutUint32(out[28:], 65001)
		binary.BigEndian.PutUint32(out[32:], 0xffffffff)
		return out
	}
	entries := newHeader()
	var labels, offsets []byte
	for i, item := range nav {
		if len(entries) > 65535 || len(labels)+len(item.label)+5 > 65535 {
			return nil, fmt.Errorf("MOBI6 navigation record exceeds limit: %w", ErrResourceLimit)
		}
		offsets = binary.BigEndian.AppendUint16(offsets, uint16(len(entries)))
		name := fmt.Sprintf("%04X", i)
		entries = append(entries, byte(len(name)))
		entries = append(entries, name...)
		entries = append(entries, 15) // tags 1..4: position, size, label, level
		end := textEnd
		if i+1 < len(nav) {
			end = nav[i+1].offset
		}
		for _, value := range []int{item.offset, end - item.offset, len(labels), 0} {
			entries = mobi6VWI(entries, value)
		}
		labels = mobi6VWI(labels, len(item.label))
		labels = append(labels, item.label...)
	}
	binary.BigEndian.PutUint32(entries[20:], uint32(len(entries)))
	binary.BigEndian.PutUint32(entries[24:], uint32(len(nav)))
	entries = append(entries, "IDXT"...)
	entries = append(entries, offsets...)
	master := newHeader()
	binary.BigEndian.PutUint32(master[12:], 1)
	binary.BigEndian.PutUint32(master[24:], 1)
	binary.BigEndian.PutUint32(master[36:], uint32(len(nav)))
	binary.BigEndian.PutUint32(master[52:], 1)
	master = append(master, "TAGX"...)
	master = binary.BigEndian.AppendUint32(master, 32)
	master = binary.BigEndian.AppendUint32(master, 1)
	for tag := byte(1); tag <= 4; tag++ {
		master = append(master, tag, 1, 1<<(tag-1), 0)
	}
	master = append(master, 0, 0, 0, 1)
	last := strings.ToUpper(strconv.FormatInt(int64(len(nav)-1), 16))
	last = strings.Repeat("0", max(4-len(last), 0)) + last
	entryOffset := len(master)
	master = append(master, byte(len(last)))
	master = append(master, last...)
	master = binary.BigEndian.AppendUint16(master, uint16(len(nav)))
	binary.BigEndian.PutUint32(master[20:], uint32(len(master)))
	master = append(master, "IDXT"...)
	master = binary.BigEndian.AppendUint16(master, uint16(entryOffset))
	return [][]byte{master, entries, labels}, nil
}
