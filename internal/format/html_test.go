package format

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
)

func TestDetectFormatHTMLFamily(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want Format
	}{
		{name: "book.html", data: []byte("<!doctype html><html><head><title>Book</title></head><body>Text</body></html>"), want: FormatHTML},
		{name: "book.htm", data: []byte("<html><body>Text</body></html>"), want: FormatHTML},
		{name: "book.xhtml", data: []byte(`<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>X</title></head></html>`), want: FormatXHTML},
		{name: "book.xhtm", data: []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body>X</body></html>`), want: FormatXHTML},
		{name: "utf16.html", data: utf16LEHTML("<!doctype html><html><head><title>Book</title></head><body>Text</body></html>"), want: FormatHTML},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFormatHTMLRejectsExtensionOnlyFiles(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "plain.html", data: []byte("not actually html")},
		{name: "pdf.html", data: []byte("%PDF-1.7\nbody")},
		{name: "binary.xhtml", data: []byte("hello\x00world")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}

func TestDetectFormatHTMLZ(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries map[string][]byte
		want    Format
	}{
		{
			name: "index html",
			entries: map[string][]byte{
				"index.html": []byte("<html><head><title>HTMLZ</title></head><body>Text</body></html>"),
				"style.css":  []byte("body { color: black; }"),
			},
			want: FormatHTMLZ,
		},
		{
			name: "fallback top-level html",
			entries: map[string][]byte{
				"book.xhtml": []byte("<html><body>Text</body></html>"),
			},
			want: FormatHTMLZ,
		},
		{
			name: "nested html ignored",
			entries: map[string][]byte{
				"OPS/index.html": []byte("<html><body>Text</body></html>"),
			},
			want: FormatUnknown,
		},
		{
			name: "binary html entry",
			entries: map[string][]byte{
				"index.html": []byte("hello\x00world"),
			},
			want: FormatUnknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := writeTestZip(t, tt.entries)
			r := bytes.NewReader(data)
			if got := DetectFormat("book.htmlz", r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestExtractHTMLMetadata(t *testing.T) {
	data := []byte(`<!doctype html>
<html>
<head>
  <!-- TITLE="Comment Title" AUTHOR="Comment Author" -->
  <title>Title Tag</title>
  <meta name="dc.title" content="Meta Title">
  <meta name="dc.creator" content="Meta Author">
  <meta name="dc.publisher" content="HTML Press">
  <meta name="dc.language" content="en">
  <meta name="dc.description" content="A &amp; B">
  <meta name="dc.date.issued" content="2020-05-02T00:00:00Z">
  <meta name="series" content="Web Books">
  <meta name="series_index" content="2.5">
  <meta name="subject" content="Web, Saved">
  <meta name="dc.identifier.isbn" content="978-0-306-40615-7">
  <meta name="dc.identifier.url" content="https://example.test/book">
</head>
</html>`)
	r := bytes.NewReader(data)
	meta, err := ExtractHTMLMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractHTMLMetadata: %v", err)
	}
	if meta.Title != "Comment Title" {
		t.Fatalf("Title = %q; want comment metadata to win", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Comment Author" {
		t.Fatalf("Authors = %+v; want comment author", meta.Authors)
	}
	if meta.Publisher != "HTML Press" || meta.Language != "en" || meta.Description != "A & B" {
		t.Fatalf("simple metadata = %+v; want publisher/language/description", meta)
	}
	if meta.Date != "2020-05-02" {
		t.Fatalf("Date = %q; want normalized date", meta.Date)
	}
	if meta.Series != "Web Books" || meta.SeriesIndex != 2.5 {
		t.Fatalf("Series = %q/%v; want Web Books/2.5", meta.Series, meta.SeriesIndex)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != "Web" || meta.Tags[1] != "Saved" {
		t.Fatalf("Tags = %+v; want split subject tags", meta.Tags)
	}
	if meta.Identifier != "isbn:978-0-306-40615-7, url:https://example.test/book" {
		t.Fatalf("Identifier = %q; want stable ISBN/URL order", meta.Identifier)
	}
}

func TestExtractHTMLMetadataDecodesLegacyText(t *testing.T) {
	title := "\u041a\u043d\u0438\u0433\u0430"
	author := "\u0410\u0432\u0442\u043e\u0440"
	for _, tt := range []struct {
		name       string
		src        string
		wantAuthor bool
	}{
		{
			name: "quoted declaration",
			src: `<meta charset="windows-1251">
  <title>` + title + `</title>
  <meta name="author" content="` + author + `">`,
			wantAuthor: true,
		},
		{
			name: "unquoted declaration",
			src: `<meta charset=windows-1251>
  <title>` + title + `</title>
  <meta name=author content=` + author + `>`,
			wantAuthor: true,
		},
		{
			name: "undeclared",
			src:  `<title>` + title + `</title>`,
		},
		{
			name: "incidental charset word",
			src: `<title>` + title + `</title>
  <meta name="description" content="The word charset here is prose, not an encoding declaration.">`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := windows1251HTML(t, "<!doctype html><html><head>"+tt.src+"</head></html>")
			r := bytes.NewReader(data)
			meta, err := ExtractHTMLMetadata(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractHTMLMetadata: %v", err)
			}
			if meta.Title != title {
				t.Fatalf("Title = %q; want %q", meta.Title, title)
			}
			if tt.wantAuthor && (len(meta.Authors) != 1 || meta.Authors[0].Name != author) {
				t.Fatalf("Authors = %+v; want %q", meta.Authors, author)
			}
		})
	}
}

func TestExtractHTMLZMetadata(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"index.html": []byte("<html><head><title>Ignored HTML Title</title></head></html>"),
		"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>HTMLZ OPF Title</dc:title>
    <dc:creator opf:file-as="Writer, Web">Web Writer</dc:creator>
  </metadata>
</package>`),
	})
	r := bytes.NewReader(data)
	meta, err := ExtractHTMLZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractHTMLZMetadata: %v", err)
	}
	if meta.Title != "HTMLZ OPF Title" {
		t.Fatalf("Title = %q; want OPF title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Web Writer" || meta.Authors[0].SortName != "Writer, Web" {
		t.Fatalf("Authors = %+v; want Web Writer / Writer, Web", meta.Authors)
	}
}

func TestExtractHTMLZMetadataFallsBackToHTML(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"index.html": []byte(`<html><head><title>HTML Only</title><meta name="author" content="HTML Author"></head></html>`),
	})
	r := bytes.NewReader(data)
	meta, err := ExtractHTMLZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractHTMLZMetadata: %v", err)
	}
	if meta.Title != "HTML Only" {
		t.Fatalf("Title = %q; want HTML title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "HTML Author" {
		t.Fatalf("Authors = %+v; want HTML Author", meta.Authors)
	}
}

func TestExtractHTMLCoverFromDataURI(t *testing.T) {
	for _, tt := range []struct {
		name      string
		mediaType string
		image     []byte
		wantExt   string
	}{
		{name: "PNG", mediaType: "image/png", image: tinyPNG, wantExt: ".png"},
		{name: "WebP", mediaType: "image/webp", image: tinyWebP, wantExt: ".webp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(`<html><head><link rel="cover" href="data:` + tt.mediaType + `;base64,` + base64.StdEncoding.EncodeToString(tt.image) + `"></head></html>`)
			r := bytes.NewReader(data)
			cover, ext, err := ExtractHTMLCover(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractHTMLCover: %v", err)
			}
			if ext != tt.wantExt || !bytes.Equal(cover, tt.image) {
				t.Fatalf("cover ext/bytes = %q/%d; want %s", ext, len(cover), tt.name)
			}
		})
	}
}

func TestExtractHTMLZCoverFromOPF(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata><meta name="cover" content="cover-image"/></metadata>
  <manifest>
    <item id="cover-image" href="images/cover.png" media-type="image/png"/>
  </manifest>
</package>`),
		"index.html":       []byte("<html><body>HTMLZ body.</body></html>"),
		"images/cover.png": tinyPNG,
	})
	r := bytes.NewReader(data)
	cover, ext, err := ExtractHTMLZCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractHTMLZCover: %v", err)
	}
	if ext != ".png" || !bytes.Equal(cover, tinyPNG) {
		t.Fatalf("cover ext/bytes = %q/%d; want OPF cover image", ext, len(cover))
	}
}

func TestExtractHTMLZCoverFallsBackToFirstHTMLImage(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"index.html":      []byte(`<html><body><img src="images/page.png"/></body></html>`),
		"images/page.png": tinyPNG,
	})
	r := bytes.NewReader(data)
	cover, ext, err := ExtractHTMLZCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractHTMLZCover: %v", err)
	}
	if ext != ".png" || !bytes.Equal(cover, tinyPNG) {
		t.Fatalf("cover ext/bytes = %q/%d; want first HTML image", ext, len(cover))
	}
}

func windows1251HTML(t *testing.T, src string) []byte {
	t.Helper()
	encoded, err := charmap.Windows1251.NewEncoder().String(src)
	if err != nil {
		t.Fatalf("encode windows-1251 HTML: %v", err)
	}
	return []byte(encoded)
}

func utf16LEHTML(src string) []byte {
	encoded := utf16.Encode([]rune(src))
	out := []byte{0xff, 0xfe}
	for _, r := range encoded {
		var buf [2]byte
		binary.LittleEndian.PutUint16(buf[:], r)
		out = append(out, buf[:]...)
	}
	return out
}
