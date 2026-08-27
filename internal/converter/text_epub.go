package converter

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
)

const epubModified = "1970-01-01T00:00:00Z"

func convertTextSourceToEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, from format.Format, size int64, opts ConversionOptions) error {
	raw, sourceFormat, meta, err := readEPUBTextSource(ctx, src, from, size)
	if err != nil {
		return err
	}
	meta = epubMetadataWithFallback(meta, opts)

	text := format.DecodeTextToUTF8(raw)
	text = cleanTextForEPUB(text)
	doc, err := epubDocumentForText(sourceFormat, text)
	if err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	return writeSimpleEPUBWithNav(ctx, w, doc.Body, meta, doc.Nav)
}

func readEPUBTextSource(ctx context.Context, src io.ReaderAt, from format.Format, size int64) ([]byte, format.Format, epubMetadata, error) {
	if size < 0 {
		return nil, format.FormatUnknown, epubMetadata{}, fmt.Errorf("source size is invalid")
	}
	meta := epubMetadata{}

	if from == format.FormatTXTZ {
		// metadata.opf is optional; a malformed file should not block converting
		// a readable text entry.
		if extracted, err := format.ExtractTXTZMetadata(src, size); err == nil {
			meta = toEPUBMetadata(extracted)
		}
		if err := checkContext(ctx); err != nil {
			return nil, format.FormatUnknown, meta, err
		}
		raw, sourceFormat, err := format.ReadTXTZTextContextLimited(ctx, src, size, maxConverterDecodedInputBytes)
		if err != nil {
			if errors.Is(err, format.ErrTextTooLarge) {
				return nil, format.FormatUnknown, meta, fmt.Errorf("read TXTZ text source: %w", ErrInputTooLarge)
			}
			return nil, format.FormatUnknown, meta, fmt.Errorf("read TXTZ text source: %w", err)
		}
		if err := checkContext(ctx); err != nil {
			return nil, format.FormatUnknown, meta, err
		}
		return raw, sourceFormat, meta, nil
	}
	if from == format.FormatMarkdown {
		if extracted, err := format.ExtractMarkdownMetadata(src, size); err == nil {
			meta = toEPUBMetadata(extracted)
		}
	}

	raw, err := readSectionContextLimited(ctx, src, size, maxConverterDecodedInputBytes, "text source")
	if err != nil {
		return nil, format.FormatUnknown, meta, fmt.Errorf("read text source: %w", err)
	}
	return raw, from, meta, nil
}

type epubTextDocument struct {
	Body string
	Nav  []epubNavItem
}

func epubDocumentForText(sourceFormat format.Format, text string) (epubTextDocument, error) {
	switch sourceFormat {
	case format.FormatTXT:
		return epubTextDocument{Body: plainTextBody(text)}, nil
	case format.FormatMarkdown:
		return markdownDocument(text), nil
	case format.FormatTextile:
		// TXTZ can contain Textile entries. Until Polka has a real Textile
		// parser, keep the export readable and escaped instead of exposing a
		// failing TXTZ -> EPUB action.
		return epubTextDocument{Body: plainTextBody(text)}, nil
	default:
		return epubTextDocument{}, fmt.Errorf("unsupported EPUB conversion source %s", format.FormatLabel(sourceFormat))
	}
}

func cleanTextForEPUB(text string) string {
	text = normalizeLineEndings(text)
	text = stripEPUBControlChars(text)
	return collapseBlankLineRuns(text, 2)
}

func stripEPUBControlChars(text string) string {
	var out strings.Builder
	for _, r := range text {
		if r == '\n' || r == '\t' || r >= 0x20 {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func collapseBlankLineRuns(text string, maxBlankLines int) string {
	lines := strings.Split(text, "\n")
	var out []string
	blankRun := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun > maxBlankLines {
				continue
			}
		} else {
			blankRun = 0
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func plainTextBody(text string) string {
	lines := strings.Split(normalizeLineEndings(text), "\n")
	if plainTextLooksLineParagraphs(lines) {
		return plainTextLineParagraphBody(lines)
	}
	var out strings.Builder
	var paragraph []string

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		out.WriteString("<p>")
		out.WriteString(escapePlainText(strings.Join(paragraph, " ")))
		out.WriteString("</p>\n")
		paragraph = paragraph[:0]
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flushParagraph()
			continue
		}
		paragraph = append(paragraph, strings.TrimRight(line, " \t"))
	}
	flushParagraph()
	if out.Len() == 0 {
		return "<p></p>\n"
	}
	return out.String()
}

func plainTextLooksLineParagraphs(lines []string) bool {
	var nonEmpty []string
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	for _, line := range lines[start:end] {
		line = strings.TrimSpace(line)
		if line == "" {
			return false
		}
		nonEmpty = append(nonEmpty, line)
	}
	if len(nonEmpty) < 6 {
		return false
	}
	punctuated := 0
	for _, line := range nonEmpty {
		if plainTextLineEndsSentence(line) {
			punctuated++
		}
	}
	return punctuated*3 >= len(nonEmpty)*2
}

func plainTextLineEndsSentence(line string) bool {
	line = strings.TrimRight(line, ` '"”’)]}`)
	if line == "" {
		return false
	}
	switch line[len(line)-1] {
	case '.', '!', '?', ':', ';':
		return true
	default:
		return false
	}
}

func plainTextLineParagraphBody(lines []string) string {
	var out strings.Builder
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out.WriteString("<p>")
		out.WriteString(escapePlainText(strings.TrimRight(line, " \t")))
		out.WriteString("</p>\n")
	}
	if out.Len() == 0 {
		return "<p></p>\n"
	}
	return out.String()
}

func escapePlainText(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		switch text[0] {
		case ' ':
			out.WriteString("&#160;")
			text = text[1:]
		case '\t':
			out.WriteString("&#160;&#160;&#160;&#160;")
			text = text[1:]
		default:
			out.WriteString(html.EscapeString(text))
			return out.String()
		}
	}
	return out.String()
}

func markdownDocument(text string) epubTextDocument {
	lines := strings.Split(normalizeLineEndings(text), "\n")
	var out strings.Builder
	var paragraph []string
	var list *markdownList
	var quote []string
	var nav []epubNavItem
	usedIDs := map[string]bool{}
	metadataPrefix := true

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		out.WriteString("<p>")
		out.WriteString(markdownInline(strings.Join(paragraph, " ")))
		out.WriteString("</p>\n")
		paragraph = paragraph[:0]
	}
	flushList := func() {
		if list == nil || len(list.Items) == 0 {
			list = nil
			return
		}
		tag := "ul"
		if list.Ordered {
			tag = "ol"
		}
		fmt.Fprintf(&out, "<%s>\n", tag)
		for _, item := range list.Items {
			fmt.Fprintf(&out, "<li>%s</li>\n", markdownInline(item))
		}
		fmt.Fprintf(&out, "</%s>\n", tag)
		list = nil
	}
	flushQuote := func() {
		if len(quote) == 0 {
			return
		}
		out.WriteString("<blockquote>\n<p>")
		out.WriteString(markdownInline(strings.Join(quote, " ")))
		out.WriteString("</p>\n</blockquote>\n")
		quote = quote[:0]
	}
	flushBlocks := func() {
		flushParagraph()
		flushList()
		flushQuote()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flushBlocks()
			continue
		}
		if level, heading, ok := markdownHeading(trimmed); ok {
			flushBlocks()
			title := markdownInlineText(heading)
			if title == "" {
				title = heading
			}
			id := nextMarkdownHeadingID(title, usedIDs, len(nav)+1)
			_, _, metadataHeading := format.MarkdownMetadataLabel(heading)
			metadataHeading = metadataPrefix && metadataHeading
			if !metadataHeading {
				metadataPrefix = false
				nav = append(nav, epubNavItem{
					Title: title,
					Href:  "text.xhtml#" + id,
				})
			}
			fmt.Fprintf(&out, `<h%d id="%s">%s</h%d>`+"\n", level, html.EscapeString(id), markdownInline(heading), level)
			continue
		}
		if markdownThematicBreak(trimmed) {
			flushBlocks()
			metadataPrefix = false
			out.WriteString("<hr/>\n")
			continue
		}
		if text, ok := markdownBlockquote(trimmed); ok {
			flushParagraph()
			flushList()
			metadataPrefix = false
			quote = append(quote, text)
			continue
		}
		if ordered, item, ok := markdownListItem(trimmed); ok {
			flushParagraph()
			flushQuote()
			metadataPrefix = false
			if list == nil || list.Ordered != ordered {
				flushList()
				list = &markdownList{Ordered: ordered}
			}
			list.Items = append(list.Items, item)
			continue
		}
		flushList()
		flushQuote()
		metadataPrefix = false
		paragraph = append(paragraph, trimmed)
	}
	flushBlocks()
	if out.Len() == 0 {
		return epubTextDocument{Body: "<p></p>\n"}
	}
	return epubTextDocument{Body: out.String(), Nav: nav}
}

type markdownList struct {
	Ordered bool
	Items   []string
}

func markdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	heading := strings.TrimSpace(line[level+1:])
	if heading == "" {
		return 0, "", false
	}
	return level, heading, true
}

func nextMarkdownHeadingID(title string, used map[string]bool, fallback int) string {
	base := markdownHeadingIDBase(title)
	if base == "" {
		base = fmt.Sprintf("heading-%d", fallback)
	}
	id := base
	for seq := 2; used[id]; seq++ {
		id = fmt.Sprintf("%s-%d", base, seq)
	}
	used[id] = true
	return id
}

func markdownHeadingIDBase(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	var out strings.Builder
	separator := false
	for _, r := range title {
		switch {
		case unicode.IsLetter(r):
			if separator && out.Len() > 0 {
				out.WriteByte('-')
			}
			out.WriteRune(unicode.ToLower(r))
			separator = false
		case unicode.IsDigit(r):
			if separator && out.Len() > 0 {
				out.WriteByte('-')
			}
			out.WriteRune(r)
			separator = false
		default:
			separator = out.Len() > 0
		}
	}
	base := out.String()
	if base == "" {
		return ""
	}
	first, _ := utf8.DecodeRuneInString(base)
	if first != '_' && !unicode.IsLetter(first) {
		base = "heading-" + base
	}
	if safeHTMLAnchorID(base) == "" {
		return ""
	}
	return base
}

func markdownThematicBreak(line string) bool {
	var marker rune
	count := 0
	for _, r := range line {
		if r == ' ' || r == '\t' {
			continue
		}
		if r != '-' && r != '*' && r != '_' {
			return false
		}
		if marker == 0 {
			marker = r
		} else if marker != r {
			return false
		}
		count++
	}
	return count >= 3
}

func markdownBlockquote(line string) (string, bool) {
	if !strings.HasPrefix(line, ">") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, ">")), true
}

func markdownListItem(line string) (ordered bool, item string, ok bool) {
	if len(line) > 2 {
		switch line[:2] {
		case "- ", "* ", "+ ":
			return false, strings.TrimSpace(line[2:]), true
		}
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) || line[i] != '.' || line[i+1] != ' ' {
		return false, "", false
	}
	return true, strings.TrimSpace(line[i+2:]), true
}

func markdownInline(s string) string {
	var out strings.Builder
	renderMarkdownInline(&out, s, false)
	return out.String()
}

func markdownInlineText(s string) string {
	var out strings.Builder
	renderMarkdownInline(&out, s, true)
	return strings.Join(strings.Fields(out.String()), " ")
}

func renderMarkdownInline(out *strings.Builder, s string, textOnly bool) {
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "![") {
			if alt, _, end, ok := markdownLinkParts(s, i+1); ok {
				renderMarkdownInline(out, alt, textOnly)
				i = end
				continue
			}
		}
		if s[i] == '[' {
			if label, href, end, ok := markdownLinkParts(s, i); ok {
				if textOnly {
					renderMarkdownInline(out, label, true)
				} else if safeHref := safeHTMLLinkHref(href); safeHref != "" {
					fmt.Fprintf(out, `<a href="%s">`, html.EscapeString(safeHref))
					renderMarkdownInline(out, label, false)
					out.WriteString("</a>")
				} else {
					renderMarkdownInline(out, label, false)
				}
				i = end
				continue
			}
		}
		if s[i] == '`' {
			if end := strings.IndexByte(s[i+1:], '`'); end >= 0 {
				code := s[i+1 : i+1+end]
				if textOnly {
					out.WriteString(code)
				} else {
					fmt.Fprintf(out, "<code>%s</code>", html.EscapeString(code))
				}
				i += end + 2
				continue
			}
		}
		if strings.HasPrefix(s[i:], "**") || strings.HasPrefix(s[i:], "__") {
			delim := s[i : i+2]
			if end := strings.Index(s[i+2:], delim); end >= 0 {
				inner := s[i+2 : i+2+end]
				if strings.TrimSpace(inner) != "" {
					if !textOnly {
						out.WriteString("<strong>")
					}
					renderMarkdownInline(out, inner, textOnly)
					if !textOnly {
						out.WriteString("</strong>")
					}
					i += end + 4
					continue
				}
			}
		}
		if s[i] == '*' || s[i] == '_' {
			delim := s[i : i+1]
			if end := strings.Index(s[i+1:], delim); end >= 0 {
				inner := s[i+1 : i+1+end]
				if strings.TrimSpace(inner) != "" {
					if !textOnly {
						out.WriteString("<em>")
					}
					renderMarkdownInline(out, inner, textOnly)
					if !textOnly {
						out.WriteString("</em>")
					}
					i += end + 2
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if textOnly {
			out.WriteRune(r)
		} else {
			out.WriteString(html.EscapeString(string(r)))
		}
		i += size
	}
}

func markdownLinkParts(s string, start int) (label, href string, end int, ok bool) {
	if start >= len(s) || s[start] != '[' {
		return "", "", 0, false
	}
	labelStart := start + 1
	labelEndRel := strings.IndexByte(s[labelStart:], ']')
	if labelEndRel < 0 {
		return "", "", 0, false
	}
	labelEnd := labelStart + labelEndRel
	hrefStart := labelEnd + 1
	if hrefStart >= len(s) || s[hrefStart] != '(' {
		return "", "", 0, false
	}
	hrefStart++
	hrefEnd := markdownLinkHrefEnd(s, hrefStart)
	if hrefEnd < 0 {
		return "", "", 0, false
	}
	return s[labelStart:labelEnd], strings.TrimSpace(s[hrefStart:hrefEnd]), hrefEnd + 1, true
}

func markdownLinkHrefEnd(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

func normalizeLineEndings(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

type epubMetadata struct {
	Title            string
	Language         string
	Authors          []string
	Publisher        string
	Date             string
	Description      string
	Identifier       string
	ExtraIdentifiers []string
	Series           string
	SeriesIndex      float64
	Tags             []string
}

func toEPUBMetadata(meta *format.Metadata) epubMetadata {
	out := epubMetadata{}
	if meta == nil {
		return out
	}
	if title := strings.TrimSpace(meta.Title); title != "" {
		out.Title = title
	}
	if language := strings.TrimSpace(meta.Language); language != "" {
		out.Language = language
	}
	out.Publisher = strings.TrimSpace(meta.Publisher)
	out.Date = strings.TrimSpace(meta.Date)
	out.Description = strings.TrimSpace(meta.Description)
	out.Series = strings.TrimSpace(meta.Series)
	out.SeriesIndex = meta.SeriesIndex
	for _, author := range meta.Authors {
		name := strings.TrimSpace(author.Name)
		if name != "" {
			out.Authors = append(out.Authors, name)
		}
	}
	for _, tag := range meta.Tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			out.Tags = append(out.Tags, tag)
		}
	}
	for _, id := range bookmeta.ParseIdentifiers(meta.Identifier) {
		if id.Value == "" || bookmeta.IsInternalIdentifier(id) {
			continue
		}
		value := epubIdentifierValue(id)
		if value == "" {
			continue
		}
		if out.Identifier == "" {
			out.Identifier = value
		} else {
			out.ExtraIdentifiers = append(out.ExtraIdentifiers, value)
		}
	}
	return out
}

func epubMetadataWithFallback(meta epubMetadata, opts ConversionOptions) epubMetadata {
	fallback := toEPUBMetadata(opts.Metadata)
	fillEPUBMetadataGaps(&meta, fallback)
	if strings.TrimSpace(meta.Title) == "" {
		meta.Title = epubTitleFromSourceName(opts.SourceName)
	}
	return meta
}

func fillEPUBMetadataGaps(meta *epubMetadata, fallback epubMetadata) {
	fillEPUBString(&meta.Title, fallback.Title)
	fillEPUBString(&meta.Language, fallback.Language)
	fillEPUBString(&meta.Publisher, fallback.Publisher)
	fillEPUBString(&meta.Date, fallback.Date)
	fillEPUBString(&meta.Description, fallback.Description)
	fillEPUBString(&meta.Identifier, fallback.Identifier)
	fillEPUBString(&meta.Series, fallback.Series)
	if meta.SeriesIndex == 0 {
		meta.SeriesIndex = fallback.SeriesIndex
	}
	if len(meta.Authors) == 0 {
		meta.Authors = append([]string(nil), fallback.Authors...)
	}
	if len(meta.ExtraIdentifiers) == 0 {
		meta.ExtraIdentifiers = append([]string(nil), fallback.ExtraIdentifiers...)
	}
	if len(meta.Tags) == 0 {
		meta.Tags = append([]string(nil), fallback.Tags...)
	}
}

func fillEPUBString(dst *string, src string) {
	if strings.TrimSpace(*dst) == "" {
		*dst = strings.TrimSpace(src)
	}
}

func epubTitleFromSourceName(sourceName string) string {
	name := strings.TrimSpace(sourceName)
	if name == "" {
		return ""
	}
	base := filepath.Base(name)
	if ext := format.BookExtension(base); ext != "" && strings.HasSuffix(strings.ToLower(base), strings.ToLower(ext)) {
		base = base[:len(base)-len(ext)]
	} else if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return strings.TrimSpace(base)
}

func normalizeEPUBMetadata(meta epubMetadata, body string) epubMetadata {
	if strings.TrimSpace(meta.Title) == "" {
		meta.Title = "Polka text export"
	}
	if strings.TrimSpace(meta.Language) == "" {
		meta.Language = "und"
	}
	if strings.TrimSpace(meta.Identifier) == "" {
		meta.Identifier = generatedEPUBIdentifier(meta, body)
	}
	return meta
}

func epubIdentifierValue(id bookmeta.Identifier) string {
	value := strings.TrimSpace(id.Value)
	if value == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(id.Type)) {
	case "isbn":
		return "urn:isbn:" + value
	case "doi":
		return "doi:" + value
	case "url":
		return value
	case "":
		return value
	default:
		return strings.ToLower(strings.TrimSpace(id.Type)) + ":" + value
	}
}

func generatedEPUBIdentifier(meta epubMetadata, body string) string {
	h := sha256.New()
	writeHashField := func(value string) {
		_, _ = io.WriteString(h, value)
		_, _ = io.WriteString(h, "\x00")
	}
	writeHashField(meta.Title)
	writeHashField(meta.Language)
	for _, author := range meta.Authors {
		writeHashField(author)
	}
	writeHashField(meta.Publisher)
	writeHashField(meta.Date)
	writeHashField(meta.Description)
	writeHashField(meta.Series)
	if meta.SeriesIndex != 0 {
		writeHashField(strconv.FormatFloat(meta.SeriesIndex, 'f', -1, 64))
	}
	for _, tag := range meta.Tags {
		writeHashField(tag)
	}
	writeHashField(body)
	sum := h.Sum(nil)
	return fmt.Sprintf("urn:polka:sha256:%x", sum[:16])
}

type epubAsset struct {
	ID        string
	Href      string
	MediaType string
	Data      []byte
	Cover     bool
}

type epubNavItem struct {
	Title string
	Href  string
}

func writeSimpleEPUBWithNav(ctx context.Context, w io.Writer, body string, meta epubMetadata, nav []epubNavItem, assets ...epubAsset) error {
	if err := claimConversionResources(ctx, len(assets), "generated EPUB assets"); err != nil {
		return err
	}
	for _, asset := range assets {
		if err := claimConversionDecodedBytes(ctx, int64(len(asset.Data)), "generated EPUB asset "+asset.Href); err != nil {
			return err
		}
	}
	meta = normalizeEPUBMetadata(meta, body)
	stylesheets := epubStylesheetHrefs(assets)
	zw := zip.NewWriter(w)
	if err := addEPUBFile(zw, "mimetype", zip.Store, "application/epub+zip"); err != nil {
		zw.Close()
		return err
	}
	files := []struct {
		name string
		body string
	}{
		{"META-INF/container.xml", epubContainerXML()},
		{"OEBPS/content.opf", epubContentOPF(meta, assets)},
		{"OEBPS/nav.xhtml", epubNavXHTML(meta, nav)},
	}
	for _, file := range files {
		if err := addEPUBFile(zw, file.name, zip.Deflate, file.body); err != nil {
			zw.Close()
			return err
		}
	}
	if err := addEPUBTextXHTML(zw, "OEBPS/text.xhtml", body, meta, stylesheets); err != nil {
		zw.Close()
		return err
	}
	for _, asset := range assets {
		if err := addEPUBBytes(zw, "OEBPS/"+asset.Href, zip.Deflate, asset.Data); err != nil {
			zw.Close()
			return err
		}
	}
	return zw.Close()
}

func epubStylesheetHrefs(assets []epubAsset) []string {
	var hrefs []string
	for _, asset := range assets {
		if strings.EqualFold(strings.TrimSpace(asset.MediaType), "text/css") && strings.TrimSpace(asset.Href) != "" && len(asset.Data) > 0 {
			hrefs = append(hrefs, asset.Href)
		}
	}
	return hrefs
}

func addEPUBFile(zw *zip.Writer, name string, method uint16, body string) error {
	header := &zip.FileHeader{Name: name, Method: method}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, body)
	return err
}

func addEPUBBytes(zw *zip.Writer, name string, method uint16, body []byte) error {
	header := &zip.FileHeader{Name: name, Method: method}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func addEPUBTextXHTML(zw *zip.Writer, name string, body string, meta epubMetadata, stylesheets []string) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, epubTextXHTMLPrefix(meta, stylesheets, strings.Contains(body, `epub:type="`))); err != nil {
		return err
	}
	if err := writeIndentedEPUBBody(w, body); err != nil {
		return err
	}
	_, err = io.WriteString(w, epubTextXHTMLSuffix())
	return err
}

func epubContainerXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`
}

func epubContentOPF(meta epubMetadata, assets []epubAsset) string {
	var identifiers strings.Builder
	fmt.Fprintf(&identifiers, "    <dc:identifier id=\"pub-id\">%s</dc:identifier>\n", html.EscapeString(meta.Identifier))
	for _, identifier := range meta.ExtraIdentifiers {
		if identifier = strings.TrimSpace(identifier); identifier != "" {
			fmt.Fprintf(&identifiers, "    <dc:identifier>%s</dc:identifier>\n", html.EscapeString(identifier))
		}
	}
	var creators strings.Builder
	for _, author := range meta.Authors {
		fmt.Fprintf(&creators, "    <dc:creator>%s</dc:creator>\n", html.EscapeString(author))
	}
	var metadata strings.Builder
	if meta.Publisher != "" {
		fmt.Fprintf(&metadata, "    <dc:publisher>%s</dc:publisher>\n", html.EscapeString(meta.Publisher))
	}
	if meta.Date != "" {
		fmt.Fprintf(&metadata, "    <dc:date>%s</dc:date>\n", html.EscapeString(meta.Date))
	}
	if meta.Description != "" {
		fmt.Fprintf(&metadata, "    <dc:description>%s</dc:description>\n", html.EscapeString(meta.Description))
	}
	for _, tag := range meta.Tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			fmt.Fprintf(&metadata, "    <dc:subject>%s</dc:subject>\n", html.EscapeString(tag))
		}
	}
	if meta.Series != "" {
		fmt.Fprintf(&metadata, "    <meta property=\"belongs-to-collection\" id=\"series-1\">%s</meta>\n", html.EscapeString(meta.Series))
		metadata.WriteString("    <meta refines=\"#series-1\" property=\"collection-type\">series</meta>\n")
		if meta.SeriesIndex != 0 {
			fmt.Fprintf(&metadata, "    <meta refines=\"#series-1\" property=\"group-position\">%s</meta>\n", html.EscapeString(strconv.FormatFloat(meta.SeriesIndex, 'f', -1, 64)))
		}
	}
	var coverMeta string
	var manifest strings.Builder
	for _, asset := range assets {
		properties := ""
		if asset.Cover {
			properties = ` properties="cover-image"`
			if coverMeta == "" {
				coverMeta = fmt.Sprintf("    <meta name=\"cover\" content=\"%s\"/>\n", html.EscapeString(asset.ID))
			}
		}
		fmt.Fprintf(&manifest, `    <item id="%s" href="%s" media-type="%s"%s/>`+"\n",
			html.EscapeString(asset.ID),
			html.EscapeString(asset.Href),
			html.EscapeString(asset.MediaType),
			properties,
		)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" unique-identifier="pub-id" xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
` + identifiers.String() + `    <dc:title>` + html.EscapeString(meta.Title) + `</dc:title>
` + creators.String() + `    <dc:language>` + html.EscapeString(meta.Language) + `</dc:language>
` + metadata.String() + `    <meta property="dcterms:modified">` + epubModified + `</meta>
` + coverMeta + `  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="text" href="text.xhtml" media-type="application/xhtml+xml"/>
` + manifest.String() + `
  </manifest>
  <spine>
    <itemref idref="text"/>
  </spine>
</package>
`
}

func epubNavXHTML(meta epubMetadata, nav []epubNavItem) string {
	var items strings.Builder
	for _, item := range nav {
		title := strings.TrimSpace(item.Title)
		href := strings.TrimSpace(item.Href)
		if title == "" || href == "" {
			continue
		}
		fmt.Fprintf(&items, `        <li><a href="%s">%s</a></li>`+"\n", html.EscapeString(href), html.EscapeString(title))
	}
	if items.Len() == 0 {
		items.WriteString(`        <li><a href="text.xhtml">Text</a></li>` + "\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="` + html.EscapeString(meta.Language) + `" lang="` + html.EscapeString(meta.Language) + `">
  <head>
    <title>Navigation</title>
  </head>
  <body>
    <nav epub:type="toc" id="toc">
      <h1>Contents</h1>
      <ol>
` + items.String() + `
      </ol>
    </nav>
  </body>
</html>
`
}

func epubTextXHTMLPrefix(meta epubMetadata, stylesheets []string, hasEPUBTypes bool) string {
	var links strings.Builder
	for _, href := range stylesheets {
		href = strings.TrimSpace(href)
		if href == "" {
			continue
		}
		fmt.Fprintf(&links, `    <link rel="stylesheet" href="%s"/>`+"\n", html.EscapeString(href))
	}
	epubNamespace := ""
	if hasEPUBTypes {
		epubNamespace = ` xmlns:epub="http://www.idpf.org/2007/ops"`
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml"` + epubNamespace + ` xml:lang="` + html.EscapeString(meta.Language) + `" lang="` + html.EscapeString(meta.Language) + `">
  <head>
    <title>` + html.EscapeString(meta.Title) + `</title>
    <style>
      body { font-family: serif; line-height: 1.5; margin: 1.5em; }
      p { margin: 0 0 1em; }
    </style>
` + links.String() + `
  </head>
  <body>
`
}

func epubTextXHTMLSuffix() string {
	return `  </body>
</html>
`
}

func writeIndentedEPUBBody(w io.Writer, body string) error {
	for len(body) > 0 {
		line := body
		body = ""
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line, body = line[:i+1], line[i+1:]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, err := io.WriteString(w, "    "); err != nil {
			return err
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	return nil
}
