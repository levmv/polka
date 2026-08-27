package format

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

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/xmlutil"
)

// OPF (Open Packaging Format) parsing. The same Dublin-Core package document is
// used both inside an EPUB (the rootfile) and as a standalone metadata.opf
// sidecar, so the types and the opf->Metadata mapping live here and are shared by
// the EPUB reader and the sidecar reader.

const maxOPFDocumentBytes = 2 << 20

var opfXMLEncodingDeclRe = regexp.MustCompile(`(?is)^(?:\xef\xbb\xbf)?\s*<\?xml\b[^?]*?\bencoding\s*=\s*(?:"([^"]*)"|'([^']*)')`)

type opfDoc struct {
	Metadata opfMetadata `xml:"metadata"`
	Manifest opfManifest `xml:"manifest"`
	Spine    opfSpine    `xml:"spine"`
	Guide    opfGuide    `xml:"guide"`
}

func (d *opfDoc) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	if sameXMLName(start.Name, "metadata") || sameXMLName(start.Name, "dc-metadata") {
		var metadata opfMetadata
		if err := dec.DecodeElement(&metadata, &start); err != nil {
			return err
		}
		d.Metadata = metadata
		return nil
	}

	var doc struct {
		Metadata       opfMetadata `xml:"metadata"`
		LegacyMetadata opfMetadata `xml:"dc-metadata"`
		Manifest       opfManifest `xml:"manifest"`
		Spine          opfSpine    `xml:"spine"`
		Guide          opfGuide    `xml:"guide"`
	}
	if err := dec.DecodeElement(&doc, &start); err != nil {
		return err
	}
	d.Metadata = doc.Metadata
	if opfMetadataEmpty(d.Metadata) {
		d.Metadata = doc.LegacyMetadata
	}
	d.Manifest = doc.Manifest
	d.Spine = doc.Spine
	d.Guide = doc.Guide
	return nil
}

type opfManifest struct {
	Items []opfItem `xml:"item"`
}

type opfSpine struct {
	Itemrefs []opfItemref `xml:"itemref"`
}

type opfItemref struct {
	IDRef string `xml:"idref,attr"`
}

type opfGuide struct {
	References []opfGuideReference `xml:"reference"`
}

type opfGuideReference struct {
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type opfItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type opfMetadata struct {
	Title       []opfTitle      `xml:"title"`
	Creators    []opfCreator    `xml:"creator"`
	Language    []string        `xml:"language"`
	Description []string        `xml:"description"`
	Publisher   []string        `xml:"publisher"`
	Date        []string        `xml:"date"`
	Identifier  []opfIdentifier `xml:"identifier"`
	Subject     []string        `xml:"subject"`
	Meta        []opfMeta       `xml:"meta"`
}

func (m *opfMetadata) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	var parsed opfMetadata
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(tok.Name.Local) {
			case "metadata", "dc-metadata", "x-metadata":
				var nested opfMetadata
				if err := dec.DecodeElement(&nested, &tok); err != nil {
					return err
				}
				parsed.merge(nested)
			case "title":
				var value opfTitle
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Title = append(parsed.Title, value)
			case "creator":
				var value opfCreator
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
				var value string
				if err := dec.DecodeElement(&value, &tok); err != nil {
					return err
				}
				parsed.Date = append(parsed.Date, value)
			case "identifier":
				var value opfIdentifier
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
				var value opfMeta
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

func (m *opfMetadata) merge(other opfMetadata) {
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

func opfMetadataEmpty(m opfMetadata) bool {
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

func sameXMLName(name xml.Name, local string) bool {
	return strings.EqualFold(name.Local, local)
}

type opfTitle struct {
	Text   string `xml:",chardata"`
	FileAs string `xml:"file-as,attr"`
	ID     string `xml:"id,attr"`
}

type opfCreator struct {
	Text   string `xml:",chardata"`
	FileAs string `xml:"file-as,attr"`
	Role   string `xml:"role,attr"`
	ID     string `xml:"id,attr"`
}

type opfIdentifier struct {
	Text   string `xml:",chardata"`
	Scheme string `xml:"scheme,attr"`
	ID     string `xml:"id,attr"`
}

type opfMeta struct {
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
	raw, err := readAllLimited(r, "OPF document", maxOPFDocumentBytes)
	if err != nil {
		return nil, err
	}
	return parseOPFBytes(raw)
}

func parseOPFBytes(raw []byte) (*Metadata, error) {
	var opf opfDoc
	if err := decodeOPFBytes(raw, &opf); err != nil {
		return nil, err
	}
	return metadataFromOPF(opf), nil
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

func decodeOPFBytes(raw []byte, opf *opfDoc) error {
	return DecodeOPFXML(raw, opf)
}

func normalizeOPFXMLDeclaration(raw []byte) []byte {
	start := 0
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		start = 3
	}
	for start < len(raw) && isXMLSpace(raw[start]) {
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
	for pos < len(decl) && isXMLSpace(decl[pos]) {
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

func isXMLSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

// metadataFromOPF maps a decoded OPF document onto our Metadata struct.
func metadataFromOPF(opf opfDoc) *Metadata {
	meta := &Metadata{}
	refinements := opfRefinementsByTarget(opf.Metadata.Meta)

	meta.Title = opfTitleValue(opf.Metadata.Title, refinements)
	meta.SortTitle = opfTitleSort(opf.Metadata.Title, refinements)
	meta.Language = opfLanguage(opf.Metadata.Language)
	if len(opf.Metadata.Description) > 0 {
		meta.Description = strings.TrimSpace(opf.Metadata.Description[0])
	}
	if len(opf.Metadata.Publisher) > 0 {
		meta.Publisher = strings.TrimSpace(opf.Metadata.Publisher[0])
	}
	meta.Date = opfDate(opf.Metadata.Date)

	var ids []bookmeta.Identifier
	for _, rawID := range opf.Metadata.Identifier {
		id := bookmeta.IdentifierFromOPF(rawID.Scheme, rawID.Text)
		if id.Value == "" || bookmeta.IsInternalIdentifier(id) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		meta.Identifier = bookmeta.FormatIdentifiers(ids)
	}

	meta.Authors = opfAuthors(opf.Metadata.Creators, refinements)

	for _, m := range opf.Metadata.Meta {
		name := opfMetaName(m.Name)
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
	}
	// Prefer explicit legacy series fields when present, but let EPUB 3 fill in
	// the missing series name/index from belongs-to-collection metadata.
	if series, index, ok := opfSeries(opf.Metadata.Meta, refinements); ok {
		if meta.Series == "" {
			meta.Series = series
		}
		if meta.Series == series && meta.SeriesIndex == 0 && index != 0 {
			meta.SeriesIndex = index
		}
	}

	meta.Tags = opfTags(opf.Metadata.Subject)

	return meta
}

func opfLanguage(languages []string) string {
	for _, raw := range languages {
		if lang := bookmeta.NormalizeLanguage(raw); lang != "" {
			return lang
		}
	}
	return ""
}

func opfMetaName(name string) string {
	name = strings.TrimSpace(name)
	const legacyCalibreNS = "{http://calibre.kovidgoyal.net/2009/metadata}"
	if after, ok := strings.CutPrefix(name, legacyCalibreNS); ok {
		name = "calibre:" + after
	}
	return strings.ToLower(name)
}

func opfDate(dates []string) string {
	var best string
	for _, raw := range dates {
		date := bookmeta.NormalizeMetadataDate(strings.TrimSpace(raw))
		if date == "" {
			continue
		}
		// Normalized dates are YYYY, YYYY-MM, or YYYY-MM-DD, so lexical order
		// follows chronological order while preserving the original precision.
		if best == "" || date < best {
			best = date
		}
	}
	return best
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

func opfRefinementsByTarget(metas []opfMeta) map[string]opfRefinement {
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

func opfTitleValue(titles []opfTitle, refinements map[string]opfRefinement) string {
	mainTitle := opfMainTitle(titles, refinements)
	if mainTitle == nil {
		return ""
	}

	title := cleanText(mainTitle.Text)
	if title == "" {
		return ""
	}

	if subtitle := opfSubtitle(titles, refinements, mainTitle); subtitle != "" {
		title += ": " + subtitle
	}
	return title
}

func opfTitleSort(titles []opfTitle, refinements map[string]opfRefinement) string {
	mainTitle := opfMainTitle(titles, refinements)
	if mainTitle == nil {
		return ""
	}
	if sortTitle := strings.TrimSpace(mainTitle.FileAs); sortTitle != "" {
		return sortTitle
	}
	return strings.TrimSpace(refinements[strings.TrimSpace(mainTitle.ID)].FileAs)
}

func opfMainTitle(titles []opfTitle, refinements map[string]opfRefinement) *opfTitle {
	var first *opfTitle
	for i := range titles {
		if cleanText(titles[i].Text) == "" {
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

func opfSubtitle(titles []opfTitle, refinements map[string]opfRefinement, mainTitle *opfTitle) string {
	for i := range titles {
		if &titles[i] == mainTitle {
			continue
		}
		titleType := refinements[strings.TrimSpace(titles[i].ID)].TitleType
		if strings.Contains(titleType, "subtitle") || strings.Contains(titleType, "sub-title") {
			return cleanText(titles[i].Text)
		}
	}
	return ""
}

func opfAuthors(creators []opfCreator, refinements map[string]opfRefinement) []bookmeta.AuthorMeta {
	type parsedAuthor struct {
		Author        bookmeta.AuthorMeta
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

		role := opfCreatorRole(c.Role, refinement.Roles)

		parsed := parsedAuthor{
			Author: bookmeta.AuthorMeta{
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

	authors := make([]bookmeta.AuthorMeta, 0, len(parsed))
	for _, a := range parsed {
		authors = append(authors, a.Author)
	}
	return authors
}

func opfRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if _, after, ok := strings.CutLast(role, ":"); ok {
		role = after
	}
	return role
}

func opfCreatorRole(attributeRole string, refinedRoles []string) string {
	if role := opfRole(attributeRole); role != "" {
		return role
	}
	var first string
	var editor string
	for _, raw := range refinedRoles {
		role := opfRole(raw)
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

func opfTags(subjects []string) []string {
	var tags []string
	seenTags := make(map[string]bool)
	for _, s := range subjects {
		for rawTag := range strings.SplitSeq(s, ",") {
			tag := strings.TrimSpace(rawTag)
			if tag == "" {
				continue
			}
			lower := strings.ToLower(tag)
			if !seenTags[lower] {
				seenTags[lower] = true
				tags = append(tags, tag)
			}
		}
	}
	return tags
}

func opfSeries(metas []opfMeta, refinements map[string]opfRefinement) (string, float64, bool) {
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

func opfMetaValue(m opfMeta) string {
	if v := strings.TrimSpace(m.Text); v != "" {
		return v
	}
	return strings.TrimSpace(m.Content)
}
