package format

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"image/color"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"

	"github.com/levmv/polka/internal/bookmeta"
)

func TestRewriteEPUBMetadataEPUB2PreservesForeignMetadata(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:identifier id="bookid" opf:scheme="uuid">urn:uuid:0732514b-1234-5678-90ab-cdef12345678</dc:identifier>
    <dc:title id="old-title">Old Title</dc:title>
    <dc:creator id="old-author" opf:file-as="Author, Old" opf:role="aut">Old Author</dc:creator>
    <dc:language>de</dc:language>
    <dc:identifier opf:scheme="ISBN">old-isbn</dc:identifier>
    <dc:identifier opf:scheme="DOI">10.old/doi</dc:identifier>
    <dc:identifier opf:scheme="google">foreign-id</dc:identifier>
    <dc:rights>Keep Rights</dc:rights>
    <meta name="cover" content="cover-image"/>
    <meta name="calibre:series" content="Old Series"/>
    <meta name="calibre:series_index" content="8"/>
    <meta refines="#cover-image" property="display-seq">9</meta>
    <meta refines="#old-author" property="file-as">Dangling Sort</meta>
  </metadata>
  <manifest>
    <item id="cover-image" href="cover.jpg" media-type="image/jpeg"/>
    <item id="chap" href="text.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="chap"/></spine>
</package>`)},
		{name: "OEBPS/text.xhtml", data: []byte("<html><body>same bytes</body></html>")},
	})

	out, err := RewriteEPUBMetadata(src, Metadata{
		Title:       "New & Better",
		SortTitle:   "Better, New",
		Authors:     []bookmeta.AuthorMeta{{Name: "Jane Writer", SortName: "Writer, Jane"}},
		Language:    "en",
		Publisher:   "Polka Press",
		Date:        "2026-07-06",
		Description: "Updated description",
		Identifier:  "isbn:978-0-306-40615-7, doi:10.1000/182",
		Series:      "New Series",
		SeriesIndex: 2.5,
		Tags:        []string{"Fiction", "Archive"},
	}, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}

	opf := testZipEntryString(t, out, "OEBPS/content.opf")
	for _, want := range []string{
		`<dc:title id="polka-title">New &amp; Better</dc:title>`,
		`<meta name="calibre:title_sort" content="Better, New"/>`,
		`<dc:creator id="polka-creator-1" opf:file-as="Writer, Jane" opf:role="aut">Jane Writer</dc:creator>`,
		`<dc:identifier id="polka-identifier-1" opf:scheme="isbn">978-0-306-40615-7</dc:identifier>`,
		`<dc:identifier id="polka-identifier-2" opf:scheme="doi">10.1000/182</dc:identifier>`,
		`<dc:identifier id="bookid" opf:scheme="uuid">urn:uuid:0732514b-1234-5678-90ab-cdef12345678</dc:identifier>`,
		`<dc:identifier opf:scheme="google">foreign-id</dc:identifier>`,
		`<dc:rights>Keep Rights</dc:rights>`,
		`<meta name="cover" content="cover-image"/>`,
		`<meta refines="#cover-image" property="display-seq">9</meta>`,
		`<meta name="calibre:series" content="New Series"/>`,
		`<meta name="calibre:series_index" content="2.5"/>`,
		`<manifest>`,
		`<spine><itemref idref="chap"/></spine>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("rewritten OPF missing %q:\n%s", want, opf)
		}
	}
	for _, old := range []string{"Old Title", "Old Author", "old-isbn", "10.old/doi", "Old Series", "Dangling Sort"} {
		if strings.Contains(opf, old) {
			t.Fatalf("rewritten OPF still contains %q:\n%s", old, opf)
		}
	}
	if strings.Contains(opf, `property="belongs-to-collection"`) || strings.Contains(opf, `refines="#polka-series"`) {
		t.Fatalf("rewritten EPUB2 OPF got EPUB3-only series metadata:\n%s", opf)
	}

	zr := testZipReader(t, out)
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Fatalf("first entry = %s method %d; want stored mimetype", zr.File[0].Name, zr.File[0].Method)
	}
	if len(zr.File[0].Extra) != 0 || zr.File[0].Flags&0x8 != 0 {
		t.Fatalf("mimetype extra len/flags = %d/%x; want no extra and no data descriptor", len(zr.File[0].Extra), zr.File[0].Flags)
	}
	if got := testZipEntryString(t, out, "OEBPS/text.xhtml"); got != "<html><body>same bytes</body></html>" {
		t.Fatalf("non-OPF entry changed: %q", got)
	}

	meta, err := ExtractEPUBMetadata(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata after rewrite: %v", err)
	}
	if meta.Title != "New & Better" || meta.SortTitle != "Better, New" {
		t.Fatalf("title fields = %q / %q", meta.Title, meta.SortTitle)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Jane Writer" || meta.Authors[0].SortName != "Writer, Jane" {
		t.Fatalf("authors = %+v", meta.Authors)
	}
	if meta.Identifier != "isbn:978-0-306-40615-7, doi:10.1000/182, google:foreign-id" {
		t.Fatalf("identifiers = %q", meta.Identifier)
	}
}

func TestRewriteEPUBMetadataNormalizesLegacyOPFEncoding(t *testing.T) {
	rawOPF := `<?xml version="1.0" encoding="iso-8859-1"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">urn:isbn:9780262367509</dc:identifier>
    <dc:title>Café Original</dc:title>
    <dc:rights>Résumé preserved</dc:rights>
  </metadata>
</package>`
	encodedOPF, err := charmap.ISO8859_1.NewEncoder().Bytes([]byte(rawOPF))
	if err != nil {
		t.Fatalf("encode legacy OPF: %v", err)
	}
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OPS/content.opf"))},
		{name: "OPS/content.opf", data: encodedOPF},
	})

	meta, err := ExtractEPUBMetadata(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatalf("extract legacy metadata: %v", err)
	}
	meta.Title = "Café Noël"
	meta.Authors = []bookmeta.AuthorMeta{{Name: "Émile Zola"}}
	out, err := RewriteEPUBMetadata(src, *meta, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}

	opf := testZipEntryString(t, out, "OPS/content.opf")
	for _, want := range []string{`encoding="UTF-8"`, "Café Noël", "Émile Zola", "Résumé preserved"} {
		if !strings.Contains(opf, want) {
			t.Fatalf("rewritten legacy OPF missing %q:\n%s", want, opf)
		}
	}
	extracted, err := ExtractEPUBMetadata(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("extract rewritten metadata: %v", err)
	}
	if extracted.Title != "Café Noël" || len(extracted.Authors) != 1 || extracted.Authors[0].Name != "Émile Zola" {
		t.Fatalf("rewritten metadata = %+v", extracted)
	}
}

func TestRewriteEPUBMetadataPreservesUnchangedIdentifierText(t *testing.T) {
	encryptionXML := `<?xml version="1.0"?>
<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#">
  <enc:EncryptedData>
    <enc:EncryptionMethod Algorithm="` + epubIDPFFontObfuscation + `"/>
  </enc:EncryptedData>
</encryption>`
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OPS/content.opf"))},
		{name: "META-INF/encryption.xml", data: []byte(encryptionXML)},
		{name: "OPS/content.opf", data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">urn:isbn:9780262367509</dc:identifier>
    <dc:identifier>url:https://standardebooks.org/ebooks/example</dc:identifier>
    <dc:identifier>code.google.com.epub-samples.example</dc:identifier>
    <dc:title>Old Title</dc:title>
  </metadata>
</package>`)},
	})

	meta, err := ExtractEPUBMetadata(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata: %v", err)
	}
	meta.Title = "New Title"
	out, err := RewriteEPUBMetadata(src, *meta, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}
	opf := testZipEntryString(t, out, "OPS/content.opf")
	for _, want := range []string{
		`<dc:identifier id="bookid">urn:isbn:9780262367509</dc:identifier>`,
		`<dc:identifier>url:https://standardebooks.org/ebooks/example</dc:identifier>`,
		`<dc:identifier>code.google.com.epub-samples.example</dc:identifier>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("rewritten OPF changed identifier %q:\n%s", want, opf)
		}
	}
	if strings.Contains(opf, "unknown:code.google.com") {
		t.Fatalf("rewritten OPF leaked internal identifier type:\n%s", opf)
	}
}

func TestRewriteEPUBMetadataToStreamsOutput(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Old</dc:title></metadata></package>`)},
	})

	var out bytes.Buffer
	err := RewriteEPUBMetadataTo(&out, bytes.NewReader(src), int64(len(src)), Metadata{Title: "Streamed Title"}, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadataTo: %v", err)
	}
	if got := testZipEntryString(t, out.Bytes(), "OEBPS/content.opf"); !strings.Contains(got, "Streamed Title") {
		t.Fatalf("streamed OPF missing new title:\n%s", got)
	}
}

func TestRewriteEPUBMetadataAndCoverMatchesExistingCoverType(t *testing.T) {
	oldCover := testProgressiveJPEG(t)
	newCover := testPNG(t, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Old Cover Title</dc:title>
  </metadata>
  <manifest>
    <item id="cover-image" href="images/cover.jpg" media-type="image/jpeg"/>
    <item id="cover-page" href="cover.xhtml" media-type="application/xhtml+xml"/>
    <item id="chap" href="text.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="chap"/></spine>
  <guide><reference type="cover" title="Cover" href="cover.xhtml"/></guide>
</package>`)},
		{name: "OEBPS/images/cover.jpg", data: oldCover},
		{name: "OEBPS/cover.xhtml", data: []byte(`<html><body><img src="images/cover.jpg"/></body></html>`)},
		{name: "OEBPS/text.xhtml", data: []byte("<html><body>same chapter</body></html>")},
	})

	var out bytes.Buffer
	err := RewriteEPUBMetadataAndCoverTo(&out, bytes.NewReader(src), int64(len(src)), Metadata{Title: "New Cover Title"}, time.Time{}, newCover)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadataAndCoverTo: %v", err)
	}

	rewrittenCover := testZipEntryBytes(t, out.Bytes(), "OEBPS/images/cover.jpg")
	if mediaType, _, ok := coverImageMediaTypeFromBytes(rewrittenCover); !ok || mediaType != "image/jpeg" {
		t.Fatalf("cover media type = %q/%v; want JPEG matching existing path", mediaType, ok)
	}
	if isProgressiveJPEG(rewrittenCover) {
		t.Fatal("rewritten JPEG cover is progressive; want baseline")
	}
	opf := testZipEntryString(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		`<dc:title id="polka-title">New Cover Title</dc:title>`,
		`<item id="cover-image" href="images/cover.jpg" media-type="image/jpeg"/>`,
		`<guide><reference type="cover" title="Cover" href="cover.xhtml"/></guide>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("rewritten OPF missing %q:\n%s", want, opf)
		}
	}
	if got := testZipEntryString(t, out.Bytes(), "OEBPS/cover.xhtml"); got != `<html><body><img src="images/cover.jpg"/></body></html>` {
		t.Fatalf("cover page changed: %q", got)
	}
	if got := testZipEntryString(t, out.Bytes(), "OEBPS/text.xhtml"); got != "<html><body>same chapter</body></html>" {
		t.Fatalf("non-cover entry changed: %q", got)
	}
	_, cover, ext, err := ExtractEPUBMetadataAndCover(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadataAndCover: %v", err)
	}
	if ext != ".jpg" || !bytes.Equal(cover, rewrittenCover) {
		t.Fatalf("extracted cover = ext %q, %d bytes; want rewritten JPEG", ext, len(cover))
	}
}

func TestRewriteEPUBMetadataAndCoverMatchesExistingPNGType(t *testing.T) {
	oldCover := testPNG(t, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	newCover := testProgressiveJPEG(t)
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>PNG Cover</dc:title>
  </metadata>
  <manifest>
    <item id="cover-image" href="images/cover.png" media-type="image/png" properties="cover-image"/>
  </manifest>
  <spine/>
</package>`)},
		{name: "OEBPS/images/cover.png", data: oldCover},
	})

	var out bytes.Buffer
	err := RewriteEPUBMetadataAndCoverTo(&out, bytes.NewReader(src), int64(len(src)), Metadata{Title: "New PNG Cover"}, time.Time{}, newCover)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadataAndCoverTo: %v", err)
	}

	rewrittenCover := testZipEntryBytes(t, out.Bytes(), "OEBPS/images/cover.png")
	if mediaType, _, ok := coverImageMediaTypeFromBytes(rewrittenCover); !ok || mediaType != "image/png" {
		t.Fatalf("cover media type = %q/%v; want PNG matching existing path", mediaType, ok)
	}
	if opf := testZipEntryString(t, out.Bytes(), "OEBPS/content.opf"); !strings.Contains(opf, `href="images/cover.png" media-type="image/png"`) {
		t.Fatalf("rewritten OPF changed PNG cover type/path:\n%s", opf)
	}
}

func TestRewriteEPUBMetadataAndCoverNormalizesProgressiveJPEG(t *testing.T) {
	progressive := testProgressiveJPEG(t)
	if !isProgressiveJPEG(progressive) {
		t.Fatal("test fixture is not progressive")
	}
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Progressive Cover</dc:title>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest><item id="cover-image" href="cover.jpg" media-type="image/jpeg"/></manifest>
  <spine/>
</package>`)},
		{name: "OEBPS/cover.jpg", data: testPNG(t, color.Black)},
	})

	var out bytes.Buffer
	err := RewriteEPUBMetadataAndCoverTo(&out, bytes.NewReader(src), int64(len(src)), Metadata{Title: "Baseline Cover"}, time.Time{}, progressive)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadataAndCoverTo: %v", err)
	}
	if got := testZipEntryBytes(t, out.Bytes(), "OEBPS/cover.jpg"); isProgressiveJPEG(got) {
		t.Fatal("rewritten cover is progressive; want baseline JPEG")
	}
}

func testProgressiveJPEG(t *testing.T) []byte {
	t.Helper()
	const encoded = `/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAMCAgMCAgMDAwMEAwMEBQgFBQQEBQoHBwYIDAoMDAsK
CwsNDhIQDQ4RDgsLEBYQERMUFRUVDA8XGBYUGBIUFRT/2wBDAQMEBAUEBQkFBQkUDQsNFBQUFBQU
FBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBT/wgARCAACAAIDASIA
AhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAf/xAAUAQEAAAAAAAAAAAAAAAAAAAAG/9oADAMB
AAIQAxAAAAG6gcA//8QAFhABAQEAAAAAAAAAAAAAAAAABAYD/9oACAEBAAEFApsBtJ7/xAAZEQAB
BQAAAAAAAAAAAAAAAAAAAQIEM3H/2gAIAQMBAT8Bk3v1T//EABkRAAEFAAAAAAAAAAAAAAAAAAAB
AgMzcf/aAAgBAgEBPwGe1+qf/8QAGxAAAgIDAQAAAAAAAAAAAAAAAQIDBAAFESL/2gAIAQEABj8C
1bNXiZmqxEkoOnyM/8QAFhABAQEAAAAAAAAAAAAAAAAAAREA/9oACAEBAAE/IUb8PI2Vm//aAAwD
AQACAAMAAAAQ9//EABcRAAMBAAAAAAAAAAAAAAAAAAABUfD/2gAIAQMBAT8Q1qz/xAAXEQADAQAA
AAAAAAAAAAAAAAAAAVHw/9oACAECAQE/ENis/8QAFxABAQEBAAAAAAAAAAAAAAAAAREAMf/aAAgB
AQABPxBLxVstClVVXt3/2Q==`
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode progressive JPEG fixture: %v", err)
	}
	return data
}

func TestRewriteEPUBMetadataAndCoverInjectsMissingCoverSlot(t *testing.T) {
	cover := testPNG(t, color.NRGBA{R: 30, G: 180, B: 90, A: 255})
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>No Cover Slot</dc:title>
  </metadata>
  <manifest>
    <item id="chap" href="text.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="chap"/></spine>
</package>`)},
		{name: "OEBPS/text.xhtml", data: []byte("<html><body>same chapter</body></html>")},
	})

	meta := Metadata{Title: "Injected Cover"}
	var out bytes.Buffer
	err := RewriteEPUBMetadataAndCoverTo(&out, bytes.NewReader(src), int64(len(src)), meta, time.Time{}, cover)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadataAndCoverTo: %v", err)
	}

	if got := testZipEntryBytes(t, out.Bytes(), "OEBPS/images/polka-cover.png"); !bytes.Equal(got, cover) {
		t.Fatalf("injected cover bytes = %d bytes; want stored cover", len(got))
	}
	opf := testZipEntryString(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		`<meta name="cover" content="cover-image"/>`,
		`<item id="cover-image" href="images/polka-cover.png" media-type="image/png" properties="cover-image"/>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("rewritten OPF missing %q:\n%s", want, opf)
		}
	}
	_, extractedCover, ext, err := ExtractEPUBMetadataAndCover(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadataAndCover: %v", err)
	}
	if ext != ".png" || !bytes.Equal(extractedCover, cover) {
		t.Fatalf("extracted cover = ext %q, %d bytes; want injected PNG", ext, len(extractedCover))
	}

	var repeated bytes.Buffer
	err = RewriteEPUBMetadataAndCoverTo(&repeated, bytes.NewReader(out.Bytes()), int64(out.Len()), meta, time.Time{}, cover)
	if err != nil {
		t.Fatalf("repeat RewriteEPUBMetadataAndCoverTo: %v", err)
	}
	if !bytes.Equal(repeated.Bytes(), out.Bytes()) {
		t.Fatal("repeated missing-cover rewrite changed EPUB bytes")
	}
}

func TestRewriteEPUBMetadataAndCoverAvoidsNormalizedCoverNameCollision(t *testing.T) {
	cover := testPNG(t, color.NRGBA{R: 90, G: 40, B: 180, A: 255})
	foreign := []byte("preserve differently cased source entry")
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>No Cover</dc:title></metadata>
  <manifest><item id="chap" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chap"/></spine>
</package>`)},
		{name: "OEBPS/text.xhtml", data: []byte("<html><body>chapter</body></html>")},
		{name: "OEBPS/images/POLKA-COVER.PNG", data: foreign},
	})

	var out bytes.Buffer
	err := RewriteEPUBMetadataAndCoverTo(&out, bytes.NewReader(src), int64(len(src)), Metadata{Title: "Safe Cover"}, time.Time{}, cover)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadataAndCoverTo: %v", err)
	}

	if got := testZipEntryBytes(t, out.Bytes(), "OEBPS/images/POLKA-COVER.PNG"); !bytes.Equal(got, foreign) {
		t.Fatalf("existing case-colliding entry changed: %q", got)
	}
	if got := testZipEntryBytes(t, out.Bytes(), "OEBPS/polka-cover.png"); !bytes.Equal(got, cover) {
		t.Fatalf("injected non-colliding cover bytes = %d; want %d", len(got), len(cover))
	}
	if opf := testZipEntryString(t, out.Bytes(), "OEBPS/content.opf"); !strings.Contains(opf, `href="polka-cover.png"`) {
		t.Fatalf("rewritten OPF did not use non-colliding cover path:\n%s", opf)
	}
}

func TestRewriteEPUBMetadataTreatsCommentsAndCDATAAsAtomic(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <!-- editor's note before preserved metadata -->
    <dc:title>Old Title</dc:title>
    <dc:creator opf:role="aut">Old Author</dc:creator>
    <dc:rights><![CDATA[Keep <literal> and </fake-tag> as text]]></dc:rights>
  </metadata>
</package>`)},
	})

	out, err := RewriteEPUBMetadata(src, Metadata{
		Title:   "New Title",
		Authors: []bookmeta.AuthorMeta{{Name: "New Author"}},
	}, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}

	opf := testZipEntryString(t, out, "OEBPS/content.opf")
	if !strings.Contains(opf, `<dc:rights><![CDATA[Keep <literal> and </fake-tag> as text]]></dc:rights>`) {
		t.Fatalf("rewritten OPF did not preserve CDATA child:\n%s", opf)
	}
	meta, err := ExtractEPUBMetadata(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata after rewrite: %v", err)
	}
	if meta.Title != "New Title" || len(meta.Authors) != 1 || meta.Authors[0].Name != "New Author" {
		t.Fatalf("rewritten metadata = %+v", meta)
	}
}

func TestRewriteEPUBMetadataEPUB3RefinementsAndDeterminism(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("EPUB/package.opf"))},
		{name: "EPUB/package.opf", data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" unique-identifier="pub-id" xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="pub-id">urn:uuid:0732514b-1234-5678-90ab-cdef12345678</dc:identifier>
    <dc:title id="old-main">Old Main</dc:title>
    <dc:creator id="old-creator">Old Creator</dc:creator>
    <meta refines="#old-main" property="title-type">main</meta>
    <meta refines="#old-creator" property="role">aut</meta>
    <meta property="belongs-to-collection" id="old-series">Old Collection</meta>
    <meta refines="#old-series" property="collection-type">series</meta>
    <meta property="dcterms:modified">2000-01-01T00:00:00Z</meta>
  </metadata>
  <manifest><item id="chap" href="chap.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chap"/></spine>
</package>`)},
		{name: "EPUB/chap.xhtml", data: []byte("<html/>")},
	})
	meta := Metadata{
		Title:       "Modern Title",
		SortTitle:   "Title, Modern",
		Authors:     []bookmeta.AuthorMeta{{Name: "First Writer", SortName: "Writer, First"}, {Name: "Second Writer"}},
		Language:    "en-US",
		Identifier:  "url:https://example.org/books/modern, google:modern-id",
		Series:      "Modern Series",
		SeriesIndex: 4,
	}
	modified := time.Date(2026, 7, 6, 12, 34, 56, 0, time.FixedZone("MSK", 3*60*60))

	out, err := RewriteEPUBMetadata(src, meta, modified)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}
	outAgain, err := RewriteEPUBMetadata(out, meta, modified)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata second pass: %v", err)
	}
	if !bytes.Equal(out, outAgain) {
		t.Fatal("second rewrite of the same snapshot changed the EPUB bytes")
	}

	opf := testZipEntryString(t, out, "EPUB/package.opf")
	for _, want := range []string{
		`<dc:title id="polka-title">Modern Title</dc:title>`,
		`<meta refines="#polka-title" property="title-type">main</meta>`,
		`<meta refines="#polka-title" property="file-as">Title, Modern</meta>`,
		`<dc:creator id="polka-creator-1">First Writer</dc:creator>`,
		`<meta refines="#polka-creator-1" property="file-as">Writer, First</meta>`,
		`<meta refines="#polka-creator-1" property="role" scheme="marc:relators">aut</meta>`,
		`<meta refines="#polka-creator-2" property="display-seq">2</meta>`,
		`<dc:identifier id="polka-identifier-1">https://example.org/books/modern</dc:identifier>`,
		`<dc:identifier id="polka-identifier-2">google:modern-id</dc:identifier>`,
		`<meta property="belongs-to-collection" id="polka-series">Modern Series</meta>`,
		`<meta refines="#polka-series" property="group-position">4</meta>`,
		`<meta property="dcterms:modified">2026-07-06T09:34:56Z</meta>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("rewritten EPUB3 OPF missing %q:\n%s", want, opf)
		}
	}
	for _, old := range []string{"Old Main", "Old Creator", "Old Collection", "2000-01-01T00:00:00Z"} {
		if strings.Contains(opf, old) {
			t.Fatalf("rewritten EPUB3 OPF still contains %q:\n%s", old, opf)
		}
	}
	if strings.Contains(opf, "opf:file-as") || strings.Contains(opf, "opf:role") {
		t.Fatalf("EPUB3 OPF got EPUB2-only opf-prefixed attributes:\n%s", opf)
	}
	if strings.Contains(opf, "opf:scheme") || strings.Contains(opf, `xmlns:opf="http://www.idpf.org/2007/opf"`) {
		t.Fatalf("EPUB3 OPF got invalid OPF2 identifier namespace/attrs:\n%s", opf)
	}
	extracted, err := ExtractEPUBMetadata(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata after EPUB3 rewrite: %v", err)
	}
	if extracted.Identifier != "url:https://example.org/books/modern, google:modern-id" {
		t.Fatalf("identifiers = %q", extracted.Identifier)
	}
}

func TestRewriteEPUBMetadataPrefixedMetadataElement(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OPS/prefixed.opf"))},
		{name: "OPS/prefixed.opf", data: []byte(`<?xml version="1.0"?>
<opf:package xmlns:opf="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="uid">
  <opf:metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="uid" opf:scheme="uuid">urn:uuid:keep-me</dc:identifier>
    <dc:title>Old Prefixed</dc:title>
    <dc:identifier opf:scheme="google">foreign-prefixed</dc:identifier>
    <meta name="cover" content="cover-image"/>
  </opf:metadata>
  <opf:manifest><opf:item id="chap" href="chap.xhtml" media-type="application/xhtml+xml"/></opf:manifest>
  <opf:spine><opf:itemref idref="chap"/></opf:spine>
</opf:package>`)},
		{name: "OPS/chap.xhtml", data: []byte("<html/>")},
	})

	out, err := RewriteEPUBMetadata(src, Metadata{
		Title:      "New Prefixed",
		Authors:    []bookmeta.AuthorMeta{{Name: "Namespaced Writer"}},
		Identifier: "isbn:9780306406157",
	}, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}

	opf := testZipEntryString(t, out, "OPS/prefixed.opf")
	for _, want := range []string{
		`<opf:metadata xmlns:dc="http://purl.org/dc/elements/1.1/">`,
		`<dc:title id="polka-title">New Prefixed</dc:title>`,
		`<dc:creator id="polka-creator-1" opf:role="aut">Namespaced Writer</dc:creator>`,
		`<dc:identifier id="polka-identifier-1" opf:scheme="isbn">9780306406157</dc:identifier>`,
		`<dc:identifier id="uid" opf:scheme="uuid">urn:uuid:keep-me</dc:identifier>`,
		`<dc:identifier opf:scheme="google">foreign-prefixed</dc:identifier>`,
		`<meta name="cover" content="cover-image"/>`,
		`</opf:metadata>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("rewritten prefixed OPF missing %q:\n%s", want, opf)
		}
	}
	if strings.Contains(opf, "Old Prefixed") {
		t.Fatalf("rewritten prefixed OPF still contains old title:\n%s", opf)
	}

	extracted, err := ExtractEPUBMetadata(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata after prefixed rewrite: %v", err)
	}
	if extracted.Title != "New Prefixed" || extracted.Identifier != "isbn:9780306406157, google:foreign-prefixed" {
		t.Fatalf("extracted prefixed metadata = %+v", extracted)
	}
}

func TestRewriteEPUBMetadataUpdatesExternalPackageIdentifier(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OPS/content.opf"))},
		{name: "OPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="book-isbn">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:identifier id="book-isbn" opf:scheme="isbn">978-1-4521-1040-0</dc:identifier>
    <dc:title>Old ISBN Package ID</dc:title>
  </metadata>
  <manifest/>
  <spine/>
</package>`)},
	})

	out, err := RewriteEPUBMetadata(src, Metadata{
		Title:      "New ISBN Package ID",
		Identifier: "isbn:9780306406157, google:new-id",
	}, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}

	opf := testZipEntryString(t, out, "OPS/content.opf")
	for _, want := range []string{
		`unique-identifier="book-isbn"`,
		`<dc:identifier id="book-isbn" opf:scheme="isbn">9780306406157</dc:identifier>`,
		`<dc:identifier id="polka-identifier-2" opf:scheme="google">new-id</dc:identifier>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("rewritten OPF missing %q:\n%s", want, opf)
		}
	}
	if strings.Contains(opf, "978-1-4521-1040-0") {
		t.Fatalf("rewritten OPF preserved stale package ISBN:\n%s", opf)
	}
}

func TestRewriteEPUBMetadataPreservesStandardObfuscatedFonts(t *testing.T) {
	for name, algorithm := range map[string]string{
		"IDPF":  epubIDPFFontObfuscation,
		"Adobe": epubAdobeFontObfuscation,
	} {
		t.Run(name, func(t *testing.T) {
			encryptionXML := `<?xml version="1.0"?>
<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#">
  <enc:EncryptedData>
    <enc:EncryptionMethod Algorithm="` + algorithm + `"/>
    <enc:CipherData><enc:CipherReference URI="OPS/fonts/book.otf"/></enc:CipherData>
  </enc:EncryptedData>
</encryption>`
			src := testWritebackEPUB(t, []testWritebackEntry{
				{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
				{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OPS/content.opf"))},
				{name: "META-INF/encryption.xml", data: []byte(encryptionXML)},
				{name: "OPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">urn:uuid:0732514b-1234-5678-90ab-cdef12345678</dc:identifier>
    <dc:title>Old Title</dc:title>
  </metadata>
  <manifest><item id="font" href="fonts/book.otf" media-type="font/otf"/></manifest>
</package>`)},
				{name: "OPS/fonts/book.otf", data: []byte("obfuscated-font-prefix-and-body")},
			})

			out, err := RewriteEPUBMetadata(src, Metadata{Title: "New Title"}, time.Time{})
			if err != nil {
				t.Fatalf("RewriteEPUBMetadata: %v", err)
			}
			if got := testZipEntryString(t, out, "OPS/content.opf"); !strings.Contains(got, "New Title") || !strings.Contains(got, "urn:uuid:0732514b-1234-5678-90ab-cdef12345678") {
				t.Fatalf("rewritten OPF lost title or package identifier:\n%s", got)
			}
			for _, entry := range []string{"META-INF/encryption.xml", "OPS/fonts/book.otf"} {
				if !bytes.Equal(testZipEntryRawBytes(t, src, entry), testZipEntryRawBytes(t, out, entry)) {
					t.Fatalf("raw compressed bytes for %s changed", entry)
				}
			}
		})
	}
}

func TestRewriteEPUBMetadataRefusesObfuscatedFontKeyChange(t *testing.T) {
	encryptionXML := `<?xml version="1.0"?>
<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#">
  <enc:EncryptedData>
    <enc:EncryptionMethod Algorithm="` + epubIDPFFontObfuscation + `"/>
    <enc:CipherData><enc:CipherReference URI="OPS/fonts/book.otf"/></enc:CipherData>
  </enc:EncryptedData>
</encryption>`
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OPS/content.opf"))},
		{name: "META-INF/encryption.xml", data: []byte(encryptionXML)},
		{name: "OPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="book-isbn">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:identifier id="book-isbn" opf:scheme="isbn">978-1-4521-1040-0</dc:identifier>
    <dc:title>Old Title</dc:title>
  </metadata>
</package>`)},
		{name: "OPS/fonts/book.otf", data: []byte("obfuscated-font")},
	})

	_, err := RewriteEPUBMetadata(src, Metadata{Title: "New Title", Identifier: "isbn:9780306406157"}, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "key-bearing package unique identifier") {
		t.Fatalf("RewriteEPUBMetadata error = %v; want obfuscated-font key refusal", err)
	}
}

func TestRewriteEPUBMetadataRefusesUnsafePackageSecurity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []testWritebackEntry
		want    string
	}{
		{
			name: "signed package",
			entries: []testWritebackEntry{
				{name: "META-INF/signatures.xml", data: []byte(`<signatures/>`)},
			},
			want: "package signatures would become stale",
		},
		{
			name: "unknown encryption",
			entries: []testWritebackEntry{
				{name: "META-INF/encryption.xml", data: []byte(`<encryption xmlns:enc="http://www.w3.org/2001/04/xmlenc#"><enc:EncryptedData><enc:EncryptionMethod Algorithm="http://www.w3.org/2001/04/xmlenc#aes256-cbc"/></enc:EncryptedData></encryption>`)},
			},
			want: "unsupported encryption algorithm",
		},
		{
			name: "malformed encryption",
			entries: []testWritebackEntry{
				{name: "META-INF/encryption.xml", data: []byte(`<encryption><EncryptedData>`)},
			},
			want: "unsafe EPUB encryption.xml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := []testWritebackEntry{
				{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
				{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OPS/content.opf"))},
				{name: "OPS/content.opf", data: []byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Old</dc:title></metadata></package>`)},
			}
			entries = append(entries, tc.entries...)
			src := testWritebackEPUB(t, entries)

			_, err := RewriteEPUBMetadata(src, Metadata{Title: "New"}, time.Time{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RewriteEPUBMetadata error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestRewriteEPUBMetadataPreservesDirectoryEntries(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/", method: zip.Store},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/", method: zip.Store},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Directory Entries</dc:title>
  </metadata>
  <manifest/>
  <spine/>
</package>`)},
	})

	out, err := RewriteEPUBMetadata(src, Metadata{Title: "Rewritten Directory Entries"}, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}

	zr := testZipReader(t, out)
	for _, name := range []string{"META-INF/", "OEBPS/"} {
		f := testZipEntry(t, zr, name)
		if !f.FileInfo().IsDir() {
			t.Fatalf("entry %s is not a directory", name)
		}
	}
}

func TestRewriteEPUBMetadataDirectoryEntriesStayByteStable(t *testing.T) {
	modified := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/", method: zip.Store, modified: modified},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/", method: zip.Store, modified: modified},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?>
<package version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Directory Extra</dc:title>
  </metadata>
  <manifest/>
  <spine/>
</package>`)},
	})

	out, err := RewriteEPUBMetadata(src, Metadata{Title: "Rewritten Directory Extra"}, modified)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}
	outAgain, err := RewriteEPUBMetadata(out, Metadata{Title: "Rewritten Directory Extra"}, modified)
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata second pass: %v", err)
	}
	if !bytes.Equal(out, outAgain) {
		t.Fatal("second rewrite with timestamped directory entries changed the EPUB bytes")
	}

	srcDir := testZipEntry(t, testZipReader(t, src), "META-INF/")
	outDir := testZipEntry(t, testZipReader(t, out), "META-INF/")
	if len(outDir.Extra) != len(srcDir.Extra) {
		t.Fatalf("directory extra length = %d, want %d", len(outDir.Extra), len(srcDir.Extra))
	}
}

func TestRewriteEPUBMetadataFailsWithoutOPF(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(`<container/>`)},
	})
	if _, err := RewriteEPUBMetadata(src, Metadata{Title: "No OPF"}, time.Time{}); err == nil {
		t.Fatal("RewriteEPUBMetadata missing OPF error = nil")
	}
}

func TestRewriteEPUBMetadataPreservesUnrelatedCollidingEntryNames(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Old</dc:title></metadata></package>`)},
		{name: "oebps/CONTENT.OPF", data: []byte("unreferenced case-colliding package")},
		{name: "OEBPS/Text.xhtml", data: []byte("first")},
		{name: "oebps/text.xhtml", data: []byte("second")},
	})

	var out bytes.Buffer
	err := RewriteEPUBMetadataTo(&out, bytes.NewReader(src), int64(len(src)), Metadata{Title: "New"}, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadataTo: %v", err)
	}
	if opf := testZipEntryString(t, out.Bytes(), "OEBPS/content.opf"); !strings.Contains(opf, ">New</dc:title>") {
		t.Fatalf("rewritten OPF missing new title:\n%s", opf)
	}
	if got := testZipEntryString(t, out.Bytes(), "OEBPS/Text.xhtml"); got != "first" {
		t.Fatalf("first colliding entry = %q", got)
	}
	if got := testZipEntryString(t, out.Bytes(), "oebps/text.xhtml"); got != "second" {
		t.Fatalf("second colliding entry = %q", got)
	}
	if got := testZipEntryString(t, out.Bytes(), "oebps/CONTENT.OPF"); got != "unreferenced case-colliding package" {
		t.Fatalf("unreferenced case-colliding OPF = %q", got)
	}
}

func TestRewriteEPUBMetadataUsesUniqueNormalizedPackagePaths(t *testing.T) {
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "meta-inf/CONTAINER.XML", data: []byte(testWritebackContainer("oebps/CONTENT.OPF"))},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Old</dc:title></metadata></package>`)},
	})

	out, err := RewriteEPUBMetadata(src, Metadata{Title: "Fallback Match"}, time.Time{})
	if err != nil {
		t.Fatalf("RewriteEPUBMetadata: %v", err)
	}
	if opf := testZipEntryString(t, out, "OEBPS/content.opf"); !strings.Contains(opf, ">Fallback Match</dc:title>") {
		t.Fatalf("rewritten fallback OPF missing new title:\n%s", opf)
	}
}

func TestRewriteEPUBMetadataRefusesDuplicatePatchTargetBeforeWriting(t *testing.T) {
	opf := []byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Old</dc:title></metadata></package>`)
	src := testWritebackEPUB(t, []testWritebackEntry{
		{name: "mimetype", method: zip.Store, data: []byte("application/epub+zip")},
		{name: "META-INF/container.xml", data: []byte(testWritebackContainer("OEBPS/content.opf"))},
		{name: "OEBPS/content.opf", data: opf},
		{name: "OEBPS/content.opf", data: opf},
	})

	var out bytes.Buffer
	err := RewriteEPUBMetadataTo(&out, bytes.NewReader(src), int64(len(src)), Metadata{Title: "New"}, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "OPF OEBPS/content.opf resolves to multiple entries") {
		t.Fatalf("RewriteEPUBMetadataTo error = %v; want duplicate patch-target diagnostic", err)
	}
	if out.Len() != 0 {
		t.Fatalf("RewriteEPUBMetadataTo wrote %d bytes before refusing duplicate patch target", out.Len())
	}
}

type testWritebackEntry struct {
	name     string
	method   uint16
	data     []byte
	modified time.Time
}

func testWritebackEPUB(t *testing.T, entries []testWritebackEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		method := entry.method
		if method == 0 {
			method = zip.Deflate
		}
		header := &zip.FileHeader{Name: entry.name, Method: method}
		if !entry.modified.IsZero() {
			header.Modified = entry.modified
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", entry.name, err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatalf("write zip entry %s: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func testWritebackContainer(opfPath string) string {
	return `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="` + opfPath + `" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`
}

func testZipReader(t *testing.T, data []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	return zr
}

func testZipEntryString(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr := testZipReader(t, data)
	f := testZipEntry(t, zr, name)
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("open zip entry %s: %v", name, err)
	}
	raw, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		t.Fatalf("read zip entry %s: %v", name, err)
	}
	if closeErr != nil {
		t.Fatalf("close zip entry %s: %v", name, closeErr)
	}
	return string(raw)
}

func testZipEntryBytes(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	zr := testZipReader(t, data)
	f := testZipEntry(t, zr, name)
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("open zip entry %s: %v", name, err)
	}
	raw, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		t.Fatalf("read zip entry %s: %v", name, err)
	}
	if closeErr != nil {
		t.Fatalf("close zip entry %s: %v", name, closeErr)
	}
	return raw
}

func testZipEntryRawBytes(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	f := testZipEntry(t, testZipReader(t, data), name)
	r, err := f.OpenRaw()
	if err != nil {
		t.Fatalf("open raw zip entry %s: %v", name, err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read raw zip entry %s: %v", name, err)
	}
	return raw
}

func testZipEntry(t *testing.T, zr *zip.Reader, name string) *zip.File {
	t.Helper()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		return f
	}
	t.Fatalf("zip entry %s not found", name)
	return nil
}
