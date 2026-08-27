package format

import (
	"bytes"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestBookFormatRegistry(t *testing.T) {
	tests := []struct {
		ext        string
		format     Format
		label      string
		mediaType  string
		readerKind ReaderKind
		container  FB2Container
	}{
		{ext: ".epub", format: FormatEPUB, label: "EPUB", mediaType: "application/epub+zip", readerKind: ReaderFoliate},
		{ext: ".kepub", format: FormatKEPUB, label: "KEPUB", mediaType: "application/epub+zip", readerKind: ReaderFoliate},
		{ext: ".kepub.epub", format: FormatKEPUB, label: "KEPUB", mediaType: "application/epub+zip", readerKind: ReaderFoliate},
		{ext: ".pdf", format: FormatPDF, label: "PDF", mediaType: "application/pdf", readerKind: ReaderPDF},
		{ext: ".fb2", format: FormatFB2, label: "FB2", mediaType: "application/x-fictionbook+xml", readerKind: ReaderFoliate},
		{ext: ".fb2.zip", format: FormatFB2, label: "FB2", mediaType: "application/zip", readerKind: ReaderFoliate, container: FB2ContainerZip},
		{ext: ".fb2.gz", format: FormatFB2, label: "FB2", mediaType: "application/gzip", readerKind: ReaderFoliate, container: FB2ContainerGzip},
		{ext: ".fbz", format: FormatFB2, label: "FB2", mediaType: "application/zip", readerKind: ReaderFoliate, container: FB2ContainerZip},
		{ext: ".mobi", format: FormatMOBI, label: "MOBI", mediaType: "application/x-mobipocket-ebook", readerKind: ReaderFoliate},
		{ext: ".azw", format: FormatAZW, label: "AZW", mediaType: "application/x-mobipocket-ebook", readerKind: ReaderFoliate},
		{ext: ".azw3", format: FormatAZW3, label: "AZW3", mediaType: "application/vnd.amazon.ebook", readerKind: ReaderFoliate},
		{ext: ".azw4", format: FormatAZW4, label: "AZW4", mediaType: "application/vnd.amazon.ebook"},
		{ext: ".prc", format: FormatPRC, label: "PRC", mediaType: "application/x-mobipocket-ebook", readerKind: ReaderFoliate},
		{ext: ".pdb", format: FormatPDB, label: "PDB", mediaType: "application/vnd.palm"},
		{ext: ".cbz", format: FormatCBZ, label: "CBZ", mediaType: "application/vnd.comicbook+zip", readerKind: ReaderFoliate},
		{ext: ".cbr", format: FormatCBR, label: "CBR", mediaType: "application/vnd.comicbook-rar", readerKind: ReaderFoliate},
		{ext: ".cb7", format: FormatCB7, label: "CB7", mediaType: "application/x-7z-compressed", readerKind: ReaderFoliate},
		{ext: ".djvu", format: FormatDJVU, label: "DJVU", mediaType: "image/vnd.djvu"},
		{ext: ".djv", format: FormatDJVU, label: "DJVU", mediaType: "image/vnd.djvu"},
		{ext: ".txt", format: FormatTXT, label: "TXT", mediaType: "text/plain"},
		{ext: ".text", format: FormatTXT, label: "TXT", mediaType: "text/plain"},
		{ext: ".txtz", format: FormatTXTZ, label: "TXTZ", mediaType: "application/zip"},
		{ext: ".md", format: FormatMarkdown, label: "Markdown", mediaType: "text/markdown"},
		{ext: ".markdown", format: FormatMarkdown, label: "Markdown", mediaType: "text/markdown"},
		{ext: ".textile", format: FormatTextile, label: "Textile", mediaType: "text/x-textile"},
		{ext: ".html", format: FormatHTML, label: "HTML", mediaType: "text/html"},
		{ext: ".htm", format: FormatHTML, label: "HTML", mediaType: "text/html"},
		{ext: ".xhtml", format: FormatXHTML, label: "XHTML", mediaType: "application/xhtml+xml"},
		{ext: ".xhtm", format: FormatXHTML, label: "XHTML", mediaType: "application/xhtml+xml"},
		{ext: ".htmlz", format: FormatHTMLZ, label: "HTMLZ", mediaType: "application/zip"},
		{ext: ".docx", format: FormatDOCX, label: "DOCX", mediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{ext: ".docm", format: FormatDOCM, label: "DOCM", mediaType: "application/vnd.ms-word.document.macroEnabled.12"},
		{ext: ".odt", format: FormatODT, label: "ODT", mediaType: "application/vnd.oasis.opendocument.text"},
		{ext: ".rtf", format: FormatRTF, label: "RTF", mediaType: "application/rtf"},
		{ext: ".chm", format: FormatCHM, label: "CHM", mediaType: "application/vnd.ms-htmlhelp"},
	}

	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			if !KnownBookExtension(tt.ext) {
				t.Fatalf("KnownBookExtension(%q) = false", tt.ext)
			}
			if got := FormatFromExt(tt.ext); got != tt.format {
				t.Fatalf("FormatFromExt = %v; want %v", got, tt.format)
			}
			if FormatLabel(tt.format) != tt.label {
				t.Fatalf("FormatLabel = %q; want %q", FormatLabel(tt.format), tt.label)
			}
			key := FormatKey(tt.format)
			if key == "" || key == "unknown" {
				t.Fatalf("FormatKey(%v) = %q; want stable non-unknown key", tt.format, key)
			}
			if got := FormatFromKey(key); got != tt.format {
				t.Fatalf("FormatFromKey(%q) = %v; want %v", key, got, tt.format)
			}
			if MediaTypeForExtension(tt.ext) != tt.mediaType {
				t.Fatalf("MediaTypeForExtension = %q; want %q", MediaTypeForExtension(tt.ext), tt.mediaType)
			}
			if reader := ReaderForFormat(tt.format); reader != tt.readerKind {
				t.Fatalf("ReaderForFormat = %q; want %q", reader, tt.readerKind)
			}
			if container := FB2ContainerForExtension(tt.ext); container != tt.container {
				t.Fatalf("FB2ContainerForExtension = %q; want %q", container, tt.container)
			}
		})
	}

	if !KnownBookExtension("pdf") {
		t.Fatal("extension without leading dot should be known")
	}
	if KnownBookExtension("notes.txt") {
		t.Fatal("full paths/names should not be treated as extensions")
	}
	if MediaTypeForExtension(".bin") != "application/octet-stream" {
		t.Fatalf("unknown media type = %q", MediaTypeForExtension(".bin"))
	}
	if FormatKey(FormatUnknown) != "unknown" {
		t.Fatalf("FormatKey(unknown) = %q; want unknown", FormatKey(FormatUnknown))
	}
	if FormatFromKey("unknown") != FormatUnknown || FormatFromKey("") != FormatUnknown || FormatFromKey("bogus") != FormatUnknown {
		t.Fatal("unknown/empty/bogus format keys should map to FormatUnknown")
	}
}

func TestBookFormatKeysStableAndUnique(t *testing.T) {
	expected := map[Format]string{
		FormatEPUB:     "epub",
		FormatKEPUB:    "kepub",
		FormatPDF:      "pdf",
		FormatFB2:      "fb2",
		FormatMOBI:     "mobi",
		FormatAZW:      "azw",
		FormatAZW3:     "azw3",
		FormatAZW4:     "azw4",
		FormatPRC:      "prc",
		FormatPDB:      "pdb",
		FormatCBZ:      "cbz",
		FormatCBR:      "cbr",
		FormatCB7:      "cb7",
		FormatDJVU:     "djvu",
		FormatTXT:      "txt",
		FormatTXTZ:     "txtz",
		FormatMarkdown: "markdown",
		FormatTextile:  "textile",
		FormatHTML:     "html",
		FormatXHTML:    "xhtml",
		FormatHTMLZ:    "htmlz",
		FormatDOCX:     "docx",
		FormatDOCM:     "docm",
		FormatODT:      "odt",
		FormatRTF:      "rtf",
		FormatCHM:      "chm",
	}
	if len(bookFormats) != len(expected) {
		t.Fatalf("bookFormats has %d entries; want %d expected keys", len(bookFormats), len(expected))
	}

	seen := make(map[string]Format, len(bookFormats))
	for _, entry := range bookFormats {
		want, ok := expected[entry.Format]
		if !ok {
			t.Fatalf("unexpected format in registry: %v", entry.Format)
		}
		if entry.Key != want {
			t.Fatalf("registry key for %s = %q; want stable key %q", entry.Label, entry.Key, want)
		}
		if entry.Key == "" || entry.Key != strings.ToLower(entry.Key) || strings.ContainsAny(entry.Key, " \t\r\n") {
			t.Fatalf("registry key for %s is not a plain lower-case key: %q", entry.Label, entry.Key)
		}
		if previous, ok := seen[entry.Key]; ok {
			t.Fatalf("duplicate registry key %q for %v and %v", entry.Key, previous, entry.Format)
		}
		seen[entry.Key] = entry.Format
		if got := FormatKey(entry.Format); got != entry.Key {
			t.Fatalf("FormatKey(%s) = %q; want registry key %q", entry.Label, got, entry.Key)
		}
		if got := FormatFromKey(entry.Key); got != entry.Format {
			t.Fatalf("FormatFromKey(%q) = %v; want %v", entry.Key, got, entry.Format)
		}
	}
}

func TestBookUploadAcceptDerivedFromRegistry(t *testing.T) {
	accept := BookUploadAccept()
	tokens := strings.Split(accept, ",")
	seen := make(map[string]int, len(tokens))
	for _, token := range tokens {
		if token == "" {
			t.Fatalf("BookUploadAccept contains an empty token: %q", accept)
		}
		seen[token]++
	}

	for _, format := range bookFormats {
		for _, ext := range format.Extensions {
			if seen[ext] != 1 {
				t.Fatalf("BookUploadAccept extension %q count = %d; want 1 in %q", ext, seen[ext], accept)
			}
			mediaType := format.MediaTypes[ext]
			if mediaType != "" && seen[mediaType] != 1 {
				t.Fatalf("BookUploadAccept media type %q count = %d; want 1 in %q", mediaType, seen[mediaType], accept)
			}
		}
	}

	for _, token := range []string{".azw4", ".cb7", ".chm", "application/vnd.amazon.ebook", "application/x-7z-compressed"} {
		if seen[token] != 1 {
			t.Fatalf("BookUploadAccept token %q count = %d; want 1 in %q", token, seen[token], accept)
		}
	}
}

func TestRegisteredFormatsReturnsDetachedCopy(t *testing.T) {
	formats := RegisteredFormats()
	formats[0] = FormatUnknown
	if RegisteredFormats()[0] == FormatUnknown {
		t.Fatal("RegisteredFormats returned mutable registry storage")
	}
}

func TestDefaultExtensionForFormat(t *testing.T) {
	tests := []struct {
		format Format
		want   string
	}{
		{format: FormatEPUB, want: ".epub"},
		{format: FormatKEPUB, want: ".kepub.epub"},
		{format: FormatDJVU, want: ".djvu"},
		{format: FormatUnknown, want: ""},
	}

	for _, tt := range tests {
		if got := DefaultExtensionForFormat(tt.format); got != tt.want {
			t.Fatalf("DefaultExtensionForFormat(%v) = %q; want %q", tt.format, got, tt.want)
		}
	}
}

func TestDetectFormatMOBIFamilyRequiresMOBIContainer(t *testing.T) {
	for _, tt := range []struct {
		name string
		want Format
	}{
		{name: "legacy.mobi", want: FormatMOBI},
		{name: "kindle.azw", want: FormatAZW},
		{name: "kindle.azw3", want: FormatAZW3},
		{name: "kindle.azw4", want: FormatAZW4},
		{name: "palm.prc", want: FormatPRC},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := testfixture.MinimalMOBI()
			r := bytes.NewReader(data)
			if got := DetectFormat(tt.name, r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFormatMOBIFamilyRejectsExtensionOnlyFiles(t *testing.T) {
	for _, name := range []string{"legacy.mobi", "kindle.azw", "kindle.azw3", "kindle.azw4", "palm.prc"} {
		t.Run(name, func(t *testing.T) {
			data := []byte("opaque book bytes")
			r := bytes.NewReader(data)
			if got := DetectFormat(name, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}

func TestBookExtensionUsesRegisteredMultipartExtensions(t *testing.T) {
	for _, tt := range []struct {
		name string
		want string
	}{
		{name: "Book.FB2.ZIP", want: ".FB2.ZIP"},
		{name: "Book.FB2.GZ", want: ".FB2.GZ"},
		{name: "Book.FBZ", want: ".FBZ"},
		{name: "Comic.CBZ", want: ".CBZ"},
		{name: "Book.KEPUB", want: ".KEPUB"},
		{name: "Book.KEPUB.EPUB", want: ".KEPUB.EPUB"},
		{name: "Book.AZW", want: ".AZW"},
		{name: "Book.PRC", want: ".PRC"},
		{name: "Book.PDB", want: ".PDB"},
		{name: "Book.AZW4", want: ".AZW4"},
		{name: "Scan.DJVU", want: ".DJVU"},
		{name: "Scan.DJV", want: ".DJV"},
		{name: "Comic.CBR", want: ".CBR"},
		{name: "Comic.CB7", want: ".CB7"},
		{name: "Archive.TXTZ", want: ".TXTZ"},
		{name: "Book.TEXT", want: ".TEXT"},
		{name: "Notes.MD", want: ".MD"},
		{name: "Notes.MARKDOWN", want: ".MARKDOWN"},
		{name: "Story.TEXTILE", want: ".TEXTILE"},
		{name: "Story.HTML", want: ".HTML"},
		{name: "Story.HTM", want: ".HTM"},
		{name: "Story.XHTML", want: ".XHTML"},
		{name: "Story.XHTM", want: ".XHTM"},
		{name: "Archive.HTMLZ", want: ".HTMLZ"},
		{name: "Document.DOCX", want: ".DOCX"},
		{name: "Document.DOCM", want: ".DOCM"},
		{name: "Document.ODT", want: ".ODT"},
		{name: "Document.RTF", want: ".RTF"},
		{name: "Manual.CHM", want: ".CHM"},
		{name: "Book.PDF", want: ".PDF"},
		{name: "notes.txt", want: ".txt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := BookExtension(tt.name); got != tt.want {
				t.Fatalf("BookExtension(%q) = %q; want %q", tt.name, got, tt.want)
			}
		})
	}
}
