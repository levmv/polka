package format

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/imagecodec"
)

const (
	maxCBZComicInfoBytes = 2 << 20
	maxCBZCoverBytes     = 32 << 20
	maxCBZCoverPixels    = 80_000_000
)

// ComicPage describes one valid image page in canonical archive reading order.
type ComicPage struct {
	Index     int
	Name      string
	Extension string
	Size      uint64
	Width     int
	Height    int
}

type comicInfoXML struct {
	Title       string `xml:"Title"`
	Series      string `xml:"Series"`
	Number      string `xml:"Number"`
	Summary     string `xml:"Summary"`
	Description string `xml:"Description"`
	Web         string `xml:"Web"`
	Writer      string `xml:"Writer"`
	Penciller   string `xml:"Penciller"`
	Inker       string `xml:"Inker"`
	Colorist    string `xml:"Colorist"`
	Letterer    string `xml:"Letterer"`
	CoverArtist string `xml:"CoverArtist"`
	Editor      string `xml:"Editor"`
	Translator  string `xml:"Translator"`
	Publisher   string `xml:"Publisher"`
	Genre       string `xml:"Genre"`
	Tags        string `xml:"Tags"`
	LanguageISO string `xml:"LanguageISO"`
	GTIN        string `xml:"GTIN"`
	Year        string `xml:"Year"`
	Month       string `xml:"Month"`
	Day         string `xml:"Day"`
}

func isCBZ(r io.ReaderAt, size int64) bool {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false
	}
	images, _, err := scanCBZ(zr)
	if err != nil {
		return false
	}
	return firstValidCBZPage(images) != nil
}

// ListCBZPages returns valid image pages in the same natural order used for
// cover extraction, reader/export work, and future page-count persistence.
func ListCBZPages(r io.ReaderAt, size int64) ([]ComicPage, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	pages, _, err := scanCBZPages(zr)
	if err != nil {
		return nil, err
	}
	out := make([]ComicPage, len(pages))
	for i, page := range pages {
		out[i] = page.Page
	}
	return out, nil
}

// ExtractCBZMetadata extracts ComicInfo.xml metadata from a CBZ archive.
func ExtractCBZMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	_, comicInfo, err := scanCBZ(zr)
	if err != nil {
		return nil, err
	}
	if comicInfo == nil {
		return &Metadata{}, nil
	}
	raw, err := readZipFileLimited(comicInfo, maxCBZComicInfoBytes)
	if err != nil {
		return nil, err
	}
	return parseComicInfoMetadata(raw)
}

// ExtractCBZCover returns the first valid JPEG/PNG/GIF/WebP/AVIF page image
// after natural path sorting. The extension describes the detected bytes
// rather than trusting the archive name.
func ExtractCBZCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, "", err
	}
	images, _, err := scanCBZ(zr)
	if err != nil {
		return nil, "", err
	}
	page := firstValidCBZCoverPage(images)
	if page == nil {
		return nil, "", nil
	}
	entry := page.File
	raw, err := readZipFileLimited(entry, maxCBZCoverBytes)
	if err != nil {
		return nil, "", err
	}
	return raw, page.Page.Extension, nil
}

type cbzPageEntry struct {
	Page ComicPage
	File *zip.File
}

func scanCBZPages(zr *zip.Reader) ([]cbzPageEntry, *zip.File, error) {
	images, comicInfo, err := scanCBZ(zr)
	if err != nil {
		return nil, nil, err
	}
	pages := make([]cbzPageEntry, 0, len(images))
	for _, f := range images {
		if page := cbzPageFromFile(f, len(pages)); page != nil {
			pages = append(pages, *page)
		}
	}
	return pages, comicInfo, nil
}

func scanCBZ(zr *zip.Reader) ([]*zip.File, *zip.File, error) {
	var images []*zip.File
	var comicInfos []*zip.File
	for _, f := range zr.File {
		name := NormalizeZipName(f.Name)
		if name == "" || f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		if isEPUBPackageCBZEntry(f, name) {
			return nil, nil, fmt.Errorf("EPUB package entry %s", name)
		}
		if isIgnoredComicEntry(name) {
			continue
		}
		if isComicInfoName(name) {
			comicInfos = append(comicInfos, f)
			continue
		}
		if isComicImageName(name) || isCBZImageContent(f) {
			images = append(images, f)
			continue
		}
		// Real CBZ archives often carry viewer sidecars, thumbnails, or newer
		// image formats. Unsupported entries are not pages, but they should not
		// make the whole archive unknown when supported pages are present.
	}

	sort.Slice(images, func(i, j int) bool {
		return naturalLess(strings.ToLower(NormalizeZipName(images[i].Name)), strings.ToLower(NormalizeZipName(images[j].Name)))
	})
	sort.Slice(comicInfos, func(i, j int) bool {
		a, b := comicInfoRank(comicInfos[i].Name), comicInfoRank(comicInfos[j].Name)
		if a != b {
			return a < b
		}
		return naturalLess(strings.ToLower(NormalizeZipName(comicInfos[i].Name)), strings.ToLower(NormalizeZipName(comicInfos[j].Name)))
	})

	var comicInfo *zip.File
	if len(comicInfos) > 0 {
		comicInfo = comicInfos[0]
	}
	return images, comicInfo, nil
}

func isIgnoredComicEntry(name string) bool {
	parts := strings.SplitSeq(name, "/")
	for part := range parts {
		lower := strings.ToLower(part)
		if lower == "__macosx" || strings.HasPrefix(part, ".") {
			return true
		}
	}
	base := strings.ToLower(path.Base(name))
	return base == "thumbs.db"
}

func isEPUBPackageCBZEntry(f *zip.File, name string) bool {
	if strings.EqualFold(name, "META-INF/container.xml") {
		return true
	}
	if !strings.EqualFold(name, "mimetype") {
		return false
	}
	raw, err := readZipFileLimited(f, 1024)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == "application/epub+zip"
}

func isComicInfoName(name string) bool {
	return strings.EqualFold(path.Base(name), "ComicInfo.xml")
}

func isComicImageName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif":
		return true
	default:
		return false
	}
}

func isCBZImageContent(f *zip.File) bool {
	rc, err := f.Open()
	if err != nil {
		return false
	}
	defer rc.Close()

	var header [512]byte
	n, err := io.ReadFull(rc, header[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false
	}
	_, _, ok := ComicImageTypeFromBytes(header[:n])
	return ok
}

func comicInfoRank(name string) int {
	name = NormalizeZipName(name)
	if name == "" {
		return 1 << 30
	}
	return strings.Count(name, "/")
}

func firstValidCBZPage(images []*zip.File) *cbzPageEntry {
	for _, f := range images {
		if page := cbzPageFromFile(f, 0); page != nil {
			return page
		}
	}
	return nil
}

func firstValidCBZCoverPage(images []*zip.File) *cbzPageEntry {
	for _, f := range images {
		page := cbzPageFromFile(f, 0)
		if page == nil || f.UncompressedSize64 > maxCBZCoverBytes {
			continue
		}
		if validCBZCoverDimensions(page.Page.Width, page.Page.Height) {
			return page
		}
	}
	return nil
}

func cbzPageFromFile(f *zip.File, index int) *cbzPageEntry {
	cfg, extension, err := decodeCBZImageConfig(f)
	if err != nil {
		return nil
	}
	name := NormalizeZipName(f.Name)
	return &cbzPageEntry{
		Page: ComicPage{
			Index:     index,
			Name:      name,
			Extension: extension,
			Size:      f.UncompressedSize64,
			Width:     cfg.Width,
			Height:    cfg.Height,
		},
		File: f,
	}
}

func decodeCBZImageConfig(f *zip.File) (image.Config, string, error) {
	rc, err := f.Open()
	if err != nil {
		return image.Config{}, "", err
	}
	defer rc.Close()
	cfg, formatName, err := imagecodec.DecodeConfig(io.LimitReader(rc, maxCBZCoverBytes+1))
	if err != nil {
		return image.Config{}, "", err
	}
	extension, ok := cbzImageExtension(formatName)
	if !ok {
		return image.Config{}, "", fmt.Errorf("unsupported CBZ image format %q", formatName)
	}
	return cfg, extension, nil
}

func cbzImageExtension(formatName string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(formatName)) {
	case "jpeg":
		return "jpg", true
	case "png":
		return "png", true
	case "gif":
		return "gif", true
	case "webp":
		return "webp", true
	case "avif":
		return "avif", true
	default:
		return "", false
	}
}

func parseComicInfoMetadata(raw []byte) (*Metadata, error) {
	var info comicInfoXML
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&info); err != nil {
		return nil, fmt.Errorf("parse ComicInfo.xml: %w", err)
	}
	return metadataFromComicInfo(info), nil
}

func validCBZCoverDimensions(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	return uint64(width) <= uint64(maxCBZCoverPixels)/uint64(height)
}

func metadataFromComicInfo(info comicInfoXML) *Metadata {
	meta := &Metadata{
		Title:       cleanText(info.Title),
		Description: comicInfoDescription(info),
		Publisher:   cleanText(info.Publisher),
		Series:      cleanText(info.Series),
		SeriesIndex: parseComicInfoNumber(info.Number),
		Language:    bookmeta.NormalizeLanguage(info.LanguageISO),
		Date:        comicInfoDate(info.Year, info.Month, info.Day),
		Tags:        comicInfoTags(info.Genre, info.Tags),
	}

	seenAuthors := make(map[string]bool)
	addComicInfoAuthors(meta, seenAuthors, info.Writer, "writer")
	addComicInfoAuthors(meta, seenAuthors, info.Penciller, "penciller")
	addComicInfoAuthors(meta, seenAuthors, info.Inker, "inker")
	addComicInfoAuthors(meta, seenAuthors, info.Colorist, "colorist")
	addComicInfoAuthors(meta, seenAuthors, info.Letterer, "letterer")
	addComicInfoAuthors(meta, seenAuthors, info.CoverArtist, "cover_artist")
	addComicInfoAuthors(meta, seenAuthors, info.Editor, "editor")
	addComicInfoAuthors(meta, seenAuthors, info.Translator, "translator")

	if ids := comicInfoIdentifiers(info); len(ids) > 0 {
		meta.Identifier = bookmeta.FormatIdentifiers(ids)
	}

	return meta
}

func comicInfoIdentifiers(info comicInfoXML) []bookmeta.Identifier {
	var ids []bookmeta.Identifier
	if gtin := cleanText(info.GTIN); gtin != "" {
		id := bookmeta.IdentifierFromOPF("isbn", gtin)
		switch {
		case id.Value != "" && bookmeta.ValidISBN(id.Value):
			ids = append(ids, id)
		case validGTIN(gtin):
			ids = append(ids, bookmeta.Identifier{Type: "gtin", Value: normalizeGTIN(gtin)})
		}
	}
	for _, web := range splitComicInfoWebList(info.Web) {
		if id := bookmeta.IdentifierFromOPF("", web); id.Type == "url" && id.Value != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func addComicInfoAuthors(meta *Metadata, seen map[string]bool, value, role string) {
	for _, name := range splitComicInfoList(value) {
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: name, SortName: bookmeta.AuthorSort(name), Role: role})
	}
}

func comicInfoDescription(info comicInfoXML) string {
	if summary := cleanText(info.Summary); summary != "" {
		return summary
	}
	return cleanText(info.Description)
}

func parseComicInfoNumber(s string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return n
}

func comicInfoDate(year, month, day string) string {
	y, err := strconv.Atoi(strings.TrimSpace(year))
	if err != nil || y < 1000 || y > 9999 {
		return ""
	}
	mRaw := strings.TrimSpace(month)
	if mRaw == "" {
		return fmt.Sprintf("%04d", y)
	}
	m, err := strconv.Atoi(mRaw)
	if err != nil || m < 1 || m > 12 {
		return fmt.Sprintf("%04d", y)
	}
	dRaw := strings.TrimSpace(day)
	if dRaw == "" {
		return fmt.Sprintf("%04d-%02d", y, m)
	}
	d, err := strconv.Atoi(dRaw)
	if err != nil || !validComicInfoDate(y, m, d) {
		return fmt.Sprintf("%04d-%02d", y, m)
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, m, d)
}

func validComicInfoDate(y, m, d int) bool {
	if y < 1000 || y > 9999 || m < 1 || m > 12 || d < 1 || d > 31 {
		return false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && int(t.Month()) == m && t.Day() == d
}

func comicInfoTags(values ...string) []string {
	return uniqueTagList(values, commaSemicolonNewlineSeparator, cleanText)
}

func validGTIN(value string) bool {
	clean := normalizeGTIN(value)
	switch len(clean) {
	case 8, 12, 13, 14:
	default:
		return false
	}
	sum := 0
	for i := len(clean) - 2; i >= 0; i-- {
		if clean[i] < '0' || clean[i] > '9' {
			return false
		}
		weight := 1
		if (len(clean)-1-i)%2 == 1 {
			weight = 3
		}
		sum += int(clean[i]-'0') * weight
	}
	check := clean[len(clean)-1]
	if check < '0' || check > '9' {
		return false
	}
	return (sum+int(check-'0'))%10 == 0
}

func normalizeGTIN(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func splitComicInfoWebList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = cleanText(field); field != "" {
			out = append(out, field)
		}
	}
	return out
}

func splitComicInfoList(value string) []string {
	return splitTagFields(value, commaSemicolonNewlineSeparator, cleanText)
}

func naturalLess(a, b string) bool {
	for len(a) > 0 && len(b) > 0 {
		ar := nextNaturalChunk(a)
		br := nextNaturalChunk(b)
		a = a[len(ar):]
		b = b[len(br):]
		if ar == br {
			continue
		}
		if ar != "" && br != "" && unicode.IsDigit(rune(ar[0])) && unicode.IsDigit(rune(br[0])) {
			an := strings.TrimLeft(ar, "0")
			bn := strings.TrimLeft(br, "0")
			if an == "" {
				an = "0"
			}
			if bn == "" {
				bn = "0"
			}
			if len(an) != len(bn) {
				return len(an) < len(bn)
			}
			if an != bn {
				return an < bn
			}
			return len(ar) < len(br)
		}
		return ar < br
	}
	return len(a) < len(b)
}

func nextNaturalChunk(s string) string {
	digit := unicode.IsDigit(rune(s[0]))
	i := 0
	for i < len(s) && unicode.IsDigit(rune(s[i])) == digit {
		i++
	}
	return s[:i]
}
