package format

import (
	"bytes"
	"io"
	"strconv"
)

const (
	maxPDFStructureReadBytes = 2 << 20
	maxPDFObjectWindow       = 64 << 10
	maxPDFXRefRevisions      = 32
	maxPDFXRefSubsections    = 1024
)

// pdfStructure resolves ordinary xref tables and uncompressed objects within
// a read budget. Newer revisions override older entries, including deletions.
// Unsupported structures leave page counting to PDFium; scanning for /Count
// cannot distinguish the current page tree from obsolete objects or outlines.
type pdfStructure struct {
	r              io.ReaderAt
	size           int64
	remainingBytes int
	revisions      [][]pdfXRefSubsection // Newest first, including free entries.
	trailer        pdfDictionary
	catalog        pdfDictionary
}

type pdfXRefSubsection struct {
	firstObject, count int64
	entriesOffset      int64
}

type pdfDictionary map[string][]byte

type pdfObject struct {
	dict         pdfDictionary
	streamOffset int64 // Start of stream data in the file; zero for a plain dictionary.
}

func openPDFStructure(r io.ReaderAt, size int64) *pdfStructure {
	if r == nil || size <= 0 {
		return nil
	}
	p := &pdfStructure{r: r, size: size, remainingBytes: maxPDFStructureReadBytes}
	if !bytes.HasPrefix(p.read(0, 16), []byte("%PDF-")) {
		return nil
	}
	tail := bytes.TrimRightFunc(p.read(max(size-8192, 0), 8192), func(c rune) bool {
		return c <= 255 && isPDFWhitespace(byte(c))
	})
	if !bytes.HasSuffix(tail, []byte("%%EOF")) {
		return nil
	}
	tail = tail[:len(tail)-5]
	index := bytes.LastIndex(tail, []byte("startxref"))
	if index < 0 {
		return nil
	}
	offset, ok := pdfInteger(tail[index+len("startxref"):])
	if !ok || offset <= 0 {
		return nil
	}
	visited := make(map[int64]bool)
	for len(visited) < maxPDFXRefRevisions {
		if visited[offset] {
			return nil
		}
		visited[offset] = true
		subsections, trailer := p.readXRef(offset)
		// Hybrid xrefs can override table entries with compressed objects.
		if trailer == nil || trailer["Encrypt"] != nil || trailer["XRefStm"] != nil {
			return nil
		}
		if p.trailer == nil {
			p.trailer = trailer
		}
		p.revisions = append(p.revisions, subsections)
		if trailer["Prev"] == nil {
			root, ok := p.resolveObject(p.trailer["Root"], maxPDFObjectWindow)
			if !ok || root.streamOffset != 0 || pdfName(root.dict["Type"]) != "Catalog" {
				return nil
			}
			p.catalog = root.dict
			return p
		}
		offset, ok = pdfInteger(trailer["Prev"])
		if !ok || offset <= 0 {
			return nil
		}
	}
	return nil
}

func (p *pdfStructure) read(offset int64, limit int) []byte {
	if offset < 0 || offset >= p.size || limit <= 0 {
		return nil
	}
	limit = int(min(int64(limit), p.size-offset))
	if limit > p.remainingBytes {
		return nil
	}
	p.remainingBytes -= limit
	return pdfReadWindow(p.r, p.size, offset, limit)
}

func (p *pdfStructure) readXRef(offset int64) ([]pdfXRefSubsection, pdfDictionary) {
	head := pdfSyntax{data: p.read(offset, 1024)}
	if string(head.next()) != "xref" {
		return nil, nil
	}
	offset += int64(head.pos)
	var subsections []pdfXRefSubsection
	for len(subsections) < maxPDFXRefSubsections {
		head = pdfSyntax{data: p.read(offset, 1024)}
		token := head.next()
		if string(token) == "trailer" {
			dict := pdfSyntax{data: p.read(offset+int64(head.pos), maxPDFObjectWindow)}
			return subsections, dict.dictionary()
		}
		first, ok := pdfInteger(token)
		if !ok || first < 0 {
			return nil, nil
		}
		count, ok := pdfInteger(head.next())
		if !ok || count < 0 {
			return nil, nil
		}
		// Classic xref entries occupy exactly 20 bytes. Skip their body and
		// later read only the entries for the objects we actually need.
		for head.pos < len(head.data) && (head.data[head.pos] == ' ' || head.data[head.pos] == '\t') {
			head.pos++
		}
		if !head.lineEnd() {
			return nil, nil
		}
		start := offset + int64(head.pos)
		if count > (p.size-start)/20 || first > 1<<31 || count > 1<<31-first {
			return nil, nil
		}
		subsections = append(subsections, pdfXRefSubsection{firstObject: first, count: count, entriesOffset: start})
		offset = start + count*20
	}
	return nil, nil
}

func (p *pdfStructure) resolveObject(ref []byte, limit int) (pdfObject, bool) {
	obj, gen, ok := pdfReference(ref)
	if !ok {
		return pdfObject{}, false
	}
	for _, revision := range p.revisions {
		var entry []byte
		found := false
		for _, subsection := range revision {
			if obj >= subsection.firstObject && obj-subsection.firstObject < subsection.count {
				if found { // Overlapping subsections are ambiguous.
					return pdfObject{}, false
				}
				found = true
				entry = p.read(subsection.entriesOffset+(obj-subsection.firstObject)*20, 20)
			}
		}
		if !found {
			continue
		}
		if len(entry) != 20 || entry[10] != ' ' || entry[16] != ' ' || entry[17] != 'n' ||
			!(entry[18] == ' ' && (entry[19] == '\r' || entry[19] == '\n') || entry[18] == '\r' && entry[19] == '\n') {
			return pdfObject{}, false // A free entry must hide older revisions.
		}
		offset, ok := pdfInteger(entry[:10])
		entryGen, genOK := pdfInteger(entry[11:16])
		if !ok || !genOK || entryGen != gen {
			return pdfObject{}, false
		}
		parser := pdfSyntax{data: p.read(offset, limit)}
		objectID, idOK := pdfInteger(parser.next())
		generation, generationOK := pdfInteger(parser.next())
		if !idOK || !generationOK || objectID != obj || generation != gen || string(parser.next()) != "obj" {
			return pdfObject{}, false
		}
		dict := parser.dictionary()
		if dict == nil {
			return pdfObject{}, false
		}
		switch string(parser.next()) {
		case "endobj":
			return pdfObject{dict: dict}, true
		case "stream":
			if parser.lineEnd() {
				return pdfObject{dict: dict, streamOffset: offset + int64(parser.pos)}, true
			}
		}
		return pdfObject{}, false
	}
	return pdfObject{}, false
}

func (p *pdfStructure) pageCount() int {
	pages, ok := p.resolveObject(p.catalog["Pages"], maxPDFObjectWindow)
	if !ok || pages.streamOffset != 0 || pdfName(pages.dict["Type"]) != "Pages" || pages.dict["Parent"] != nil {
		return 0
	}
	count, ok := pdfInteger(pages.dict["Count"])
	if !ok || count <= 0 || count >= 0xFFFFF { // PDFium's page-count limit.
		return 0
	}
	kids := pdfSyntax{data: pages.dict["Kids"]}
	if string(kids.next()) != "[" {
		return 0
	}
	// Like PDFium, trust the current root's declared count. Kids may be
	// subtrees, so their number is only a lower bound on the page count.
	childCount := int64(0)
	for {
		start := kids.pos
		if string(kids.next()) == "]" {
			if childCount > 0 && childCount <= count && len(kids.next()) == 0 {
				return int(count)
			}
			return 0
		}
		kids.pos = start
		if !kids.skipValue(0) {
			return 0
		}
		if _, _, ok := pdfReference(kids.data[start:kids.pos]); !ok {
			return 0
		}
		childCount++
	}
}

// Once xref identifies the current objects, keep all recovery within them.
// A whole-file scan could restore deleted Info fields or pick up another
// object's XMP, such as metadata attached to an embedded illustration.
func (p *pdfStructure) fillMetadata(meta *Metadata) {
	info, ok := p.resolveObject(p.trailer["Info"], maxPDFObjectWindow)
	if !ok {
		// Retry large Info dictionaries without enlarging every object read.
		info, ok = p.resolveObject(p.trailer["Info"], maxPDFInfoObjectBytes)
	}
	if ok && info.streamOffset == 0 {
		pdfFillInfoMetadata(meta, func(key string) (string, bool) {
			return pdfTextString(info.dict[key])
		})
	}
	if meta.Title == "" || len(meta.Authors) == 0 {
		if object, ok := p.resolveObject(p.catalog["Metadata"], maxPDFObjectWindow); ok {
			packet := pdfMetadataStreamPacket(p.r, p.size, object)
			pdfFillMissingMetadata(meta, metadataFromPDFXMP(packet))
		}
	}
}

func pdfInteger(b []byte) (int64, bool) {
	p := pdfSyntax{data: b}
	b = p.next()
	if len(p.next()) != 0 {
		return 0, false
	}
	if len(b) == 0 {
		return 0, false
	}
	for i, c := range b {
		if (c < '0' || c > '9') && !(i == 0 && (c == '+' || c == '-')) {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	return n, err == nil
}

func pdfReference(b []byte) (int64, int64, bool) {
	p := pdfSyntax{data: b}
	obj, objOK := pdfInteger(p.next())
	gen, genOK := pdfInteger(p.next())
	return obj, gen, objOK && genOK && obj > 0 && gen >= 0 && gen <= 65535 && string(p.next()) == "R" && len(p.next()) == 0
}

func pdfName(b []byte) string {
	p := pdfSyntax{data: b}
	b = p.next()
	if len(p.next()) != 0 {
		return ""
	}
	if len(b) < 2 || b[0] != '/' {
		return ""
	}
	name := make([]byte, 0, len(b)-1)
	for i := 1; i < len(b); i++ {
		if b[i] == '#' {
			if i+2 >= len(b) {
				return ""
			}
			hi, hiOK := pdfHexDigit(b[i+1])
			lo, loOK := pdfHexDigit(b[i+2])
			if !hiOK || !loOK {
				return ""
			}
			name = append(name, hi<<4|lo)
			i += 2
		} else {
			name = append(name, b[i])
		}
	}
	return string(name)
}

// pdfSyntax tokenizes bounded object windows. Dictionaries keep raw value
// slices; nested values are skipped. Strings and comments cannot introduce
// structural keys.
type pdfSyntax struct {
	data []byte
	pos  int
}

func (p *pdfSyntax) next() []byte {
	for p.pos < len(p.data) {
		if isPDFWhitespace(p.data[p.pos]) {
			p.pos++
		} else if p.data[p.pos] == '%' {
			for p.pos < len(p.data) && p.data[p.pos] != '\r' && p.data[p.pos] != '\n' {
				p.pos++
			}
		} else {
			break
		}
	}
	start := p.pos
	if start == len(p.data) {
		return nil
	}
	p.pos++
	switch p.data[start] {
	case '(':
		depth := 1
		for p.pos < len(p.data) {
			c := p.data[p.pos]
			p.pos++
			switch c {
			case '\\':
				if p.pos < len(p.data) {
					p.pos++
				}
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return p.data[start:p.pos]
				}
			}
		}
		return nil
	case '<':
		if p.pos < len(p.data) && p.data[p.pos] == '<' {
			p.pos++
			break
		}
		for p.pos < len(p.data) {
			c := p.data[p.pos]
			p.pos++
			if c == '>' {
				return p.data[start:p.pos]
			}
			if _, ok := pdfHexDigit(c); !ok && !isPDFWhitespace(c) {
				return nil
			}
		}
		return nil
	case '>':
		if p.pos < len(p.data) && p.data[p.pos] == '>' {
			p.pos++
		}
	case '[', ']', ')', '{', '}':
	default:
		for p.pos < len(p.data) && !isPDFDelimiter(p.data[p.pos]) {
			p.pos++
		}
	}
	return p.data[start:p.pos]
}

func (p *pdfSyntax) lineEnd() bool {
	if p.pos >= len(p.data) {
		return false
	}
	if p.data[p.pos] == '\r' {
		p.pos++
		if p.pos < len(p.data) && p.data[p.pos] == '\n' {
			p.pos++
		}
		return true
	}
	if p.data[p.pos] == '\n' {
		p.pos++
		return true
	}
	return false
}

func (p *pdfSyntax) dictionary() pdfDictionary {
	if string(p.next()) != "<<" {
		return nil
	}
	dict := make(pdfDictionary)
	for {
		key := p.next()
		if string(key) == ">>" {
			return dict
		}
		name := pdfName(key)
		if name == "" || dict[name] != nil {
			return nil
		}
		start := p.pos
		if !p.skipValue(0) {
			return nil
		}
		dict[name] = bytes.TrimSpace(p.data[start:p.pos])
	}
}

func (p *pdfSyntax) skipValue(depth int) bool {
	if depth > 32 {
		return false
	}
	token := p.next()
	if len(token) == 0 {
		return false
	}
	switch string(token) {
	case "<<", "[":
		dict := string(token) == "<<"
		for {
			start := p.pos
			next := p.next()
			if dict && string(next) == ">>" || !dict && string(next) == "]" {
				return true
			}
			if dict {
				if pdfName(next) == "" {
					return false
				}
			} else {
				p.pos = start
			}
			if !p.skipValue(depth + 1) {
				return false
			}
		}
	case "true", "false", "null":
		return true
	}
	if token[0] == '/' {
		return pdfName(token) != ""
	}
	if token[0] == '(' || token[0] == '<' {
		return true
	}
	if _, ok := pdfInteger(token); ok {
		end := p.pos
		if _, ok := pdfInteger(p.next()); ok && string(p.next()) == "R" {
			return true
		}
		p.pos = end
		return true
	}
	// PDF reals have a decimal point and no exponent.
	if bytes.Count(token, []byte(".")) != 1 {
		return false
	}
	_, ok := pdfInteger(bytes.ReplaceAll(token, []byte("."), nil))
	return ok
}
