package format

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

var ErrAZW4PDFNotFound = errors.New("azw4 contains no embedded PDF")

var (
	azw4PDFStartMarker = []byte("%PDF-")
	azw4PDFEndMarker   = []byte("%%EOF")
)

const azw4ScanChunkSize = 64 << 10

// ExtractAZW4PDFContext writes the embedded PDF payload from an AZW4/PalmDB
// container. AZW4 is a Kindle Print Replica shape that commonly stores a PDF
// payload inside a MOBI-like container. Callers should detect the source format
// first; this function only finds and copies the PDF section.
func ExtractAZW4PDFContext(ctx context.Context, w io.Writer, r io.ReaderAt, size int64) error {
	start, end, err := azw4PDFBoundsContext(ctx, r, size)
	if err != nil {
		return err
	}
	return copyContext(ctx, w, io.NewSectionReader(r, start, end-start))
}

// HasAZW4PDF reports whether an AZW4-like container has a bounded embedded PDF
// payload that Polka can unwrap.
func HasAZW4PDF(r io.ReaderAt, size int64) bool {
	_, _, err := azw4PDFBounds(r, size)
	return err == nil
}

func azw4PDFBounds(r io.ReaderAt, size int64) (int64, int64, error) {
	return azw4PDFBoundsContext(context.Background(), r, size)
}

func azw4PDFBoundsContext(ctx context.Context, r io.ReaderAt, size int64) (int64, int64, error) {
	if size <= 0 {
		return 0, 0, ErrAZW4PDFNotFound
	}

	overlapSize := max(len(azw4PDFStartMarker), len(azw4PDFEndMarker)) - 1
	buf := make([]byte, azw4ScanChunkSize)
	overlap := make([]byte, 0, overlapSize)
	start := int64(-1)
	lastEOF := int64(-1)

	for offset := int64(0); offset < size; {
		if err := checkContext(ctx); err != nil {
			return 0, 0, err
		}
		n := int64(len(buf))
		if remaining := size - offset; remaining < n {
			n = remaining
		}
		if _, err := r.ReadAt(buf[:n], offset); err != nil && err != io.EOF {
			return 0, 0, err
		}

		base := offset - int64(len(overlap))
		window := make([]byte, 0, len(overlap)+int(n))
		window = append(window, overlap...)
		window = append(window, buf[:n]...)

		if start < 0 {
			if idx := bytes.Index(window, azw4PDFStartMarker); idx >= 0 {
				start = base + int64(idx)
			}
		}
		for searchFrom := 0; searchFrom < len(window); {
			idx := bytes.Index(window[searchFrom:], azw4PDFEndMarker)
			if idx < 0 {
				break
			}
			pos := base + int64(searchFrom+idx)
			if pos > lastEOF {
				lastEOF = pos
			}
			searchFrom += idx + 1
		}

		if len(window) > overlapSize {
			overlap = append(overlap[:0], window[len(window)-overlapSize:]...)
		} else {
			overlap = append(overlap[:0], window...)
		}
		offset += n
	}

	if start < 0 {
		return 0, 0, ErrAZW4PDFNotFound
	}
	if lastEOF < start {
		return 0, 0, fmt.Errorf("%w: missing PDF EOF marker", ErrAZW4PDFNotFound)
	}
	return start, lastEOF + int64(len(azw4PDFEndMarker)), nil
}
