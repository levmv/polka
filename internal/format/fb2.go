package format

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/charmap"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/xmlutil"
)

type fb2Doc struct {
	Description fb2Description `xml:"description"`
	Binaries    []fb2Binary    `xml:"binary"`
}

type fb2Description struct {
	TitleInfo    fb2TitleInfo   `xml:"title-info"`
	SrcTitleInfo fb2TitleInfo   `xml:"src-title-info"`
	DocumentInfo fb2TitleInfo   `xml:"document-info"`
	PublishInfo  fb2PublishInfo `xml:"publish-info"`
}

type fb2TitleInfo struct {
	Genres     []string      `xml:"genre"`
	Keywords   string        `xml:"keywords"`
	Authors    []fb2Author   `xml:"author"`
	BookTitle  string        `xml:"book-title"`
	Annotation fb2Annotation `xml:"annotation"`
	Date       fb2Date       `xml:"date"`
	Coverpage  fb2Coverpage  `xml:"coverpage"`
	Lang       string        `xml:"lang"`
	Sequences  []fb2Sequence `xml:"sequence"`
}

type fb2PublishInfo struct {
	Publisher string        `xml:"publisher"`
	Year      string        `xml:"year"`
	BookTitle string        `xml:"book-title"`
	ISBN      string        `xml:"isbn"`
	Sequences []fb2Sequence `xml:"sequence"`
}

type fb2Author struct {
	FirstName  string `xml:"first-name"`
	MiddleName string `xml:"middle-name"`
	LastName   string `xml:"last-name"`
	Nickname   string `xml:"nickname"`
}

type fb2Annotation struct {
	InnerXML string `xml:",innerxml"`
}

type fb2Date struct {
	Value string `xml:"value,attr"`
	Text  string `xml:",chardata"`
}

type fb2Coverpage struct {
	Images []fb2Image `xml:"image"`
}

type fb2Image struct {
	Href string `xml:"href,attr"`
}

type fb2Sequence struct {
	Name   string `xml:"name,attr"`
	Number string `xml:"number,attr"`
}

type fb2Binary struct {
	ID          string `xml:"id,attr"`
	ContentType string `xml:"content-type,attr"`
	Data        string `xml:",chardata"`
}

type FB2Source struct {
	Reader        io.ReadCloser
	Filename      string
	ContentLength int64
	Container     FB2Container
}

const maxFB2DocumentBytes int64 = 256 << 20

// FB2 publish-info/isbn is often a free-form field, not a single clean ISBN.
// Extract candidates first, then validate checksums before storing identifiers.
var (
	isbnCandidateRe        = regexp.MustCompile(`(?i)(?:97[89][-\s]?)?[0-9][0-9\-\s]{7,}[0-9x]`)
	fb2LangHintRe          = regexp.MustCompile(`(?is)<\s*lang\s*>\s*([a-z]{2,3})(?:[-_][a-z0-9]+)?\s*<\s*/\s*lang\s*>`)
	fb2XMLEncodingDeclRe   = regexp.MustCompile(`(?is)^(\s*<\?xml\b[^?>]*\bencoding\s*=\s*)(?:"[^"]*"|'[^']*')`)
	fb2CyrillicLanguageIDs = map[string]bool{"be": true, "bg": true, "kk": true, "mk": true, "ru": true, "sr": true, "uk": true}
)

// ExtractFB2Metadata extracts the core library metadata from a FictionBook 2 file.
func ExtractFB2Metadata(r io.ReaderAt, size int64) (*Metadata, error) {
	doc, err := decodeFB2Metadata(r, size)
	if err != nil {
		return nil, err
	}
	return metadataFromFB2(doc), nil
}

// ExtractFB2MetadataAndCover decodes one FB2 document for both metadata and
// cover data. Importers that need both should use this instead of normalizing
// and parsing the full XML document twice.
func ExtractFB2MetadataAndCover(r io.ReaderAt, size int64) (*Metadata, []byte, string, error) {
	doc, err := decodeFB2(r, size)
	if err != nil {
		return nil, nil, "", err
	}
	meta := metadataFromFB2(doc)
	cover, ext, err := fb2Cover(doc)
	return meta, cover, ext, err
}

// ExtractFB2MetadataFromXMLBytes extracts metadata from already-normalized FB2
// XML bytes. Callers that have not already opened and normalized the source
// should use ExtractFB2Metadata instead.
func ExtractFB2MetadataFromXMLBytes(raw []byte) (*Metadata, error) {
	doc, err := decodeFB2MetadataXML(raw)
	if err != nil {
		return nil, err
	}
	return metadataFromFB2(doc), nil
}

// ExtractFB2Cover extracts the cover image referenced by title-info/coverpage.
// If no explicit coverpage image exists, it falls back to the first decodable
// image binary.
func ExtractFB2Cover(r io.ReaderAt, size int64) ([]byte, string, error) {
	doc, err := decodeFB2(r, size)
	if err != nil {
		return nil, "", err
	}
	return fb2Cover(doc)
}

func fb2Cover(doc *fb2Doc) ([]byte, string, error) {
	wantID := ""
	for _, titleInfo := range fb2TitleInfoFallbacks(doc) {
		for _, img := range titleInfo.Coverpage.Images {
			if id := fb2BinaryID(img.Href); id != "" {
				wantID = id
				break
			}
		}
		if wantID != "" {
			break
		}
	}

	var fallback []byte
	fallbackExt := ""
	for i := range doc.Binaries {
		bin := &doc.Binaries[i]
		if wantID != "" && strings.TrimSpace(bin.ID) == wantID {
			if b, ext, err := decodeFB2ImageBinary(bin); err != nil || len(b) > 0 {
				return b, ext, err
			}
			continue
		}
		if fallback != nil || !strings.HasPrefix(fb2BinaryContentType(bin.ContentType), "image/") {
			continue
		}
		b, ext, err := decodeFB2ImageBinary(bin)
		if err != nil {
			return nil, "", err
		}
		if len(b) > 0 {
			fallback = b
			fallbackExt = ext
		}
	}
	// If the declared cover target is missing or unusable, the first decodable
	// image is still useful. Broken binaries before it should not hide it.
	return fallback, fallbackExt, nil
}

func decodeFB2(r io.ReaderAt, size int64) (*fb2Doc, error) {
	source, err := OpenFB2Source(r, size, "")
	if err != nil {
		return nil, err
	}
	defer source.Reader.Close()
	return decodeFB2Reader(source.Reader)
}

func OpenFB2Source(r io.ReaderAt, size int64, filename string) (FB2Source, error) {
	if size < 0 {
		return FB2Source{}, fmt.Errorf("fb2 source size is invalid")
	}
	container := FB2ContainerNone
	if filename != "" {
		container = FB2ContainerForExtension(BookExtension(filename))
	}
	switch container {
	case FB2ContainerZip:
		source, err := openFB2ZipSource(r, size, filename)
		if err != nil {
			return FB2Source{}, fmt.Errorf("decode fb2 zip: %w", err)
		}
		return source, nil
	case FB2ContainerGzip:
		source, err := openFB2GzipSource(r, size, filename)
		if err != nil {
			return FB2Source{}, fmt.Errorf("decode fb2 gzip: %w", err)
		}
		return source, nil
	case FB2ContainerNone:
		if filename != "" {
			return openFB2PlainSource(r, size, filename), nil
		}
	}

	if zr, err := zip.NewReader(r, size); err == nil {
		source, err := fb2ZipSource(zr, filename)
		if err != nil {
			return FB2Source{}, fmt.Errorf("decode fb2 zip: %w", err)
		}
		return source, nil
	}
	if source, err := openFB2GzipSource(r, size, filename); err == nil {
		return source, nil
	}
	return openFB2PlainSource(r, size, filename), nil
}

func openFB2ZipSource(r io.ReaderAt, size int64, filename string) (FB2Source, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return FB2Source{}, err
	}
	return fb2ZipSource(zr, filename)
}

func fb2ZipSource(zr *zip.Reader, filename string) (FB2Source, error) {
	f, err := SingleFB2ZipEntry(zr)
	if err != nil {
		return FB2Source{}, err
	}
	rc, err := f.Open()
	if err != nil {
		return FB2Source{}, fmt.Errorf("open fb2 zip entry %s: %w", f.Name, err)
	}
	return FB2Source{
		Reader:        rc,
		Filename:      FB2PlainFilename(filename),
		ContentLength: int64(f.UncompressedSize64),
		Container:     FB2ContainerZip,
	}, nil
}

func validateFB2Gzip(r io.ReaderAt, size int64) error {
	source, err := openFB2GzipSource(r, size, "")
	if err != nil {
		return err
	}
	defer source.Reader.Close()
	_, err = normalizeFB2Reader(source.Reader)
	return err
}

func openFB2GzipSource(r io.ReaderAt, size int64, filename string) (FB2Source, error) {
	gr, err := gzip.NewReader(io.NewSectionReader(r, 0, size))
	if err != nil {
		return FB2Source{}, err
	}
	return FB2Source{
		Reader:        gr,
		Filename:      FB2PlainFilename(filename),
		ContentLength: -1,
		Container:     FB2ContainerGzip,
	}, nil
}

func openFB2PlainSource(r io.ReaderAt, size int64, filename string) FB2Source {
	return FB2Source{
		Reader:        io.NopCloser(io.NewSectionReader(r, 0, size)),
		Filename:      FB2PlainFilename(filename),
		ContentLength: size,
		Container:     FB2ContainerNone,
	}
}

func FB2PlainFilename(filename string) string {
	if filename == "" {
		return "book.fb2"
	}
	ext := BookExtension(filename)
	switch FB2ContainerForExtension(ext) {
	case FB2ContainerZip, FB2ContainerGzip:
		base := filename[:len(filename)-len(ext)]
		return base + ".fb2"
	case FB2ContainerNone:
		return filename
	}
	return filename
}

func decodeFB2Reader(r io.Reader) (*fb2Doc, error) {
	raw, err := readAllLimited(r, "FB2 document", maxFB2DocumentBytes)
	if err != nil {
		return nil, fmt.Errorf("read fb2: %w", err)
	}
	// A successful full decode also proves that the XML is valid, so the common
	// path does not need a separate validation pass. Keep normalization and its
	// bounded repairs as the retry path for imperfect real-world files.
	if doc, err := decodeFB2XML(raw); err == nil {
		return doc, nil
	}
	raw, err = NormalizeFB2XMLBytes(raw)
	if err != nil {
		return nil, err
	}
	return decodeFB2XML(raw)
}

func decodeFB2Metadata(r io.ReaderAt, size int64) (*fb2Doc, error) {
	source, err := OpenFB2Source(r, size, "")
	if err != nil {
		return nil, err
	}
	defer source.Reader.Close()
	return decodeFB2MetadataReader(source.Reader)
}

func decodeFB2MetadataReader(r io.Reader) (*fb2Doc, error) {
	raw, err := normalizeFB2Reader(r)
	if err != nil {
		return nil, err
	}
	return decodeFB2MetadataXML(raw)
}

func normalizeFB2Reader(r io.Reader) ([]byte, error) {
	raw, err := readAllLimited(r, "FB2 document", maxFB2DocumentBytes)
	if err != nil {
		return nil, fmt.Errorf("read fb2: %w", err)
	}

	raw, err = NormalizeFB2XMLBytes(raw)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// NormalizeFB2XMLBytes returns UTF-8 FB2 XML bytes that the strict XML parser
// accepts. It keeps the existing narrow structural repairs and adds a fallback
// for real-world files whose XML declaration is missing or lies about a legacy
// single-byte encoding.
func NormalizeFB2XMLBytes(raw []byte) ([]byte, error) {
	normalized, _, err := normalizeFB2XMLBytes(raw)
	return normalized, err
}

type fb2NormalizationDiagnostics struct {
	removedInvalidControls bool
	repairedAmpersand      bool
	legacyEncoding         bool
}

func normalizeFB2XMLBytes(raw []byte) ([]byte, fb2NormalizationDiagnostics, error) {
	raw, removedControls := xmlutil.RemoveInvalidXML10ControlBytes(raw)
	diagnostics := fb2NormalizationDiagnostics{removedInvalidControls: removedControls}
	if err := validateFB2XML(raw); err == nil {
		return raw, diagnostics, nil
	} else {
		firstErr := err
		if repaired, ok := repairFB2Ampersand(raw); ok {
			if retryErr := validateFB2XML(repaired); retryErr == nil {
				diagnostics.repairedAmpersand = true
				return repaired, diagnostics, nil
			}
		}
		for _, decoded := range fb2EncodingFallbacks(raw) {
			decoded = normalizeFB2XMLEncodingDecl(decoded)
			if retryErr := validateFB2XML(decoded); retryErr == nil {
				diagnostics.legacyEncoding = true
				return decoded, diagnostics, nil
			}
			if repaired, ok := repairFB2Ampersand(decoded); ok {
				if retryErr := validateFB2XML(repaired); retryErr == nil {
					diagnostics.legacyEncoding = true
					diagnostics.repairedAmpersand = true
					return repaired, diagnostics, nil
				}
			}
		}
		return nil, diagnostics, firstErr
	}
}

func repairFB2Ampersand(raw []byte) ([]byte, bool) {
	if !bytes.Contains(raw, []byte("& ")) {
		return nil, false
	}
	// Some FB2 files contain prose like "A & B" without escaping the
	// ampersand. Keep this retry deliberately narrow so valid entities and
	// more serious XML damage still fail clearly.
	return bytes.ReplaceAll(raw, []byte("& "), []byte("&amp; ")), true
}

func fb2EncodingFallbacks(raw []byte) [][]byte {
	var candidates []*charmap.Charmap
	if fb2HasCyrillicLanguageHint(raw) {
		candidates = []*charmap.Charmap{charmap.Windows1251, charmap.Windows1252}
	} else {
		candidates = []*charmap.Charmap{charmap.Windows1252, charmap.Windows1251}
	}
	out := make([][]byte, 0, len(candidates))
	for _, candidate := range candidates {
		decoded, _ := candidate.NewDecoder().Bytes(raw) // Charmap decoders map every byte.
		out = append(out, decoded)
	}
	return out
}

func fb2HasCyrillicLanguageHint(raw []byte) bool {
	match := fb2LangHintRe.FindSubmatch(raw)
	if len(match) < 2 {
		return false
	}
	return fb2CyrillicLanguageIDs[strings.ToLower(string(match[1]))]
}

func normalizeFB2XMLEncodingDecl(raw []byte) []byte {
	return fb2XMLEncodingDeclRe.ReplaceAll(raw, []byte(`${1}"utf-8"`))
}

func decodeFB2XML(raw []byte) (*fb2Doc, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	var doc fb2Doc
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode fb2: %w", err)
	}
	return &doc, nil
}

func decodeFB2MetadataXML(raw []byte) (*fb2Doc, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return &fb2Doc{}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode fb2: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "description" {
			continue
		}
		var desc fb2Description
		if err := decoder.DecodeElement(&desc, &start); err != nil {
			return nil, fmt.Errorf("decode fb2: %w", err)
		}
		return &fb2Doc{Description: desc}, nil
	}
}

func validateFB2XML(raw []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	for {
		if _, err := decoder.Token(); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("decode fb2: %w", err)
		}
	}
}

func metadataFromFB2(doc *fb2Doc) *Metadata {
	publishInfo := doc.Description.PublishInfo

	meta := &Metadata{
		Title:       fb2Title(doc),
		Language:    bookmeta.NormalizeLanguage(cleanText(doc.Description.TitleInfo.Lang)),
		Description: fb2DescriptionText(doc),
		Publisher:   cleanText(publishInfo.Publisher),
	}

	for _, a := range fb2Authors(doc) {
		name := fb2AuthorName(a)
		if name == "" {
			continue
		}
		meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{
			Name:     name,
			SortName: fb2AuthorSortName(a, name),
		})
	}

	dateInfo := fb2DateInfo(doc)
	date := dateInfo.Value
	if date == "" {
		date = dateInfo.Text
	}
	if date == "" {
		date = publishInfo.Year
	}
	meta.Date = bookmeta.NormalizeMetadataDate(cleanText(date))

	if ids := fb2ISBNIdentifiers(publishInfo.ISBN); len(ids) > 0 {
		meta.Identifier = bookmeta.FormatIdentifiers(ids)
	}

	if len(doc.Description.TitleInfo.Sequences) > 0 {
		meta.Series = cleanText(doc.Description.TitleInfo.Sequences[0].Name)
		meta.SeriesIndex = parseFB2SequenceNumber(doc.Description.TitleInfo.Sequences[0].Number)
	} else if len(publishInfo.Sequences) > 0 {
		meta.Series = cleanText(publishInfo.Sequences[0].Name)
		meta.SeriesIndex = parseFB2SequenceNumber(publishInfo.Sequences[0].Number)
	}

	seenTags := make(map[string]bool)
	for _, rawTag := range fb2Tags(doc) {
		tag := cleanText(rawTag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if seenTags[key] {
			continue
		}
		seenTags[key] = true
		meta.Tags = append(meta.Tags, tag)
	}

	return meta
}

func fb2TitleInfoFallbacks(doc *fb2Doc) []fb2TitleInfo {
	// src-title-info may describe the original/source book, so use it as a
	// fallback for missing descriptive fields, not for language or series.
	return []fb2TitleInfo{
		doc.Description.TitleInfo,
		doc.Description.SrcTitleInfo,
		doc.Description.DocumentInfo,
	}
}

func fb2Title(doc *fb2Doc) string {
	if title := cleanText(doc.Description.TitleInfo.BookTitle); title != "" {
		return title
	}
	if title := cleanText(doc.Description.PublishInfo.BookTitle); title != "" {
		return title
	}
	return cleanText(doc.Description.SrcTitleInfo.BookTitle)
}

func fb2Authors(doc *fb2Doc) []fb2Author {
	for _, titleInfo := range fb2TitleInfoFallbacks(doc) {
		if len(titleInfo.Authors) > 0 {
			return titleInfo.Authors
		}
	}
	return nil
}

func fb2DescriptionText(doc *fb2Doc) string {
	for _, titleInfo := range []fb2TitleInfo{doc.Description.TitleInfo, doc.Description.SrcTitleInfo} {
		if text := textFromXMLFragment(titleInfo.Annotation.InnerXML); text != "" {
			return text
		}
	}
	return ""
}

func fb2DateInfo(doc *fb2Doc) fb2Date {
	for _, titleInfo := range []fb2TitleInfo{doc.Description.TitleInfo, doc.Description.SrcTitleInfo} {
		if titleInfo.Date.Value != "" || titleInfo.Date.Text != "" {
			return titleInfo.Date
		}
	}
	return fb2Date{}
}

func fb2Tags(doc *fb2Doc) []string {
	if tags := fb2TagsFromTitleInfo(doc.Description.TitleInfo); len(tags) > 0 {
		return tags
	}
	return fb2TagsFromTitleInfo(doc.Description.SrcTitleInfo)
}

func fb2TagsFromTitleInfo(titleInfo fb2TitleInfo) []string {
	tags := append([]string(nil), titleInfo.Genres...)
	for _, keyword := range strings.FieldsFunc(titleInfo.Keywords, func(r rune) bool {
		return r == ',' || r == ';'
	}) {
		if keyword = cleanText(keyword); keyword != "" {
			tags = append(tags, keyword)
		}
	}
	return tags
}

func fb2AuthorName(a fb2Author) string {
	parts := []string{cleanText(a.FirstName), cleanText(a.MiddleName), cleanText(a.LastName)}
	name := strings.Join(nonEmpty(parts), " ")
	if name != "" {
		return name
	}
	return cleanText(a.Nickname)
}

func fb2AuthorSortName(a fb2Author, name string) string {
	last := cleanText(a.LastName)
	if last == "" {
		return bookmeta.AuthorSort(name)
	}
	first := strings.Join(nonEmpty([]string{cleanText(a.FirstName), cleanText(a.MiddleName)}), " ")
	if first == "" {
		return last
	}
	return last + ", " + first
}

func parseFB2SequenceNumber(s string) float64 {
	var n float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%f", &n); err != nil {
		return 0
	}
	return n
}

func fb2ISBNIdentifiers(raw string) []bookmeta.Identifier {
	var ids []bookmeta.Identifier
	seen := make(map[string]bool)
	for _, candidate := range isbnCandidateRe.FindAllString(cleanText(raw), -1) {
		id := bookmeta.IdentifierFromOPF("isbn", candidate)
		if id.Value == "" || !bookmeta.ValidISBN(id.Value) {
			continue
		}
		key := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(id.Value))
		if seen[key] {
			continue
		}
		seen[key] = true
		ids = append(ids, id)
	}
	return ids
}

func fb2BinaryID(href string) string {
	href = strings.TrimSpace(href)
	href = strings.TrimPrefix(href, "#")
	return href
}

func decodeFB2ImageBinary(bin *fb2Binary) ([]byte, string, error) {
	encoded := strings.Join(strings.Fields(bin.Data), "")
	if encoded == "" {
		return nil, "", nil
	}
	b, err := DecodeLenientBase64(encoded)
	if err != nil {
		// Bad embedded cover bytes should leave the book importable without a
		// cover; they are not evidence that the FB2 metadata itself is broken.
		return nil, "", nil
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		// FB2 content-type is often wrong, so trust the actual bytes.
		return nil, "", nil
	}
	if ext, ok := coverImageExtensionFromFormatName(format); ok {
		return b, ext, nil
	}
	return nil, "", nil
}

func fb2BinaryContentType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

// DecodeLenientBase64 decodes common padded, raw, and URL-safe base64 variants,
// with a final fallback that strips non-base64 junk from damaged producer output.
func DecodeLenientBase64(encoded string) ([]byte, error) {
	// Broken FB2 generators sometimes strip padding or use URL-safe alphabet
	// despite embedding the data in XML. Try the common variants first, then a
	// garbage-byte-skipping fallback; image sniffing still validates the result.
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var firstErr error
	for _, enc := range encodings {
		if b, err := enc.DecodeString(encoded); err == nil {
			return b, nil
		} else if firstErr == nil {
			firstErr = err
		}
	}
	if filtered := fb2Base64DataOnly(encoded); filtered != encoded && filtered != "" {
		for _, enc := range encodings {
			if b, err := enc.DecodeString(filtered); err == nil {
				return b, nil
			}
		}
	}
	return nil, firstErr
}

func fb2Base64DataOnly(encoded string) string {
	var out strings.Builder
	out.Grow(len(encoded))
	for _, r := range encoded {
		switch {
		case 'A' <= r && r <= 'Z':
			out.WriteRune(r)
		case 'a' <= r && r <= 'z':
			out.WriteRune(r)
		case '0' <= r && r <= '9':
			out.WriteRune(r)
		case r == '+' || r == '/' || r == '=' || r == '-' || r == '_':
			out.WriteRune(r)
		}
	}
	return out.String()
}

func textFromXMLFragment(fragment string) string {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return ""
	}
	decoder := xml.NewDecoder(strings.NewReader("<root>" + fragment + "</root>"))
	var parts []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return cleanText(stripXMLTags(fragment))
		}
		if charData, ok := token.(xml.CharData); ok {
			text := cleanText(string(charData))
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
	return cleanText(strings.Join(parts, " "))
}

func stripXMLTags(s string) string {
	var out bytes.Buffer
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
