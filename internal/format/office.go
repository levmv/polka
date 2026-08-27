package format

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/levmv/polka/internal/bookmeta"
	"golang.org/x/text/encoding/charmap"
)

const (
	odtMediaType          = "application/vnd.oasis.opendocument.text"
	odtOfficeNamespace    = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
	maxODTMetadataSize    = 2 << 20
	maxODTContentScanSize = 4 << 20
	maxODTCoverBytes      = 32 << 20
	maxRTFMetadataSize    = 256 << 10
)

var rtfCodepageRe = regexp.MustCompile(`\\ansicpg(\d+)`)

type odtDocumentMeta struct {
	Meta odtMeta `xml:"meta"`
}

type odtMeta struct {
	Title          string           `xml:"title"`
	Description    string           `xml:"description"`
	Subject        string           `xml:"subject"`
	Creator        string           `xml:"creator"`
	InitialCreator string           `xml:"initial-creator"`
	Date           string           `xml:"date"`
	Language       string           `xml:"language"`
	Keywords       []string         `xml:"keyword"`
	KeywordLists   []string         `xml:"keywords"`
	UserDefined    []odtUserDefined `xml:"user-defined"`
}

type odtUserDefined struct {
	Name      string `xml:"name,attr"`
	ValueType string `xml:"value-type,attr"`
	Value     string `xml:",chardata"`
}

type odtCoverCandidate struct {
	Href     string
	Explicit bool
}

func isODT(r io.ReaderAt, size int64) bool {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false
	}
	mimetype := zipEntryByName(zr, "mimetype")
	content := zipEntryByName(zr, "content.xml")
	if mimetype == nil || content == nil {
		return false
	}
	raw, err := readZipFileLimited(mimetype, int64(len(odtMediaType)+16))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == odtMediaType && odtHasTextContent(content)
}

// odtHasTextContent distinguishes OpenDocument Text from another ZIP package
// carrying a misleading mimetype. The office:body/office:text elements occur
// near the start of content.xml; keep detection bounded even for huge books.
func odtHasTextContent(entry *zip.File) bool {
	r, err := entry.Open()
	if err != nil {
		return false
	}
	defer r.Close()

	decoder := xml.NewDecoder(io.LimitReader(r, maxODTContentScanSize))
	depth := 0
	bodyDepth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 && (value.Name.Space != odtOfficeNamespace || value.Name.Local != "document-content") {
				return false
			}
			if value.Name.Space != odtOfficeNamespace {
				continue
			}
			switch value.Name.Local {
			case "body":
				bodyDepth = depth
			case "text":
				if bodyDepth > 0 {
					return true
				}
			}
		case xml.EndElement:
			if depth == bodyDepth {
				bodyDepth = 0
			}
			depth--
		}
	}
}

// ExtractODTMetadata reads OpenDocument metadata from meta.xml.
func ExtractODTMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	entry := zipEntryByName(zr, "meta.xml")
	if entry == nil {
		return &Metadata{}, nil
	}
	raw, err := readZipFileLimited(entry, maxODTMetadataSize)
	if err != nil {
		return nil, err
	}
	return metadataFromODTMeta(raw), nil
}

func metadataFromODTMeta(raw []byte) *Metadata {
	var doc odtDocumentMeta
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		return &Metadata{}
	}
	odt := doc.Meta
	meta := &Metadata{
		Title:       cleanXMLText(odt.Title),
		Description: cleanXMLText(odt.Description),
		Date:        bookmeta.NormalizeMetadataDate(cleanXMLText(odt.Date)),
		Language:    bookmeta.NormalizeLanguage(cleanXMLText(odt.Language)),
		Tags:        odtTags(odt.Subject, append(odt.Keywords, odt.KeywordLists...)...),
	}
	creator := cleanXMLText(odt.InitialCreator)
	if creator == "" {
		creator = cleanXMLText(odt.Creator)
	}
	for _, author := range bookmeta.ParseAuthorList(creator) {
		meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
	}
	applyODTUserMetadata(meta, odt.UserDefined)
	return meta
}

// ExtractODTCover reads a cheap embedded cover candidate from ODT package XML.
// An explicit draw frame named opf.cover wins when OPF user metadata is present;
// otherwise the first image that looks cover-like is used.
func ExtractODTCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, "", err
	}
	content := zipEntryByName(zr, "content.xml")
	if content == nil {
		return nil, "", nil
	}
	opfmeta := false
	opfnocover := false
	if meta := zipEntryByName(zr, "meta.xml"); meta != nil {
		raw, err := readZipFileLimited(meta, maxODTMetadataSize)
		if err != nil {
			return nil, "", err
		}
		opfmeta, opfnocover = odtOPFMetadataCoverFlags(raw)
	}
	if opfnocover {
		return nil, "", nil
	}
	raw, err := readZipFileLimited(content, maxODTContentScanSize)
	if err != nil {
		return nil, "", err
	}
	candidates := odtCoverCandidates(raw, opfmeta)
	for _, candidate := range candidates {
		if !candidate.Explicit {
			continue
		}
		cover, ext, _, _, ok, err := odtReadCoverCandidate(zr, candidate)
		if err != nil || ok {
			return cover, ext, err
		}
	}
	for _, candidate := range candidates {
		cover, ext, width, height, ok, err := odtReadCoverCandidate(zr, candidate)
		if err != nil {
			return nil, "", err
		}
		if ok && odtLooksLikeCover(width, height) {
			return cover, ext, nil
		}
	}
	return nil, "", nil
}

func odtOPFMetadataCoverFlags(raw []byte) (bool, bool) {
	var doc odtDocumentMeta
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		return false, false
	}
	opfmeta := false
	opfnocover := false
	for _, item := range doc.Meta.UserDefined {
		if strings.EqualFold(strings.TrimSpace(item.Name), "opf.metadata") && strings.EqualFold(cleanXMLText(item.Value), "true") {
			opfmeta = true
		}
		if strings.EqualFold(strings.TrimSpace(item.Name), "opf.nocover") && strings.EqualFold(cleanXMLText(item.Value), "true") {
			opfnocover = true
		}
	}
	return opfmeta, opfnocover
}

func odtCoverCandidates(raw []byte, opfmeta bool) []odtCoverCandidate {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var frames []string
	var candidates []odtCoverCandidate
	for {
		token, err := decoder.Token()
		if err != nil {
			return candidates
		}
		switch tok := token.(type) {
		case xml.StartElement:
			switch tok.Name.Local {
			case "frame":
				frames = append(frames, attrValue(tok.Attr, "name"))
			case "image":
				if len(frames) == 0 {
					continue
				}
				href := attrValue(tok.Attr, "href")
				if href == "" {
					continue
				}
				frameName := frames[len(frames)-1]
				candidates = append(candidates, odtCoverCandidate{
					Href:     href,
					Explicit: opfmeta && strings.EqualFold(frameName, "opf.cover"),
				})
			}
		case xml.EndElement:
			if tok.Name.Local == "frame" && len(frames) > 0 {
				frames = frames[:len(frames)-1]
			}
		}
	}
}

func odtReadCoverCandidate(zr *zip.Reader, candidate odtCoverCandidate) ([]byte, string, int, int, bool, error) {
	entryName := odtZipPath(candidate.Href)
	if entryName == "" {
		return nil, "", 0, 0, false, nil
	}
	entry := zipEntryByName(zr, entryName)
	if entry == nil {
		return nil, "", 0, 0, false, nil
	}
	raw, err := readZipFileLimited(entry, maxODTCoverBytes)
	if err != nil {
		return nil, "", 0, 0, false, err
	}
	cfg, formatName, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, "", 0, 0, false, nil
	}
	ext, ok := coverImageExtensionFromFormatName(formatName)
	if !ok {
		return nil, "", 0, 0, false, nil
	}
	return raw, ext, cfg.Width, cfg.Height, true, nil
}

func odtZipPath(href string) string {
	href = strings.TrimPrefix(path.Clean(strings.ReplaceAll(cleanOPFHref(href), "\\", "/")), "./")
	if href == "." || strings.HasPrefix(href, "../") || strings.HasPrefix(href, "/") {
		return ""
	}
	return href
}

func odtLooksLikeCover(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	ratio := float64(height) / float64(width)
	return ratio >= 0.8 && ratio <= 1.8 && width*height >= 12000
}

func applyODTUserMetadata(meta *Metadata, userDefined []odtUserDefined) {
	data := map[string]string{}
	for _, item := range userDefined {
		name := strings.ToLower(strings.TrimSpace(item.Name))
		value := cleanXMLText(item.Value)
		if name != "" && value != "" {
			data[name] = value
		}
	}
	if !strings.EqualFold(data["opf.metadata"], "true") {
		return
	}
	if value := data["opf.titlesort"]; value != "" {
		meta.SortTitle = value
	}
	if value := data["opf.authors"]; value != "" {
		meta.Authors = nil
		for _, author := range bookmeta.ParseAuthorList(value) {
			meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
		}
		if sortName := data["opf.authorsort"]; sortName != "" && len(meta.Authors) == 1 {
			meta.Authors[0].SortName = sortName
		}
	}
	if value := data["opf.isbn"]; value != "" {
		if id := bookmeta.IdentifierFromOPF("isbn", value); id.Value != "" {
			meta.Identifier = bookmeta.FormatIdentifiers([]bookmeta.Identifier{id})
		}
	}
	if value := data["opf.identifiers"]; value != "" {
		if ids := identifiersFromODTJSON(value); len(ids) > 0 {
			meta.Identifier = bookmeta.FormatIdentifiers(ids)
		}
	}
	if value := data["opf.publisher"]; value != "" {
		meta.Publisher = value
	}
	if value := data["opf.pubdate"]; value != "" {
		meta.Date = bookmeta.NormalizeMetadataDate(value)
	}
	if value := data["opf.series"]; value != "" {
		meta.Series = value
		if index, err := strconv.ParseFloat(data["opf.seriesindex"], 64); err == nil {
			meta.SeriesIndex = index
		}
	}
	if value := data["opf.language"]; value != "" {
		meta.Language = bookmeta.NormalizeLanguage(value)
	}
}

func identifiersFromODTJSON(raw string) []bookmeta.Identifier {
	values := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &values, jsontext.AllowDuplicateNames(true)); err != nil {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var ids []bookmeta.Identifier
	for _, key := range keys {
		id := bookmeta.IdentifierFromOPF(key, values[key])
		if id.Value != "" && !bookmeta.IsInternalIdentifier(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func odtTags(subject string, values ...string) []string {
	all := append([]string{subject}, values...)
	return uniqueTagList(all, commaSemicolonNewlineTabSeparator, cleanXMLText)
}

func cleanXMLText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func isRTF(r io.ReaderAt, size int64) bool {
	sample, err := readAtMost(r, size, 16)
	if err != nil {
		return false
	}
	sample = bytes.TrimPrefix(sample, []byte{0xef, 0xbb, 0xbf})
	return bytes.HasPrefix(sample, []byte(`{\rtf`))
}

// ExtractRTFMetadata reads the small RTF {\info ...} metadata block.
func ExtractRTFMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	raw, err := readAtMost(r, size, maxRTFMetadataSize)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), []byte(`{\rtf`)) {
		return &Metadata{}, nil
	}
	info := rtfInfoBlock(raw)
	if len(info) == 0 {
		return &Metadata{}, nil
	}
	codepage, err := rtfCodepage(raw)
	if err != nil {
		return nil, err
	}
	publisher := rtfField(info, "company", codepage)
	if publisher == "" {
		publisher = rtfField(info, "manager", codepage)
	}
	meta := &Metadata{
		Title:       rtfField(info, "title", codepage),
		Description: rtfField(info, "subject", codepage),
		Publisher:   publisher,
		Tags:        rtfTags(rtfField(info, "category", codepage), rtfField(info, "keywords", codepage)),
	}
	for author := range strings.SplitSeq(rtfField(info, "author", codepage), ",") {
		author = strings.TrimSpace(author)
		if author != "" {
			meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
		}
	}
	return meta, nil
}

func rtfInfoBlock(raw []byte) []byte {
	rootStart := bytes.Index(raw, []byte(`{\rtf`))
	if rootStart < 0 {
		return nil
	}
	// The metadata prefix is bounded, while the outer document group commonly
	// closes far beyond it. Scan its direct children without requiring the whole
	// RTF body to fit in the metadata window.
	return rtfChildGroup(raw[rootStart:], "info")
}

func rtfField(info []byte, name string, codepage string) string {
	group := rtfChildGroup(info, name)
	if len(group) == 0 {
		return ""
	}
	_, bodyStart := rtfGroupControlWord(group)
	body := bytes.TrimSpace(group[bodyStart : len(group)-1])
	return strings.TrimSpace(rtfDecodeText(body, codepage))
}

// rtfChildGroup finds a direct child destination. Restricting metadata fields
// to direct children of {\info ...} prevents title-like bytes in nested or
// ignorable destinations from overriding the real field.
func rtfChildGroup(parent []byte, name string) []byte {
	if len(parent) < 2 || parent[0] != '{' {
		return nil
	}
	for i := 1; i < len(parent)-1; {
		switch parent[i] {
		case '\\':
			if end, ok := rtfBinaryPayloadEnd(parent, i); ok {
				i = end
				continue
			}
			i += 2
		case '{':
			group := rtfBalancedGroup(parent, i)
			if len(group) == 0 {
				return nil
			}
			if word, _ := rtfGroupControlWord(group); word == name {
				return group
			}
			i += len(group)
		default:
			i++
		}
	}
	return nil
}

func rtfGroupControlWord(group []byte) (string, int) {
	if len(group) < 4 || group[0] != '{' || group[1] != '\\' {
		return "", 0
	}
	i := 2
	start := i
	for i < len(group)-1 && isASCIIAlpha(group[i]) {
		i++
	}
	if i == start {
		return "", 0
	}
	bodyStart := i
	if bodyStart < len(group)-1 && group[bodyStart] == ' ' {
		bodyStart++
	}
	return string(group[start:i]), bodyStart
}

func rtfBalancedGroup(raw []byte, start int) []byte {
	if start < 0 || start >= len(raw) || raw[start] != '{' {
		return nil
	}
	depth := 0
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			if end, ok := rtfBinaryPayloadEnd(raw, i); ok {
				i = end - 1
				continue
			}
			i++
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return nil
}

// rtfBinaryPayloadEnd returns the first byte after a \binN payload. Binary RTF
// data is opaque and may contain braces or backslashes that must not affect
// group balancing or metadata text.
func rtfBinaryPayloadEnd(raw []byte, slash int) (int, bool) {
	if slash < 0 || slash+4 > len(raw) || raw[slash] != '\\' {
		return 0, false
	}
	i := slash + 1
	wordStart := i
	for i < len(raw) && isASCIIAlpha(raw[i]) {
		i++
	}
	if string(raw[wordStart:i]) != "bin" {
		return 0, false
	}
	sign := 1
	if i < len(raw) && raw[i] == '-' {
		sign = -1
		i++
	}
	digitStart := i
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	if i == digitStart {
		return 0, false
	}
	count, err := strconv.Atoi(string(raw[digitStart:i]))
	if err != nil || sign < 0 {
		return len(raw), true
	}
	if i < len(raw) && raw[i] == ' ' {
		i++
	}
	if count > len(raw)-i {
		return len(raw), true
	}
	return i + count, true
}

func rtfCodepage(raw []byte) (string, error) {
	codepage := "1252"
	if match := rtfCodepageRe.FindSubmatch(raw); len(match) > 1 {
		codepage = string(match[1])
		if codepage == "0" {
			codepage = "1252"
		}
	}
	if rtfCharmap(codepage) == nil {
		return "", fmt.Errorf("unsupported RTF code page %s", codepage)
	}
	return codepage, nil
}

func rtfDecodeText(raw []byte, codepage string) string {
	var out strings.Builder
	ucSkip := 1
	var ucStack []int
	var highSurrogate uint16
	flushHighSurrogate := func() {
		if highSurrogate == 0 {
			return
		}
		out.WriteRune('\uFFFD')
		highSurrogate = 0
	}
	writeText := func(text string) {
		flushHighSurrogate()
		out.WriteString(text)
	}
	writeCodeUnit := func(unit uint16) {
		switch {
		case 0xd800 <= unit && unit <= 0xdbff:
			flushHighSurrogate()
			highSurrogate = unit
		case 0xdc00 <= unit && unit <= 0xdfff:
			if highSurrogate == 0 {
				out.WriteRune('\uFFFD')
				return
			}
			out.WriteRune(utf16.DecodeRune(rune(highSurrogate), rune(unit)))
			highSurrogate = 0
		default:
			flushHighSurrogate()
			out.WriteRune(rune(unit))
		}
	}
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b == '{' {
			if i+2 < len(raw) && raw[i+1] == '\\' && raw[i+2] == '*' {
				group := rtfBalancedGroup(raw, i)
				if len(group) == 0 {
					break
				}
				i += len(group) - 1
				continue
			}
			ucStack = append(ucStack, ucSkip)
			continue
		}
		if b == '}' {
			if len(ucStack) > 0 {
				ucSkip = ucStack[len(ucStack)-1]
				ucStack = ucStack[:len(ucStack)-1]
			}
			continue
		}
		if b != '\\' {
			writeText(rtfDecodeByte(b, codepage))
			continue
		}
		if end, ok := rtfBinaryPayloadEnd(raw, i); ok {
			i = end - 1
			continue
		}
		if i+1 >= len(raw) {
			break
		}
		i++
		switch next := raw[i]; {
		case next == '\\' || next == '{' || next == '}':
			writeText(string(next))
		case next == '\'' && i+2 < len(raw):
			decoded, err := hex.DecodeString(string(raw[i+1 : i+3]))
			if err == nil && len(decoded) == 1 {
				writeText(rtfDecodeByte(decoded[0], codepage))
			}
			i += 2
		case next == '~':
			writeText(" ")
		case next == '-':
		case next == '_':
			writeText("-")
		case isASCIIAlpha(next):
			start := i
			for i+1 < len(raw) && isASCIIAlpha(raw[i+1]) {
				i++
			}
			word := string(raw[start : i+1])
			sign := 1
			if i+1 < len(raw) && raw[i+1] == '-' {
				sign = -1
				i++
			}
			numStart := i + 1
			for i+1 < len(raw) && raw[i+1] >= '0' && raw[i+1] <= '9' {
				i++
			}
			hasNumber := i+1 > numStart
			value := 0
			if hasNumber {
				if parsed, err := strconv.Atoi(string(raw[numStart : i+1])); err == nil {
					value = parsed * sign
				}
			}
			if i+1 < len(raw) && raw[i+1] == ' ' {
				i++
			}
			switch word {
			case "u":
				if hasNumber {
					if value < 0 {
						value += 65536
					}
					if 0 <= value && value <= 65535 {
						writeCodeUnit(uint16(value))
					} else {
						flushHighSurrogate()
						out.WriteRune('\uFFFD')
					}
					i = rtfSkipFallback(raw, i, ucSkip)
				}
			case "uc":
				if hasNumber && value >= 0 {
					ucSkip = value
				}
			case "tab", "emdash", "endash":
				writeText(" ")
			case "line", "par":
				writeText("\n")
			}
		default:
			writeText(string(next))
		}
	}
	flushHighSurrogate()
	return out.String()
}

func rtfSkipFallback(raw []byte, i int, count int) int {
	for skipped := 0; skipped < count && i+1 < len(raw); skipped++ {
		if raw[i+1] == '\\' {
			if i+2 < len(raw) && raw[i+2] == '\'' && i+4 < len(raw) {
				i += 4
				continue
			}
			i++
			continue
		}
		i++
	}
	return i
}

func rtfDecodeByte(b byte, codepage string) string {
	if b < 0x80 {
		return string([]byte{b})
	}
	decoder := rtfCharmap(codepage).NewDecoder()
	s, err := decoder.String(string([]byte{b}))
	if err != nil {
		return "?"
	}
	return s
}

func rtfCharmap(codepage string) *charmap.Charmap {
	switch codepage {
	case "1250":
		return charmap.Windows1250
	case "1251":
		return charmap.Windows1251
	case "1252":
		return charmap.Windows1252
	case "1253":
		return charmap.Windows1253
	case "1254":
		return charmap.Windows1254
	case "1255":
		return charmap.Windows1255
	case "1256":
		return charmap.Windows1256
	case "1257":
		return charmap.Windows1257
	case "1258":
		return charmap.Windows1258
	default:
		return nil
	}
}

func rtfTags(values ...string) []string {
	return uniqueTagList(values, commaSemicolonNewlineTabSeparator, strings.TrimSpace)
}

func isASCIIAlpha(b byte) bool {
	return ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}
