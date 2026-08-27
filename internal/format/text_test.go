package format

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	"io"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestDetectFormatTextFamily(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want Format
	}{
		{name: "story.txt", data: []byte("First paragraph.\n\nSecond paragraph.\n"), want: FormatTXT},
		{name: "story.text", data: []byte("Plain text alias.\n"), want: FormatTXT},
		{name: "notes.md", data: []byte("# Heading\n\nMarkdown body.\n"), want: FormatMarkdown},
		{name: "notes.markdown", data: []byte("# Heading\n\nMarkdown body.\n"), want: FormatMarkdown},
		{name: "story.textile", data: []byte("h1. Heading\n\nTextile body.\n"), want: FormatTextile},
		{name: "utf16.txt", data: []byte{0xff, 0xfe, 'H', 0, 'i', 0, '\n', 0}, want: FormatTXT},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFormatTextFamilyRejectsBinary(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "renamed-pdf.txt", data: []byte("%PDF-1.7\nbody")},
		{name: "nul-byte.md", data: []byte("hello\x00world")},
		{name: "zip-header.textile", data: []byte("PK\x03\x04archive")},
		{name: "control-byte.markdown", data: []byte("hello\x01world")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}

func TestDetectFormatTXTZ(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries map[string][]byte
		want    Format
	}{
		{
			name: "valid txtz",
			entries: map[string][]byte{
				"book.md":      []byte("# Heading\n\nBook body.\n"),
				"metadata.opf": []byte(`<package><metadata><title>Ignored here</title></metadata></package>`),
				"image.png":    {0x89, 'P', 'N', 'G'},
			},
			want: FormatTXTZ,
		},
		{
			name: "nested text",
			entries: map[string][]byte{
				"text/book.txt": []byte("Nested text body.\n"),
			},
			want: FormatTXTZ,
		},
		{
			name: "no text entries",
			entries: map[string][]byte{
				"metadata.opf": []byte(`<package/>`),
				"image.png":    {0x89, 'P', 'N', 'G'},
			},
			want: FormatUnknown,
		},
		{
			name: "binary text entry",
			entries: map[string][]byte{
				"book.txt": []byte("hello\x00world"),
			},
			want: FormatUnknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := writeTestZip(t, tt.entries)
			r := bytes.NewReader(data)
			if got := DetectFormat("book.txtz", r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestOpenTXTZTextSource(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"z.txt":     []byte("Later plain text.\n"),
		"a/book.md": []byte("# Picked markdown\n"),
	})
	r := bytes.NewReader(data)
	src, err := OpenTXTZTextSource(r, r.Size())
	if err != nil {
		t.Fatalf("OpenTXTZTextSource: %v", err)
	}
	defer src.Reader.Close()

	if src.Filename != "a/book.md" {
		t.Fatalf("Filename = %q; want a/book.md", src.Filename)
	}
	if src.Format != FormatMarkdown {
		t.Fatalf("Format = %v; want FormatMarkdown", src.Format)
	}
	body, err := io.ReadAll(src.Reader)
	if err != nil {
		t.Fatalf("read text source: %v", err)
	}
	if string(body) != "# Picked markdown\n" {
		t.Fatalf("body = %q; want markdown body", body)
	}
}

func TestReadTXTZTextConcatenatesReadableEntries(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"02.txt":    []byte("Second chapter.\n"),
		"01.txt":    []byte("First chapter.\n"),
		"image.png": tinyPNG,
	})
	r := bytes.NewReader(data)
	body, sourceFormat, err := ReadTXTZTextContextLimited(context.Background(), r, r.Size(), -1)
	if err != nil {
		t.Fatalf("ReadTXTZText: %v", err)
	}
	if sourceFormat != FormatTXT {
		t.Fatalf("sourceFormat = %v; want FormatTXT", sourceFormat)
	}
	want := "First chapter.\n\nSecond chapter."
	if string(body) != want {
		t.Fatalf("body = %q; want %q", body, want)
	}
}

func TestReadTXTZTextLimitedRejectsOversizedConcatenation(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"01.txt": []byte("abc\n"),
		"02.txt": []byte("def\n"),
	})
	r := bytes.NewReader(data)
	_, _, err := ReadTXTZTextContextLimited(context.Background(), r, r.Size(), 5)
	if !errors.Is(err, ErrTextTooLarge) {
		t.Fatalf("ReadTXTZTextLimited error = %v; want ErrTextTooLarge", err)
	}
}

func TestReadTXTZTextContextLimitedCanceled(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"book.txt": []byte("Canceled text.\n"),
	})
	r := bytes.NewReader(data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := ReadTXTZTextContextLimited(ctx, r, r.Size(), 128)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadTXTZTextContextLimited error = %v; want context.Canceled", err)
	}
}

func TestReadTXTZTextHonorsTextFormattingHint(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"book.txt": []byte("# Markdown heading\n"),
		"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<metadata>
  <text-formatting>markdown</text-formatting>
</metadata>`),
	})
	r := bytes.NewReader(data)
	body, sourceFormat, err := ReadTXTZTextContextLimited(context.Background(), r, r.Size(), -1)
	if err != nil {
		t.Fatalf("ReadTXTZText: %v", err)
	}
	if sourceFormat != FormatMarkdown {
		t.Fatalf("sourceFormat = %v; want FormatMarkdown", sourceFormat)
	}
	if string(body) != "# Markdown heading" {
		t.Fatalf("body = %q; want markdown body", body)
	}
}

func TestExtractMarkdownMetadataFromLeadingLabels(t *testing.T) {
	data := []byte("# Title: Alice's Adventures in Wonderland\r\n\r\n## Author: Lewis Carroll\r\n## Year: 1865\r\n---\r\n\r\n## Chapter 1\r\nBody.\r\n")
	r := bytes.NewReader(data)
	meta, err := ExtractMarkdownMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMarkdownMetadata: %v", err)
	}
	if meta.Title != "Alice's Adventures in Wonderland" {
		t.Fatalf("Title = %q", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Lewis Carroll" {
		t.Fatalf("Authors = %+v; want Lewis Carroll", meta.Authors)
	}
	if meta.Date != "1865" {
		t.Fatalf("Date = %q; want 1865", meta.Date)
	}
}

func TestExtractMarkdownMetadataDecodesLegacyText(t *testing.T) {
	data, err := charmap.Windows1251.NewEncoder().String("# Title: \u041a\u043d\u0438\u0433\u0430\n\n## Author: \u0410\u0432\u0442\u043e\u0440\n")
	if err != nil {
		t.Fatalf("encode Windows-1251: %v", err)
	}
	r := bytes.NewReader([]byte(data))
	meta, err := ExtractMarkdownMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMarkdownMetadata: %v", err)
	}
	if meta.Title != "\u041a\u043d\u0438\u0433\u0430" {
		t.Fatalf("Title = %q; want decoded Windows-1251 title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "\u0410\u0432\u0442\u043e\u0440" {
		t.Fatalf("Authors = %+v; want decoded Windows-1251 author", meta.Authors)
	}
}

func TestExtractMarkdownMetadataIgnoresPlainHeading(t *testing.T) {
	data := []byte("# Chapter 1\n\nBody.\n")
	r := bytes.NewReader(data)
	meta, err := ExtractMarkdownMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractMarkdownMetadata: %v", err)
	}
	if meta.Title != "" || len(meta.Authors) != 0 || meta.Date != "" {
		t.Fatalf("Metadata = %+v; want empty metadata for plain heading", meta)
	}
}

func TestExtractTXTZMetadata(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"book.txt": []byte("Book body.\n"),
		"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Text Archive</dc:title>
    <dc:creator opf:file-as="Writer, Text">Text Writer</dc:creator>
    <dc:language>en</dc:language>
  </metadata>
</package>`),
	})
	r := bytes.NewReader(data)
	meta, err := ExtractTXTZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractTXTZMetadata: %v", err)
	}
	if meta.Title != "Text Archive" {
		t.Fatalf("Title = %q; want Text Archive", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Text Writer" || meta.Authors[0].SortName != "Writer, Text" {
		t.Fatalf("Authors = %+v; want Text Writer / Writer, Text", meta.Authors)
	}
}

func TestExtractTXTZLegacyOEBMetadata(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"index.txt": []byte("Legacy text archive body.\n"),
		"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<metadata>
  <dc-metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:Title>Legacy TXTZ</dc:Title>
    <dc:Creator file-as="Author, Legacy">Legacy Author</dc:Creator>
    <dc:Language>en</dc:Language>
  </dc-metadata>
</metadata>`),
	})
	r := bytes.NewReader(data)
	meta, err := ExtractTXTZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractTXTZMetadata: %v", err)
	}
	if meta.Title != "Legacy TXTZ" {
		t.Fatalf("Title = %q; want Legacy TXTZ", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Legacy Author" || meta.Authors[0].SortName != "Author, Legacy" {
		t.Fatalf("Authors = %+v; want legacy TXTZ author with sort", meta.Authors)
	}
	if meta.Language != "en" {
		t.Fatalf("Language = %q; want en", meta.Language)
	}
}

func TestExtractTXTZCover(t *testing.T) {
	cover := testPNG(t, color.NRGBA{R: 90, G: 20, B: 10, A: 255})
	data := writeTestZip(t, map[string][]byte{
		"index.txt": []byte("Text archive body.\n"),
		"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<metadata>
  <cover-relpath-from-base>images/cover.png</cover-relpath-from-base>
</metadata>`),
		"images/cover.png": cover,
	})
	r := bytes.NewReader(data)
	got, ext, err := ExtractTXTZCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractTXTZCover: %v", err)
	}
	if ext != ".png" {
		t.Fatalf("ext = %q; want .png", ext)
	}
	if !bytes.Equal(got, cover) {
		t.Fatalf("cover bytes mismatch: got %d bytes", len(got))
	}
}
