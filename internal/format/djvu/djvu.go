package djvu

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

func Is(r io.ReaderAt, size int64) bool {
	if size < 16 {
		return false
	}
	header := make([]byte, 16)
	if _, err := r.ReadAt(header, 0); err != nil {
		return false
	}
	if !bytes.Equal(header[:8], []byte("AT&TFORM")) {
		return false
	}
	formType := string(header[12:16])
	return formType == "DJVU" || formType == "DJVM"
}

func CountPages(r io.ReaderAt, size int64) (int, error) {
	if !Is(r, size) {
		return 0, errors.New("not a DjVu document")
	}
	var header [16]byte
	if _, err := r.ReadAt(header[:], 0); err != nil {
		return 0, err
	}
	end := int64(12) + int64(binary.BigEndian.Uint32(header[8:12]))
	if end > size || end < 16 {
		return 0, errors.New("truncated DjVu document")
	}
	if string(header[12:]) == "DJVU" {
		return 1, nil
	}
	pages := 0
	for off := int64(16); off+8 <= end; {
		if _, err := r.ReadAt(header[:8], off); err != nil {
			return 0, err
		}
		n := int64(binary.BigEndian.Uint32(header[4:8]))
		if n > end-off-8 {
			return 0, errors.New("truncated DjVu chunk")
		}
		if string(header[:4]) == "FORM" && n >= 4 {
			if _, err := r.ReadAt(header[8:12], off+8); err != nil {
				return 0, err
			}
			if string(header[8:12]) == "DJVU" {
				pages++
			}
		}
		off += 8 + n + n%2
	}
	// An indirect DJVM has no embedded page FORMs. DIRM also lists shared
	// resources, so its component count must never be presented as page count.
	return pages, nil
}
