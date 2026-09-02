package format

import (
	"archive/zip"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

type Format int

const (
	FormatUnknown Format = iota
	FormatEPUB
	FormatKEPUB
	FormatPDF
	FormatFB2
	FormatMOBI
	FormatAZW
	FormatAZW3
	FormatAZW4
	FormatPRC
	FormatPDB
	FormatCBZ
	FormatCBR
	FormatCB7
	FormatDJVU
	FormatTXT
	FormatTXTZ
	FormatMarkdown
	FormatTextile
	FormatHTML
	FormatXHTML
	FormatHTMLZ
	FormatDOCX
	FormatDOCM
	FormatODT
	FormatRTF
	FormatCHM
)

type ReaderKind string

const (
	ReaderNone    ReaderKind = ""
	ReaderPDF     ReaderKind = "pdf"
	ReaderFoliate ReaderKind = "foliate"
)

type FB2Container string

const (
	FB2ContainerNone FB2Container = ""
	FB2ContainerZip  FB2Container = "zip"
	FB2ContainerGzip FB2Container = "gzip"
)

type bookFormat struct {
	Format           Format
	Key              string
	Label            string
	Extensions       []string
	DefaultExtension string
	MediaTypes       map[string]string
	Reader           ReaderKind
	FB2Containers    map[string]FB2Container
	Verify           func(io.ReaderAt, int64) bool
}

var bookFormats = []bookFormat{
	{
		Format:     FormatEPUB,
		Key:        "epub",
		Label:      "EPUB",
		Extensions: []string{".epub"},
		MediaTypes: map[string]string{".epub": "application/epub+zip"},
		Reader:     ReaderFoliate,
	},
	{
		Format:           FormatKEPUB,
		Key:              "kepub",
		Label:            "KEPUB",
		Extensions:       []string{".kepub", ".kepub.epub"},
		DefaultExtension: ".kepub.epub",
		MediaTypes: map[string]string{
			".kepub":      "application/epub+zip",
			".kepub.epub": "application/epub+zip",
		},
		Reader: ReaderFoliate,
	},
	{
		Format:     FormatPDF,
		Key:        "pdf",
		Label:      "PDF",
		Extensions: []string{".pdf"},
		MediaTypes: map[string]string{".pdf": "application/pdf"},
		Reader:     ReaderPDF,
	},
	{
		Format:     FormatFB2,
		Key:        "fb2",
		Label:      "FB2",
		Extensions: []string{".fb2", ".fb2.zip", ".fb2.gz", ".fbz"},
		MediaTypes: map[string]string{
			".fb2":     "application/x-fictionbook+xml",
			".fb2.zip": "application/zip",
			".fb2.gz":  "application/gzip",
			".fbz":     "application/zip",
		},
		Reader: ReaderFoliate,
		FB2Containers: map[string]FB2Container{
			".fb2.zip": FB2ContainerZip,
			".fb2.gz":  FB2ContainerGzip,
			".fbz":     FB2ContainerZip,
		},
	},
	{
		Format:     FormatMOBI,
		Key:        "mobi",
		Label:      "MOBI",
		Extensions: []string{".mobi"},
		MediaTypes: map[string]string{".mobi": "application/x-mobipocket-ebook"},
		Reader:     ReaderFoliate,
	},
	{
		Format:     FormatAZW,
		Key:        "azw",
		Label:      "AZW",
		Extensions: []string{".azw"},
		MediaTypes: map[string]string{".azw": "application/x-mobipocket-ebook"},
		Reader:     ReaderFoliate,
	},
	{
		Format:     FormatAZW3,
		Key:        "azw3",
		Label:      "AZW3",
		Extensions: []string{".azw3"},
		MediaTypes: map[string]string{".azw3": "application/vnd.amazon.ebook"},
		Reader:     ReaderFoliate,
	},
	{
		Format:     FormatAZW4,
		Key:        "azw4",
		Label:      "AZW4",
		Extensions: []string{".azw4"},
		MediaTypes: map[string]string{".azw4": "application/vnd.amazon.ebook"},
	},
	{
		Format:     FormatPRC,
		Key:        "prc",
		Label:      "PRC",
		Extensions: []string{".prc"},
		MediaTypes: map[string]string{".prc": "application/x-mobipocket-ebook"},
		Reader:     ReaderFoliate,
	},
	{
		Format:     FormatPDB,
		Key:        "pdb",
		Label:      "PDB",
		Extensions: []string{".pdb"},
		MediaTypes: map[string]string{".pdb": "application/vnd.palm"},
		Verify:     isPalmDOC,
	},
	{
		Format:     FormatCBZ,
		Key:        "cbz",
		Label:      "CBZ",
		Extensions: []string{".cbz"},
		MediaTypes: map[string]string{".cbz": "application/vnd.comicbook+zip"},
		Reader:     ReaderFoliate,
		Verify:     isCBZ,
	},
	{
		Format:     FormatCBR,
		Key:        "cbr",
		Label:      "CBR",
		Extensions: []string{".cbr"},
		MediaTypes: map[string]string{".cbr": "application/vnd.comicbook-rar"},
		Reader:     ReaderFoliate,
		Verify:     isCBR,
	},
	{
		Format:     FormatCB7,
		Key:        "cb7",
		Label:      "CB7",
		Extensions: []string{".cb7"},
		MediaTypes: map[string]string{".cb7": "application/x-7z-compressed"},
		Reader:     ReaderFoliate,
		Verify:     isCB7,
	},
	{
		Format:     FormatDJVU,
		Key:        "djvu",
		Label:      "DJVU",
		Extensions: []string{".djvu", ".djv"},
		MediaTypes: map[string]string{
			".djvu": "image/vnd.djvu",
			".djv":  "image/vnd.djvu",
		},
		Verify: isDJVU,
	},
	{
		Format:     FormatTXT,
		Key:        "txt",
		Label:      "TXT",
		Extensions: []string{".txt", ".text"},
		MediaTypes: map[string]string{
			".txt":  "text/plain",
			".text": "text/plain",
		},
		Verify: isPlainText,
	},
	{
		Format:     FormatTXTZ,
		Key:        "txtz",
		Label:      "TXTZ",
		Extensions: []string{".txtz"},
		MediaTypes: map[string]string{".txtz": "application/zip"},
		Verify:     isTXTZ,
	},
	{
		Format:     FormatMarkdown,
		Key:        "markdown",
		Label:      "Markdown",
		Extensions: []string{".md", ".markdown"},
		MediaTypes: map[string]string{
			".md":       "text/markdown",
			".markdown": "text/markdown",
		},
		Verify: isPlainText,
	},
	{
		Format:     FormatTextile,
		Key:        "textile",
		Label:      "Textile",
		Extensions: []string{".textile"},
		MediaTypes: map[string]string{".textile": "text/x-textile"},
		Verify:     isPlainText,
	},
	{
		Format:     FormatHTML,
		Key:        "html",
		Label:      "HTML",
		Extensions: []string{".html", ".htm"},
		MediaTypes: map[string]string{
			".html": "text/html",
			".htm":  "text/html",
		},
		Verify: isHTML,
	},
	{
		Format:     FormatXHTML,
		Key:        "xhtml",
		Label:      "XHTML",
		Extensions: []string{".xhtml", ".xhtm"},
		MediaTypes: map[string]string{
			".xhtml": "application/xhtml+xml",
			".xhtm":  "application/xhtml+xml",
		},
		Verify: isHTML,
	},
	{
		Format:     FormatHTMLZ,
		Key:        "htmlz",
		Label:      "HTMLZ",
		Extensions: []string{".htmlz"},
		MediaTypes: map[string]string{".htmlz": "application/zip"},
		Verify:     isHTMLZ,
	},
	{
		Format:     FormatDOCX,
		Key:        "docx",
		Label:      "DOCX",
		Extensions: []string{".docx"},
		MediaTypes: map[string]string{
			".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		},
		Verify: isDOCX,
	},
	{
		Format:     FormatDOCM,
		Key:        "docm",
		Label:      "DOCM",
		Extensions: []string{".docm"},
		MediaTypes: map[string]string{
			".docm": "application/vnd.ms-word.document.macroEnabled.12",
		},
		Verify: isDOCX,
	},
	{
		Format:     FormatODT,
		Key:        "odt",
		Label:      "ODT",
		Extensions: []string{".odt"},
		MediaTypes: map[string]string{
			".odt": "application/vnd.oasis.opendocument.text",
		},
		Verify: isODT,
	},
	{
		Format:     FormatRTF,
		Key:        "rtf",
		Label:      "RTF",
		Extensions: []string{".rtf"},
		MediaTypes: map[string]string{".rtf": "application/rtf"},
		Verify:     isRTF,
	},
	{
		Format:     FormatCHM,
		Key:        "chm",
		Label:      "CHM",
		Extensions: []string{".chm"},
		MediaTypes: map[string]string{".chm": "application/vnd.ms-htmlhelp"},
		Verify:     isCHM,
	},
}

// RegisteredFormats returns every import format in registry order. The slice is
// detached from the registry so diagnostics can enumerate capabilities without
// gaining a mutable view of format configuration.
func RegisteredFormats() []Format {
	formats := make([]Format, 0, len(bookFormats))
	for _, registered := range bookFormats {
		formats = append(formats, registered.Format)
	}
	return formats
}

// BookExtension returns the import/storage extension for a book-like path. It
// preserves the original case and keeps registry-known multipart extensions
// (for example .fb2.zip) as one logical extension.
func BookExtension(p string) string {
	lower := strings.ToLower(p)
	var matchLen int
	for _, format := range bookFormats {
		for _, ext := range format.Extensions {
			if len(ext) > matchLen && strings.HasSuffix(lower, ext) {
				matchLen = len(ext)
			}
		}
	}
	if matchLen > 0 {
		return p[len(p)-matchLen:]
	}
	return filepath.Ext(p)
}

// FormatFromExt maps a file extension (with or without the leading dot) to a
// Format, without inspecting file contents. Used when the format is already
// trusted (e.g. re-reading an asset whose extension the DB recorded).
func FormatFromExt(ext string) Format {
	if format, _, ok := bookFormatByExtension(ext); ok {
		return format.Format
	}
	return FormatUnknown
}

func FormatLabel(format Format) string {
	for _, f := range bookFormats {
		if f.Format == format {
			return f.Label
		}
	}
	return "Unknown"
}

// FormatKey returns the stable lower-case key stored in SQLite for a detected
// asset format. Keep this independent from Go enum values and display labels so
// DB rows do not depend on iota ordering or UI copy.
func FormatKey(format Format) string {
	for _, f := range bookFormats {
		if f.Format == format {
			return f.Key
		}
	}
	return "unknown"
}

func FormatFromKey(key string) Format {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" || key == "unknown" {
		return FormatUnknown
	}
	for _, f := range bookFormats {
		if key == f.Key {
			return f.Format
		}
	}
	return FormatUnknown
}

// DefaultExtensionForFormat returns the canonical extension for a registered
// format. Callers use it when they need a representative extension for a known
// format, such as choosing a media type fallback.
func DefaultExtensionForFormat(format Format) string {
	for _, f := range bookFormats {
		if f.Format != format || len(f.Extensions) == 0 {
			continue
		}
		if f.DefaultExtension != "" {
			return f.DefaultExtension
		}
		return f.Extensions[0]
	}
	return ""
}

func bookFormatByExtension(ext string) (*bookFormat, string, bool) {
	ext = normalizeBookExtension(ext)
	if ext == "" {
		return nil, "", false
	}
	for i := range bookFormats {
		format := &bookFormats[i]
		if slices.Contains(format.Extensions, ext) {
			return format, ext, true
		}
	}
	return nil, "", false
}

// KnownBookExtension reports whether ext is one of Polka's accepted book
// extensions. Pass an extension, not a full path; use BookExtension(path) first
// when starting from a filename.
func KnownBookExtension(ext string) bool {
	_, _, ok := bookFormatByExtension(ext)
	return ok
}

// BookUploadAccept returns the browser file-input accept list for formats Polka
// can import. Keep this derived from the registry so upload does not drift from
// CLI import support when new formats are added.
func BookUploadAccept() string {
	tokens := make([]string, 0, len(bookFormats)*2)
	seenMediaTypes := make(map[string]bool)
	for _, format := range bookFormats {
		tokens = append(tokens, format.Extensions...)
		for _, ext := range format.Extensions {
			mediaType := format.MediaTypes[ext]
			if mediaType == "" || seenMediaTypes[mediaType] {
				continue
			}
			seenMediaTypes[mediaType] = true
			tokens = append(tokens, mediaType)
		}
	}
	return strings.Join(tokens, ",")
}

func MediaTypeForExtension(ext string) string {
	if format, ext, ok := bookFormatByExtension(ext); ok {
		if mediaType := format.MediaTypes[ext]; mediaType != "" {
			return mediaType
		}
	}
	return "application/octet-stream"
}

func ReaderForFormat(format Format) ReaderKind {
	for _, f := range bookFormats {
		if f.Format == format {
			return f.Reader
		}
	}
	return ReaderNone
}

func CanRead(format Format) bool {
	return ReaderForFormat(format) != ReaderNone
}

func FB2ContainerForExtension(ext string) FB2Container {
	if format, ext, ok := bookFormatByExtension(ext); ok {
		return format.FB2Containers[ext]
	}
	return FB2ContainerNone
}

func normalizeBookExtension(ext string) string {
	value := strings.ToLower(strings.TrimSpace(ext))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, ".") {
		return value
	}
	return "." + value
}

// DetectFormat treats the filename extension as authoritative: it selects the
// only candidate format considered here, and content checks accept or reject
// that candidate where required. Contents do not select a different format,
// except that PalmDOC content in a .mobi or .prc file returns FormatPDB.
func DetectFormat(p string, r io.ReaderAt, size int64) Format {
	formatInfo, ext, known := bookFormatByExtension(BookExtension(p))

	if known && IsEPUBContainerFormat(formatInfo.Format) {
		if isEPUB(r, size) {
			return formatInfo.Format
		}
	}

	if ext == ".pdf" {
		if isPDF(r) {
			return FormatPDF
		}
	}

	if known && formatInfo.Format == FormatFB2 {
		switch formatInfo.FB2Containers[ext] {
		case FB2ContainerNone:
			// Bare .fb2 is accepted by extension without a content signature; it
			// should never fall through as FormatUnknown.
			return FormatFB2
		case FB2ContainerZip:
			if isZippedFB2(r, size) {
				return FormatFB2
			}
		case FB2ContainerGzip:
			if isGzipFB2(r, size) {
				return FormatFB2
			}
		}
	}

	if known {
		switch formatInfo.Format {
		case FormatMOBI, FormatAZW, FormatAZW3, FormatAZW4, FormatPRC:
			if isMOBI(r, size) {
				return formatInfo.Format
			}
			if isPalmDOCContainerExtension(ext) && isPalmDOC(r, size) {
				return FormatPDB
			}
		}
		if formatInfo.Verify != nil && formatInfo.Verify(r, size) {
			return formatInfo.Format
		}
	}

	return FormatUnknown
}

func isPDF(r io.ReaderAt) bool {
	var head [5]byte
	n, _ := r.ReadAt(head[:], 0)
	return n == len(head) && string(head[:]) == "%PDF-"
}

func isPalmDOCContainerExtension(ext string) bool {
	switch ext {
	case ".mobi", ".prc":
		return true
	default:
		return false
	}
}

// IsEPUBContainerFormat reports whether a format is physically an EPUB ZIP
// container and can share EPUB metadata/container handling.
func IsEPUBContainerFormat(kind Format) bool {
	return kind == FormatEPUB || kind == FormatKEPUB
}

// metadataWritebackFormats is the single source of truth for the formats whose
// embedded metadata Polka can rewrite in place. SupportsMetadataWriteback (the
// renderer dispatch), the DB dirty queries and writeback partial index (via
// MetadataWritebackFormatKeys), and the corpus write-back check all derive from
// this set; keep them in sync when it grows.
var metadataWritebackFormats = []Format{FormatEPUB, FormatKEPUB, FormatFB2}

// SupportsMetadataWriteback reports whether Polka has an embedded-metadata
// renderer for a format. Unwritable formats are simply never dirty.
func SupportsMetadataWriteback(kind Format) bool {
	return slices.Contains(metadataWritebackFormats, kind)
}

// MetadataWritebackFormatKeys returns the sorted DB format keys for the formats
// SupportsMetadataWriteback accepts, for composing SQL IN clauses that must not
// drift from the renderer dispatch.
func MetadataWritebackFormatKeys() []string {
	keys := make([]string, 0, len(metadataWritebackFormats))
	for _, f := range metadataWritebackFormats {
		keys = append(keys, FormatKey(f))
	}
	slices.Sort(keys)
	return keys
}

func isEPUB(r io.ReaderAt, size int64) bool {
	zr, err := zip.NewReader(r, size)
	if err != nil || len(zr.File) == 0 {
		return false
	}
	if hasStrictEPUBMimetype(zr) {
		return true
	}
	_, ok, err := readEPUBOPF(zr)
	return err == nil && ok
}

func hasStrictEPUBMimetype(zr *zip.Reader) bool {
	if len(zr.File) == 0 {
		return false
	}
	f := zr.File[0]
	if f.Name != "mimetype" || f.Method != zip.Store {
		return false
	}
	b, err := readZipFileLimited(f, maxEPUBMimetypeBytes)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == epubMimetype
}

func isZippedFB2(r io.ReaderAt, size int64) bool {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false
	}
	_, err = SingleFB2ZipEntry(zr)
	return err == nil
}

func isGzipFB2(r io.ReaderAt, size int64) bool {
	err := validateFB2Gzip(r, size)
	return err == nil
}

// SingleFB2ZipEntry returns the sole .fb2 file inside a zip-backed FB2 asset.
// Archives with no .fb2 entry or multiple .fb2 entries are ambiguous for Polka's
// reader and are treated as unreadable.
func SingleFB2ZipEntry(zr *zip.Reader) (*zip.File, error) {
	var found *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(f.Name), ".fb2") {
			continue
		}
		if found != nil {
			return nil, errors.New("FB2 archive contains multiple .fb2 files")
		}
		found = f
	}
	if found == nil {
		return nil, errors.New("FB2 archive contains no .fb2 file")
	}
	return found, nil
}
