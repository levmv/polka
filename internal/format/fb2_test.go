package format

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func testFB2(cover []byte) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      <genre>sf_history</genre>
      <genre> sf_history </genre>
      <genre>adventure</genre>
      <author>
        <first-name>Arkady</first-name>
        <middle-name>Natanovich</middle-name>
        <last-name>Strugatsky</last-name>
      </author>
      <author>
        <nickname>Boris Strugatsky</nickname>
      </author>
      <book-title>Roadside Picnic</book-title>
      <annotation>
        <p>A short <strong>classic</strong> annotation.</p>
      </annotation>
      <date value="1972-01-01">1972</date>
      <coverpage>
        <image l:href="#cover.png"/>
      </coverpage>
      <lang>en</lang>
      <sequence name="Noon Universe" number="3"/>
    </title-info>
    <publish-info>
      <publisher>Mir</publisher>
      <year>1980</year>
      <isbn>978-0-306-40615-7</isbn>
      <sequence name="Publisher Classics" number="0"/>
    </publish-info>
  </description>
  <binary id="cover.png" content-type="image/png">` + base64.StdEncoding.EncodeToString(cover) + `</binary>
</FictionBook>`)
}

func testFB2CoverDocument(ref string, binaries ...string) []byte {
	coverpage := ""
	if ref != "" {
		coverpage = `<coverpage><image l:href="#` + ref + `"/></coverpage>`
	}
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      ` + coverpage + `
    </title-info>
  </description>
  ` + strings.Join(binaries, "\n  ") + `
</FictionBook>`)
}

func testFB2Zip(t *testing.T, name string, fb2 []byte) []byte {
	t.Helper()
	return testFB2ZipFiles(t, map[string][]byte{name: fb2})
}

func testFB2ZipFiles(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func testFB2Gzip(t *testing.T, fb2 []byte) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := gzip.NewWriter(buf)
	if _, err := w.Write(fb2); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func windows1251FB2(t *testing.T, src string) []byte {
	t.Helper()
	encoded, err := charmap.Windows1251.NewEncoder().String(src)
	if err != nil {
		t.Fatalf("encode windows-1251 FB2: %v", err)
	}
	return []byte(encoded)
}

func TestReadAllLimitedRejectsOversizedInput(t *testing.T) {
	_, err := readAllLimited(strings.NewReader("abcd"), "tiny input", 3)
	if err == nil || !strings.Contains(err.Error(), "tiny input exceeds 3 bytes") {
		t.Fatalf("readAllLimited error = %v; want size limit", err)
	}
}

func TestExtractFB2Metadata(t *testing.T) {
	r := bytes.NewReader(testFB2(tinyPNG))
	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata: %v", err)
	}
	if meta.Title != "Roadside Picnic" {
		t.Fatalf("Title = %q; want Roadside Picnic", meta.Title)
	}
	if meta.Language != "en" {
		t.Fatalf("Language = %q; want en", meta.Language)
	}
	if meta.Description != "A short classic annotation." {
		t.Fatalf("Description = %q; want flattened annotation", meta.Description)
	}
	if meta.Publisher != "Mir" {
		t.Fatalf("Publisher = %q; want Mir", meta.Publisher)
	}
	if meta.Date != "1972-01-01" {
		t.Fatalf("Date = %q; want 1972-01-01", meta.Date)
	}
	if meta.Identifier != "isbn:978-0-306-40615-7" {
		t.Fatalf("Identifier = %q; want isbn", meta.Identifier)
	}
	if meta.Series != "Noon Universe" || meta.SeriesIndex != 3 {
		t.Fatalf("Series = %q %v; want Noon Universe 3", meta.Series, meta.SeriesIndex)
	}
	if len(meta.Authors) != 2 {
		t.Fatalf("Authors = %d; want 2", len(meta.Authors))
	}
	if meta.Authors[0].Name != "Arkady Natanovich Strugatsky" {
		t.Fatalf("author[0].Name = %q", meta.Authors[0].Name)
	}
	if meta.Authors[0].SortName != "Strugatsky, Arkady Natanovich" {
		t.Fatalf("author[0].SortName = %q", meta.Authors[0].SortName)
	}
	if meta.Authors[1].Name != "Boris Strugatsky" {
		t.Fatalf("author[1].Name = %q", meta.Authors[1].Name)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != "sf_history" || meta.Tags[1] != "adventure" {
		t.Fatalf("Tags = %+v; want deduped FB2 genres", meta.Tags)
	}
}

func TestExtractFB2MetadataAndCoverUsesNormalizationFallback(t *testing.T) {
	data := bytes.Replace(testFB2(tinyPNG), []byte("Roadside Picnic"), []byte("Roadside & Picnic"), 1)
	r := bytes.NewReader(data)
	meta, cover, ext, err := ExtractFB2MetadataAndCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2MetadataAndCover: %v", err)
	}
	if meta.Title != "Roadside & Picnic" {
		t.Fatalf("Title = %q; want repaired ampersand", meta.Title)
	}
	if ext != ".png" || !bytes.Equal(cover, tinyPNG) {
		t.Fatalf("cover = %d bytes %q; want tiny PNG", len(cover), ext)
	}
}

func TestExtractFB2MetadataNormalizesLanguage(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>Russian FB2</book-title>
      <lang>rus</lang>
    </title-info>
  </description>
  <body><section><p>Text.</p></section></body>
</FictionBook>`)
	r := bytes.NewReader(data)
	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata: %v", err)
	}
	if meta.Language != "ru" {
		t.Fatalf("Language = %q; want normalized ru", meta.Language)
	}
}

func TestExtractFB2MetadataFallbackSections(t *testing.T) {
	const fb2 = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
    </title-info>
    <src-title-info>
      <genre>sf</genre>
      <genre>adventure</genre>
      <book-title>Source Title</book-title>
      <annotation><p>Source annotation.</p></annotation>
      <date>1968</date>
    </src-title-info>
    <document-info>
      <author>
        <nickname>Document Author</nickname>
      </author>
    </document-info>
  </description>
</FictionBook>`

	r := bytes.NewReader([]byte(fb2))
	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata: %v", err)
	}
	if meta.Title != "Source Title" {
		t.Fatalf("Title = %q; want source title fallback", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Document Author" {
		t.Fatalf("Authors = %+v; want document-info author fallback", meta.Authors)
	}
	if meta.Description != "Source annotation." {
		t.Fatalf("Description = %q; want source annotation fallback", meta.Description)
	}
	if meta.Date != "1968" {
		t.Fatalf("Date = %q; want source date fallback", meta.Date)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != "sf" || meta.Tags[1] != "adventure" {
		t.Fatalf("Tags = %+v; want source genres", meta.Tags)
	}
}

func TestExtractFB2TitleFallsBackToPublishInfoBeforeSource(t *testing.T) {
	const fb2 = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info></title-info>
    <publish-info>
      <book-title>Published Title</book-title>
    </publish-info>
    <src-title-info>
      <book-title>Source Title</book-title>
    </src-title-info>
  </description>
</FictionBook>`

	r := bytes.NewReader([]byte(fb2))
	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata: %v", err)
	}
	if meta.Title != "Published Title" {
		t.Fatalf("Title = %q; want publish-info title fallback before source title", meta.Title)
	}
}

func TestExtractFB2MetadataNamespaceVariants(t *testing.T) {
	tests := []struct {
		name string
		fb2  string
		want string
	}{
		{
			name: "fictionbook 2.1 namespace",
			fb2: `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.1">
  <description><title-info><book-title>FB2 2.1</book-title></title-info></description>
</FictionBook>`,
			want: "FB2 2.1",
		},
		{
			name: "empty description namespace",
			fb2: `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description xmlns=""><title-info><book-title>Empty Description Namespace</book-title></title-info></description>
</FictionBook>`,
			want: "Empty Description Namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader([]byte(tt.fb2))
			meta, err := ExtractFB2Metadata(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractFB2Metadata: %v", err)
			}
			if meta.Title != tt.want {
				t.Fatalf("Title = %q; want %q", meta.Title, tt.want)
			}
		})
	}
}

func TestExtractFB2MetadataKeywordsAsTags(t *testing.T) {
	const fb2 = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <genre>sf</genre>
      <keywords>classic; sf, roadside picnic</keywords>
      <book-title>Keyword Test</book-title>
    </title-info>
  </description>
</FictionBook>`

	r := bytes.NewReader([]byte(fb2))
	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata: %v", err)
	}
	want := []string{"sf", "classic", "roadside picnic"}
	if len(meta.Tags) != len(want) {
		t.Fatalf("Tags = %+v; want %+v", meta.Tags, want)
	}
	for i := range want {
		if meta.Tags[i] != want[i] {
			t.Fatalf("Tags = %+v; want %+v", meta.Tags, want)
		}
	}
}

func TestExtractFB2MetadataToleratesKnownXMLDamage(t *testing.T) {
	for _, tt := range []struct {
		name      string
		fb2       []byte
		wantTitle string
	}{
		{
			name: "NUL byte",
			fb2: []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" +
				"<FictionBook xmlns=\"http://www.gribuser.ru/xml/fictionbook/2.0\">\x00\n" +
				"  <description><title-info><book-title>NUL Byte FB2</book-title></title-info></description>\n" +
				"</FictionBook>"),
			wantTitle: "NUL Byte FB2",
		},
		{
			name: "other forbidden XML control",
			fb2: []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" +
				"<FictionBook xmlns=\"http://www.gribuser.ru/xml/fictionbook/2.0\">\n" +
				"  <description><title-info><book-title>Control\x0b Character</book-title></title-info></description>\n" +
				"</FictionBook>"),
			wantTitle: "Control Character",
		},
		{
			name: "unescaped ampersand before space",
			fb2: []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <book-title>A & B FB2</book-title>
    </title-info>
  </description>
</FictionBook>`),
			wantTitle: "A & B FB2",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.fb2)
			meta, err := ExtractFB2Metadata(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractFB2Metadata: %v", err)
			}
			if meta.Title != tt.wantTitle {
				t.Fatalf("Title = %q; want %q", meta.Title, tt.wantTitle)
			}
		})
	}
}

func TestExtractFB2MetadataFallsBackFromWrongUTF8Declaration(t *testing.T) {
	title := "\u041a\u043d\u0438\u0433\u0430"
	author := "\u0410\u0432\u0442\u043e\u0440"
	fb2 := windows1251FB2(t, `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <author><nickname>`+author+`</nickname></author>
      <book-title>`+title+`</book-title>
      <lang>ru</lang>
    </title-info>
  </description>
</FictionBook>`)

	r := bytes.NewReader(fb2)
	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata: %v", err)
	}
	if meta.Title != title {
		t.Fatalf("Title = %q; want %q", meta.Title, title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != author {
		t.Fatalf("Authors = %+v; want %q", meta.Authors, author)
	}
	diagnostics := InspectDiagnostics(r, r.Size(), FormatFB2, "book.fb2")
	if len(diagnostics) != 1 || diagnostics[0].Code != "fb2.legacy_encoding_fallback" {
		t.Fatalf("Diagnostics = %+v; want legacy encoding fallback", diagnostics)
	}
}

func TestExtractFB2MetadataMultipleISBNs(t *testing.T) {
	const fb2 = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <book-title>ISBN Test</book-title>
    </title-info>
    <publish-info>
      <isbn>ISBN 978-0-306-40615-7, 0-8044-2957-X; not an isbn; 9780306406157</isbn>
    </publish-info>
  </description>
</FictionBook>`

	r := bytes.NewReader([]byte(fb2))
	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata: %v", err)
	}
	want := "isbn:978-0-306-40615-7, isbn:0-8044-2957-X"
	if meta.Identifier != want {
		t.Fatalf("Identifier = %q; want %q", meta.Identifier, want)
	}
}

func TestExtractFB2CoverSelection(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(tinyPNG)
	unpadded := strings.TrimRight(encoded, "=")
	noisy := encoded[:12] + "!!" + encoded[12:24] + "\n??" + encoded[24:]
	validBinary := `<binary id="cover.png" content-type="image/png">` + encoded + `</binary>`

	for _, tt := range []struct {
		name      string
		fb2       []byte
		wantCover bool
	}{
		{
			name:      "referenced image",
			fb2:       testFB2CoverDocument("cover.png", validBinary),
			wantCover: true,
		},
		{
			name:      "unpadded base64 and content-type parameters",
			fb2:       testFB2CoverDocument("cover.png", `<binary id="cover.png" content-type="image/png; charset=binary">`+unpadded+`</binary>`),
			wantCover: true,
		},
		{
			name:      "invalid base64 characters",
			fb2:       testFB2CoverDocument("cover.png", `<binary id="cover.png" content-type="image/png">`+noisy+`</binary>`),
			wantCover: true,
		},
		{
			name:      "missing referenced binary",
			fb2:       testFB2CoverDocument("missing.png", `<binary id="fallback.png" content-type="image/png">`+encoded+`</binary>`),
			wantCover: true,
		},
		{
			name: "invalid fallback before valid image",
			fb2: testFB2CoverDocument(
				"",
				`<binary id="broken.png" content-type="image/png">not-valid-base64</binary>`,
				`<binary id="fallback.png" content-type="image/png">`+encoded+`</binary>`,
			),
			wantCover: true,
		},
		{
			name: "invalid referenced image",
			fb2: testFB2CoverDocument(
				"broken.png",
				`<binary id="broken.png" content-type="image/png">`+base64.StdEncoding.EncodeToString([]byte("not an image"))+`</binary>`,
				`<binary id="fallback.png" content-type="image/png">`+encoded+`</binary>`,
			),
			wantCover: true,
		},
		{
			name: "invalid only image",
			fb2:  testFB2CoverDocument("broken.png", `<binary id="broken.png" content-type="image/png">not-valid-base64</binary>`),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.fb2)
			got, ext, err := ExtractFB2Cover(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractFB2Cover: %v", err)
			}
			if tt.wantCover {
				if ext != ".png" || !bytes.Equal(got, tinyPNG) {
					t.Fatalf("cover = %d bytes %q; want tiny PNG", len(got), ext)
				}
			} else if got != nil || ext != "" {
				t.Fatalf("cover = %d bytes %q; want no cover", len(got), ext)
			}
		})
	}
}

func TestExtractFB2FromGzip(t *testing.T) {
	gzipped := testFB2Gzip(t, testFB2(tinyPNG))
	r := bytes.NewReader(gzipped)

	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata gzip: %v", err)
	}
	if meta.Title != "Roadside Picnic" {
		t.Fatalf("Title = %q; want Roadside Picnic", meta.Title)
	}

	r = bytes.NewReader(gzipped)
	got, ext, err := ExtractFB2Cover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Cover gzip: %v", err)
	}
	if ext != ".png" {
		t.Fatalf("cover ext = %q; want .png", ext)
	}
	if !bytes.Equal(got, tinyPNG) {
		t.Fatalf("gzip cover bytes mismatch")
	}

	r = bytes.NewReader(gzipped)
	if format := DetectFormat("book.fb2.gz", r, r.Size()); format != FormatFB2 {
		t.Fatalf("DetectFormat fb2.gz = %v; want FormatFB2", format)
	}
	if ext := BookExtension("Book.FB2.GZ"); ext != ".FB2.GZ" {
		t.Fatalf("BookExtension = %q; want .FB2.GZ", ext)
	}
	if format := FormatFromExt(".fb2.gz"); format != FormatFB2 {
		t.Fatalf("FormatFromExt fb2.gz = %v; want FormatFB2", format)
	}

	r = bytes.NewReader(gzipped)
	source, err := OpenFB2Source(r, r.Size(), "Book.FB2.GZ")
	if err != nil {
		t.Fatalf("OpenFB2Source gzip: %v", err)
	}
	defer source.Reader.Close()
	if source.Filename != "Book.fb2" {
		t.Fatalf("gzip source filename = %q; want Book.fb2", source.Filename)
	}
	if source.ContentLength != -1 {
		t.Fatalf("gzip source length = %d; want -1", source.ContentLength)
	}
	if source.Container != FB2ContainerGzip {
		t.Fatalf("gzip source container = %q; want gzip", source.Container)
	}
	if body, err := io.ReadAll(source.Reader); err != nil || !bytes.Contains(body, []byte("Roadside Picnic")) {
		t.Fatalf("gzip source body = %q err=%v; want FB2 bytes", body, err)
	}
}

func TestExtractFB2FromZip(t *testing.T) {
	fb2 := testFB2(tinyPNG)
	zipped := testFB2Zip(t, "nested/book.fb2", fb2)
	r := bytes.NewReader(zipped)

	meta, err := ExtractFB2Metadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Metadata zip: %v", err)
	}
	if meta.Title != "Roadside Picnic" {
		t.Fatalf("Title = %q; want Roadside Picnic", meta.Title)
	}

	r = bytes.NewReader(zipped)
	got, ext, err := ExtractFB2Cover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractFB2Cover zip: %v", err)
	}
	if ext != ".png" {
		t.Fatalf("cover ext = %q; want .png", ext)
	}
	if !bytes.Equal(got, tinyPNG) {
		t.Fatalf("zip cover bytes mismatch")
	}

	r = bytes.NewReader(zipped)
	source, err := OpenFB2Source(r, r.Size(), "book.fbz")
	if err != nil {
		t.Fatalf("OpenFB2Source zip: %v", err)
	}
	defer source.Reader.Close()
	if source.Filename != "book.fb2" {
		t.Fatalf("zip source filename = %q; want book.fb2", source.Filename)
	}
	if source.ContentLength != int64(len(fb2)) {
		t.Fatalf("zip source length = %d; want %d", source.ContentLength, len(fb2))
	}
	if source.Container != FB2ContainerZip {
		t.Fatalf("zip source container = %q; want zip", source.Container)
	}
	if body, err := io.ReadAll(source.Reader); err != nil || !bytes.Equal(body, fb2) {
		t.Fatalf("zip source body len=%d err=%v; want inner FB2 bytes", len(body), err)
	}
}

func TestOpenFB2SourcePlain(t *testing.T) {
	fb2 := testFB2(tinyPNG)
	r := bytes.NewReader(fb2)
	source, err := OpenFB2Source(r, r.Size(), "plain.fb2")
	if err != nil {
		t.Fatalf("OpenFB2Source plain: %v", err)
	}
	defer source.Reader.Close()
	if source.Filename != "plain.fb2" {
		t.Fatalf("plain source filename = %q; want plain.fb2", source.Filename)
	}
	if source.ContentLength != int64(len(fb2)) {
		t.Fatalf("plain source length = %d; want %d", source.ContentLength, len(fb2))
	}
	if source.Container != FB2ContainerNone {
		t.Fatalf("plain source container = %q; want none", source.Container)
	}
	if body, err := io.ReadAll(source.Reader); err != nil || !bytes.Equal(body, fb2) {
		t.Fatalf("plain source body len=%d err=%v; want FB2 bytes", len(body), err)
	}
}

func TestFB2ZipRequiresSingleEntry(t *testing.T) {
	zipped := testFB2ZipFiles(t, map[string][]byte{
		"one.fb2": testFB2(tinyPNG),
		"two.fb2": testFB2(tinyPNG),
	})
	r := bytes.NewReader(zipped)

	if format := DetectFormat("book.fb2.zip", r, r.Size()); format != FormatUnknown {
		t.Fatalf("DetectFormat multi-entry fb2 zip = %v; want FormatUnknown", format)
	}

	r = bytes.NewReader(zipped)
	if _, err := ExtractFB2Metadata(r, r.Size()); err == nil || !strings.Contains(err.Error(), "multiple .fb2 files") {
		t.Fatalf("ExtractFB2Metadata multi-entry error = %v; want multiple .fb2 files", err)
	}
}
