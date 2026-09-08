package format

import (
	"bytes"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestBookFormatAliases(t *testing.T) {
	for _, tt := range []struct {
		ext       string
		format    Format
		mediaType string
		reader    ReaderKind
		container FB2Container
	}{
		{"EPUB", FormatEPUB, "application/epub+zip", ReaderFoliate, ""},
		{".kepub.epub", FormatKEPUB, "application/epub+zip", ReaderFoliate, ""},
		{".PDF", FormatPDF, "application/pdf", ReaderPDF, ""},
		{".fb2.zip", FormatFB2, "application/zip", ReaderFoliate, FB2ContainerZip},
		{".fbz", FormatFB2, "application/zip", ReaderFoliate, FB2ContainerZip},
		{".fb2.gz", FormatFB2, "application/gzip", ReaderFoliate, FB2ContainerGzip},
		{".djv", FormatDJVU, "image/vnd.djvu", "", ""},
		{".bin", FormatUnknown, "application/octet-stream", "", ""},
	} {
		t.Run(tt.ext, func(t *testing.T) {
			if got := FormatFromExt(tt.ext); got != tt.format {
				t.Fatalf("FormatFromExt = %v; want %v", got, tt.format)
			}
			if got := MediaTypeForExtension(tt.ext); got != tt.mediaType {
				t.Errorf("MediaTypeForExtension = %q; want %q", got, tt.mediaType)
			}
			if got := ReaderForFormat(tt.format); got != tt.reader {
				t.Errorf("ReaderForFormat = %q; want %q", got, tt.reader)
			}
			if got := FB2ContainerForExtension(tt.ext); got != tt.container {
				t.Errorf("FB2ContainerForExtension = %q; want %q", got, tt.container)
			}
		})
	}
	if KnownBookExtension("notes.txt") {
		t.Fatal("full file names should not be treated as extensions")
	}
}

func TestBookFormatKeyRoundTrip(t *testing.T) {
	for _, format := range RegisteredFormats() {
		key := FormatKey(format)
		if key == "" || key == "unknown" || FormatFromKey(key) != format {
			t.Errorf("format %v does not have an unambiguous key: %q", format, key)
		}
	}
	for _, key := range []string{"unknown", "", "bogus"} {
		if got := FormatFromKey(key); got != FormatUnknown {
			t.Errorf("FormatFromKey(%q) = %v; want unknown", key, got)
		}
	}
}

func TestBookUploadAccept(t *testing.T) {
	counts := make(map[string]int)
	for _, token := range strings.Split(BookUploadAccept(), ",") {
		counts[token]++
	}
	for _, token := range []string{".epub", ".kepub.epub", ".fb2.zip", ".fbz", "application/epub+zip", "application/zip"} {
		if counts[token] != 1 {
			t.Errorf("upload accept token %q count = %d; want 1", token, counts[token])
		}
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

func TestBookExtension(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"Book.FB2.ZIP", ".FB2.ZIP"},
		{"Book.FB2.GZ", ".FB2.GZ"},
		{"Book.KEPUB.EPUB", ".KEPUB.EPUB"},
		{"notes.txt", ".txt"},
		{"opaque.BIN", ".BIN"},
		{"no-extension", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := BookExtension(tt.name); got != tt.want {
				t.Fatalf("BookExtension(%q) = %q; want %q", tt.name, got, tt.want)
			}
		})
	}
}
