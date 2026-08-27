package format

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

const maxDJVUAnnotationBytes = 2 << 20

var djvuMetadataPairRe = regexp.MustCompile(`\(([A-Za-z][A-Za-z0-9_.:-]*)\s+"((?:\\.|[^"\\])*)"\)`)

func isDJVU(r io.ReaderAt, size int64) bool {
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

// ExtractDJVUMetadata reads cheap document metadata from plain ANTa annotation
// chunks. Compressed ANTz annotations and page rendering remain separate DJVU
// work because they need more machinery than import-time metadata sniffing.
func ExtractDJVUMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	if !isDJVU(r, size) {
		return nil, fmt.Errorf("not a DJVU document")
	}
	annotations, err := djvuAnnotationChunks(r, size)
	if err != nil {
		return nil, err
	}
	meta := &Metadata{}
	for _, annotation := range annotations {
		mergeDJVUMetadata(meta, metadataFromDJVUAnnotation(annotation))
	}
	return meta, nil
}

func djvuAnnotationChunks(r io.ReaderAt, size int64) ([][]byte, error) {
	if size < 16 {
		return nil, nil
	}
	formSize, err := readDJVUUint32(r, 8)
	if err != nil {
		return nil, err
	}
	end := int64(12) + int64(formSize)
	if end > size || end < 16 {
		end = size
	}
	return scanDJVUChunks(r, 16, end, 0)
}

func scanDJVUChunks(r io.ReaderAt, start, end int64, depth int) ([][]byte, error) {
	if depth > 8 {
		return nil, nil
	}
	var annotations [][]byte
	for off := start; off+8 <= end; {
		var header [8]byte
		if _, err := r.ReadAt(header[:], off); err != nil {
			return nil, err
		}
		id := string(header[:4])
		chunkSize := int64(binary.BigEndian.Uint32(header[4:8]))
		dataStart := off + 8
		dataEnd := dataStart + chunkSize
		if dataEnd < dataStart || dataEnd > end {
			break
		}
		switch id {
		case "FORM":
			if chunkSize >= 4 {
				nested, err := scanDJVUChunks(r, dataStart+4, dataEnd, depth+1)
				if err != nil {
					return nil, err
				}
				annotations = append(annotations, nested...)
			}
		case "ANTa":
			if chunkSize > maxDJVUAnnotationBytes {
				return nil, fmt.Errorf("DJVU ANTa chunk exceeds %d bytes", maxDJVUAnnotationBytes)
			}
			buf := make([]byte, chunkSize)
			if _, err := r.ReadAt(buf, dataStart); err != nil {
				return nil, err
			}
			annotations = append(annotations, buf)
		}
		off = dataEnd
		if off%2 != 0 {
			off++
		}
	}
	return annotations, nil
}

func readDJVUUint32(r io.ReaderAt, off int64) (uint32, error) {
	var buf [4]byte
	if _, err := r.ReadAt(buf[:], off); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

func metadataFromDJVUAnnotation(raw []byte) *Metadata {
	meta := &Metadata{}
	src := string(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}))
	if !strings.Contains(strings.ToLower(src), "metadata") {
		return meta
	}
	for _, match := range djvuMetadataPairRe.FindAllStringSubmatch(src, -1) {
		key := strings.ToLower(strings.TrimSpace(match[1]))
		value := cleanDJVUText(unescapeDJVUString(match[2]))
		if value == "" {
			continue
		}
		switch key {
		case "title", "booktitle":
			if meta.Title == "" {
				meta.Title = value
			}
		case "author", "creator":
			if len(meta.Authors) == 0 {
				for _, author := range bookmeta.ParseAuthorList(value) {
					meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author, SortName: bookmeta.AuthorSort(author)})
				}
			}
		case "publisher":
			if meta.Publisher == "" {
				meta.Publisher = value
			}
		case "description", "abstract", "summary":
			if meta.Description == "" {
				meta.Description = value
			}
		case "language", "lang":
			if meta.Language == "" {
				meta.Language = bookmeta.NormalizeLanguage(value)
			}
		case "date", "year":
			if meta.Date == "" {
				meta.Date = bookmeta.NormalizeMetadataDate(value)
			}
		case "subject", "keywords":
			meta.Tags = appendDJVUTags(meta.Tags, value)
		case "isbn":
			if meta.Identifier == "" {
				if id := bookmeta.IdentifierFromOPF("isbn", value); id.Value != "" {
					meta.Identifier = bookmeta.FormatIdentifiers([]bookmeta.Identifier{id})
				}
			}
		}
	}
	return meta
}

func mergeDJVUMetadata(dst, src *Metadata) {
	if src == nil {
		return
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if len(dst.Authors) == 0 {
		dst.Authors = src.Authors
	}
	if dst.Publisher == "" {
		dst.Publisher = src.Publisher
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	if dst.Language == "" {
		dst.Language = src.Language
	}
	if dst.Date == "" {
		dst.Date = src.Date
	}
	if len(dst.Tags) == 0 {
		dst.Tags = src.Tags
	}
	if dst.Identifier == "" {
		dst.Identifier = src.Identifier
	}
}

func unescapeDJVUString(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	escaped := false
	for _, r := range s {
		if escaped {
			switch r {
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			default:
				out.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		out.WriteRune(r)
	}
	if escaped {
		out.WriteByte('\\')
	}
	return out.String()
}

func cleanDJVUText(s string) string {
	return strings.Join(strings.Fields(strings.ToValidUTF8(strings.TrimSpace(s), "\uFFFD")), " ")
}

func appendDJVUTags(tags []string, s string) []string {
	return appendUniqueTagList(tags, []string{s}, commaSemicolonSeparator, cleanDJVUText)
}
