package format

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestExtractEPUBMetadataAndCover(t *testing.T) {
	r := createTestEPUB(t, map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Combined EPUB</dc:title>
    <dc:creator>Jane Writer</dc:creator>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="cover-image" href="images/cover.png" media-type="image/png"/>
  </manifest>
</package>`,
	}, map[string][]byte{
		"OEBPS/images/cover.png": tinyPNG,
	})

	meta, coverBytes, coverExt, err := ExtractEPUBMetadataAndCover(r, int64(r.Len()))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadataAndCover failed: %v", err)
	}
	if meta == nil {
		t.Fatal("Expected metadata, got nil")
	}
	if meta.Title != "Combined EPUB" {
		t.Fatalf("title = %q; want Combined EPUB", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Jane Writer" {
		t.Fatalf("authors = %+v; want Jane Writer", meta.Authors)
	}
	if coverExt != ".png" {
		t.Fatalf("cover ext = %q; want .png", coverExt)
	}
	if !bytes.Equal(coverBytes, tinyPNG) {
		t.Fatal("cover bytes mismatch")
	}
}

func TestExtractEPUBMetadataUsesFirstExistingRootfile(t *testing.T) {
	r := createTestEPUB(t, map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/missing.opf" media-type="application/oebps-package+xml"/>
    <rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"OPS/content.opf": `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Fallback OPF</dc:title>
  </metadata>
</package>`,
	}, nil)

	meta, err := ExtractEPUBMetadata(r, int64(r.Len()))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata failed: %v", err)
	}
	if meta == nil {
		t.Fatal("Expected metadata, got nil")
	}
	if meta.Title != "Fallback OPF" {
		t.Fatalf("title = %q; want fallback rootfile metadata", meta.Title)
	}
}

func TestExtractEPUBMetadataSkipsMalformedDeclaredRootfile(t *testing.T) {
	r := createTestEPUB(t, map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="STALE/broken.opf" media-type="application/oebps-package+xml"/>
    <rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"STALE/broken.opf": `<package><metadata>`,
		"OPS/content.opf": `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Usable Declared Root</dc:title>
  </metadata>
</package>`,
	}, nil)

	meta, err := ExtractEPUBMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata: %v", err)
	}
	if meta == nil || meta.Title != "Usable Declared Root" {
		t.Fatalf("Metadata = %+v; want second declared OPF", meta)
	}
	if got := DetectFormat("book.epub", r, r.Size()); got != FormatEPUB {
		t.Fatalf("DetectFormat = %v; want FormatEPUB from second declared OPF", got)
	}
}

func TestExtractEPUBMetadataUsesCompatibleOPFParser(t *testing.T) {
	r := createTestEPUB(t, map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.1" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>XML 1.1 EPUB</dc:title>
  </metadata>
</package>`,
	}, nil)

	meta, err := ExtractEPUBMetadata(r, int64(r.Len()))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata failed: %v", err)
	}
	if meta == nil || meta.Title != "XML 1.1 EPUB" {
		t.Fatalf("metadata = %+v; want XML 1.1 EPUB title", meta)
	}
}

func TestFormatDetection(t *testing.T) {
	// EPUB
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	f0, _ := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	f0.Write([]byte("application/epub+zip"))
	w.Close()
	epubBytes := buf.Bytes()
	r1 := bytes.NewReader(epubBytes)

	fmt1 := DetectFormat("test.epub", r1, r1.Size())
	if fmt1 != FormatEPUB {
		t.Errorf("DetectFormat EPUB failed, got %v", fmt1)
	}

	for _, tt := range []struct {
		name string
		want Format
	}{
		{name: "test.kepub", want: FormatKEPUB},
		{name: "test.kepub.epub", want: FormatKEPUB},
	} {
		r := bytes.NewReader(epubBytes)
		if got := DetectFormat(tt.name, r, r.Size()); got != tt.want {
			t.Errorf("DetectFormat %s = %v, want %v", tt.name, got, tt.want)
		}
	}

	// PDF
	pdfBytes := []byte("%PDF-1.4\n...")
	r2 := bytes.NewReader(pdfBytes)
	fmt2 := DetectFormat("test.pdf", r2, r2.Size())
	if fmt2 != FormatPDF {
		t.Errorf("DetectFormat PDF failed, got %v", fmt2)
	}

	// FB2
	fb2Bytes := []byte(`<?xml version="1.0"?><FictionBook></FictionBook>`)
	r3 := bytes.NewReader(fb2Bytes)
	fmt3 := DetectFormat("test.fb2", r3, r3.Size())
	if fmt3 != FormatFB2 {
		t.Errorf("DetectFormat FB2 failed, got %v", fmt3)
	}

	// FB2 in zip
	zippedFB2 := new(bytes.Buffer)
	w = zip.NewWriter(zippedFB2)
	f, _ := w.Create("book.fb2")
	f.Write(fb2Bytes)
	w.Close()
	r4 := bytes.NewReader(zippedFB2.Bytes())
	fmt4 := DetectFormat("test.fb2.zip", r4, r4.Size())
	if fmt4 != FormatFB2 {
		t.Errorf("DetectFormat FB2 ZIP failed, got %v", fmt4)
	}

	r5 := bytes.NewReader(zippedFB2.Bytes())
	fmt5 := DetectFormat("test.fbz", r5, r5.Size())
	if fmt5 != FormatFB2 {
		t.Errorf("DetectFormat FBZ failed, got %v", fmt5)
	}
}

func TestDetectFormatEPUBRejectsRenamedOrBadContainers(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "renamed-pdf.epub", data: []byte("%PDF-1.7\nbody\n%%EOF")},
		{name: "renamed-html.epub", data: []byte("<!doctype html><html><body>not an epub</body></html>")},
		{name: "compressed-mimetype.epub", data: testEPUBWithMimetype(t, false, false)},
		{name: "non-first-mimetype.epub", data: testEPUBWithMimetype(t, true, true)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat(%s) = %v; want FormatUnknown", tt.name, got)
			}
		})
	}
}

func TestDetectFormatEPUBAcceptsValidOPFWithImperfectMimetype(t *testing.T) {
	tests := []struct {
		name            string
		mimetypeSecond  bool
		storeMimetype   bool
		includeMimetype bool
	}{
		{name: "missing mimetype", includeMimetype: false},
		{name: "compressed mimetype", includeMimetype: true, storeMimetype: false},
		{name: "non-first mimetype", includeMimetype: true, mimetypeSecond: true, storeMimetype: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := testEPUBWithMimetypeAndOPF(t, tt.mimetypeSecond, tt.storeMimetype, tt.includeMimetype)
			r := bytes.NewReader(data)
			if got := DetectFormat("book.epub", r, r.Size()); got != FormatEPUB {
				t.Fatalf("DetectFormat = %v; want FormatEPUB", got)
			}
		})
	}
}

func TestDetectFormatEPUBFallsBackWhenStrictMimetypeTooLarge(t *testing.T) {
	data := testEPUBWithMimetypeBodyAndOPF(t, append([]byte(epubMimetype), bytes.Repeat([]byte(" "), int(maxEPUBMimetypeBytes))...))
	r := bytes.NewReader(data)
	if got := DetectFormat("book.epub", r, r.Size()); got != FormatEPUB {
		t.Fatalf("DetectFormat = %v; want FormatEPUB", got)
	}
}

func testEPUBWithMimetype(t *testing.T, mimetypeSecond bool, storeMimetype bool) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	if mimetypeSecond {
		f, err := w.Create("META-INF/container.xml")
		if err != nil {
			t.Fatalf("create container: %v", err)
		}
		if _, err := f.Write([]byte("<container/>")); err != nil {
			t.Fatalf("write container: %v", err)
		}
	}
	header := &zip.FileHeader{Name: "mimetype"}
	if storeMimetype {
		header.Method = zip.Store
	} else {
		header.Method = zip.Deflate
	}
	f, err := w.CreateHeader(header)
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := f.Write([]byte("application/epub+zip")); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close epub zip: %v", err)
	}
	return buf.Bytes()
}

func testEPUBWithMimetypeBodyAndOPF(t *testing.T, mimetypeBody []byte) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	header := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	f, err := w.CreateHeader(header)
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := f.Write(mimetypeBody); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}
	f, err = w.Create("META-INF/container.xml")
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if _, err := f.Write([]byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)); err != nil {
		t.Fatalf("write container: %v", err)
	}
	f, err = w.Create("OEBPS/content.opf")
	if err != nil {
		t.Fatalf("create opf: %v", err)
	}
	if _, err := f.Write([]byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Fallback EPUB</dc:title>
  </metadata>
</package>`)); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close epub zip: %v", err)
	}
	return buf.Bytes()
}

func testEPUBWithMimetypeAndOPF(t *testing.T, mimetypeSecond bool, storeMimetype bool, includeMimetype bool) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	writeMimetype := func() {
		header := &zip.FileHeader{Name: "mimetype"}
		if storeMimetype {
			header.Method = zip.Store
		} else {
			header.Method = zip.Deflate
		}
		f, err := w.CreateHeader(header)
		if err != nil {
			t.Fatalf("create mimetype: %v", err)
		}
		if _, err := f.Write([]byte("application/epub+zip")); err != nil {
			t.Fatalf("write mimetype: %v", err)
		}
	}
	if includeMimetype && !mimetypeSecond {
		writeMimetype()
	}
	f, err := w.Create("META-INF/container.xml")
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if _, err := f.Write([]byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)); err != nil {
		t.Fatalf("write container: %v", err)
	}
	f, err = w.Create("OEBPS/content.opf")
	if err != nil {
		t.Fatalf("create opf: %v", err)
	}
	if _, err := f.Write([]byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Fallback EPUB</dc:title>
  </metadata>
</package>`)); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	if includeMimetype && mimetypeSecond {
		writeMimetype()
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close epub zip: %v", err)
	}
	return buf.Bytes()
}
