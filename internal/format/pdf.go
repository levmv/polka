package format

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"io"
	"iter"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/levmv/polka/internal/bookmeta"
)

// ExtractPDFMetadata reads Title, Author and the native page count.
// Unsupported or unreadable values remain empty; see ExtractPDFMetadataReader.
func ExtractPDFMetadata(b []byte) *Metadata {
	return ExtractPDFMetadataReader(bytes.NewReader(b), int64(len(b)))
}

// ExtractPDFMetadataReader resolves current Info, XMP and Pages objects through
// ordinary xref tables. If that structure is unavailable, a scan with bounded
// buffers recovers metadata from uncompressed objects. Page-count fallback to
// PDFium belongs to the caller.
func ExtractPDFMetadataReader(r io.ReaderAt, size int64) *Metadata {
	meta := &Metadata{}
	if r == nil || size <= 0 {
		return meta
	}
	if structure := openPDFStructure(r, size); structure != nil {
		meta.PageCount = structure.pageCount()
		structure.fillMetadata(meta)
		return meta
	}

	if !pdfHasEncryptReferenceReader(r, size) {
		if info := pdfInfoObjectReader(r, size); len(info) > 0 {
			pdfFillInfoMetadata(meta, func(key string) (string, bool) {
				return pdfInfoString(info, key)
			})
		} else {
			pdfFillInfoMetadata(meta, func(key string) (string, bool) {
				return pdfInfoStringReader(r, size, key)
			})
		}
	}

	if meta.Title == "" || len(meta.Authors) == 0 {
		pdfFillMissingMetadata(meta, metadataFromPDFXMP(pdfXMPPacketReader(r, size)))
	}
	return meta
}

func pdfFillMissingMetadata(meta, fallback *Metadata) {
	if fallback == nil {
		return
	}
	if meta.Title == "" {
		meta.Title = fallback.Title
	}
	if len(meta.Authors) == 0 {
		meta.Authors = fallback.Authors
	}
}

func pdfFillInfoMetadata(meta *Metadata, readString func(string) (string, bool)) {
	if title, ok := readString("Title"); ok {
		meta.Title = title
	}
	if author, ok := readString("Author"); ok {
		meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
	}
}

const (
	pdfMetadataScanChunk   = 256 << 10
	maxPDFInfoObjectBytes  = 1 << 20
	maxPDFInfoStringBytes  = 64 << 10
	maxPDFMetadataDictSize = 64 << 10
)

func pdfHasEncryptReferenceReader(r io.ReaderAt, size int64) bool {
	for idx := range pdfTokenOffsetsReader(r, size, []byte("/Encrypt"), true) {
		ref := pdfReadWindow(r, size, idx+int64(len("/Encrypt")), 128)
		if _, _, ok := pdfReferencePrefix(ref); ok {
			return true
		}
	}
	return false
}

func pdfInfoObjectReader(r io.ReaderAt, size int64) []byte {
	for idx := range pdfTokenOffsetsReader(r, size, []byte("/Info"), true) {
		ref := pdfReadWindow(r, size, idx+int64(len("/Info")), 128)
		if obj, gen, ok := pdfReferencePrefix(ref); ok {
			return pdfIndirectObjectReader(r, size, obj, gen)
		}
	}
	return nil
}

func pdfIndirectObjectReader(r io.ReaderAt, size, obj, gen int64) []byte {
	marker := []byte(strconv.FormatInt(obj, 10) + " " + strconv.FormatInt(gen, 10) + " obj")
	for idx := range pdfTokenOffsetsReader(r, size, marker, false) {
		if idx == 0 || isPDFWhitespace(pdfByteAt(r, idx-1)) {
			start := idx + int64(len(marker))
			object := pdfReadWindow(r, size, start, maxPDFInfoObjectBytes)
			if before, _, ok := bytes.Cut(object, []byte("endobj")); ok {
				return before
			}
			return object
		}
	}
	return nil
}

func pdfInfoStringReader(r io.ReaderAt, size int64, key string) (string, bool) {
	needle := []byte("/" + key)
	for idx := range pdfTokenOffsetsReader(r, size, needle, false) {
		window := pdfReadWindow(r, size, idx, maxPDFInfoStringBytes)
		if value, ok := pdfInfoString(window, key); ok {
			return value, true
		}
	}
	return "", false
}

// Scan literal token endings in either direction, reusing each window.
// Matches inside strings or streams are still candidates for callers to check.
// The delimiter check rejects prefixes of longer names.
func pdfTokenOffsetsReader(r io.ReaderAt, size int64, needle []byte, reverse bool) iter.Seq[int64] {
	return func(yield func(int64) bool) {
		buffer := make([]byte, pdfMetadataScanChunk)
		overlap := int64(len(needle) - 1)
		for start, end := int64(0), size; start < end; {
			offset := start
			if reverse {
				offset = max(end-int64(len(buffer)), start)
			}
			window := buffer[:min(int64(len(buffer)), end-offset)]
			n, err := r.ReadAt(window, offset)
			if err != nil && err != io.EOF {
				return
			}
			window = window[:n]
			for lo, hi := 0, n; lo < hi; {
				var index int
				if reverse {
					index = bytes.LastIndex(window[lo:hi], needle)
				} else {
					index = bytes.Index(window[lo:hi], needle)
				}
				if index < 0 {
					break
				}
				index += lo
				nameEnd := index + len(needle)
				var next byte
				if nameEnd < n {
					next = window[nameEnd]
				} else if offset+int64(nameEnd) < size {
					next = pdfByteAt(r, offset+int64(nameEnd))
				}
				if isPDFDelimiter(next) && !yield(offset+int64(index)) {
					return
				}
				if reverse {
					hi = index
				} else {
					lo = nameEnd
				}
			}
			if int64(n) <= overlap || reverse && offset == 0 {
				return
			}
			if reverse {
				end = offset + overlap
			} else {
				start = offset + int64(n) - overlap
			}
		}
	}
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

func pdfReferencePrefix(b []byte) (int64, int64, bool) {
	p := pdfSyntax{data: b}
	if !p.skipValue(0) {
		return 0, 0, false
	}
	return pdfReference(b[:p.pos])
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
		if s, ok := pdfTextString(b[idx:]); ok {
			found = s
			foundOK = true
		}
		offset = idx + 1
	}
}

func pdfTextString(b []byte) (string, bool) {
	p := pdfSyntax{data: b}
	raw := p.next()
	if len(raw) == 0 {
		return "", false
	}
	var text string
	var ok bool
	if raw[0] == '(' {
		text, ok = parsePDFLiteralString(raw, 0)
	} else if raw[0] == '<' && !bytes.HasPrefix(raw, []byte("<<")) {
		text, ok = parsePDFHexString(raw, 0)
	}
	return text, ok && strings.TrimSpace(text) != ""
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
	var decoded string
	switch {
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		decoded = decodePDFUTF16(b[2:], binary.BigEndian)
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		decoded = decodePDFUTF16(b[2:], binary.LittleEndian)
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		decoded = strings.ToValidUTF8(string(b[3:]), string(utf8.RuneError))
	default:
		decoded = decodePDFDocEncoding(b)
	}
	return decodeLegacyHexWrappedText(decoded)
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

func metadataFromPDFXMP(packet []byte) *Metadata {
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
	text = decodeLegacyHexWrappedText(strings.TrimSpace(text))
	if text == "" {
		return values
	}
	if slices.Contains(values, text) {
		return values
	}
	return append(values, text)
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
	buffer := make([]byte, pdfMetadataScanChunk)
	for offset := int64(0); offset < size; {
		window := buffer[:min(int64(len(buffer)), size-offset)]
		n, err := r.ReadAt(window, offset)
		if n == 0 || err != nil && err != io.EOF {
			return 0, false
		}
		window = window[:n]
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
	// /Type occurs in almost every page and resource dictionary. Look for the
	// much narrower Metadata name before reading a candidate stream header.
	for offset := range pdfTokenOffsetsReader(r, size, []byte("/Metadata"), false) {
		if packet := pdfMetadataStreamPacketAt(r, size, offset); len(packet) > 0 {
			return packet
		}
	}
	return nil
}

func pdfMetadataStreamPacketAt(r io.ReaderAt, size, metadataOffset int64) []byte {
	headerStart := max(metadataOffset-maxPDFMetadataDictSize, 0)
	header := pdfReadWindow(r, size, headerStart, maxPDFMetadataDictSize*2)
	metadataIndex := int(metadataOffset - headerStart)
	if metadataIndex < 0 || metadataIndex >= len(header) {
		return nil
	}
	dictStart := bytes.LastIndex(header[:metadataIndex+1], []byte("<<"))
	if dictStart < 0 {
		return nil
	}
	parser := pdfSyntax{data: header, pos: dictStart}
	dict := parser.dictionary()
	if dict == nil || string(parser.next()) != "stream" || !parser.lineEnd() {
		return nil
	}
	return pdfMetadataStreamPacket(r, size, pdfObject{dict: dict, streamOffset: headerStart + int64(parser.pos)})
}

func pdfMetadataStreamPacket(r io.ReaderAt, size int64, object pdfObject) []byte {
	if object.streamOffset == 0 || pdfName(object.dict["Type"]) != "Metadata" || pdfName(object.dict["Subtype"]) != "XML" {
		return nil
	}
	// XMP has its own compressed and decoded size limit, separate from the
	// small xref/object lookup budget. Recover an indirect or incorrect Length
	// within this stream without searching unrelated objects in the file.
	if length, ok := pdfInteger(object.dict["Length"]); ok && length > 0 && length <= maxPDFMetadataStream && length <= size-object.streamOffset {
		data := pdfReadWindow(r, size, object.streamOffset, int(length))
		if packet := pdfDecodeXMPStream(object.dict, data); len(packet) > 0 {
			return packet
		}
	}
	// Most XMP packets are small. Grow the buffer as needed instead of reading
	// the full limit for every stream whose Length is an indirect reference.
	scanner := bufio.NewScanner(io.NewSectionReader(r, object.streamOffset, size-object.streamOffset))
	scanner.Buffer(nil, maxPDFMetadataStream+len("endstream"))
	scanner.Split(func(data []byte, _ bool) (int, []byte, error) {
		if end := bytes.Index(data, []byte("endstream")); end >= 0 {
			return end + len("endstream"), data[:end], bufio.ErrFinalToken
		}
		return 0, nil, nil
	})
	if scanner.Scan() {
		return pdfDecodeXMPStream(object.dict, trimPDFStreamEOL(scanner.Bytes()))
	}
	return nil
}

func pdfXMLPacket(b []byte) []byte {
	if packet := pdfXMLPacketByLocalName(b, "xmpmeta"); len(packet) > 0 {
		return packet
	}
	return pdfXMLPacketByLocalName(b, "RDF")
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

func pdfDecodeXMPStream(dict pdfDictionary, data []byte) []byte {
	if len(data) > maxPDFMetadataStream {
		return nil
	}
	if dict["Filter"] == nil {
		return pdfXMLPacket(data)
	}
	parser := pdfSyntax{data: dict["Filter"]}
	name := parser.next()
	if string(name) == "[" {
		name = parser.next()
		if string(parser.next()) != "]" {
			return nil
		}
	}
	if pdfName(name) != "FlateDecode" || len(parser.next()) != 0 {
		return nil
	}
	if decoded, ok := pdfInflateZlib(data); ok {
		return pdfXMLPacket(decoded)
	}
	if decoded, ok := pdfInflateRaw(data); ok {
		return pdfXMLPacket(decoded)
	}
	return nil
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
