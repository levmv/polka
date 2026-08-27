package format

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/levmv/polka/internal/bookmeta"
)

// ExtractPDFMetadata performs lightweight fallback extraction of Title and
// Author from direct PDF metadata bytes. Encrypted Info dictionaries,
// compressed object streams, and indirect metadata values remain out of scope;
// those cases should fall back to filename/external metadata rather than
// pulling in another parser just for PDF metadata.
func ExtractPDFMetadata(b []byte) *Metadata {
	meta := &Metadata{}
	if !pdfHasEncryptReference(b) {
		scope := b
		if info := pdfInfoObject(b); len(info) > 0 {
			scope = info
		}
		pdfFillInfoMetadata(meta, scope)
	}

	// XMP is a fill-in fallback only: PDFs can carry stale editor/producer
	// metadata there, while the trailer Info dictionary is the direct book-level
	// source when present.
	if xmp := pdfXMPMetadata(b); xmp != nil {
		if meta.Title == "" {
			meta.Title = xmp.Title
		}
		if len(meta.Authors) == 0 {
			meta.Authors = xmp.Authors
		}
	}

	return meta
}

// ExtractPDFMetadataReader performs the same deliberately lightweight metadata
// extraction as ExtractPDFMetadata without retaining the whole PDF. It scans
// through bounded windows, then reads only the referenced Info object or XMP
// packet. This keeps large PDF import memory independent of source size while
// preserving filename fallback when metadata is absent or outside the narrow
// supported shapes.
func ExtractPDFMetadataReader(r io.ReaderAt, size int64) *Metadata {
	meta := &Metadata{}
	if r == nil || size <= 0 {
		return meta
	}

	if !pdfHasEncryptReferenceReader(r, size) {
		if info := pdfInfoObjectReader(r, size); len(info) > 0 {
			pdfFillInfoMetadata(meta, info)
		} else {
			if title, ok := pdfInfoStringReader(r, size, "Title"); ok {
				meta.Title = title
			}
			if author, ok := pdfInfoStringReader(r, size, "Author"); ok {
				meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
			}
		}
	}

	if meta.Title == "" || len(meta.Authors) == 0 {
		if xmp := pdfXMPMetadataPacket(pdfXMPPacketReader(r, size)); xmp != nil {
			if meta.Title == "" {
				meta.Title = xmp.Title
			}
			if len(meta.Authors) == 0 {
				meta.Authors = xmp.Authors
			}
		}
	}
	return meta
}

func pdfFillInfoMetadata(meta *Metadata, info []byte) {
	if title, ok := pdfInfoString(info, "Title"); ok {
		meta.Title = title
	}
	if author, ok := pdfInfoString(info, "Author"); ok {
		meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
	}
}

const (
	pdfMetadataScanChunk   = 256 << 10
	maxPDFInfoObjectBytes  = 1 << 20
	maxPDFInfoStringBytes  = 64 << 10
	maxPDFMetadataDictSize = 64 << 10
)

func pdfHasEncryptReference(b []byte) bool {
	searchEnd := len(b)
	for {
		idx := bytes.LastIndex(b[:searchEnd], []byte("/Encrypt"))
		if idx == -1 {
			return false
		}
		nameEnd := idx + len("/Encrypt")
		if nameEnd == len(b) || isPDFDelimiter(b[nameEnd]) {
			if _, _, ok := readPDFIndirectReference(b[nameEnd:]); ok {
				return true
			}
		}
		searchEnd = idx
	}
}

func pdfHasEncryptReferenceReader(r io.ReaderAt, size int64) bool {
	searchEnd := size
	for {
		idx := pdfLastIndexReader(r, searchEnd, []byte("/Encrypt"))
		if idx < 0 {
			return false
		}
		if pdfHasEncryptReference(pdfReadWindow(r, size, idx, 128)) {
			return true
		}
		searchEnd = idx
	}
}

func pdfInfoObjectReader(r io.ReaderAt, size int64) []byte {
	searchEnd := size
	for {
		idx := pdfLastIndexReader(r, searchEnd, []byte("/Info"))
		if idx < 0 {
			return nil
		}
		ref := pdfReadWindow(r, size, idx+int64(len("/Info")), 128)
		if obj, gen, ok := readPDFIndirectReference(ref); ok {
			return pdfIndirectObjectReader(r, size, obj, gen)
		}
		searchEnd = idx
	}
}

func pdfIndirectObjectReader(r io.ReaderAt, size int64, obj, gen int) []byte {
	marker := []byte(strconv.Itoa(obj) + " " + strconv.Itoa(gen) + " obj")
	searchAt := int64(0)
	for {
		idx := pdfIndexReader(r, size, marker, searchAt)
		if idx < 0 {
			return nil
		}
		if idx == 0 || isPDFWhitespace(pdfByteAt(r, idx-1)) {
			start := idx + int64(len(marker))
			object := pdfReadWindow(r, size, start, maxPDFInfoObjectBytes)
			if before, _, ok := bytes.Cut(object, []byte("endobj")); ok {
				return before
			}
			return object
		}
		searchAt = idx + 1
	}
}

func pdfInfoStringReader(r io.ReaderAt, size int64, key string) (string, bool) {
	needle := []byte("/" + key)
	searchAt := int64(0)
	for {
		idx := pdfIndexReader(r, size, needle, searchAt)
		if idx < 0 {
			return "", false
		}
		window := pdfReadWindow(r, size, idx, maxPDFInfoStringBytes)
		if value, ok := pdfInfoString(window, key); ok {
			return value, true
		}
		searchAt = idx + int64(len(needle))
	}
}

func pdfIndexReader(r io.ReaderAt, size int64, needle []byte, start int64) int64 {
	if len(needle) == 0 || start < 0 || start >= size {
		return -1
	}
	overlap := int64(len(needle) - 1)
	for offset := start; offset < size; {
		window := pdfReadWindow(r, size, offset, pdfMetadataScanChunk)
		if len(window) == 0 {
			return -1
		}
		if idx := bytes.Index(window, needle); idx >= 0 {
			return offset + int64(idx)
		}
		step := int64(len(window)) - overlap
		if step <= 0 {
			return -1
		}
		offset += step
	}
	return -1
}

func pdfLastIndexReader(r io.ReaderAt, end int64, needle []byte) int64 {
	if len(needle) == 0 || end <= 0 {
		return -1
	}
	overlap := int64(len(needle) - 1)
	for end > 0 {
		start := max(end-pdfMetadataScanChunk, 0)
		window := pdfReadWindow(r, end, start, int(end-start))
		if idx := bytes.LastIndex(window, needle); idx >= 0 {
			return start + int64(idx)
		}
		if start == 0 {
			return -1
		}
		end = start + overlap
	}
	return -1
}

func pdfReadWindow(r io.ReaderAt, size, offset int64, limit int) []byte {
	if limit <= 0 || offset < 0 || offset >= size {
		return nil
	}
	if remaining := size - offset; int64(limit) > remaining {
		limit = int(remaining)
	}
	data := make([]byte, limit)
	n, err := r.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return nil
	}
	return data[:n]
}

func pdfByteAt(r io.ReaderAt, offset int64) byte {
	var b [1]byte
	if _, err := r.ReadAt(b[:], offset); err != nil {
		return 0
	}
	return b[0]
}

func pdfInfoObject(b []byte) []byte {
	// Restrict metadata lookup to the trailer's Info object when available:
	// outline/bookmark dictionaries also use /Title, and they are not book titles.
	searchEnd := len(b)
	for {
		idx := bytes.LastIndex(b[:searchEnd], []byte("/Info"))
		if idx == -1 {
			return nil
		}
		obj, gen, ok := readPDFIndirectReference(b[idx+len("/Info"):])
		if !ok {
			searchEnd = idx
			continue
		}
		if objBytes := pdfIndirectObject(b, obj, gen); len(objBytes) > 0 {
			return objBytes
		}
		searchEnd = idx
	}
}

func pdfIndirectObject(b []byte, obj, gen int) []byte {
	marker := []byte(strconv.Itoa(obj) + " " + strconv.Itoa(gen) + " obj")
	offset := 0
	for {
		idx := bytes.Index(b[offset:], marker)
		if idx == -1 {
			return nil
		}
		idx += offset
		if idx == 0 || isPDFWhitespace(b[idx-1]) {
			start := idx + len(marker)
			if end := bytes.Index(b[start:], []byte("endobj")); end >= 0 {
				return b[start : start+end]
			}
			return b[start:]
		}
		offset = idx + 1
	}
}

func readPDFInt(b []byte) (int, int, bool) {
	consumed := 0
	for len(b) > 0 && isPDFWhitespace(b[0]) {
		b = b[1:]
		consumed++
	}
	start := 0
	for start < len(b) && b[start] >= '0' && b[start] <= '9' {
		start++
	}
	if start == 0 {
		return 0, 0, false
	}
	v, err := strconv.Atoi(string(b[:start]))
	if err != nil {
		return 0, 0, false
	}
	return v, consumed + start, true
}

func readPDFIndirectReference(b []byte) (int, int, bool) {
	obj, n, ok := readPDFInt(b)
	if !ok {
		return 0, 0, false
	}
	b = b[n:]
	gen, n, ok := readPDFInt(b)
	if !ok {
		return 0, 0, false
	}
	b = skipPDFWhitespace(b[n:])
	if len(b) == 0 || b[0] != 'R' || len(b) > 1 && !isPDFDelimiter(b[1]) {
		return 0, 0, false
	}
	return obj, gen, true
}

func skipPDFWhitespace(b []byte) []byte {
	for len(b) > 0 && isPDFWhitespace(b[0]) {
		b = b[1:]
	}
	return b
}

func pdfInfoString(b []byte, key string) (string, bool) {
	needle := []byte("/" + key)
	offset := 0
	var found string
	foundOK := false
	for {
		idx := bytes.Index(b[offset:], needle)
		if idx == -1 {
			return found, foundOK
		}
		idx += offset + len(needle)
		for idx < len(b) && isPDFWhitespace(b[idx]) {
			idx++
		}
		if idx >= len(b) {
			return found, foundOK
		}
		if b[idx] == '(' {
			if s, ok := parsePDFLiteralString(b, idx); ok {
				found = s
				foundOK = true
			}
			offset = idx + 1
			continue
		}
		if b[idx] == '<' && idx+1 < len(b) && b[idx+1] != '<' {
			if s, ok := parsePDFHexString(b, idx); ok {
				found = s
				foundOK = true
			}
			offset = idx + 1
			continue
		}
		offset = idx + 1
	}
}

func parsePDFLiteralString(b []byte, start int) (string, bool) {
	depth := 1
	out := make([]byte, 0, 64)
	for i := start + 1; i < len(b); i++ {
		c := b[i]
		switch c {
		case '\\':
			next, consumed, ok := parsePDFEscape(b[i+1:])
			if !ok {
				return "", false
			}
			out = append(out, next...)
			i += consumed
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return decodePDFString(out), true
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return "", false
}

func parsePDFHexString(b []byte, start int) (string, bool) {
	out := make([]byte, 0, 64)
	var high byte
	hasHigh := false
	for i := start + 1; i < len(b); i++ {
		c := b[i]
		if c == '>' {
			if hasHigh {
				out = append(out, high<<4)
			}
			return decodePDFString(out), true
		}
		if isPDFWhitespace(c) {
			continue
		}
		v, ok := pdfHexDigit(c)
		if !ok {
			return "", false
		}
		if hasHigh {
			out = append(out, high<<4|v)
			hasHigh = false
		} else {
			high = v
			hasHigh = true
		}
	}
	return "", false
}

func pdfHexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

func decodePDFString(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return decodePDFUTF16(b[2:], binary.BigEndian)
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return decodePDFUTF16(b[2:], binary.LittleEndian)
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return strings.ToValidUTF8(string(b[3:]), string(utf8.RuneError))
	default:
		return decodePDFDocEncoding(b)
	}
}

func decodePDFUTF16(b []byte, order binary.ByteOrder) string {
	if len(b)%2 == 1 {
		b = b[:len(b)-1]
	}
	units := make([]uint16, 0, len(b)/2)
	for len(b) >= 2 {
		units = append(units, order.Uint16(b[:2]))
		b = b[2:]
	}
	return string(utf16.Decode(units))
}

func decodePDFDocEncoding(b []byte) string {
	var out strings.Builder
	out.Grow(len(b))
	for _, c := range b {
		out.WriteRune(pdfDocEncodingRune(c))
	}
	return out.String()
}

func pdfDocEncodingRune(c byte) rune {
	switch c {
	case 0x18:
		return '\u02d8'
	case 0x19:
		return '\u02c7'
	case 0x1a:
		return '\u02c6'
	case 0x1b:
		return '\u02d9'
	case 0x1c:
		return '\u02dd'
	case 0x1d:
		return '\u02db'
	case 0x1e:
		return '\u02da'
	case 0x1f:
		return '\u02dc'
	case 0x7f, 0x9f, 0xad:
		return utf8.RuneError
	case 0x80:
		return '\u2022'
	case 0x81:
		return '\u2020'
	case 0x82:
		return '\u2021'
	case 0x83:
		return '\u2026'
	case 0x84:
		return '\u2014'
	case 0x85:
		return '\u2013'
	case 0x86:
		return '\u0192'
	case 0x87:
		return '\u2044'
	case 0x88:
		return '\u2039'
	case 0x89:
		return '\u203a'
	case 0x8a:
		return '\u2212'
	case 0x8b:
		return '\u2030'
	case 0x8c:
		return '\u201e'
	case 0x8d:
		return '\u201c'
	case 0x8e:
		return '\u201d'
	case 0x8f:
		return '\u2018'
	case 0x90:
		return '\u2019'
	case 0x91:
		return '\u201a'
	case 0x92:
		return '\u2122'
	case 0x93:
		return '\ufb01'
	case 0x94:
		return '\ufb02'
	case 0x95:
		return '\u0141'
	case 0x96:
		return '\u0152'
	case 0x97:
		return '\u0160'
	case 0x98:
		return '\u0178'
	case 0x99:
		return '\u017d'
	case 0x9a:
		return '\u0131'
	case 0x9b:
		return '\u0142'
	case 0x9c:
		return '\u0153'
	case 0x9d:
		return '\u0161'
	case 0x9e:
		return '\u017e'
	case 0xa0:
		return '\u20ac'
	default:
		return rune(c)
	}
}

const (
	pdfDCNamespace  = "http://purl.org/dc/elements/1.1/"
	pdfRDFNamespace = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"

	maxPDFMetadataStream = 4 << 20
)

func pdfXMPMetadata(b []byte) *Metadata {
	return pdfXMPMetadataPacket(pdfXMPPacket(b))
}

func pdfXMPMetadataPacket(packet []byte) *Metadata {
	if len(packet) == 0 {
		return nil
	}

	decoder := xml.NewDecoder(bytes.NewReader(packet))
	var prop string
	propDepth := 0
	liDepth := 0
	var direct strings.Builder
	var li strings.Builder
	var titles []string
	var creators []string

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := token.(type) {
		case xml.StartElement:
			if propDepth > 0 {
				propDepth++
				if liDepth > 0 {
					liDepth++
				} else if t.Name.Local == "li" && (t.Name.Space == pdfRDFNamespace || t.Name.Space == "") {
					liDepth = 1
					li.Reset()
				}
				continue
			}
			if t.Name.Space != pdfDCNamespace {
				continue
			}
			switch t.Name.Local {
			case "title", "creator":
				prop = t.Name.Local
				propDepth = 1
				direct.Reset()
			}
		case xml.CharData:
			if propDepth == 0 {
				continue
			}
			if liDepth > 0 {
				li.Write([]byte(t))
			} else {
				direct.Write([]byte(t))
			}
		case xml.EndElement:
			if propDepth == 0 {
				continue
			}
			if liDepth > 0 {
				liDepth--
				if liDepth == 0 {
					switch prop {
					case "title":
						titles = appendPDFXMPText(titles, li.String())
					case "creator":
						creators = appendPDFXMPText(creators, li.String())
					}
					li.Reset()
				}
			}
			propDepth--
			if propDepth == 0 {
				switch prop {
				case "title":
					titles = appendPDFXMPText(titles, direct.String())
				case "creator":
					creators = appendPDFXMPText(creators, direct.String())
				}
				prop = ""
			}
		}
	}

	meta := &Metadata{}
	if len(titles) > 0 {
		meta.Title = titles[0]
	}
	for _, creator := range creators {
		meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: creator})
	}
	if meta.Title == "" && len(meta.Authors) == 0 {
		return nil
	}
	return meta
}

func appendPDFXMPText(values []string, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return values
	}
	if slices.Contains(values, text) {
		return values
	}
	return append(values, text)
}

func pdfXMPPacket(b []byte) []byte {
	if packet := pdfXMLPacket(b); len(packet) > 0 {
		return packet
	}
	return pdfXMPPacketFromMetadataStreams(b)
}

func pdfXMPPacketReader(r io.ReaderAt, size int64) []byte {
	for _, localName := range []string{"xmpmeta", "RDF"} {
		if offset, ok := pdfXMLStartReader(r, size, localName); ok {
			window := pdfReadWindow(r, size, offset, maxPDFMetadataStream)
			if packet := pdfXMLPacketByLocalName(window, localName); len(packet) > 0 {
				return packet
			}
		}
	}
	return pdfXMPPacketFromMetadataStreamsReader(r, size)
}

func pdfXMLStartReader(r io.ReaderAt, size int64, localName string) (int64, bool) {
	const xmlNameOverlap = 256
	for offset := int64(0); offset < size; {
		window := pdfReadWindow(r, size, offset, pdfMetadataScanChunk)
		if len(window) == 0 {
			return 0, false
		}
		searchAt := 0
		for {
			idx := bytes.IndexByte(window[searchAt:], '<')
			if idx < 0 {
				break
			}
			idx += searchAt
			if name, ok := pdfXMLStartName(window[idx+1:]); ok && pdfXMLLocalName(name) == localName {
				return offset + int64(idx), true
			}
			searchAt = idx + 1
		}
		step := len(window) - xmlNameOverlap
		if step <= 0 {
			return 0, false
		}
		offset += int64(step)
	}
	return 0, false
}

func pdfXMPPacketFromMetadataStreamsReader(r io.ReaderAt, size int64) []byte {
	needle := []byte("/Type")
	searchAt := int64(0)
	for {
		typeOffset := pdfIndexReader(r, size, needle, searchAt)
		if typeOffset < 0 {
			return nil
		}
		if packet := pdfMetadataStreamPacketAt(r, size, typeOffset); len(packet) > 0 {
			return packet
		}
		searchAt = typeOffset + int64(len(needle))
	}
}

func pdfMetadataStreamPacketAt(r io.ReaderAt, size, typeOffset int64) []byte {
	headerStart := max(typeOffset-maxPDFMetadataDictSize, 0)
	header := pdfReadWindow(r, size, headerStart, maxPDFMetadataDictSize*2)
	typeIndex := int(typeOffset - headerStart)
	if typeIndex < 0 || typeIndex >= len(header) {
		return nil
	}
	dictStart := bytes.LastIndex(header[:typeIndex+1], []byte("<<"))
	if dictStart < 0 {
		return nil
	}
	streamRelative := bytes.Index(header[typeIndex:], []byte("stream"))
	if streamRelative < 0 {
		return nil
	}
	streamIndex := typeIndex + streamRelative
	dict := header[dictStart:streamIndex]
	if !pdfDictNameValueEquals(dict, "Type", "Metadata") || !pdfDictNameValueEquals(dict, "Subtype", "XML") {
		return nil
	}

	dataOffset := headerStart + int64(streamIndex+len("stream"))
	lineStart := pdfReadWindow(r, size, dataOffset, 2)
	if len(lineStart) > 0 && lineStart[0] == '\r' {
		dataOffset++
		if len(lineStart) > 1 && lineStart[1] == '\n' {
			dataOffset++
		}
	} else if len(lineStart) > 0 && lineStart[0] == '\n' {
		dataOffset++
	}

	var streamData []byte
	if length, ok := pdfDictIntValue(dict, "Length"); ok {
		if length < 0 || length > maxPDFMetadataStream {
			return nil
		}
		streamData = pdfReadWindow(r, size, dataOffset, length)
		if len(streamData) != length {
			return nil
		}
	} else {
		window := pdfReadWindow(r, size, dataOffset, maxPDFMetadataStream+len("endstream"))
		before, _, ok := bytes.Cut(window, []byte("endstream"))
		if !ok {
			return nil
		}
		streamData = trimPDFStreamEOL(before)
	}
	decoded, ok := pdfDecodeStream(dict, streamData)
	if !ok {
		return nil
	}
	return pdfXMLPacket(decoded)
}

func pdfXMLPacket(b []byte) []byte {
	if packet := pdfXMLPacketByLocalName(b, "xmpmeta"); len(packet) > 0 {
		return packet
	}
	return pdfXMLPacketByLocalName(b, "RDF")
}

func pdfXMPPacketFromMetadataStreams(b []byte) []byte {
	offset := 0
	for {
		streamIdx := bytes.Index(b[offset:], []byte("stream"))
		if streamIdx == -1 {
			return nil
		}
		streamIdx += offset

		nextOffset := streamIdx + len("stream")
		dictStart := bytes.LastIndex(b[:streamIdx], []byte("<<"))
		if dictStart == -1 {
			offset = nextOffset
			continue
		}
		dict := b[dictStart:streamIdx]
		if !pdfDictNameValueEquals(dict, "Type", "Metadata") || !pdfDictNameValueEquals(dict, "Subtype", "XML") {
			offset = nextOffset
			continue
		}

		streamData, nextOffset, ok := pdfStreamData(b, streamIdx, dict)
		if !ok {
			offset = nextOffset
			continue
		}
		offset = nextOffset

		decoded, ok := pdfDecodeStream(dict, streamData)
		if !ok {
			continue
		}
		if packet := pdfXMLPacket(decoded); len(packet) > 0 {
			return packet
		}
	}
}

func pdfStreamData(b []byte, streamIdx int, dict []byte) ([]byte, int, bool) {
	dataStart := streamIdx + len("stream")
	if dataStart < len(b) && b[dataStart] == '\r' {
		dataStart++
		if dataStart < len(b) && b[dataStart] == '\n' {
			dataStart++
		}
	} else if dataStart < len(b) && b[dataStart] == '\n' {
		dataStart++
	}

	if length, ok := pdfDictIntValue(dict, "Length"); ok && length >= 0 && length <= len(b)-dataStart {
		dataEnd := dataStart + length
		next := dataEnd
		if endstream := bytes.Index(b[dataEnd:], []byte("endstream")); endstream >= 0 {
			next = dataEnd + endstream + len("endstream")
		}
		return b[dataStart:dataEnd], next, true
	}

	endstream := bytes.Index(b[dataStart:], []byte("endstream"))
	if endstream == -1 {
		return nil, dataStart, false
	}
	dataEnd := dataStart + endstream
	return trimPDFStreamEOL(b[dataStart:dataEnd]), dataEnd + len("endstream"), true
}

func trimPDFStreamEOL(b []byte) []byte {
	if len(b) >= 2 && b[len(b)-2] == '\r' && b[len(b)-1] == '\n' {
		return b[:len(b)-2]
	}
	if len(b) > 0 && (b[len(b)-1] == '\r' || b[len(b)-1] == '\n') {
		return b[:len(b)-1]
	}
	return b
}

func pdfDecodeStream(dict, data []byte) ([]byte, bool) {
	if !pdfDictHasName(dict, "Filter") {
		if len(data) > maxPDFMetadataStream {
			return nil, false
		}
		return data, true
	}
	if !pdfDictHasName(dict, "FlateDecode") {
		return nil, false
	}
	if decoded, ok := pdfInflateZlib(data); ok {
		return decoded, true
	}
	return pdfInflateRaw(data)
}

func pdfInflateZlib(data []byte) ([]byte, bool) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	defer r.Close()
	return readPDFMetadataStream(r)
}

func pdfInflateRaw(data []byte) ([]byte, bool) {
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close()
	return readPDFMetadataStream(r)
}

func readPDFMetadataStream(r io.Reader) ([]byte, bool) {
	data, err := io.ReadAll(io.LimitReader(r, maxPDFMetadataStream+1))
	if err != nil || len(data) > maxPDFMetadataStream {
		return nil, false
	}
	return data, true
}

func pdfDictNameValueEquals(dict []byte, key, value string) bool {
	offset := 0
	needle := []byte("/" + key)
	for {
		idx := bytes.Index(dict[offset:], needle)
		if idx == -1 {
			return false
		}
		idx += offset
		nameEnd := idx + len(needle)
		if nameEnd < len(dict) && !isPDFDelimiter(dict[nameEnd]) {
			offset = nameEnd
			continue
		}
		rest := skipPDFWhitespace(dict[nameEnd:])
		if len(rest) == 0 || rest[0] != '/' {
			offset = nameEnd
			continue
		}
		end := 1
		for end < len(rest) && !isPDFDelimiter(rest[end]) {
			end++
		}
		if string(rest[1:end]) == value {
			return true
		}
		offset = nameEnd
	}
}

func pdfDictIntValue(dict []byte, key string) (int, bool) {
	offset := 0
	needle := []byte("/" + key)
	for {
		idx := bytes.Index(dict[offset:], needle)
		if idx == -1 {
			return 0, false
		}
		idx += offset
		nameEnd := idx + len(needle)
		if nameEnd < len(dict) && !isPDFDelimiter(dict[nameEnd]) {
			offset = nameEnd
			continue
		}
		if value, _, ok := readPDFInt(dict[nameEnd:]); ok {
			return value, true
		}
		offset = nameEnd
	}
}

func pdfDictHasName(dict []byte, name string) bool {
	offset := 0
	needle := []byte("/" + name)
	for {
		idx := bytes.Index(dict[offset:], needle)
		if idx == -1 {
			return false
		}
		idx += offset
		nameEnd := idx + len(needle)
		if nameEnd == len(dict) || isPDFDelimiter(dict[nameEnd]) {
			return true
		}
		offset = nameEnd
	}
}

func isPDFDelimiter(b byte) bool {
	if isPDFWhitespace(b) {
		return true
	}
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func pdfXMLPacketByLocalName(b []byte, localName string) []byte {
	offset := 0
	for {
		idx := bytes.IndexByte(b[offset:], '<')
		if idx == -1 {
			return nil
		}
		idx += offset

		name, ok := pdfXMLStartName(b[idx+1:])
		if !ok {
			offset = idx + 1
			continue
		}
		if pdfXMLLocalName(name) != localName {
			offset = idx + 1
			continue
		}

		tagEnd := bytes.IndexByte(b[idx:], '>')
		if tagEnd == -1 {
			return nil
		}
		tagEnd += idx
		if tagEnd > idx && b[tagEnd-1] == '/' {
			return b[idx : tagEnd+1]
		}

		endTag := []byte("</" + name + ">")
		searchStart := tagEnd + 1
		end := bytes.Index(b[searchStart:], endTag)
		if end == -1 {
			return nil
		}
		return b[idx : searchStart+end+len(endTag)]
	}
}

func pdfXMLStartName(b []byte) (string, bool) {
	if len(b) == 0 || b[0] == '/' || b[0] == '?' || b[0] == '!' {
		return "", false
	}
	end := 0
	for end < len(b) {
		switch b[end] {
		case ' ', '\t', '\n', '\r', '/', '>':
			if end == 0 {
				return "", false
			}
			return string(b[:end]), true
		}
		end++
	}
	return "", false
}

func pdfXMLLocalName(name string) string {
	if _, after, ok := strings.CutLast(name, ":"); ok {
		return after
	}
	return name
}

func parsePDFEscape(b []byte) ([]byte, int, bool) {
	if len(b) == 0 {
		return nil, 0, false
	}
	switch b[0] {
	case 'n':
		return []byte{'\n'}, 1, true
	case 'r':
		return []byte{'\r'}, 1, true
	case 't':
		return []byte{'\t'}, 1, true
	case 'b':
		return []byte{'\b'}, 1, true
	case 'f':
		return []byte{'\f'}, 1, true
	case '(', ')', '\\':
		return []byte{b[0]}, 1, true
	case '\r':
		if len(b) > 1 && b[1] == '\n' {
			return nil, 2, true
		}
		return nil, 1, true
	case '\n':
		return nil, 1, true
	}

	if b[0] < '0' || b[0] > '7' {
		return []byte{b[0]}, 1, true
	}
	value := byte(0)
	consumed := 0
	for consumed < len(b) && consumed < 3 && b[consumed] >= '0' && b[consumed] <= '7' {
		value = value*8 + (b[consumed] - '0')
		consumed++
	}
	return []byte{value}, consumed, true
}

func isPDFWhitespace(b byte) bool {
	switch b {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}
