package format

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

const (
	maxPlainTextProbeBytes   = 64 << 10
	maxMarkdownMetadataBytes = 64 << 10
)

var ErrTextTooLarge = errors.New("text input exceeds size limit")

func isPlainText(r io.ReaderAt, size int64) bool {
	if size == 0 {
		return true
	}
	n := min(size, maxPlainTextProbeBytes)
	if n < 0 {
		return false
	}
	sample := make([]byte, n)
	read, err := r.ReadAt(sample, 0)
	if err != nil && err != io.EOF {
		return false
	}
	return isPlainTextSample(sample[:read])
}

func isPlainTextSample(sample []byte) bool {
	if len(sample) == 0 {
		return true
	}
	if hasBinarySignature(sample) {
		return false
	}
	if hasUTF16BOM(sample) {
		return true
	}
	sample = bytes.TrimPrefix(sample, []byte{0xef, 0xbb, 0xbf})
	for _, b := range sample {
		if b == 0 {
			return false
		}
		if b < 0x20 && !isAllowedPlainTextControl(b) {
			return false
		}
	}
	return true
}

func hasUTF16BOM(sample []byte) bool {
	return bytes.HasPrefix(sample, []byte{0xff, 0xfe}) || bytes.HasPrefix(sample, []byte{0xfe, 0xff})
}

func isAllowedPlainTextControl(b byte) bool {
	switch b {
	case '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func hasBinarySignature(sample []byte) bool {
	signatures := [][]byte{
		[]byte("%PDF-"),
		[]byte("PK\x03\x04"),
		[]byte("PK\x05\x06"),
		[]byte("PK\x07\x08"),
		[]byte("AT&TFORM"),
		{0x7f, 'E', 'L', 'F'},
		{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		{0xff, 0xd8, 0xff},
		{'G', 'I', 'F', '8'},
	}
	for _, sig := range signatures {
		if bytes.HasPrefix(sample, sig) {
			return true
		}
	}
	return false
}

func isTXTZ(r io.ReaderAt, size int64) bool {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(txtzTextEntries(zr), zipEntryLooksPlainText)
}

// TXTZTextSource is the text payload selected from a TXTZ archive.
type TXTZTextSource struct {
	Reader   io.ReadCloser
	Filename string
	Format   Format
}

// OpenTXTZTextSource opens the deterministic text entry Polka uses as the
// readable body of a TXTZ archive.
func OpenTXTZTextSource(r io.ReaderAt, size int64) (TXTZTextSource, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return TXTZTextSource{}, err
	}
	for _, f := range txtzTextEntries(zr) {
		if zipEntryLooksPlainText(f) {
			rc, err := f.Open()
			if err != nil {
				return TXTZTextSource{}, err
			}
			name := NormalizeZipName(f.Name)
			return TXTZTextSource{
				Reader:   rc,
				Filename: name,
				Format:   FormatFromExt(path.Ext(name)),
			}, nil
		}
	}
	return TXTZTextSource{}, fmt.Errorf("TXTZ archive contains no readable text entry")
}

// ReadTXTZTextContextLimited reads all readable text entries in deterministic
// archive order, with cancellation checks while selecting and reading text
// entries. The returned format is the first readable entry's format;
// mixed-format TXTZ archives are still concatenated, matching the existing
// first-entry processor choice rather than silently dropping later chapters.
// Pass a negative maxBytes to read without a cap.
func ReadTXTZTextContextLimited(ctx context.Context, r io.ReaderAt, size int64, maxBytes int64) ([]byte, Format, error) {
	if err := checkContext(ctx); err != nil {
		return nil, FormatUnknown, err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, FormatUnknown, err
	}
	var out bytes.Buffer
	formatHint := txtzTextFormattingFromOPF(zr)
	sourceFormat := FormatUnknown
	for _, f := range txtzTextEntries(zr) {
		if err := checkContext(ctx); err != nil {
			return nil, FormatUnknown, err
		}
		if !zipEntryLooksPlainTextContext(ctx, f) {
			if err := checkContext(ctx); err != nil {
				return nil, FormatUnknown, err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, FormatUnknown, err
		}
		remaining := maxBytes
		if maxBytes >= 0 {
			if out.Len() > 0 {
				remaining -= 2
			}
			remaining -= int64(out.Len())
			if remaining < 0 {
				rc.Close()
				return nil, FormatUnknown, fmt.Errorf("TXTZ text exceeds limit (%d bytes): %w", maxBytes, ErrTextTooLarge)
			}
		}
		reader := io.Reader(contextReader{ctx: ctx, r: rc})
		if maxBytes >= 0 {
			reader = io.LimitReader(reader, remaining+1)
		}
		raw, readErr := io.ReadAll(reader)
		closeErr := rc.Close()
		if readErr != nil {
			return nil, FormatUnknown, readErr
		}
		if closeErr != nil {
			return nil, FormatUnknown, closeErr
		}
		if maxBytes >= 0 && int64(len(raw)) > remaining {
			return nil, FormatUnknown, fmt.Errorf("TXTZ text exceeds limit (%d bytes): %w", maxBytes, ErrTextTooLarge)
		}
		raw = bytes.Trim(raw, "\r\n")
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		if sourceFormat == FormatUnknown {
			sourceFormat = FormatFromExt(path.Ext(NormalizeZipName(f.Name)))
			if formatHint != FormatUnknown {
				sourceFormat = formatHint
			}
		}
		out.Write(raw)
	}
	if sourceFormat == FormatUnknown {
		return nil, FormatUnknown, fmt.Errorf("TXTZ archive contains no readable text entry")
	}
	return out.Bytes(), sourceFormat, nil
}

func txtzTextFormattingFromOPF(zr *zip.Reader) Format {
	opf := FirstRootOPF(zr)
	if opf == nil {
		return FormatUnknown
	}
	raw, err := readZipFileLimited(opf, maxOPFDocumentBytes)
	if err != nil {
		return FormatUnknown
	}
	return txtzTextFormatting(raw)
}

func txtzTextFormatting(raw []byte) Format {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return FormatUnknown
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "text-formatting":
			var value string
			if err := dec.DecodeElement(&value, &start); err != nil {
				return FormatUnknown
			}
			return txtzTextFormattingValue(value)
		case "meta":
			if format := txtzTextFormattingMeta(start); format != FormatUnknown {
				return format
			}
		}
	}
}

func txtzTextFormattingMeta(start xml.StartElement) Format {
	var name, content string
	for _, attr := range start.Attr {
		switch strings.ToLower(attr.Name.Local) {
		case "name", "property":
			name = strings.ToLower(strings.TrimSpace(attr.Value))
		case "content":
			content = attr.Value
		}
	}
	switch name {
	case "text-formatting", "calibre:text-formatting", "calibre:text_formatting":
		return txtzTextFormattingValue(content)
	default:
		return FormatUnknown
	}
}

func txtzTextFormattingValue(value string) Format {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "plain", "txt", "text":
		return FormatTXT
	case "markdown", "md":
		return FormatMarkdown
	case "textile":
		return FormatTextile
	default:
		return FormatUnknown
	}
}

func txtzTextEntries(zr *zip.Reader) []*zip.File {
	entries := make([]*zip.File, 0, len(zr.File))
	for _, f := range zr.File {
		if isTXTZTextEntry(f) {
			entries = append(entries, f)
		}
	}
	slices.SortFunc(entries, func(a, b *zip.File) int {
		return strings.Compare(
			strings.ToLower(NormalizeZipName(a.Name)),
			strings.ToLower(NormalizeZipName(b.Name)),
		)
	})
	return entries
}

func isTXTZTextEntry(f *zip.File) bool {
	name := NormalizeZipName(f.Name)
	if name == "" || f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".text", ".md", ".markdown", ".textile":
		return true
	default:
		return false
	}
}

func zipEntryLooksPlainText(f *zip.File) bool {
	return zipEntryLooksPlainTextContext(context.Background(), f)
}

func zipEntryLooksPlainTextContext(ctx context.Context, f *zip.File) bool {
	rc, err := f.Open()
	if err != nil {
		return false
	}
	defer rc.Close()
	sample, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: rc}, maxPlainTextProbeBytes))
	if err != nil {
		return false
	}
	return isPlainTextSample(sample)
}

// ExtractMarkdownMetadata reads a narrow leading-heading convention used by
// some public-domain markdown book collections:
//
//	# Title: Alice's Adventures in Wonderland
//	## Author: Lewis Carroll
//	## Year: 1865
//
// Plain headings such as "# Chapter 1" are intentionally ignored so ordinary
// markdown books keep the filename fallback instead of guessing.
func ExtractMarkdownMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	raw, err := readAtMost(r, size, maxMarkdownMetadataBytes)
	if err != nil {
		return nil, err
	}
	meta := &Metadata{}
	for _, field := range leadingMarkdownMetadataFields(markdownText(raw)) {
		switch strings.ToLower(field.name) {
		case "title":
			meta.Title = firstNonEmptyMarkdown(meta.Title, field.value)
		case "author", "authors":
			if len(meta.Authors) == 0 {
				for _, author := range bookmeta.ParseAuthorList(field.value) {
					meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author})
				}
			}
		case "year", "date":
			meta.Date = firstNonEmptyMarkdown(meta.Date, bookmeta.NormalizeMetadataDate(field.value))
		case "language", "lang":
			meta.Language = firstNonEmptyMarkdown(meta.Language, bookmeta.NormalizeLanguage(field.value))
		}
	}
	return meta, nil
}

func firstNonEmptyMarkdown(current, candidate string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	return strings.TrimSpace(candidate)
}

type markdownMetadataField struct {
	name  string
	value string
}

func leadingMarkdownMetadataFields(text string) []markdownMetadataField {
	var fields []markdownMetadataField
	for line := range strings.SplitSeq(normalizeMarkdownLineEndings(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isMarkdownThematicBreak(trimmed) {
			break
		}
		_, heading, ok := markdownHeadingLine(trimmed)
		if !ok {
			break
		}
		name, value, ok := MarkdownMetadataLabel(heading)
		if !ok {
			break
		}
		fields = append(fields, markdownMetadataField{name: name, value: value})
	}
	return fields
}

func markdownText(raw []byte) string {
	return DecodeTextToUTF8(raw)
}

func normalizeMarkdownLineEndings(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func markdownHeadingLine(line string) (int, string, bool) {
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

// MarkdownMetadataLabel parses Polka's narrow leading Markdown metadata heading
// convention ("Title: ...", "Author: ...", etc.).
func MarkdownMetadataLabel(heading string) (string, string, bool) {
	name, value, ok := strings.Cut(heading, ":")
	if !ok {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if name == "" || value == "" {
		return "", "", false
	}
	switch strings.ToLower(name) {
	case "title", "author", "authors", "year", "date", "language", "lang":
		return name, value, true
	default:
		return "", "", false
	}
}

func isMarkdownThematicBreak(line string) bool {
	if len(line) < 3 {
		return false
	}
	var marker rune
	for _, r := range line {
		switch r {
		case ' ', '\t':
			continue
		case '-', '*', '_':
			if marker == 0 {
				marker = r
			}
			if r != marker {
				return false
			}
		default:
			return false
		}
	}
	return marker != 0
}

// ExtractTXTZMetadata reads metadata.opf from a TXTZ archive.
func ExtractTXTZMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	opf := FirstRootOPF(zr)
	if opf == nil {
		return &Metadata{}, nil
	}
	raw, err := readZipFileLimited(opf, maxOPFDocumentBytes)
	if err != nil {
		return nil, err
	}
	meta, err := ParseOPF(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse TXTZ metadata.opf: %w", err)
	}
	return meta, nil
}

// ExtractTXTZCover reads cover-relpath-from-base from metadata.opf when a TXTZ
// archive provides one.
func ExtractTXTZCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, "", err
	}
	opf := FirstRootOPF(zr)
	if opf == nil {
		return nil, "", nil
	}
	raw, err := readZipFileLimited(opf, maxOPFDocumentBytes)
	if err != nil {
		return nil, "", err
	}
	coverHref := txtzCoverRelpath(raw)
	if coverHref == "" {
		return nil, "", nil
	}
	return readEPUBImageByHref(zr, "", coverHref)
}

func txtzCoverRelpath(raw []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "cover-relpath-from-base" {
			continue
		}
		var href string
		if err := dec.DecodeElement(&href, &start); err != nil {
			return ""
		}
		return strings.TrimSpace(href)
	}
}

// FirstRootOPF returns the lowest-name top-level OPF entry in a zip archive.
func FirstRootOPF(zr *zip.Reader) *zip.File {
	var best *zip.File
	var bestName string
	for _, f := range zr.File {
		name := NormalizeZipName(f.Name)
		if name == "" || f.FileInfo().IsDir() || strings.Contains(name, "/") {
			continue
		}
		if !strings.EqualFold(path.Ext(name), ".opf") {
			continue
		}
		lower := strings.ToLower(name)
		if best == nil || lower < bestName {
			best = f
			bestName = lower
		}
	}
	return best
}
