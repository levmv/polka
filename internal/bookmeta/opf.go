package bookmeta

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"

	"github.com/levmv/polka/internal/xmlutil"
)

// OPF (Open Packaging Format) parsing. The same Dublin-Core package document is
// used both inside an EPUB (the rootfile) and as a standalone metadata.opf
// sidecar and in legacy PalmDOC books. Metadata interpretation is shared here;
// EPUB manifest and spine handling stays with the container reader.

const MaxOPFDocumentBytes = 2 << 20

var opfXMLEncodingDeclRe = regexp.MustCompile(`(?is)^(?:\xef\xbb\xbf)?\s*<\?xml\b[^?]*?\bencoding\s*=\s*(?:"([^"]*)"|'([^']*)')`)

type OPFMetadata struct {
	Title       []OPFTitle      `xml:"title"`
	Creators    []OPFCreator    `xml:"creator"`
	Language    []string        `xml:"language"`
	Description []string        `xml:"description"`
	Publisher   []string        `xml:"publisher"`
	Date        []OPFDateRecord `xml:"date"`
	Identifier  []OPFIdentifier `xml:"identifier"`
	Subject     []string        `xml:"subject"`
	Meta        []OPFMeta       `xml:"meta"`
}

func (m *OPFMetadata) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	var parsed OPFMetadata
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(tok.Name.Local)
			if IsOPFMetadataContainer(local) {
				var nested OPFMetadata
				if err := dec.DecodeElement(&nested, &tok); err != nil {
					return err
				}
				parsed.merge(nested)
				continue
			}
			switch local {
			case "title":
				var value OPFTitle
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Title = append(parsed.Title, value)
			case "creator":
				var value OPFCreator
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Creators = append(parsed.Creators, value)
			case "language":
				var value string
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Language = append(parsed.Language, value)
			case "description":
				var value string
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Description = append(parsed.Description, value)
			case "publisher":
				var value string
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Publisher = append(parsed.Publisher, value)
			case "date":
				value := OPFDateRecord{Event: opfDateEvent(tok.Attr)}
				if err := dec.DecodeElement(&value.Text, &tok); err != nil {
					return err
				}
				parsed.Date = append(parsed.Date, value)
			case "identifier":
				var value OPFIdentifier
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Identifier = append(parsed.Identifier, value)
			case "subject":
				var value string
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Subject = append(parsed.Subject, value)
			case "meta":
				var value OPFMeta
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Meta = append(parsed.Meta, value)
			default:
				if err := dec.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if tok.Name == start.Name {
				*m = parsed
				return nil
			}
		}
	}
}

func IsOPFMetadataContainer(local string) bool {
	return local == "metadata" || local == "dc-metadata" || local == "x-metadata"
}

func (m *OPFMetadata) merge(other OPFMetadata) {
	m.Title = append(m.Title, other.Title...)
	m.Creators = append(m.Creators, other.Creators...)
	m.Language = append(m.Language, other.Language...)
	m.Description = append(m.Description, other.Description...)
	m.Publisher = append(m.Publisher, other.Publisher...)
	m.Date = append(m.Date, other.Date...)
	m.Identifier = append(m.Identifier, other.Identifier...)
	m.Subject = append(m.Subject, other.Subject...)
	m.Meta = append(m.Meta, other.Meta...)
}

func (m OPFMetadata) Empty() bool {
	return len(m.Title) == 0 &&
		len(m.Creators) == 0 &&
		len(m.Language) == 0 &&
		len(m.Description) == 0 &&
		len(m.Publisher) == 0 &&
		len(m.Date) == 0 &&
		len(m.Identifier) == 0 &&
		len(m.Subject) == 0 &&
		len(m.Meta) == 0
}

type OPFTitle struct {
	Text   string `xml:",chardata"`
	FileAs string `xml:"file-as,attr"`
	ID     string `xml:"id,attr"`
}

type OPFCreator struct {
	Text   string `xml:",chardata"`
	FileAs string `xml:"file-as,attr"`
	Role   string `xml:"role,attr"`
	ID     string `xml:"id,attr"`
}

type OPFIdentifier struct {
	Text   string `xml:",chardata"`
	Scheme string `xml:"scheme,attr"`
	ID     string `xml:"id,attr"`
}

type OPFMeta struct {
	Text     string `xml:",chardata"`
	Name     string `xml:"name,attr"`
	Content  string `xml:"content,attr"`
	Property string `xml:"property,attr"`
	Refines  string `xml:"refines,attr"`
	ID       string `xml:"id,attr"`
}

// ParseOPF decodes a standalone OPF package document, such as a metadata.opf
// sidecar, into Metadata.
func ParseOPF(r io.Reader) (*Metadata, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxOPFDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > MaxOPFDocumentBytes {
		return nil, fmt.Errorf("OPF document exceeds %d bytes", MaxOPFDocumentBytes)
	}
	var document opfMetadataDocument
	if err := DecodeOPFXML(raw, &document); err != nil {
		return nil, err
	}
	return document.Metadata.BookMetadata(), nil
}

// The standalone reader accepts the same package/legacy roots as the EPUB
// container reader, without bringing manifests or spine structure into bookmeta.
type opfMetadataDocument struct{ Metadata OPFMetadata }

func (d *opfMetadataDocument) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	if strings.EqualFold(start.Name.Local, "metadata") || strings.EqualFold(start.Name.Local, "dc-metadata") {
		return dec.DecodeElement(&d.Metadata, &start)
	}
	var doc struct {
		Metadata       OPFMetadata `xml:"metadata"`
		LegacyMetadata OPFMetadata `xml:"dc-metadata"`
	}
	if err := dec.DecodeElement(&doc, &start); err != nil {
		return err
	}
	d.Metadata = doc.Metadata
	if d.Metadata.Empty() {
		d.Metadata = doc.LegacyMetadata
	}
	return nil
}

// DecodeOPFXML decodes OPF XML into the supplied document shape using the same
// compatibility rules as ParseOPF.
func DecodeOPFXML(raw []byte, v any) error {
	normalized, err := NormalizeOPFXML(raw)
	if err != nil {
		return err
	}
	dec := xml.NewDecoder(bytes.NewReader(normalized))
	return dec.Decode(v)
}

// NormalizeOPFXML returns a UTF-8 XML 1.0 OPF document using the same bounded
// compatibility rules as ParseOPF. The document is not parsed and reserialized,
// so unrelated markup and lexical structure survive the normalization.
func NormalizeOPFXML(raw []byte) ([]byte, error) {
	if opfLooksUTF32(raw) {
		return nil, fmt.Errorf("unsupported OPF XML encoding UTF-32")
	}
	var normalized []byte
	if decoder, ok := opfUTF16Decoder(raw); ok {
		var err error
		normalized, err = decodeOPFXMLReader(decoder.Reader(bytes.NewReader(raw)), "UTF-16", len(raw))
		if err != nil {
			return nil, err
		}
		normalized = normalizeOPFXMLEncodingDeclaration(normalized)
	} else {
		raw, _ = xmlutil.RemoveInvalidXML10ControlBytes(raw)
		label := opfXMLDeclaredEncoding(raw)
		if label != "" && !opfEncodingIsUTF8(label) {
			reader, err := charset.NewReaderLabel(label, bytes.NewReader(raw))
			if err != nil {
				return nil, fmt.Errorf("unsupported OPF XML encoding %q: %w", label, err)
			}
			normalized, err = decodeOPFXMLReader(reader, label, len(raw))
			if err != nil {
				return nil, err
			}
			normalized = normalizeOPFXMLEncodingDeclaration(normalized)
		} else {
			normalized = raw
		}
	}
	if !utf8.Valid(normalized) {
		return nil, fmt.Errorf("OPF is not valid UTF-8")
	}
	normalized = normalizeOPFXMLDeclaration(normalized)
	return xmlutil.RemoveInvalidXML10Chars(normalized), nil
}

func opfUTF16Decoder(raw []byte) (*encoding.Decoder, bool) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xff, 0xfe}):
		return unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder(), true
	case bytes.HasPrefix(raw, []byte{0xfe, 0xff}):
		return unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder(), true
	case bytes.HasPrefix(raw, []byte{'<', 0x00, '?', 0x00}):
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder(), true
	case bytes.HasPrefix(raw, []byte{0x00, '<', 0x00, '?'}):
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder(), true
	default:
		return nil, false
	}
}

func opfLooksUTF32(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte{0xff, 0xfe, 0x00, 0x00}) ||
		bytes.HasPrefix(raw, []byte{0x00, 0x00, 0xfe, 0xff}) ||
		bytes.HasPrefix(raw, []byte{0x00, 0x00, 0x00, '<'}) ||
		bytes.HasPrefix(raw, []byte{'<', 0x00, 0x00, 0x00})
}

func decodeOPFXMLReader(reader io.Reader, label string, inputBytes int) ([]byte, error) {
	// Supported XML encodings expand by less than this; keeping the bound
	// proportional to the caller-bounded input avoids an unbounded decode.
	limit := int64(inputBytes)*4 + 1
	normalized, err := io.ReadAll(io.LimitReader(reader, limit))
	if err != nil {
		return nil, fmt.Errorf("decode OPF XML encoding %q: %w", label, err)
	}
	if int64(len(normalized)) >= limit {
		return nil, fmt.Errorf("decoded OPF XML encoding %q exceeds bounded expansion", label)
	}
	return normalized, nil
}

func opfXMLDeclaredEncoding(raw []byte) string {
	match := opfXMLEncodingDeclRe.FindSubmatch(raw)
	if len(match) < 2 {
		return ""
	}
	for _, value := range match[1:] {
		if len(value) > 0 {
			return strings.TrimSpace(string(value))
		}
	}
	return ""
}

func opfEncodingIsUTF8(label string) bool {
	label = strings.ToLower(strings.TrimSpace(label))
	label = strings.NewReplacer("-", "", "_", "").Replace(label)
	return label == "utf8"
}

func normalizeOPFXMLEncodingDeclaration(raw []byte) []byte {
	match := opfXMLEncodingDeclRe.FindSubmatchIndex(raw)
	for index := 2; index+1 < len(match); index += 2 {
		start, end := match[index], match[index+1]
		if start < 0 {
			continue
		}
		out := make([]byte, 0, len(raw)+len("UTF-8")-(end-start))
		out = append(out, raw[:start]...)
		out = append(out, "UTF-8"...)
		out = append(out, raw[end:]...)
		return out
	}
	return raw
}

func normalizeOPFXMLDeclaration(raw []byte) []byte {
	start := 0
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		start = 3
	}
	for start < len(raw) && xmlutil.IsSpace(raw[start]) {
		start++
	}
	if len(raw)-start < len("<?xml") || !strings.EqualFold(string(raw[start:start+len("<?xml")]), "<?xml") {
		return raw
	}

	searchEnd := min(len(raw), start+256)
	declEnd := bytes.Index(raw[start:searchEnd], []byte("?>"))
	if declEnd < 0 {
		return raw
	}
	decl := string(raw[start : start+declEnd])
	lower := strings.ToLower(decl)
	version := strings.Index(lower, "version")
	if version < 0 {
		return raw
	}
	eq := strings.IndexByte(lower[version:], '=')
	if eq < 0 {
		return raw
	}
	pos := version + eq + 1
	for pos < len(decl) && xmlutil.IsSpace(decl[pos]) {
		pos++
	}
	if pos >= len(decl) || (decl[pos] != '"' && decl[pos] != '\'') {
		return raw
	}
	quote := decl[pos]
	valueStart := pos + 1
	valueEndRel := strings.IndexByte(decl[valueStart:], quote)
	if valueEndRel < 0 {
		return raw
	}
	valueEnd := valueStart + valueEndRel
	if decl[valueStart:valueEnd] != "1.1" {
		return raw
	}

	out := bytes.Clone(raw)
	copy(out[start+valueStart:start+valueEnd], "1.0")
	return out
}

// BookMetadata maps decoded OPF metadata onto the common book metadata model.
func (metadata OPFMetadata) BookMetadata() *Metadata {
	meta := &Metadata{PageCount: OPFDeclaredPageCount(metadata.Meta)}
	refinements := opfRefinementsByTarget(metadata.Meta)

	meta.Title = opfTitleValue(metadata.Title, refinements)
	meta.SortTitle = opfTitleSort(metadata.Title, refinements)
	meta.Language = opfLanguage(metadata.Language)
	if len(metadata.Description) > 0 {
		meta.Description = strings.TrimSpace(metadata.Description[0])
	}
	if len(metadata.Publisher) > 0 {
		meta.Publisher = strings.TrimSpace(metadata.Publisher[0])
	}
	meta.Date = OPFPublicationDate(metadata.Date)

	var ids []Identifier
	for _, rawID := range metadata.Identifier {
		id := IdentifierFromOPF(rawID.Scheme, rawID.Text)
		if id.Value == "" || IsInternalIdentifier(id) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		meta.Identifier = FormatIdentifiers(ids)
	}

	meta.Authors = opfAuthors(metadata.Creators, refinements)

	for _, m := range metadata.Meta {
		name := OPFMetaName(m.Name)
		content := strings.TrimSpace(m.Content)
		if name == "calibre:title_sort" && meta.SortTitle == "" {
			meta.SortTitle = content
		}
		if name == "calibre:series" {
			meta.Series = content
		}
		if name == "calibre:series_index" {
			if v, err := strconv.ParseFloat(content, 64); err == nil {
				meta.SeriesIndex = v
			}
		}
		if name == "calibre:timestamp" {
			meta.CalibreTimestamp = content
		}
		if strings.EqualFold(strings.TrimSpace(m.Property), "calibre:timestamp") {
			meta.CalibreTimestamp = strings.TrimSpace(m.Text)
		}
	}
	// Prefer explicit legacy series fields when present, but let EPUB 3 fill in
	// the missing series name/index from belongs-to-collection metadata.
	if series, index, ok := opfSeries(metadata.Meta, refinements); ok {
		if meta.Series == "" {
			meta.Series = series
		}
		if meta.Series == series && meta.SeriesIndex == 0 && index != 0 {
			meta.SeriesIndex = index
		}
	}

	meta.Genres, meta.Tags = opfClassification(metadata)
	meta.Title = DecodeLegacyHexWrappedText(meta.Title)
	meta.SortTitle = DecodeLegacyHexWrappedText(meta.SortTitle)

	return meta
}

func opfLanguage(languages []string) string {
	for _, raw := range languages {
		if lang := NormalizeLanguage(raw); lang != "" {
			return lang
		}
	}
	return ""
}

func OPFMetaName(name string) string {
	name = strings.TrimSpace(name)
	const legacyCalibreNS = "{http://calibre.kovidgoyal.net/2009/metadata}"
	if after, ok := strings.CutPrefix(name, legacyCalibreNS); ok {
		name = "calibre:" + after
	}
	return strings.ToLower(name)
}

// EPUB 3 moved some metadata that OPF 2 exposed as attributes into generic
// <meta refines="#id" property="..."> records. Keep the small subset we map to
// the current Metadata shape instead of carrying the whole OPF graph around.
type opfRefinement struct {
	Roles            []string
	FileAs           string
	TitleType        string
	DisplaySeq       int
	HasDisplaySeq    bool
	CollectionType   string
	GroupPosition    float64
	HasGroupPosition bool
}

func opfRefinementsByTarget(metas []OPFMeta) map[string]opfRefinement {
	refinements := make(map[string]opfRefinement)
	for _, m := range metas {
		target := strings.TrimPrefix(strings.TrimSpace(m.Refines), "#")
		if target == "" {
			continue
		}
		property := strings.ToLower(strings.TrimSpace(m.Property))
		value := opfMetaValue(m)
		if property == "" || value == "" {
			continue
		}

		r := refinements[target]
		switch property {
		case "role":
			r.Roles = append(r.Roles, value)
		case "file-as":
			r.FileAs = value
		case "title-type":
			r.TitleType = strings.ToLower(value)
		case "display-seq":
			if seq, err := strconv.Atoi(value); err == nil {
				r.DisplaySeq = seq
				r.HasDisplaySeq = true
			}
		case "collection-type":
			r.CollectionType = strings.ToLower(value)
		case "group-position":
			if pos, err := strconv.ParseFloat(value, 64); err == nil {
				r.GroupPosition = pos
				r.HasGroupPosition = true
			}
		}
		refinements[target] = r
	}
	return refinements
}

func opfTitleValue(titles []OPFTitle, refinements map[string]opfRefinement) string {
	mainTitle := opfMainTitle(titles, refinements)
	if mainTitle == nil {
		return ""
	}

	title := strings.Join(strings.Fields(mainTitle.Text), " ")
	if title == "" {
		return ""
	}

	if subtitle := opfSubtitle(titles, refinements, mainTitle); subtitle != "" {
		title += ": " + subtitle
	}
	return title
}

func opfTitleSort(titles []OPFTitle, refinements map[string]opfRefinement) string {
	mainTitle := opfMainTitle(titles, refinements)
	if mainTitle == nil {
		return ""
	}
	if sortTitle := strings.TrimSpace(mainTitle.FileAs); sortTitle != "" {
		return sortTitle
	}
	return strings.TrimSpace(refinements[strings.TrimSpace(mainTitle.ID)].FileAs)
}

func opfMainTitle(titles []OPFTitle, refinements map[string]opfRefinement) *OPFTitle {
	var first *OPFTitle
	for i := range titles {
		if strings.Join(strings.Fields(titles[i].Text), " ") == "" {
			continue
		}
		if first == nil {
			first = &titles[i]
		}
		if refinements[strings.TrimSpace(titles[i].ID)].TitleType == "main" {
			return &titles[i]
		}
	}
	return first
}

func opfSubtitle(titles []OPFTitle, refinements map[string]opfRefinement, mainTitle *OPFTitle) string {
	for i := range titles {
		if &titles[i] == mainTitle {
			continue
		}
		titleType := refinements[strings.TrimSpace(titles[i].ID)].TitleType
		if strings.Contains(titleType, "subtitle") || strings.Contains(titleType, "sub-title") {
			return strings.Join(strings.Fields(titles[i].Text), " ")
		}
	}
	return ""
}

func opfAuthors(creators []OPFCreator, refinements map[string]opfRefinement) []AuthorMeta {
	type parsedAuthor struct {
		Author        AuthorMeta
		Index         int
		DisplaySeq    int
		HasDisplaySeq bool
	}

	var authorRoleCreators []parsedAuthor
	var unroledCreators []parsedAuthor
	var editorRoleCreators []parsedAuthor
	for i, c := range creators {
		name := strings.TrimSpace(c.Text)
		if name == "" {
			continue
		}

		refinement := refinements[strings.TrimSpace(c.ID)]
		sortName := strings.TrimSpace(c.FileAs)
		if sortName == "" {
			sortName = refinement.FileAs
		}
		// Some OPF sidecars mirror the combined author-sort string into each
		// creator's file-as. That combined string is not any single creator's
		// sort, so applying it per creator mislabels each one. Drop the combined
		// string so the per-creator sort derives from the name downstream.
		if strings.Contains(sortName, " & ") {
			sortName = ""
		}

		role := OPFCreatorRole(c.Role, refinement.Roles)

		parsed := parsedAuthor{
			Author: AuthorMeta{
				Name:     name,
				SortName: sortName,
				Role:     role,
			},
			Index:         i,
			DisplaySeq:    refinement.DisplaySeq,
			HasDisplaySeq: refinement.HasDisplaySeq,
		}

		switch {
		case opfRoleIsAuthor(role):
			authorRoleCreators = append(authorRoleCreators, parsed)
		case opfRoleIsEditor(role):
			editorRoleCreators = append(editorRoleCreators, parsed)
		case role == "":
			unroledCreators = append(unroledCreators, parsed)
		}
	}

	parsed := authorRoleCreators
	if len(parsed) == 0 {
		parsed = unroledCreators
	}
	if len(parsed) == 0 {
		parsed = editorRoleCreators
	}

	hasDisplaySeq := false
	for _, a := range parsed {
		if a.HasDisplaySeq {
			hasDisplaySeq = true
			break
		}
	}
	if hasDisplaySeq {
		sort.SliceStable(parsed, func(i, j int) bool {
			a, b := parsed[i], parsed[j]
			if a.HasDisplaySeq && b.HasDisplaySeq {
				if a.DisplaySeq != b.DisplaySeq {
					return a.DisplaySeq < b.DisplaySeq
				}
				return a.Index < b.Index
			}
			if a.HasDisplaySeq != b.HasDisplaySeq {
				return a.HasDisplaySeq
			}
			return a.Index < b.Index
		})
	}

	authors := make([]AuthorMeta, 0, len(parsed))
	for _, a := range parsed {
		authors = append(authors, a.Author)
	}
	return authors
}

func OPFRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if _, after, ok := strings.CutLast(role, ":"); ok {
		role = after
	}
	return role
}

func OPFCreatorRole(attributeRole string, refinedRoles []string) string {
	if role := OPFRole(attributeRole); role != "" {
		return role
	}
	var first string
	var editor string
	for _, raw := range refinedRoles {
		role := OPFRole(raw)
		if role == "" {
			continue
		}
		if first == "" {
			first = role
		}
		if opfRoleIsAuthor(role) {
			return role
		}
		if editor == "" && opfRoleIsEditor(role) {
			editor = role
		}
	}
	if editor != "" {
		return editor
	}
	return first
}

func opfRoleIsAuthor(role string) bool {
	return role == "aut" || role == "author"
}

func opfRoleIsEditor(role string) bool {
	return role == "edt" || role == "editor"
}

func opfSeries(metas []OPFMeta, refinements map[string]opfRefinement) (string, float64, bool) {
	type candidate struct {
		name       string
		index      float64
		typed      bool
		untypedSeq bool
	}

	// A collection can be a set, series, playlist, etc. Use collection-type
	// "series" when available; otherwise only fall back when group-position makes
	// the collection look sequence-like.
	var fallback *candidate
	for _, m := range metas {
		if strings.TrimSpace(m.Property) != "belongs-to-collection" {
			continue
		}
		name := opfMetaValue(m)
		if name == "" {
			continue
		}

		refinement := refinements[strings.TrimSpace(m.ID)]
		c := candidate{
			name:  name,
			index: refinement.GroupPosition,
			typed: refinement.CollectionType == "series",
		}
		if c.typed {
			return c.name, c.index, true
		}
		c.untypedSeq = refinement.CollectionType == "" && refinement.HasGroupPosition
		if fallback == nil && c.untypedSeq {
			copy := c
			fallback = &copy
		}
	}

	if fallback != nil {
		return fallback.name, fallback.index, true
	}
	return "", 0, false
}

func opfMetaValue(m OPFMeta) string {
	if v := strings.TrimSpace(m.Text); v != "" {
		return v
	}
	return strings.TrimSpace(m.Content)
}
