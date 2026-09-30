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
)

// mobi6Book projects a publication into legacy Mobipocket HTML.
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
	records, err := kindleTextRecords(ctx, book.text)
	if err != nil {
		return err
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
	fcis := flis + 1
	records = append(records, kindleTrailer(len(book.text))...)
	if len(records) > 65535 {
		return fmt.Errorf("too many MOBI6 records: %w", ErrResourceLimit)
	}

	exth := kindleEXTH(book.meta, book.cover, book.start, "")
	if len(exth)+len(book.meta.Title) > int(maxConverterMetadataBytes) {
		return fmt.Errorf("MOBI6 metadata exceeds limit: %w", ErrResourceLimit)
	}
	header := make([]byte, 16+0xe8)
	u32 := func(offset int, value uint32) { binary.BigEndian.PutUint32(header[offset:], value) }
	binary.BigEndian.PutUint16(header, 2) // PalmDOC compression
	u32(4, uint32(len(book.text)))
	binary.BigEndian.PutUint16(header[8:], uint16(textRecords))
	binary.BigEndian.PutUint16(header[10:], kindleRecordSize)
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
	u32(92, kindleLanguage(book.meta.Language))
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

	return writeKindlePDB(ctx, w, book.meta.Title, records)
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
			entries = appendKindleVarint(entries, value)
		}
		labels = appendKindleVarint(labels, len(item.label))
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
