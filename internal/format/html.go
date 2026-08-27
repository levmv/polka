package format

import (
	"archive/zip"
	"bytes"
	"cmp"
	"encoding/base64"
	"fmt"
	stdhtml "html"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"

	"github.com/levmv/polka/internal/bookmeta"
)

const maxHTMLMetadataBytes = 150000

var (
	htmlTitleRe   = regexp.MustCompile(`(?is)<\s*title\b[^>]*>(.*?)<\s*/\s*title\s*>`)
	htmlMetaRe    = regexp.MustCompile(`(?is)<\s*meta\b([^>]*)>`)
	htmlLinkRe    = regexp.MustCompile(`(?is)<\s*link\b([^>]*)>`)
	htmlCommentRe = regexp.MustCompile(`(?is)<!--(.*?)-->`)
	htmlAttrRe    = regexp.MustCompile(`(?is)([^\s=<>"']+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+))`)
)

func isHTML(r io.ReaderAt, size int64) bool {
	sample, err := readAtMost(r, size, maxHTMLMetadataBytes)
	if err != nil {
		return false
	}
	return isHTMLSample(sample)
}

func isHTMLSample(sample []byte) bool {
	if len(sample) == 0 || hasBinarySignature(sample) {
		return false
	}
	decoded, err := DecodeHTMLToUTF8(sample)
	if err != nil {
		decoded = trimUTF8BOM(sample)
	}
	if !isPlainTextSample(decoded) {
		return false
	}
	lower := strings.ToLower(string(trimUTF8BOM(decoded)))
	return strings.Contains(lower, "<!doctype html") ||
		strings.Contains(lower, "<html") ||
		strings.Contains(lower, "<head") ||
		strings.Contains(lower, "<body") ||
		strings.Contains(lower, "<title")
}

func isHTMLZ(r io.ReaderAt, size int64) bool {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false
	}
	entry := HTMLZIndexEntry(zr)
	if entry == nil {
		return false
	}
	return zipEntryLooksHTML(entry)
}

func zipEntryLooksHTML(f *zip.File) bool {
	rc, err := f.Open()
	if err != nil {
		return false
	}
	defer rc.Close()
	sample, err := io.ReadAll(io.LimitReader(rc, maxHTMLMetadataBytes))
	if err != nil {
		return false
	}
	return isHTMLSample(sample)
}

// HTMLZIndexEntry returns the top-level HTML entry Polka treats as the HTMLZ
// document, preferring conventional index names and otherwise lowest name.
func HTMLZIndexEntry(zr *zip.Reader) *zip.File {
	var best *zip.File
	var bestName string
	for _, f := range zr.File {
		name := NormalizeZipName(f.Name)
		if !isTopLevelHTMLName(name) || f.FileInfo().IsDir() {
			continue
		}
		lower := strings.ToLower(name)
		if isHTMLIndexName(lower) {
			return f
		}
		if best == nil || lower < bestName {
			best = f
			bestName = lower
		}
	}
	return best
}

func isTopLevelHTMLName(name string) bool {
	if name == "" || strings.Contains(name, "/") || strings.HasSuffix(name, "/") {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm", ".xhtml", ".xhtm":
		return true
	default:
		return false
	}
}

func isHTMLIndexName(name string) bool {
	switch name {
	case "index.html", "index.htm", "index.xhtml", "index.xhtm":
		return true
	default:
		return false
	}
}

// ExtractHTMLMetadata reads simple HTML metadata from an HTML/XHTML document.
func ExtractHTMLMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	raw, err := readAtMost(r, size, maxHTMLMetadataBytes)
	if err != nil {
		return nil, err
	}
	return MetadataFromHTML(raw), nil
}

// ExtractHTMLZMetadata reads root metadata.opf from a HTMLZ archive when present,
// otherwise it falls back to the top-level HTML document metadata.
func ExtractHTMLZMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	if opf := FirstRootOPF(zr); opf != nil {
		raw, err := readZipFileLimited(opf, maxOPFDocumentBytes)
		if err != nil {
			return nil, err
		}
		return ParseOPF(bytes.NewReader(raw))
	}
	entry := HTMLZIndexEntry(zr)
	if entry == nil {
		return &Metadata{}, nil
	}
	raw, err := readZipFileLimited(entry, maxHTMLMetadataBytes)
	if err != nil {
		return nil, err
	}
	return MetadataFromHTML(raw), nil
}

func ExtractHTMLCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	raw, err := readAtMost(r, size, maxHTMLMetadataBytes)
	if err != nil {
		return nil, "", err
	}
	raw, err = DecodeHTMLToUTF8(raw)
	if err != nil {
		return nil, "", err
	}
	if href := htmlCoverHref(raw); href != "" {
		return imageFromDataURI(href)
	}
	if href := firstImageHref(bytes.NewReader(raw)); href != "" {
		return imageFromDataURI(href)
	}
	return nil, "", nil
}

func ExtractHTMLZCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, "", err
	}
	if opf := FirstRootOPF(zr); opf != nil {
		raw, err := readZipFileLimited(opf, maxOPFDocumentBytes)
		if err != nil {
			return nil, "", err
		}
		var doc opfDoc
		if err := decodeOPFBytes(raw, &doc); err != nil {
			return nil, "", fmt.Errorf("parse HTMLZ OPF %s: %w", opf.Name, err)
		}
		if cover, ext, err := epubCoverFromOPF(zr, NormalizeZipName(opf.Name), doc); err != nil || len(cover) > 0 {
			return cover, ext, err
		}
	}
	if entry := HTMLZIndexEntry(zr); entry != nil {
		cover, ext, err := htmlZCoverFromHTML(zr, entry)
		if err != nil || len(cover) > 0 {
			return cover, ext, err
		}
	}
	for _, href := range conventionalCoverHrefs {
		if cover, ext, err := readEPUBImageByHref(zr, "", href); err != nil || len(cover) > 0 {
			return cover, ext, err
		}
	}
	return nil, "", nil
}

func htmlZCoverFromHTML(zr *zip.Reader, entry *zip.File) ([]byte, string, error) {
	rc, err := entry.Open()
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()

	decoded, err := htmlReaderToUTF8(rc)
	if err != nil {
		return nil, "", err
	}
	href := firstImageHref(decoded)
	if href == "" {
		return nil, "", nil
	}
	if cover, ext, err := imageFromDataURI(href); err != nil || len(cover) > 0 {
		return cover, ext, err
	}
	return readEPUBImageByHref(zr, NormalizeZipName(entry.Name), href)
}

func htmlCoverHref(raw []byte) string {
	src := string(raw)
	for _, match := range htmlLinkRe.FindAllStringSubmatch(src, -1) {
		attrs := htmlAttributes(match[1])
		rel := strings.ToLower(attrs["rel"])
		if !strings.Contains(" "+rel+" ", " cover ") && !strings.Contains(" "+rel+" ", " image_src ") {
			continue
		}
		if href := strings.TrimSpace(attrs["href"]); href != "" {
			return href
		}
	}
	return ""
}

func imageFromDataURI(href string) ([]byte, string, error) {
	href = strings.TrimSpace(href)
	if !strings.HasPrefix(strings.ToLower(href), "data:") {
		return nil, "", nil
	}
	meta, encoded, ok := strings.Cut(href[5:], ",")
	if !ok || !strings.Contains(strings.ToLower(meta), ";base64") {
		return nil, "", nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, "", err
	}
	return normalizedImageBytes(data)
}

func normalizedImageBytes(data []byte) ([]byte, string, error) {
	if ext, ok := coverImageExtensionFromBytes(data); ok {
		return data, ext, nil
	}
	return nil, "", nil
}

func MetadataFromHTML(raw []byte) *Metadata {
	decoded, err := DecodeHTMLToUTF8(raw)
	if err != nil {
		decoded = trimUTF8BOM(raw)
	}
	src := string(trimUTF8BOM(decoded))
	metaTags := map[string][]string{}
	commentTags := map[string][]string{}

	for _, match := range htmlMetaRe.FindAllStringSubmatch(src, -1) {
		attrs := htmlAttributes(match[1])
		name := attrs["name"]
		if name == "" {
			name = attrs["property"]
		}
		content := strings.TrimSpace(attrs["content"])
		if name == "" || content == "" {
			continue
		}
		field, ok := htmlMetaField(name)
		if !ok {
			if scheme := htmlIdentifierScheme(name, attrs["scheme"]); scheme != "" {
				metaTags["identifier:"+scheme] = append(metaTags["identifier:"+scheme], content)
			}
			continue
		}
		metaTags[field] = append(metaTags[field], content)
	}

	for _, match := range htmlCommentRe.FindAllStringSubmatch(src, -1) {
		attrs := htmlAttributes(match[1])
		for name, content := range attrs {
			field, ok := htmlCommentField(name)
			if !ok {
				continue
			}
			content = strings.TrimSpace(content)
			if content != "" {
				commentTags[field] = append(commentTags[field], content)
			}
		}
	}

	title := firstHTMLMetadataValue(commentTags, metaTags, "title")
	if title == "" {
		if match := htmlTitleRe.FindStringSubmatch(src); len(match) > 1 {
			title = htmlText(match[1])
		}
	}

	meta := &Metadata{Title: title}
	for _, author := range htmlAuthors(firstHTMLMetadataValues(commentTags, metaTags, "authors")) {
		meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
	}
	meta.Publisher = firstHTMLMetadataValue(commentTags, metaTags, "publisher")
	meta.Language = bookmeta.NormalizeLanguage(firstHTMLMetadataValue(commentTags, metaTags, "language"))
	meta.Description = firstHTMLMetadataValue(commentTags, metaTags, "description")
	meta.Date = bookmeta.NormalizeMetadataDate(firstHTMLMetadataValue(commentTags, metaTags, "date"))
	meta.Series = firstHTMLMetadataValue(commentTags, metaTags, "series")
	if rawIndex := firstHTMLMetadataValue(commentTags, metaTags, "series_index"); rawIndex != "" {
		if index, err := strconv.ParseFloat(rawIndex, 64); err == nil {
			meta.SeriesIndex = index
		}
	}
	meta.Tags = htmlTags(firstHTMLMetadataValues(commentTags, metaTags, "tags"))
	meta.Identifier = htmlIdentifier(metaTags)
	return meta
}

func htmlAttributes(src string) map[string]string {
	attrs := map[string]string{}
	for _, match := range htmlAttrRe.FindAllStringSubmatch(src, -1) {
		name := strings.ToLower(strings.TrimSpace(match[1]))
		value := match[2]
		if value == "" {
			value = match[3]
		}
		if value == "" {
			value = match[4]
		}
		if name != "" {
			attrs[name] = htmlText(value)
		}
	}
	return attrs
}

func htmlMetaField(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.ReplaceAll(key, ":", ".")
	switch key {
	case "title", "dc.title", "dcterms.title":
		return "title", true
	case "author", "dc.creator", "dcterms.creator", "dc.creator.aut", "dcterms.creator.aut":
		return "authors", true
	case "publisher", "dc.publisher", "dcterms.publisher":
		return "publisher", true
	case "language", "dc.language", "dcterms.language":
		return "language", true
	case "description", "comments", "dc.description", "dcterms.description":
		return "description", true
	case "pubdate", "date", "date of publication", "dc.date.published", "dc.date.publication", "dc.date.issued", "dcterms.issued":
		return "date", true
	case "series":
		return "series", true
	case "seriesnumber", "series_index", "series.index":
		return "series_index", true
	case "tags", "subject", "dc.subject", "dcterms.subject":
		return "tags", true
	case "isbn":
		return "identifier:isbn", true
	default:
		return "", false
	}
}

func htmlIdentifierScheme(name, scheme string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	normalized := strings.ReplaceAll(key, ":", ".")
	if scheme != "" && (normalized == "dc.identifier" || normalized == "dcterms.identifier") {
		return strings.ToLower(strings.TrimSpace(scheme))
	}
	for _, prefix := range []string{"dc.identifier.", "dcterms.identifier."} {
		if after, ok := strings.CutPrefix(normalized, prefix); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

func htmlCommentField(name string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "TITLE":
		return "title", true
	case "AUTHOR", "AUTHORS":
		return "authors", true
	case "PUBLISHER":
		return "publisher", true
	case "LANGUAGE":
		return "language", true
	case "COMMENTS":
		return "description", true
	case "PUBDATE", "TIMESTAMP":
		return "date", true
	case "SERIES":
		return "series", true
	case "SERIESNUMBER":
		return "series_index", true
	case "TAGS":
		return "tags", true
	case "ISBN":
		return "identifier:isbn", true
	default:
		return "", false
	}
}

func firstHTMLMetadataValue(commentTags, metaTags map[string][]string, field string) string {
	values := firstHTMLMetadataValues(commentTags, metaTags, field)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func firstHTMLMetadataValues(commentTags, metaTags map[string][]string, field string) []string {
	if values := nonEmptyHTMLValues(commentTags[field]); len(values) > 0 {
		return values
	}
	return nonEmptyHTMLValues(metaTags[field])
}

func nonEmptyHTMLValues(values []string) []string {
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func htmlAuthors(values []string) []string {
	var authors []string
	for _, value := range values {
		for _, author := range bookmeta.ParseAuthorList(value) {
			if author != "" {
				authors = append(authors, author)
			}
		}
	}
	return authors
}

func htmlTags(values []string) []string {
	var tags []string
	for _, value := range values {
		for tag := range strings.SplitSeq(value, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	return tags
}

func htmlIdentifier(metaTags map[string][]string) string {
	var keys []string
	for key := range metaTags {
		if !strings.HasPrefix(key, "identifier:") {
			continue
		}
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if rank := cmp.Compare(htmlIdentifierRank(a), htmlIdentifierRank(b)); rank != 0 {
			return rank
		}
		return strings.Compare(a, b)
	})

	var ids []bookmeta.Identifier
	for _, key := range keys {
		values := metaTags[key]
		value := ""
		for _, candidate := range values {
			if strings.TrimSpace(candidate) != "" {
				value = strings.TrimSpace(candidate)
				break
			}
		}
		if value == "" {
			continue
		}
		identifier := bookmeta.IdentifierFromOPF(strings.TrimPrefix(key, "identifier:"), value)
		if identifier.Value != "" && !bookmeta.IsInternalIdentifier(identifier) {
			ids = append(ids, identifier)
		}
	}
	return bookmeta.FormatIdentifiers(ids)
}

func htmlIdentifierRank(key string) int {
	switch key {
	case "identifier:isbn":
		return 0
	case "identifier:doi":
		return 1
	case "identifier:url":
		return 2
	default:
		return 100
	}
}

func htmlText(src string) string {
	return strings.TrimSpace(stdhtml.UnescapeString(stripHTMLTags(src)))
}

func stripHTMLTags(src string) string {
	var b strings.Builder
	inTag := false
	for _, r := range src {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// DecodeHTMLToUTF8 applies the HTML charset sniffing rules before metadata
// extraction or sanitizer parsing. It handles BOMs, <meta charset>, http-equiv
// charset declarations, valid UTF-8 sniffing, and the HTML default encoding.
func DecodeHTMLToUTF8(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !htmlHasEncodingSignal(raw) && !utf8.Valid(raw) {
		return []byte(DecodeTextToUTF8(raw)), nil
	}
	r, err := htmlReaderToUTF8(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	decoded, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return trimUTF8BOM(decoded), nil
}

func htmlHasEncodingSignal(raw []byte) bool {
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) || hasUTF16BOM(raw) {
		return true
	}
	if len(raw) > 4096 {
		raw = raw[:4096]
	}
	for _, match := range htmlMetaRe.FindAllStringSubmatch(string(raw), -1) {
		attrs := htmlAttributes(match[1])
		if strings.TrimSpace(attrs["charset"]) != "" {
			return true
		}
		content := strings.ToLower(attrs["content"])
		httpEquiv := strings.ToLower(attrs["http-equiv"])
		if strings.Contains(content, "charset") && strings.Contains(httpEquiv, "content-type") {
			return true
		}
	}
	return false
}

func htmlReaderToUTF8(r io.Reader) (io.Reader, error) {
	return charset.NewReader(r, "")
}

func trimUTF8BOM(raw []byte) []byte {
	return bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
}

func readAtMost(r io.ReaderAt, size int64, maxBytes int64) ([]byte, error) {
	if size <= 0 {
		return nil, nil
	}
	n := min(size, maxBytes)
	buf := make([]byte, n)
	read, err := r.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}
