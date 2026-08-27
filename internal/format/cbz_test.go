package format

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/testfixture"
)

func writeTestZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func testPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	return testPNGSize(t, 1, 1, c)
}

func testPNGSize(t *testing.T, width, height int, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestDetectFormatCBZValidatesImageArchive(t *testing.T) {
	pngBytes := testPNG(t, color.NRGBA{R: 20, G: 40, B: 60, A: 255})
	tests := []struct {
		name    string
		entries map[string][]byte
		want    Format
	}{
		{
			name: "valid cbz",
			entries: map[string][]byte{
				"ComicInfo.xml": []byte(`<ComicInfo><Title>Valid</Title></ComicInfo>`),
				"page001.png":   pngBytes,
			},
			want: FormatCBZ,
		},
		{
			name: "invalid image bytes",
			entries: map[string][]byte{
				"page001.png": []byte("not an image"),
			},
			want: FormatUnknown,
		},
		{
			name: "supported image without extension",
			entries: map[string][]byte{
				"page001.bin": pngBytes,
			},
			want: FormatCBZ,
		},
		{
			name: "empty of pages",
			entries: map[string][]byte{
				"readme.txt": []byte("no pages"),
			},
			want: FormatUnknown,
		},
		{
			name: "unsupported sidecars do not reject supported pages",
			entries: map[string][]byte{
				"page001.png":        pngBytes,
				"metadata.json":      []byte(`{"source":"viewer"}`),
				"thumbnails/01.webp": tinyWebP,
			},
			want: FormatCBZ,
		},
		{
			name: "WebP images only",
			entries: map[string][]byte{
				"page001.webp":  tinyWebP,
				"metadata.json": []byte(`{"source":"viewer"}`),
			},
			want: FormatCBZ,
		},
		{
			name: "WebP image behind a mismatched extension",
			entries: map[string][]byte{
				"page001.png": tinyWebP,
			},
			want: FormatCBZ,
		},
		{
			name: "epub-like zip is not cbz",
			entries: map[string][]byte{
				"mimetype":               []byte("application/epub+zip"),
				"META-INF/container.xml": []byte("<container/>"),
				"OEBPS/images/page.png":  pngBytes,
			},
			want: FormatUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := writeTestZip(t, tt.entries)
			r := bytes.NewReader(data)
			if got := DetectFormat("comic.cbz", r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestListCBZPagesUsesCanonicalPageOrder(t *testing.T) {
	page1 := testPNGSize(t, 2, 3, color.NRGBA{R: 0, G: 0, B: 255, A: 255})
	page2 := testPNGSize(t, 4, 5, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	data := writeTestZip(t, map[string][]byte{
		"ComicInfo.xml":       []byte(`<ComicInfo><Title>Valid</Title></ComicInfo>`),
		"page001-broken.png":  []byte("not an image"),
		"chapter/page10.png":  page2,
		"chapter\\page2.bin":  page1,
		"__MACOSX/._junk.png": []byte("ignored"),
		"notes.txt":           []byte("allowed aux file"),
	})

	pages, err := ListCBZPages(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ListCBZPages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d; want 2: %#v", len(pages), pages)
	}
	if pages[0].Index != 0 || pages[0].Name != "chapter/page2.bin" || pages[0].Extension != "png" || pages[0].Width != 2 || pages[0].Height != 3 || pages[0].Size != uint64(len(page1)) {
		t.Fatalf("pages[0] = %#v; want normalized page2 metadata", pages[0])
	}
	if pages[1].Index != 1 || pages[1].Name != "chapter/page10.png" || pages[1].Width != 4 || pages[1].Height != 5 || pages[1].Size != uint64(len(page2)) {
		t.Fatalf("pages[1] = %#v; want naturally sorted page10 metadata", pages[1])
	}

	cover, ext, err := ExtractCBZCover(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ExtractCBZCover: %v", err)
	}
	if ext != "png" || !bytes.Equal(cover, page1) {
		t.Fatalf("cover ext=%q len=%d; want first valid listed page", ext, len(cover))
	}
}

func TestCBZWebPPageAndCover(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{"page001.webp": tinyWebP})
	pages, err := ListCBZPages(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ListCBZPages: %v", err)
	}
	if len(pages) != 1 || pages[0].Extension != "webp" || pages[0].Width <= 0 || pages[0].Height <= 0 {
		t.Fatalf("pages = %#v; want one decoded WebP page", pages)
	}
	cover, ext, err := ExtractCBZCover(bytes.NewReader(data), int64(len(data)))
	if err != nil || ext != "webp" || !bytes.Equal(cover, tinyWebP) {
		t.Fatalf("cover ext=%q bytes=%d err=%v; want original WebP", ext, len(cover), err)
	}
}

func TestCBZAVIFPageAndCover(t *testing.T) {
	avif := testfixture.AVIF()
	data := writeTestZip(t, map[string][]byte{"page001.avif": avif})
	pages, err := ListCBZPages(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ListCBZPages: %v", err)
	}
	if len(pages) != 1 || pages[0].Extension != "avif" || pages[0].Width != 2 || pages[0].Height != 2 {
		t.Fatalf("pages = %#v; want one decoded 2x2 AVIF page", pages)
	}
	cover, ext, err := ExtractCBZCover(bytes.NewReader(data), int64(len(data)))
	if err != nil || ext != "avif" || !bytes.Equal(cover, avif) {
		t.Fatalf("cover ext=%q bytes=%d err=%v; want original AVIF", ext, len(cover), err)
	}
}

func TestCBZCoverBoundsSkipOversizedEntries(t *testing.T) {
	validPage := testPNGSize(t, 2, 3, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	oversized, err := zw.Create("page001.png")
	if err != nil {
		t.Fatalf("create oversized page: %v", err)
	}
	if _, err := oversized.Write(validPage); err != nil {
		t.Fatalf("write oversized page header: %v", err)
	}
	padding := int64(maxCBZCoverBytes) - int64(len(validPage)) + 1
	if _, err := io.CopyN(oversized, testZeroReader{}, padding); err != nil {
		t.Fatalf("write oversized page padding: %v", err)
	}

	hugeDimensions, err := zw.Create("page002.gif")
	if err != nil {
		t.Fatalf("create huge-dimension page: %v", err)
	}
	if _, err := hugeDimensions.Write(testGIFConfig(10_000, 10_000)); err != nil {
		t.Fatalf("write huge-dimension page: %v", err)
	}

	valid, err := zw.Create("page003.png")
	if err != nil {
		t.Fatalf("create valid page: %v", err)
	}
	if _, err := valid.Write(validPage); err != nil {
		t.Fatalf("write valid page: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close bounded CBZ: %v", err)
	}

	data := buf.Bytes()
	pages, err := ListCBZPages(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ListCBZPages: %v", err)
	}
	if len(pages) != 3 || pages[0].Name != "page001.png" || pages[1].Name != "page002.gif" || pages[2].Name != "page003.png" {
		t.Fatalf("listed pages = %#v; want all three valid image headers", pages)
	}
	cover, ext, err := ExtractCBZCover(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ExtractCBZCover: %v", err)
	}
	if ext != "png" || !bytes.Equal(cover, validPage) {
		t.Fatalf("bounded cover ext=%q len=%d; want valid page003.png", ext, len(cover))
	}
}

func TestExtractCBZMetadataAndCover(t *testing.T) {
	firstPage := testPNG(t, color.NRGBA{R: 0, G: 0, B: 255, A: 255})
	laterPage := testPNG(t, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	data := writeTestZip(t, map[string][]byte{
		"metadata/COMICINFO.XML": []byte(`<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Batman: The Dark Knight Returns</Title>
  <Series>Batman</Series>
  <Number>1.5</Number>
  <Summary>In a bleak future, Bruce Wayne returns.</Summary>
  <Year>1986</Year>
  <Month>2</Month>
  <Day>1</Day>
  <Writer>Frank Miller; Klaus Janson</Writer>
  <Publisher>DC Comics</Publisher>
  <Genre>Superhero, Action</Genre>
  <Tags>Classic; superhero</Tags>
  <LanguageISO>eng_us</LanguageISO>
  <GTIN>9781563893421</GTIN>
  <Web>https://comicvine.gamespot.com/batman-the-dark-knight-returns/4050-2138/</Web>
</ComicInfo>`),
		"__MACOSX/._cover.png": laterPage,
		"page10.png":           laterPage,
		"page2.png":            firstPage,
	})
	r := bytes.NewReader(data)

	meta, err := ExtractCBZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractCBZMetadata: %v", err)
	}
	if meta.Title != "Batman: The Dark Knight Returns" {
		t.Fatalf("Title = %q; want ComicInfo title", meta.Title)
	}
	if len(meta.Authors) != 2 || meta.Authors[0].Name != "Frank Miller" || meta.Authors[1].Name != "Klaus Janson" {
		t.Fatalf("Authors = %+v; want ComicInfo writers", meta.Authors)
	}
	if meta.Series != "Batman" || meta.SeriesIndex != 1.5 {
		t.Fatalf("Series = %q/%v; want Batman/1.5", meta.Series, meta.SeriesIndex)
	}
	if meta.Description != "In a bleak future, Bruce Wayne returns." {
		t.Fatalf("Description = %q; want ComicInfo summary", meta.Description)
	}
	if meta.Publisher != "DC Comics" {
		t.Fatalf("Publisher = %q; want DC Comics", meta.Publisher)
	}
	if meta.Date != "1986-02-01" {
		t.Fatalf("Date = %q; want 1986-02-01", meta.Date)
	}
	if meta.Language != "en-US" {
		t.Fatalf("Language = %q; want normalized en-US", meta.Language)
	}
	if meta.Identifier != "isbn:9781563893421, url:https://comicvine.gamespot.com/batman-the-dark-knight-returns/4050-2138/" {
		t.Fatalf("Identifier = %q; want isbn and URL", meta.Identifier)
	}
	wantTags := []string{"Superhero", "Action", "Classic"}
	if !equalStrings(meta.Tags, wantTags) {
		t.Fatalf("Tags = %#v; want %#v", meta.Tags, wantTags)
	}

	cover, ext, err := ExtractCBZCover(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ExtractCBZCover: %v", err)
	}
	if ext != "png" {
		t.Fatalf("cover ext = %q; want png", ext)
	}
	if !bytes.Equal(cover, firstPage) {
		t.Fatalf("cover did not use first naturally sorted visible page")
	}
}

func TestExtractCBZMetadataDescriptionFallback(t *testing.T) {
	page := testPNG(t, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	data := writeTestZip(t, map[string][]byte{
		"ComicInfo.xml": []byte(`<ComicInfo><Title>Fallback</Title><Description>Legacy description.</Description></ComicInfo>`),
		"page001.png":   page,
	})
	r := bytes.NewReader(data)

	meta, err := ExtractCBZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractCBZMetadata: %v", err)
	}
	if meta.Description != "Legacy description." {
		t.Fatalf("Description = %q; want ComicInfo description fallback", meta.Description)
	}
}

func TestExtractCBZMetadataGTINAndMultipleWebIdentifiers(t *testing.T) {
	page := testPNG(t, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	data := writeTestZip(t, map[string][]byte{
		"ComicInfo.xml": []byte(`<ComicInfo>
  <Title>Identifier Edges</Title>
  <GTIN>036000291452</GTIN>
  <Web>https://example.test/one; https://example.test/two
https://example.test/three</Web>
</ComicInfo>`),
		"page001.png": page,
	})
	r := bytes.NewReader(data)

	meta, err := ExtractCBZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractCBZMetadata: %v", err)
	}
	want := "gtin:036000291452, url:https://example.test/one, url:https://example.test/two, url:https://example.test/three"
	if meta.Identifier != want {
		t.Fatalf("Identifier = %q; want %q", meta.Identifier, want)
	}
}

func TestExtractCBZMetadataIgnoresInvalidGTIN(t *testing.T) {
	page := testPNG(t, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	data := writeTestZip(t, map[string][]byte{
		"ComicInfo.xml": []byte(`<ComicInfo>
  <Title>Invalid Identifier</Title>
  <GTIN>not-a-gtin</GTIN>
</ComicInfo>`),
		"page001.png": page,
	})
	r := bytes.NewReader(data)

	meta, err := ExtractCBZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractCBZMetadata: %v", err)
	}
	if meta.Identifier != "" {
		t.Fatalf("Identifier = %q; want empty invalid GTIN ignored", meta.Identifier)
	}
}

func TestExtractCBZMetadataContributorRoles(t *testing.T) {
	page := testPNG(t, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	data := writeTestZip(t, map[string][]byte{
		"ComicInfo.xml": []byte(`<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Contributor Roles</Title>
  <Writer>Writer One; Shared Creator</Writer>
  <Penciller>Pencil Person, Shared Creator</Penciller>
  <Inker>Ink Person</Inker>
  <Colorist>Color Person</Colorist>
  <Letterer>Letter Person</Letterer>
  <CoverArtist>Cover Person</CoverArtist>
  <Editor>Editor Person</Editor>
  <Translator>Translator Person</Translator>
</ComicInfo>`),
		"page001.png": page,
	})
	r := bytes.NewReader(data)

	meta, err := ExtractCBZMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractCBZMetadata: %v", err)
	}

	want := []bookmeta.AuthorMeta{
		{Name: "Writer One", Role: "writer"},
		{Name: "Shared Creator", Role: "writer"},
		{Name: "Pencil Person", Role: "penciller"},
		{Name: "Ink Person", Role: "inker"},
		{Name: "Color Person", Role: "colorist"},
		{Name: "Letter Person", Role: "letterer"},
		{Name: "Cover Person", Role: "cover_artist"},
		{Name: "Editor Person", Role: "editor"},
		{Name: "Translator Person", Role: "translator"},
	}
	if len(meta.Authors) != len(want) {
		t.Fatalf("Authors = %+v; want %d contributors", meta.Authors, len(want))
	}
	for i := range want {
		if meta.Authors[i].Name != want[i].Name || meta.Authors[i].Role != want[i].Role {
			t.Fatalf("Authors[%d] = %+v; want %+v", i, meta.Authors[i], want[i])
		}
		if meta.Authors[i].SortName != bookmeta.AuthorSort(want[i].Name) {
			t.Fatalf("Authors[%d].SortName = %q; want derived sort", i, meta.Authors[i].SortName)
		}
	}
}

func TestExtractCBZMetadataPrefersRootComicInfo(t *testing.T) {
	page := testPNG(t, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	for _, tt := range []struct {
		name string
		body map[string][]byte
		want string
	}{
		{
			name: "root wins over nested",
			body: map[string][]byte{
				"nested/ComicInfo.xml": []byte(`<ComicInfo><Title>Nested Metadata</Title></ComicInfo>`),
				"ComicInfo.xml":        []byte(`<ComicInfo><Title>Root Metadata</Title></ComicInfo>`),
				"page001.png":          page,
			},
			want: "Root Metadata",
		},
		{
			name: "nested used without root",
			body: map[string][]byte{
				"nested/ComicInfo.xml": []byte(`<ComicInfo><Title>Nested Metadata</Title></ComicInfo>`),
				"page001.png":          page,
			},
			want: "Nested Metadata",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := writeTestZip(t, tt.body)
			r := bytes.NewReader(data)

			meta, err := ExtractCBZMetadata(r, r.Size())
			if err != nil {
				t.Fatalf("ExtractCBZMetadata: %v", err)
			}
			if meta.Title != tt.want {
				t.Fatalf("Title = %q; want %q", meta.Title, tt.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type testZeroReader struct{}

func (testZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func testGIFConfig(width, height int) []byte {
	return []byte{
		'G', 'I', 'F', '8', '9', 'a',
		byte(width), byte(width >> 8), byte(height), byte(height >> 8),
		0, 0, 0,
	}
}
