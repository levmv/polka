package format

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/htmlindex"

	"github.com/levmv/polka/internal/bookmeta"
)

// FB2 write-back rewrites the <description> element in place and leaves the
// <body> and <binary> blobs byte-untouched. Polka owns <title-info> and
// <publish-info>, so those are regenerated from the durable metadata snapshot;
// the description children Polka does not model (<src-title-info>,
// <document-info>, <custom-info>, and any unknown children) are preserved
// verbatim, mirroring how EPUB write-back keeps foreign OPF records. The
// existing cover reference (title-info/coverpage) is carried through so the
// embedded cover binary stays linked. This renderer never adds or replaces
// cover bytes.

var (
	fb2XMLDeclRe     = regexp.MustCompile(`(?is)<\?xml\b[^>]*\?>`)
	fb2EncodingAttr  = regexp.MustCompile(`(?is)encoding\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	fb2ImageTagRe    = regexp.MustCompile(`(?is)<\s*image\b[^>]*?/?>`)
	fb2ZipMagicBytes = []byte("PK\x03\x04")
	fb2GzipMagic     = []byte{0x1f, 0x8b}
)

// RewriteFB2Metadata renders FB2 bytes with the supplied metadata written into
// the description. It performs no filesystem or database work.
func RewriteFB2Metadata(src []byte, meta Metadata) ([]byte, error) {
	var out bytes.Buffer
	if err := RewriteFB2MetadataTo(&out, bytes.NewReader(src), int64(len(src)), meta); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// RewriteFB2MetadataTo streams an FB2 document (plain, .fb2.zip, or .fb2.gz)
// with the supplied metadata written into the description. The container is
// detected from the leading magic bytes, not the extension, so a zip-in-.fb2
// still repacks correctly. FB2 carries no separate cover write-back path.
func RewriteFB2MetadataTo(w io.Writer, src io.ReaderAt, size int64, meta Metadata) error {
	magic := make([]byte, 4)
	n, _ := src.ReadAt(magic, 0)
	magic = magic[:n]
	switch {
	case bytes.HasPrefix(magic, fb2ZipMagicBytes):
		return rewriteFB2ZipTo(w, src, size, meta)
	case bytes.HasPrefix(magic, fb2GzipMagic):
		return rewriteFB2GzipTo(w, src, size, meta)
	default:
		raw, err := readFB2Raw(src, size)
		if err != nil {
			return err
		}
		out, err := rewriteFB2XMLBytes(raw, meta)
		if err != nil {
			return err
		}
		_, err = w.Write(out)
		return err
	}
}

func readFB2Raw(src io.ReaderAt, size int64) ([]byte, error) {
	if size < 0 || size > maxFB2DocumentBytes {
		return nil, fmt.Errorf("fb2 write-back: document size %d out of range", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(src, 0, size), buf); err != nil {
		return nil, fmt.Errorf("fb2 write-back: read document: %w", err)
	}
	return buf, nil
}

func rewriteFB2ZipTo(w io.Writer, src io.ReaderAt, size int64, meta Metadata) error {
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return fmt.Errorf("fb2 write-back: open zip: %w", err)
	}
	entry, err := SingleFB2ZipEntry(zr)
	if err != nil {
		return fmt.Errorf("fb2 write-back: %w", err)
	}
	rawInner, err := readZipFileLimited(entry, maxFB2DocumentBytes)
	if err != nil {
		return fmt.Errorf("fb2 write-back: read zip entry: %w", err)
	}
	outInner, err := rewriteFB2XMLBytes(rawInner, meta)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)
	if zr.Comment != "" {
		if err := zw.SetComment(zr.Comment); err != nil {
			zw.Close()
			return fmt.Errorf("fb2 write-back: copy zip comment: %w", err)
		}
	}
	replaced := false
	for _, f := range zr.File {
		if f.Name == entry.Name {
			if err := writeZipEntryData(zw, f, outInner); err != nil {
				zw.Close()
				return err
			}
			replaced = true
			continue
		}
		if err := copyZipEntryRaw(zw, f); err != nil {
			zw.Close()
			return err
		}
	}
	if !replaced {
		zw.Close()
		return fmt.Errorf("fb2 write-back: zip entry %s not found on repack", entry.Name)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("fb2 write-back: close zip: %w", err)
	}
	return nil
}

func rewriteFB2GzipTo(w io.Writer, src io.ReaderAt, size int64, meta Metadata) error {
	if size < 0 {
		return fmt.Errorf("fb2 write-back: gzip size is invalid")
	}
	gr, err := gzip.NewReader(io.NewSectionReader(src, 0, size))
	if err != nil {
		return fmt.Errorf("fb2 write-back: open gzip: %w", err)
	}
	defer gr.Close()
	rawInner, err := readAllLimited(gr, "FB2 document", maxFB2DocumentBytes)
	if err != nil {
		return fmt.Errorf("fb2 write-back: read gzip: %w", err)
	}
	outInner, err := rewriteFB2XMLBytes(rawInner, meta)
	if err != nil {
		return err
	}
	gw := gzip.NewWriter(w)
	// Preserve the original gzip header so repeated passes stay byte-identical.
	gw.Header = gzip.Header{
		Name:    gr.Name,
		Comment: gr.Comment,
		Extra:   gr.Extra,
		ModTime: gr.ModTime,
		OS:      gr.OS,
	}
	if _, err := gw.Write(outInner); err != nil {
		gw.Close()
		return fmt.Errorf("fb2 write-back: write gzip: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("fb2 write-back: close gzip: %w", err)
	}
	return nil
}

type fb2Span struct{ start, end int }

type fb2CoverRef struct {
	attrName string
	href     string
}

// fb2Segment is one direct child of <description> in the rewritten output:
// either freshly generated Polka-owned XML (gen, UTF-8) or a preserved raw
// region carried verbatim from the source (raw, source encoding).
type fb2Segment struct {
	gen string
	raw []byte
}

// rewriteFB2XMLBytes replaces the <description> element of a single FB2 XML
// document with metadata from meta, keeping every other byte of the document
// intact. See the file header for the ownership and encoding rules.
func rewriteFB2XMLBytes(raw []byte, meta bookmeta.Metadata) ([]byte, error) {
	open, closeTag, ok := locateFB2Description(raw)
	if !ok {
		return nil, fmt.Errorf("fb2 write-back: <description> element not found")
	}
	inner := raw[open.end:closeTag.start]

	var srcTitle, docInfo []byte
	var customInfos, others [][]byte
	var cover fb2CoverRef
	titleSeen := false
	for _, c := range scanFB2DirectChildren(inner) {
		region := inner[c.start:c.end]
		switch c.local {
		case "title-info":
			if !titleSeen {
				cover = extractFB2CoverRef(region)
				titleSeen = true
			}
		case "publish-info":
			// Regenerated from the snapshot below.
		case "src-title-info":
			if srcTitle == nil {
				srcTitle = region
			}
		case "document-info":
			if docInfo == nil {
				docInfo = region
			}
		case "custom-info":
			customInfos = append(customInfos, region)
		default:
			others = append(others, region)
		}
	}

	newline := opfLineEnding(raw)
	childIndent := opfChildIndent(inner)
	endIndent := opfEndIndent(inner)

	segments := []fb2Segment{{gen: buildFB2TitleInfo(meta, cover, newline, childIndent)}}
	if srcTitle != nil {
		segments = append(segments, fb2Segment{raw: srcTitle})
	}
	if docInfo != nil {
		segments = append(segments, fb2Segment{raw: docInfo})
	}
	if publish := buildFB2PublishInfo(meta, newline, childIndent); publish != "" {
		segments = append(segments, fb2Segment{gen: publish})
	}
	for _, ci := range customInfos {
		segments = append(segments, fb2Segment{raw: ci})
	}
	for _, o := range others {
		segments = append(segments, fb2Segment{raw: o})
	}

	identity := func(r []byte) ([]byte, error) { return r, nil }
	genBytes := func(g string) ([]byte, error) { return []byte(g), nil }

	label := fb2DeclaredEncoding(raw)
	if fb2IsUTF8Label(label) {
		innerBytes, err := assembleFB2Inner(segments, newline, childIndent, endIndent, genBytes, identity)
		if err != nil {
			return nil, err
		}
		return spliceFB2(raw, open.end, closeTag.start, innerBytes), nil
	}

	enc, err := htmlindex.Get(label)
	if err != nil {
		return nil, fmt.Errorf("fb2 write-back: unsupported declared encoding %q: %w", label, err)
	}

	// Prefer keeping the declared legacy encoding; encode the generated parts
	// and splice them alongside the still-encoded preserved regions.
	encodeLegacy := func(g string) ([]byte, error) {
		encoded, err := enc.NewEncoder().Bytes([]byte(g))
		if err != nil {
			return nil, err
		}
		// Guard against encoders that substitute rather than error on an
		// unmappable rune: a value only counts as representable if it round-trips.
		back, err := enc.NewDecoder().Bytes(encoded)
		if err != nil || !bytes.Equal(back, []byte(g)) {
			return nil, fmt.Errorf("value not representable in %s", label)
		}
		return encoded, nil
	}
	if innerBytes, err := assembleFB2Inner(segments, newline, childIndent, endIndent, encodeLegacy, identity); err == nil {
		return spliceFB2(raw, open.end, closeTag.start, innerBytes), nil
	}

	// Some generated character is not representable in the declared encoding:
	// convert the whole document to UTF-8 and update the XML declaration.
	decodeRaw := func(r []byte) ([]byte, error) { return enc.NewDecoder().Bytes(r) }
	prefix, err := enc.NewDecoder().Bytes(raw[:open.end])
	if err != nil {
		return nil, fmt.Errorf("fb2 write-back: decode document head: %w", err)
	}
	prefix = normalizeFB2XMLEncodingDecl(prefix)
	suffix, err := enc.NewDecoder().Bytes(raw[closeTag.start:])
	if err != nil {
		return nil, fmt.Errorf("fb2 write-back: decode document tail: %w", err)
	}
	innerBytes, err := assembleFB2Inner(segments, newline, childIndent, endIndent, genBytes, decodeRaw)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(prefix)+len(innerBytes)+len(suffix))
	out = append(out, prefix...)
	out = append(out, innerBytes...)
	out = append(out, suffix...)
	return out, nil
}

func locateFB2Description(raw []byte) (open fb2Span, closeTag fb2Span, ok bool) {
	for pos := 0; pos < len(raw); {
		lt := bytes.IndexByte(raw[pos:], '<')
		if lt < 0 {
			break
		}
		tagStart := pos + lt
		tagEnd := opfTagEnd(raw, tagStart)
		if tagEnd < 0 {
			return open, closeTag, false
		}
		info := opfParseTag(raw[tagStart:tagEnd])
		if info.local != "description" || info.end {
			pos = tagEnd
			continue
		}
		if info.selfClosing {
			return open, closeTag, false
		}
		endStart, endEnd, err := opfFindElementEnd(raw, tagEnd, "description")
		if err != nil {
			return open, closeTag, false
		}
		return fb2Span{tagStart, tagEnd}, fb2Span{endStart, endEnd}, true
	}
	return open, closeTag, false
}

// scanFB2DirectChildren returns the byte ranges of the direct element children
// of a <description> body, using the shared byte-level XML tag scanner.
func scanFB2DirectChildren(inner []byte) []fb2Child {
	var children []fb2Child
	depth := 0
	childStart := -1
	childLocal := ""
	for pos := 0; pos < len(inner); {
		lt := bytes.IndexByte(inner[pos:], '<')
		if lt < 0 {
			break
		}
		tagStart := pos + lt
		tagEnd := opfTagEnd(inner, tagStart)
		if tagEnd < 0 {
			break
		}
		info := opfParseTag(inner[tagStart:tagEnd])
		if info.local == "" {
			pos = tagEnd
			continue
		}
		switch {
		case info.end:
			if depth > 0 {
				depth--
				if depth == 0 && childStart >= 0 {
					children = append(children, fb2Child{local: childLocal, start: childStart, end: tagEnd})
					childStart = -1
				}
			}
		case info.selfClosing:
			if depth == 0 {
				children = append(children, fb2Child{local: info.local, start: tagStart, end: tagEnd})
			}
		default:
			if depth == 0 {
				childStart = tagStart
				childLocal = info.local
			}
			depth++
		}
		pos = tagEnd
	}
	return children
}

type fb2Child struct {
	local string
	start int
	end   int
}

// extractFB2CoverRef returns the first cover image reference in a title-info
// region, keeping the source attribute name (typically l:href) so the re-emitted
// coverpage keeps the file's xlink namespace prefix.
func extractFB2CoverRef(titleInfo []byte) fb2CoverRef {
	loc := fb2ImageTagRe.FindIndex(titleInfo)
	if loc == nil {
		return fb2CoverRef{}
	}
	tag := titleInfo[loc[0]:loc[1]]
	for _, m := range opfAttrRe.FindAllSubmatch(tag, -1) {
		if len(m) < 5 {
			continue
		}
		name := strings.TrimSpace(string(m[1]))
		if opfXMLLocalName(name) != "href" {
			continue
		}
		value := m[3]
		if len(value) == 0 {
			value = m[4]
		}
		href := html.UnescapeString(string(value))
		if strings.TrimSpace(href) == "" {
			return fb2CoverRef{}
		}
		return fb2CoverRef{attrName: name, href: href}
	}
	return fb2CoverRef{}
}

func assembleFB2Inner(segments []fb2Segment, newline, childIndent, endIndent string, renderGen func(string) ([]byte, error), renderRaw func([]byte) ([]byte, error)) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(newline)
	for _, s := range segments {
		buf.WriteString(childIndent)
		var (
			b   []byte
			err error
		)
		if s.raw != nil {
			b, err = renderRaw(s.raw)
		} else {
			b, err = renderGen(s.gen)
		}
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteString(newline)
	}
	buf.WriteString(endIndent)
	return buf.Bytes(), nil
}

func spliceFB2(raw []byte, start, end int, mid []byte) []byte {
	out := make([]byte, 0, start+len(mid)+len(raw)-end)
	out = append(out, raw[:start]...)
	out = append(out, mid...)
	out = append(out, raw[end:]...)
	return out
}

// buildFB2TitleInfo renders the snapshot in FB2 schema order.
func buildFB2TitleInfo(meta bookmeta.Metadata, cover fb2CoverRef, newline, indent string) string {
	sub := indent + "  "
	var b strings.Builder
	b.WriteString("<title-info>")
	for _, tag := range meta.Tags {
		if t := strings.TrimSpace(tag); t != "" {
			b.WriteString(newline + sub + "<genre>" + opfEscapeText(t) + "</genre>")
		}
	}
	for _, author := range meta.Authors {
		if elem := fb2AuthorElement(author); elem != "" {
			b.WriteString(newline + sub + elem)
		}
	}
	if title := strings.TrimSpace(meta.Title); title != "" {
		b.WriteString(newline + sub + "<book-title>" + opfEscapeText(title) + "</book-title>")
	}
	if desc := strings.TrimSpace(meta.Description); desc != "" {
		b.WriteString(newline + sub + "<annotation>" + newline + sub + "  <p>" + opfEscapeText(desc) + "</p>" + newline + sub + "</annotation>")
	}
	if date := strings.TrimSpace(meta.Date); date != "" {
		b.WriteString(newline + sub + `<date value="` + opfEscapeAttr(date) + `">` + opfEscapeText(date) + "</date>")
	}
	if cover.href != "" {
		b.WriteString(newline + sub + "<coverpage>" + newline + sub + "  <image " + cover.attrName + `="` + opfEscapeAttr(cover.href) + `"/>` + newline + sub + "</coverpage>")
	}
	if lang := strings.TrimSpace(meta.Language); lang != "" {
		b.WriteString(newline + sub + "<lang>" + opfEscapeText(lang) + "</lang>")
	}
	if series := strings.TrimSpace(meta.Series); series != "" {
		b.WriteString(newline + sub + `<sequence name="` + opfEscapeAttr(series) + `"`)
		if meta.SeriesIndex != 0 {
			b.WriteString(` number="` + opfEscapeAttr(strconv.FormatFloat(meta.SeriesIndex, 'f', -1, 64)) + `"`)
		}
		b.WriteString("/>")
	}
	b.WriteString(newline + indent + "</title-info>")
	return b.String()
}

// buildFB2PublishInfo renders a <publish-info> element, or "" when the snapshot
// has no publish-info content FB2 can carry (publisher / ISBN).
func buildFB2PublishInfo(meta bookmeta.Metadata, newline, indent string) string {
	sub := indent + "  "
	publisher := strings.TrimSpace(meta.Publisher)
	isbns := fb2ISBNValues(meta.Identifier)
	if publisher == "" && len(isbns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<publish-info>")
	if publisher != "" {
		b.WriteString(newline + sub + "<publisher>" + opfEscapeText(publisher) + "</publisher>")
	}
	if len(isbns) > 0 {
		b.WriteString(newline + sub + "<isbn>" + opfEscapeText(strings.Join(isbns, ", ")) + "</isbn>")
	}
	b.WriteString(newline + indent + "</publish-info>")
	return b.String()
}

// fb2AuthorElement renders one <author>. FB2 authors are structured (first /
// middle / last), so fb2AuthorNameFields splits the display name back out using
// the sort name when it is a reconstructable "Last, First".
func fb2AuthorElement(author bookmeta.AuthorMeta) string {
	first, middle, last := fb2AuthorNameFields(author)
	if first == "" && middle == "" && last == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<author>")
	if first != "" {
		b.WriteString("<first-name>" + opfEscapeText(first) + "</first-name>")
	}
	if middle != "" {
		b.WriteString("<middle-name>" + opfEscapeText(middle) + "</middle-name>")
	}
	if last != "" {
		b.WriteString("<last-name>" + opfEscapeText(last) + "</last-name>")
	}
	b.WriteString("</author>")
	return b.String()
}

func fb2AuthorNameFields(author bookmeta.AuthorMeta) (first, middle, last string) {
	name := cleanText(author.Name)
	sort := strings.TrimSpace(author.SortName)
	if name == "" {
		return "", "", ""
	}
	if before, after, ok := strings.Cut(sort, ","); ok {
		lastPart := cleanText(before)
		firstPart := cleanText(after)
		if lastPart != "" && firstPart != "" && strings.EqualFold(cleanText(firstPart+" "+lastPart), name) {
			tokens := strings.Fields(firstPart)
			middleName := ""
			if len(tokens) > 1 {
				middleName = strings.Join(tokens[1:], " ")
			}
			return tokens[0], middleName, lastPart
		}
	}
	// No reconstructable "Last, First" sort: keep the whole display name in
	// first-name so extraction rebuilds the same Name (and a derived sort).
	return name, "", ""
}

func fb2ISBNValues(identifier string) []string {
	var out []string
	for _, id := range bookmeta.ParseIdentifiers(identifier) {
		if strings.EqualFold(strings.TrimSpace(id.Type), "isbn") {
			if v := strings.TrimSpace(id.Value); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func fb2DeclaredEncoding(raw []byte) string {
	decl := fb2XMLDeclRe.Find(raw)
	if decl == nil {
		return ""
	}
	m := fb2EncodingAttr.FindSubmatch(decl)
	if m == nil {
		return ""
	}
	if len(m[1]) > 0 {
		return string(m[1])
	}
	return string(m[2])
}

func fb2IsUTF8Label(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "utf-8", "utf8", "unicode-1-1-utf-8":
		return true
	}
	return false
}
