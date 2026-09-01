package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/testfixture"
)

func writeFB2Zip(t *testing.T, path string, fb2 []byte) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	f, err := w.Create("book.fb2")
	if err != nil {
		t.Fatalf("create fb2 zip entry: %v", err)
	}
	if _, err := f.Write(fb2); err != nil {
		t.Fatalf("write fb2 zip entry: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fb2 zip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write fb2 zip: %v", err)
	}
}

func writeEPUB(t *testing.T, path string, opf []byte) {
	t.Helper()
	writeEPUBWithBinaryFiles(t, path, opf, nil)
}

func writeEPUBWithBinaryFiles(t *testing.T, path string, opf []byte, binaryFiles map[string][]byte) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	f0, err := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := f0.Write([]byte("application/epub+zip")); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}
	f1, err := w.Create("META-INF/container.xml")
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if _, err := f1.Write([]byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)); err != nil {
		t.Fatalf("write container: %v", err)
	}
	f2, err := w.Create("OEBPS/content.opf")
	if err != nil {
		t.Fatalf("create opf: %v", err)
	}
	if _, err := f2.Write(opf); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	for name, contents := range binaryFiles {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("create binary epub file: %v", err)
		}
		if _, err := f.Write(contents); err != nil {
			t.Fatalf("write binary epub file: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
}

func writeCBZ(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	for name, data := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("create cbz entry: %v", err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatalf("write cbz entry: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close cbz: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write cbz: %v", err)
	}
}

func writeTestDOCX(t *testing.T, path string) {
	t.Helper()
	writeTestDOCXWithParts(t, path, "", "", nil)
}

func writeTestDOCXWithCover(t *testing.T, path string, cover []byte) {
	t.Helper()
	writeTestDOCXWithParts(t, path, `<?xml version="1.0" encoding="UTF-8"?>
<w:document
  xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
  xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
  xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body><w:p><w:r><w:drawing><a:blip r:embed="rCover"/></w:drawing></w:r></w:p></w:body>
</w:document>`, `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rCover" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/cover.png"/>
</Relationships>`, map[string][]byte{
		"word/media/cover.png": cover,
	})
}

func writeTestDOCXWithParts(t *testing.T, path, document, documentRels string, extra map[string][]byte) {
	t.Helper()
	if document == "" {
		document = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`
	}
	entries := map[string][]byte{
		"[Content_Types].xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
  <Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>
</Types>`),
		"_rels/.rels": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`),
		"word/document.xml": []byte(document),
		"docProps/core.xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
  xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:title>Document Book</dc:title>
  <dc:creator>Doc Author</dc:creator>
  <dc:language>en</dc:language>
</cp:coreProperties>`),
		"docProps/app.xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties">
  <Company>Doc Press</Company>
</Properties>`),
	}
	if documentRels != "" {
		entries["word/_rels/document.xml.rels"] = []byte(documentRels)
	}
	maps.Copy(entries, extra)
	writeCBZ(t, path, entries)
}

func writeTestODT(t *testing.T, path string) {
	t.Helper()
	writeTestODTWithEntries(t, path, nil)
}

func writeTestODTWithCover(t *testing.T, path string, cover []byte) {
	t.Helper()
	writeTestODTWithEntries(t, path, map[string][]byte{
		"content.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"
  xmlns:xlink="http://www.w3.org/1999/xlink">
  <office:body><office:text>
    <draw:frame draw:name="opf.cover"><draw:image xlink:href="Pictures/cover.png"/></draw:frame>
  </office:text></office:body>
</office:document-content>`),
		"Pictures/cover.png": cover,
	})
}

func writeTestODTWithEntries(t *testing.T, path string, extra map[string][]byte) {
	t.Helper()
	entries := map[string][]byte{
		"mimetype": []byte("application/vnd.oasis.opendocument.text"),
		"content.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0">
  <office:body><office:text/></office:body>
</office:document-content>`),
		"meta.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<office:document-meta
  xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
  xmlns:dc="http://purl.org/dc/elements/1.1/"
  xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0">
  <office:meta>
    <dc:title>ODT Book</dc:title>
    <meta:initial-creator>ODT Author</meta:initial-creator>
    <dc:language>en</dc:language>
    <meta:user-defined meta:name="opf.metadata" meta:value-type="boolean">true</meta:user-defined>
  </office:meta>
</office:document-meta>`),
	}
	maps.Copy(entries, extra)
	writeCBZ(t, path, entries)
}

func testCBZPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func testGIFConfig(width, height uint16) []byte {
	return []byte{
		'G', 'I', 'F', '8', '9', 'a',
		byte(width), byte(width >> 8), byte(height), byte(height >> 8),
		0x80, 0x00, 0x00,
		0x00, 0x00, 0x00,
		0xff, 0xff, 0xff,
		0x3b,
	}
}

func testSizedPNG(t *testing.T, width, height int, c color.Color) []byte {
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

func testPalmDOCBytes(title string) []byte {
	return testPalmDBBytes(title, "TEXtREAd", 2)
}

func testPalmDBBytes(title, typeCreator string, compression uint16) []byte {
	const (
		palmDBHeaderSize = 78
		palmDBRecordSize = 8
		record0Offset    = palmDBHeaderSize + palmDBRecordSize
	)

	record0 := make([]byte, 16)
	binary.BigEndian.PutUint16(record0[0:2], compression)
	binary.BigEndian.PutUint32(record0[4:8], 1024)
	binary.BigEndian.PutUint16(record0[8:10], 1)
	binary.BigEndian.PutUint16(record0[10:12], 4096)

	data := make([]byte, record0Offset+len(record0))
	copy(data[:32], []byte(title))
	copy(data[60:68], []byte(typeCreator))
	binary.BigEndian.PutUint16(data[76:78], 1)
	binary.BigEndian.PutUint32(data[78:82], record0Offset)
	copy(data[record0Offset:], record0)
	return data
}

type testMOBIEXTHRecord struct {
	typ   uint32
	value []byte
}

func testMOBIBytesWithMetadataAndCover(cover []byte) []byte {
	const (
		palmDBHeaderSize = 78
		palmDBRecordSize = 8
		mobiHeaderLength = 0xe8
	)
	exth := testMOBIEXTH([]testMOBIEXTHRecord{
		{typ: 503, value: []byte("MOBI Book")},
		{typ: 100, value: []byte("Doe, Jane")},
		{typ: 201, value: testMOBIUint32(0)},
	})
	title := []byte("MOBI Book")
	titleOffset := 16 + mobiHeaderLength + len(exth)
	record0 := make([]byte, titleOffset+len(title))
	binary.BigEndian.PutUint16(record0[0:2], 1)
	copy(record0[16:20], "MOBI")
	binary.BigEndian.PutUint32(record0[20:24], mobiHeaderLength)
	binary.BigEndian.PutUint32(record0[28:32], 65001)
	binary.BigEndian.PutUint32(record0[0x54:0x58], uint32(titleOffset))
	binary.BigEndian.PutUint32(record0[0x58:0x5c], uint32(len(title)))
	binary.BigEndian.PutUint32(record0[0x5c:0x60], 0x09)
	binary.BigEndian.PutUint32(record0[0x68:0x6c], 8)
	binary.BigEndian.PutUint32(record0[0x6c:0x70], 2)
	binary.BigEndian.PutUint32(record0[0x80:0x84], 0x40)
	copy(record0[16+mobiHeaderLength:], exth)
	copy(record0[titleOffset:], title)

	records := [][]byte{record0, []byte("dummy text record"), cover}
	header := make([]byte, palmDBHeaderSize)
	copy(header[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(header[76:78], uint16(len(records)))

	offset := palmDBHeaderSize + len(records)*palmDBRecordSize
	table := make([]byte, len(records)*palmDBRecordSize)
	for i, body := range records {
		binary.BigEndian.PutUint32(table[i*palmDBRecordSize:i*palmDBRecordSize+4], uint32(offset))
		offset += len(body)
	}

	out := append(header, table...)
	for _, body := range records {
		out = append(out, body...)
	}
	return out
}

func testMOBIEXTH(records []testMOBIEXTHRecord) []byte {
	var body []byte
	for _, rec := range records {
		buf := make([]byte, 8+len(rec.value))
		binary.BigEndian.PutUint32(buf[0:4], rec.typ)
		binary.BigEndian.PutUint32(buf[4:8], uint32(len(buf)))
		copy(buf[8:], rec.value)
		body = append(body, buf...)
	}
	length := 12 + len(body)
	exth := make([]byte, length)
	copy(exth[0:4], "EXTH")
	binary.BigEndian.PutUint32(exth[4:8], uint32(length))
	binary.BigEndian.PutUint32(exth[8:12], uint32(len(records)))
	copy(exth[12:], body)
	for len(exth)%4 != 0 {
		exth = append(exth, 0)
	}
	return exth
}

func testMOBIUint32(value uint32) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, value)
	return buf
}

func testDJVUWithAnnotation(formType, annotation string) []byte {
	return append([]byte("AT&T"), testDJVUFormChunk(formType, testDJVUChunk("ANTa", []byte(annotation)))...)
}

func testDJVUFormChunk(formType string, chunks ...[]byte) []byte {
	payload := []byte(formType)
	for _, chunk := range chunks {
		payload = append(payload, chunk...)
	}
	return testDJVUChunk("FORM", payload)
}

func testDJVUChunk(id string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload)+1)
	copy(out[:4], id)
	binary.BigEndian.PutUint32(out[4:8], uint32(len(payload)))
	out = append(out, payload...)
	if len(payload)%2 != 0 {
		out = append(out, 0)
	}
	return out
}

func testCHMBytesWithTitle(title string) []byte {
	const (
		headerSize      = 0x60
		directoryOffset = 0x78
		dirHeaderLen    = 0x54
		blockSize       = 0x1000
		chunksOffset    = directoryOffset + dirHeaderLen
		directoryLength = dirHeaderLen + blockSize
		contentOffset   = directoryOffset + directoryLength
		pmglHeaderSize  = 0x14
	)

	value := append([]byte(title), 0)
	system := make([]byte, 4+4+len(value))
	binary.LittleEndian.PutUint32(system[:4], 3)
	binary.LittleEndian.PutUint16(system[4:6], 3)
	binary.LittleEndian.PutUint16(system[6:8], uint16(len(value)))
	copy(system[8:], value)

	data := make([]byte, contentOffset+len(system))
	copy(data[:4], "ITSF")
	binary.LittleEndian.PutUint32(data[4:8], 3)
	binary.LittleEndian.PutUint32(data[8:12], headerSize)
	binary.LittleEndian.PutUint64(data[0x48:0x50], directoryOffset)
	binary.LittleEndian.PutUint64(data[0x50:0x58], directoryLength)
	binary.LittleEndian.PutUint64(data[0x58:0x60], contentOffset)

	copy(data[directoryOffset:directoryOffset+4], "ITSP")
	binary.LittleEndian.PutUint32(data[directoryOffset+4:directoryOffset+8], 1)
	binary.LittleEndian.PutUint32(data[directoryOffset+8:directoryOffset+12], dirHeaderLen)
	binary.LittleEndian.PutUint32(data[directoryOffset+16:directoryOffset+20], blockSize)
	binary.LittleEndian.PutUint32(data[directoryOffset+32:directoryOffset+36], 0)
	binary.LittleEndian.PutUint32(data[directoryOffset+36:directoryOffset+40], 0)
	binary.LittleEndian.PutUint32(data[directoryOffset+44:directoryOffset+48], 1)

	entry := testCHMDirectoryEntry("/#SYSTEM", 0, 0, uint64(len(system)))
	block := data[chunksOffset : chunksOffset+blockSize]
	copy(block[:4], "PMGL")
	binary.LittleEndian.PutUint32(block[4:8], uint32(blockSize-pmglHeaderSize-len(entry)))
	binary.LittleEndian.PutUint32(block[12:16], 0xffffffff)
	binary.LittleEndian.PutUint32(block[16:20], 0xffffffff)
	copy(block[pmglHeaderSize:], entry)

	copy(data[contentOffset:], system)
	return data
}

func testCHMDirectoryEntry(name string, section, offset, size uint64) []byte {
	var out []byte
	out = append(out, testCHMCWord(uint64(len(name)))...)
	out = append(out, name...)
	out = append(out, testCHMCWord(section)...)
	out = append(out, testCHMCWord(offset)...)
	out = append(out, testCHMCWord(size)...)
	return out
}

func testCHMCWord(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var rev []byte
	for v > 0 {
		rev = append(rev, byte(v&0x7f))
		v >>= 7
	}
	out := make([]byte, len(rev))
	for i := range rev {
		b := rev[len(rev)-1-i]
		if i < len(rev)-1 {
			b |= 0x80
		}
		out[i] = b
	}
	return out
}

func testRAR4Bytes() []byte {
	return testfixture.CBR3()
}

func TestResolveUsesOriginalNameForUploadFallbacks(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "upload.tmp")
	if err := os.WriteFile(tmpPath, []byte("opaque book bytes"), 0o644); err != nil {
		t.Fatalf("write temp upload: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: tmpPath, OriginalName: "Uploaded Book.fb2"}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if plan.Title != "Uploaded Book" {
		t.Fatalf("Title = %q; want %q", plan.Title, "Uploaded Book")
	}
	if plan.Extension != ".fb2" {
		t.Fatalf("Extension = %q; want .fb2", plan.Extension)
	}
	if plan.Format != format.FormatFB2 {
		t.Fatalf("Format = %v; want FormatFB2", plan.Format)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Unknown Author" {
		t.Fatalf("Authors = %+v; want Unknown Author fallback", plan.Authors)
	}
}

func TestResolveKEPUBUsesEPUBMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Kobo Book.kepub.epub")
	writeEPUB(t, path, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Kobo Book</dc:title>
    <dc:creator opf:file-as="Author, Kobo">Kobo Author</dc:creator>
  </metadata>
</package>`))

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Format != format.FormatKEPUB {
		t.Fatalf("Format = %v; want FormatKEPUB", plan.Format)
	}
	if plan.Extension != ".kepub.epub" {
		t.Fatalf("Extension = %q; want .kepub.epub", plan.Extension)
	}
	if !plan.CanRead {
		t.Fatalf("CanRead = false; want true for KEPUB reader path")
	}
	if plan.Title != "Kobo Book" {
		t.Fatalf("Title = %q; want Kobo Book", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Kobo Author" || plan.Authors[0].SortName != "Author, Kobo" {
		t.Fatalf("Authors = %+v; want Kobo Author with file-as sort", plan.Authors)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for valid KEPUB", plan.Warnings)
	}
}

func TestResolveDJVUUsesFilenameFallbacks(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "Scanned Book.djvu", data: testfixture.MinimalDJVU("DJVU")},
		{name: "Multipage Scan.djv", data: testfixture.MinimalDJVU("DJVM")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.data, 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if plan.Format != format.FormatDJVU {
				t.Fatalf("Format = %v; want FormatDJVU", plan.Format)
			}
			if plan.CanRead {
				t.Fatalf("CanRead = true; want false until DJVU reader exists")
			}
			if len(plan.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized DJVU", plan.Warnings)
			}
			wantTitle := strings.TrimSuffix(tt.name, format.BookExtension(tt.name))
			if plan.Title != wantTitle {
				t.Fatalf("Title = %q; want %q", plan.Title, wantTitle)
			}
		})
	}
}

func TestResolveDJVUMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Fallback Name.djvu")
	data := testDJVUWithAnnotation("DJVU", `(metadata
  (title "Annotated DJVU")
  (author "DjVu Author")
  (language "en")
)`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Format != format.FormatDJVU {
		t.Fatalf("Format = %v; want FormatDJVU", plan.Format)
	}
	if plan.Title != "Annotated DJVU" {
		t.Fatalf("Title = %q; want embedded DJVU title", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "DjVu Author" {
		t.Fatalf("Authors = %+v; want embedded DJVU author", plan.Authors)
	}
	if plan.Metadata.Language != "en" {
		t.Fatalf("Language = %q; want en", plan.Metadata.Language)
	}
	if plan.CanRead {
		t.Fatalf("CanRead = true; want false until DJVU reader exists")
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for DJVU metadata", plan.Warnings)
	}
}

func TestResolveMOBIFamilyUsesFilenameFallbacks(t *testing.T) {
	for _, tt := range []struct {
		name       string
		wantFormat format.Format
		canRead    bool
	}{
		{name: "Legacy Book.mobi", wantFormat: format.FormatMOBI, canRead: true},
		{name: "Kindle Book.azw", wantFormat: format.FormatAZW, canRead: true},
		{name: "Kindle Book.azw3", wantFormat: format.FormatAZW3, canRead: true},
		{name: "Print Replica.azw4", wantFormat: format.FormatAZW4},
		{name: "Palm Book.prc", wantFormat: format.FormatPRC, canRead: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, testfixture.MinimalMOBI(), 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if !IsSupportedBook(tt.name) {
				t.Fatalf("%q should be accepted by import-folder", tt.name)
			}
			if plan.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", plan.Format, tt.wantFormat)
			}
			if plan.CanRead != tt.canRead {
				t.Fatalf("CanRead = %v; want %v", plan.CanRead, tt.canRead)
			}
			if len(plan.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized MOBI-family format", plan.Warnings)
			}
			wantTitle := tt.name[:len(tt.name)-len(filepath.Ext(tt.name))]
			if plan.Title != wantTitle {
				t.Fatalf("Title = %q; want %q", plan.Title, wantTitle)
			}
			if len(plan.Authors) != 1 || plan.Authors[0].Name != "Unknown Author" {
				t.Fatalf("Authors = %+v; want Unknown Author fallback", plan.Authors)
			}
		})
	}
}

func TestResolvePalmDOCMetadata(t *testing.T) {
	for _, tt := range []struct {
		name  string
		title string
	}{
		{name: "fallback-name.pdb", title: "Lem Stanislaw - Solaris"},
		{name: "textread.mobi", title: "Libmobi test sample"},
		{name: "textread.prc", title: "Libmobi test sample"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, testPalmDOCBytes(tt.title), 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if plan.Format != format.FormatPDB {
				t.Fatalf("Format = %v; want FormatPDB", plan.Format)
			}
			if plan.CanRead {
				t.Fatalf("CanRead = true; want false until PalmDOC reader/export exists")
			}
			if len(plan.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized PalmDOC", plan.Warnings)
			}
			if plan.Title != tt.title {
				t.Fatalf("Title = %q; want %q", plan.Title, tt.title)
			}
			if len(plan.Authors) != 1 || plan.Authors[0].Name != "Unknown Author" {
				t.Fatalf("Authors = %+v; want Unknown Author fallback", plan.Authors)
			}
		})
	}
}

func TestResolveComicArchivesUseAvailableCapabilities(t *testing.T) {
	for _, tt := range []struct {
		name       string
		data       []byte
		wantFormat format.Format
		canRead    bool
		hasCover   bool
		wantTitle  string
		wantAuthor string
	}{
		{name: "Rar Comic.cbr", data: testRAR4Bytes(), wantFormat: format.FormatCBR, canRead: true, hasCover: true, wantTitle: "Rar Comic", wantAuthor: "Unknown Author"},
		{name: "Seven Zip Comic.cb7", data: testfixture.CB7(), wantFormat: format.FormatCB7, canRead: true, hasCover: true, wantTitle: "CB7 Fixture", wantAuthor: "Fixture Author"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.data, 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if !IsSupportedBook(tt.name) {
				t.Fatalf("%q should be accepted by import-folder", tt.name)
			}
			if plan.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", plan.Format, tt.wantFormat)
			}
			if plan.CanRead != tt.canRead {
				t.Fatalf("CanRead = %v; want %v", plan.CanRead, tt.canRead)
			}
			if (len(plan.CoverBytes) > 0) != tt.hasCover {
				t.Fatalf("CoverBytes length = %d; has cover want %v", len(plan.CoverBytes), tt.hasCover)
			}
			if len(plan.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized comic archive", plan.Warnings)
			}
			if plan.Title != tt.wantTitle {
				t.Fatalf("Title = %q; want %q", plan.Title, tt.wantTitle)
			}
			if len(plan.Authors) != 1 || plan.Authors[0].Name != tt.wantAuthor {
				t.Fatalf("Authors = %+v; want %q", plan.Authors, tt.wantAuthor)
			}
		})
	}
}

func TestResolveTextFormatsUseFilenameFallbacks(t *testing.T) {
	for _, tt := range []struct {
		name       string
		data       []byte
		wantFormat format.Format
	}{
		{name: "Plain Book.txt", data: []byte("A plain text book.\n"), wantFormat: format.FormatTXT},
		{name: "Text Alias.text", data: []byte("A plain text alias.\n"), wantFormat: format.FormatTXT},
		{name: "Markdown Book.md", data: []byte("# Markdown Book\n"), wantFormat: format.FormatMarkdown},
		{name: "Long Markdown.markdown", data: []byte("# Long Markdown\n"), wantFormat: format.FormatMarkdown},
		{name: "Textile Book.textile", data: []byte("h1. Textile Book\n"), wantFormat: format.FormatTextile},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.data, 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if !IsSupportedBook(tt.name) {
				t.Fatalf("%q should be accepted by import-folder", tt.name)
			}
			if plan.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", plan.Format, tt.wantFormat)
			}
			if plan.CanRead {
				t.Fatalf("CanRead = true; want false until text reader/export exists")
			}
			if len(plan.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized text format", plan.Warnings)
			}
			wantTitle := strings.TrimSuffix(tt.name, format.BookExtension(tt.name))
			if plan.Title != wantTitle {
				t.Fatalf("Title = %q; want %q", plan.Title, wantTitle)
			}
			if len(plan.Authors) != 1 || plan.Authors[0].Name != "Unknown Author" {
				t.Fatalf("Authors = %+v; want Unknown Author fallback", plan.Authors)
			}
		})
	}
}

func TestResolveMarkdownMetadataFromLeadingLabels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classic-books-markdown-alice.md")
	if err := os.WriteFile(path, []byte("# Title: Alice's Adventures in Wonderland\n\n## Author: Lewis Carroll\n## Year: 1865\n\n-------\n\n## Chapter 1\nBody.\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if plan.Format != format.FormatMarkdown {
		t.Fatalf("Format = %v; want Markdown", plan.Format)
	}
	if plan.Title != "Alice's Adventures in Wonderland" {
		t.Fatalf("Title = %q; want markdown title", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Lewis Carroll" || plan.Authors[0].SortName != "Carroll, Lewis" {
		t.Fatalf("Authors = %+v; want Lewis Carroll with sort", plan.Authors)
	}
	if plan.Metadata == nil || plan.Metadata.Date != "1865" {
		t.Fatalf("Metadata = %+v; want year from markdown labels", plan.Metadata)
	}
}

func TestResolveParsedMetadataDispatch(t *testing.T) {
	for _, tt := range []struct {
		name            string
		wantFormat      format.Format
		wantTitle       string
		wantAuthor      string
		wantAuthorSort  string
		wantLanguage    string
		wantPublisher   string
		wantDescription string
		write           func(t *testing.T, path string)
	}{
		{
			name:           "archive.txtz",
			wantFormat:     format.FormatTXTZ,
			wantTitle:      "Archived Text",
			wantAuthor:     "Archive Author",
			wantAuthorSort: "Author, Archive",
			write: func(t *testing.T, path string) {
				writeCBZ(t, path, map[string][]byte{
					"book.txt": []byte("Text archive body.\n"),
					"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Archived Text</dc:title>
    <dc:creator opf:file-as="Author, Archive">Archive Author</dc:creator>
  </metadata>
</package>`),
				})
			},
		},
		{
			name:           "archive.htmlz",
			wantFormat:     format.FormatHTMLZ,
			wantTitle:      "Archived HTML",
			wantAuthor:     "HTML Author",
			wantAuthorSort: "Author, HTML",
			write: func(t *testing.T, path string) {
				writeCBZ(t, path, map[string][]byte{
					"index.html": []byte("<html><head><title>Ignored HTML Title</title></head></html>"),
					"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Archived HTML</dc:title>
    <dc:creator opf:file-as="Author, HTML">HTML Author</dc:creator>
  </metadata>
</package>`),
				})
			},
		},
		{
			name:           "Document Book.docx",
			wantFormat:     format.FormatDOCX,
			wantTitle:      "Document Book",
			wantAuthor:     "Doc Author",
			wantAuthorSort: "Author, Doc",
			wantLanguage:   "en",
			wantPublisher:  "Doc Press",
			write:          writeTestDOCX,
		},
		{
			name:           "Macro Document.docm",
			wantFormat:     format.FormatDOCM,
			wantTitle:      "Document Book",
			wantAuthor:     "Doc Author",
			wantAuthorSort: "Author, Doc",
			wantLanguage:   "en",
			wantPublisher:  "Doc Press",
			write:          writeTestDOCX,
		},
		{
			name:           "ODT Book.odt",
			wantFormat:     format.FormatODT,
			wantTitle:      "ODT Book",
			wantAuthor:     "ODT Author",
			wantAuthorSort: "Author, ODT",
			wantLanguage:   "en",
			write:          writeTestODT,
		},
		{
			name:            "RTF Book.rtf",
			wantFormat:      format.FormatRTF,
			wantTitle:       "RTF Book",
			wantAuthor:      "RTF Author",
			wantAuthorSort:  "Author, RTF",
			wantPublisher:   "RTF Press",
			wantDescription: "RTF subject",
			write: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{\rtf1\ansi{\info{\title RTF Book}{\author RTF Author}{\subject RTF subject}{\manager RTF Press}}Body}`), 0o644); err != nil {
					t.Fatalf("write source: %v", err)
				}
			},
		},
		{
			name:       "manual.chm",
			wantFormat: format.FormatCHM,
			wantTitle:  "Oracle PL/SQL by Example, Third Edition",
			wantAuthor: "Unknown Author",
			write: func(t *testing.T, path string) {
				if err := os.WriteFile(path, testCHMBytesWithTitle("Oracle PL/SQL by Example, Third Edition"), 0o644); err != nil {
					t.Fatalf("write source: %v", err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			tt.write(t, path)

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if plan.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", plan.Format, tt.wantFormat)
			}
			if plan.CanRead {
				t.Fatalf("CanRead = true; want false for parsed metadata format")
			}
			if len(plan.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for parsed metadata", plan.Warnings)
			}
			if plan.Title != tt.wantTitle {
				t.Fatalf("Title = %q; want %q", plan.Title, tt.wantTitle)
			}
			if len(plan.Authors) != 1 || plan.Authors[0].Name != tt.wantAuthor {
				t.Fatalf("Authors = %+v; want %s", plan.Authors, tt.wantAuthor)
			}
			if tt.wantAuthorSort != "" && plan.Authors[0].SortName != tt.wantAuthorSort {
				t.Fatalf("Author sort = %q; want %q", plan.Authors[0].SortName, tt.wantAuthorSort)
			}
			if tt.wantLanguage != "" && (plan.Metadata == nil || plan.Metadata.Language != tt.wantLanguage) {
				t.Fatalf("Metadata = %+v; want language %q", plan.Metadata, tt.wantLanguage)
			}
			if tt.wantPublisher != "" && (plan.Metadata == nil || plan.Metadata.Publisher != tt.wantPublisher) {
				t.Fatalf("Metadata = %+v; want publisher %q", plan.Metadata, tt.wantPublisher)
			}
			if tt.wantDescription != "" && (plan.Metadata == nil || plan.Metadata.Description != tt.wantDescription) {
				t.Fatalf("Metadata = %+v; want description %q", plan.Metadata, tt.wantDescription)
			}
		})
	}
}

func TestResolveRTFUnsupportedCodepageWarnsAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Fallback Title.rtf")
	if err := os.WriteFile(path, []byte(`{\rtf1\ansi\ansicpg932{\info{\title \'82\'a0}}Body}`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Format != format.FormatRTF || plan.Title != "Fallback Title" {
		t.Fatalf("Plan = format %v title %q; want RTF with filename fallback", plan.Format, plan.Title)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0].Error(), "unsupported RTF code page 932") {
		t.Fatalf("Warnings = %+v; want unsupported code page warning", plan.Warnings)
	}
}

func TestResolveContainerCoverDispatch(t *testing.T) {
	for _, tt := range []struct {
		name       string
		wantFormat format.Format
		cover      func(t *testing.T) []byte
		write      func(t *testing.T, path string, cover []byte)
	}{
		{
			name:       "archive.txtz",
			wantFormat: format.FormatTXTZ,
			cover: func(t *testing.T) []byte {
				return testCBZPNG(t, color.NRGBA{R: 10, G: 80, B: 30, A: 255})
			},
			write: func(t *testing.T, path string, cover []byte) {
				writeCBZ(t, path, map[string][]byte{
					"book.txt": []byte("Text archive body.\n"),
					"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<metadata>
  <cover-relpath-from-base>images/cover.png</cover-relpath-from-base>
</metadata>`),
					"images/cover.png": cover,
				})
			},
		},
		{
			name:       "archive.htmlz",
			wantFormat: format.FormatHTMLZ,
			cover: func(t *testing.T) []byte {
				return testCBZPNG(t, color.NRGBA{R: 60, G: 90, B: 140, A: 255})
			},
			write: func(t *testing.T, path string, cover []byte) {
				writeCBZ(t, path, map[string][]byte{
					"index.html":       []byte(`<html><body><img src="images/cover.png"/></body></html>`),
					"images/cover.png": cover,
				})
			},
		},
		{
			name:       "Document Cover.docx",
			wantFormat: format.FormatDOCX,
			cover: func(t *testing.T) []byte {
				return testSizedPNG(t, 400, 600, color.NRGBA{R: 70, G: 120, B: 160, A: 255})
			},
			write: writeTestDOCXWithCover,
		},
		{
			name:       "ODT Cover.odt",
			wantFormat: format.FormatODT,
			cover: func(t *testing.T) []byte {
				return testSizedPNG(t, 120, 160, color.NRGBA{R: 120, G: 80, B: 160, A: 255})
			},
			write: writeTestODTWithCover,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			cover := tt.cover(t)
			tt.write(t, path, cover)

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if plan.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", plan.Format, tt.wantFormat)
			}
			if !bytes.Equal(plan.CoverBytes, cover) {
				t.Fatalf("CoverBytes = %d bytes; want embedded/container cover", len(plan.CoverBytes))
			}
			if len(plan.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for valid cover", plan.Warnings)
			}
		})
	}
}

func TestResolveHTMLFormatsUseMetadata(t *testing.T) {
	for _, tt := range []struct {
		name       string
		data       []byte
		wantFormat format.Format
	}{
		{
			name:       "Saved Page.html",
			data:       []byte(`<html><head><title>Saved Page</title><meta name="author" content="Web Author"></head><body>Text</body></html>`),
			wantFormat: format.FormatHTML,
		},
		{
			name:       "Saved Page.htm",
			data:       []byte(`<html><head><title>Saved Page</title><meta name="author" content="Web Author"></head></html>`),
			wantFormat: format.FormatHTML,
		},
		{
			name:       "Saved Page.xhtml",
			data:       []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Saved Page</title><meta name="author" content="Web Author"></head></html>`),
			wantFormat: format.FormatXHTML,
		},
		{
			name:       "Saved Page.xhtm",
			data:       []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Saved Page</title><meta name="author" content="Web Author"></head></html>`),
			wantFormat: format.FormatXHTML,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.data, 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !IsSupportedBook(tt.name) {
				t.Fatalf("%q should be accepted by import-folder", tt.name)
			}
			if plan.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", plan.Format, tt.wantFormat)
			}
			if plan.CanRead {
				t.Fatalf("CanRead = true; want false until sanitized HTML reader/export exists")
			}
			if len(plan.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized HTML", plan.Warnings)
			}
			if plan.Title != "Saved Page" {
				t.Fatalf("Title = %q; want HTML metadata title", plan.Title)
			}
			if len(plan.Authors) != 1 || plan.Authors[0].Name != "Web Author" || plan.Authors[0].SortName != "Author, Web" {
				t.Fatalf("Authors = %+v; want Web Author with sort", plan.Authors)
			}
		})
	}
}

func TestResolveCHMUsesFilenameFallbacks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Technical Manual.chm")
	if err := os.WriteFile(path, testfixture.MinimalCHM(3), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !IsSupportedBook("Technical Manual.chm") {
		t.Fatalf("CHM should be accepted by import-folder")
	}
	if plan.Format != format.FormatCHM {
		t.Fatalf("Format = %v; want FormatCHM", plan.Format)
	}
	if plan.CanRead {
		t.Fatalf("CanRead = true; want false until CHM reader/export exists")
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for recognized CHM", plan.Warnings)
	}
	if plan.Title != "Technical Manual" {
		t.Fatalf("Title = %q; want filename fallback", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Unknown Author" {
		t.Fatalf("Authors = %+v; want Unknown Author fallback", plan.Authors)
	}
}

func TestResolveStructuredFilenameFallbackMetadata(t *testing.T) {
	dir := t.TempDir()
	name := "Structured Title -- Jane Writer -- 2020 -- Example Press -- isbn13 9780306406157 -- source.pdf"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("%PDF-1.4\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Format != format.FormatPDF {
		t.Fatalf("Format = %v; want FormatPDF", plan.Format)
	}
	if plan.Title != "Structured Title" {
		t.Fatalf("Title = %q; want structured filename title", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Jane Writer" || plan.Authors[0].SortName != "Writer, Jane" {
		t.Fatalf("Authors = %+v; want Jane Writer with sort", plan.Authors)
	}
	if plan.Metadata == nil || plan.Metadata.Identifier != "isbn:9780306406157" {
		t.Fatalf("Metadata = %+v; want ISBN from structured filename", plan.Metadata)
	}
}

func TestResolveIgnoresUnsignaledDelimitedFilename(t *testing.T) {
	dir := t.TempDir()
	name := "One -- Two -- Three -- Four.pdf"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("%PDF-1.4\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Title != "One -- Two -- Three -- Four" {
		t.Fatalf("Title = %q; want plain filename fallback", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Unknown Author" {
		t.Fatalf("Authors = %+v; want Unknown Author fallback", plan.Authors)
	}
}

func TestResolveWarnsForUnrecognizedRegisteredFormats(t *testing.T) {
	for _, tt := range []struct {
		name      string
		data      []byte
		wantTitle string
	}{
		{name: "broken.mobi", data: []byte("not a mobi container")},
		{name: "broken.azw", data: []byte("not a mobi container")},
		{name: "broken.azw3", data: []byte("not a mobi container")},
		{name: "broken.azw4", data: []byte("not a mobi container")},
		{name: "broken.prc", data: []byte("not a mobi container")},
		{name: "broken.epub", data: []byte("not an epub"), wantTitle: "broken"},
		{name: "broken.kepub", data: []byte("not an epub"), wantTitle: "broken"},
		{name: "broken.kepub.epub", data: []byte("not an epub"), wantTitle: "broken"},
		{name: "not-palmdb.pdb", data: []byte("not a PalmDOC database")},
		{name: "non-palmdoc.pdb", data: testPalmDBBytes("Calendar", "DATAAPP1", 2)},
		{name: "broken.cbz", data: []byte("not a zip"), wantTitle: "broken"},
		{name: "broken.cbr", data: []byte("not a comic archive")},
		{name: "broken.cb7", data: []byte("not a comic archive")},
		{name: "broken.djvu", data: []byte("not a djvu")},
		{name: "broken.djv", data: []byte("not a djvu")},
		{name: "broken.txt", data: []byte("%PDF-1.7\nnot really text")},
		{name: "broken.text", data: []byte("hello\x00world")},
		{name: "broken.md", data: []byte("PK\x03\x04archive")},
		{name: "broken.markdown", data: []byte("hello\x01world")},
		{name: "broken.textile", data: []byte("hello\x01world")},
		{name: "broken.txtz", data: []byte("not a zip archive")},
		{name: "broken.html", data: []byte("not actually html")},
		{name: "broken.htm", data: []byte("%PDF-1.7\nnot html")},
		{name: "broken.xhtml", data: []byte("hello\x00world")},
		{name: "broken.xhtm", data: []byte("plain text")},
		{name: "broken.htmlz", data: []byte("not a zip archive")},
		{name: "broken.docx", data: []byte("not a docx")},
		{name: "broken.docm", data: []byte("not a docx")},
		{name: "broken.odt", data: []byte("not an office document")},
		{name: "broken.rtf", data: []byte("not an office document")},
		{name: "broken.chm", data: []byte("not a chm")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.data, 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if plan.Format != format.FormatUnknown {
				t.Fatalf("Format = %v; want FormatUnknown", plan.Format)
			}
			if plan.CanRead {
				t.Fatalf("CanRead = true; want false for unrecognized format")
			}
			want := "unrecognized " + strings.ToLower(format.BookExtension(tt.name)) + " contents"
			if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0].Error(), want) {
				t.Fatalf("Warnings = %+v; want %q warning", plan.Warnings, want)
			}
			if tt.wantTitle != "" && plan.Title != tt.wantTitle {
				t.Fatalf("Title = %q; want %q", plan.Title, tt.wantTitle)
			}
		})
	}
}

func TestResolvePrefersSidecarCoverOverEmbeddedEPUBCover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "with-cover.epub")
	embeddedCover := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
		0x00, 0x05, 0xfe, 0x02, 0xfe, 0xdc, 0xcc, 0x59,
		0xe7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
	sidecarCover := testCBZPNG(t, color.NRGBA{R: 30, G: 60, B: 90, A: 255})
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>EPUB With Cover</dc:title>
    <dc:creator>Cover Author</dc:creator>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="cover-image" href="images/cover.png" media-type="image/png"/>
  </manifest>
</package>`)
	writeEPUBWithBinaryFiles(t, path, opf, map[string][]byte{
		"OEBPS/images/cover.png": embeddedCover,
	})
	if err := os.WriteFile(filepath.Join(dir, "cover.jpeg"), sidecarCover, 0o644); err != nil {
		t.Fatalf("write sidecar cover: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path, SidecarDir: dir}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !bytes.Equal(plan.CoverBytes, sidecarCover) {
		t.Fatalf("CoverBytes = %q; want sidecar cover", plan.CoverBytes)
	}
}

func TestResolveDropsInvalidSidecarCover(t *testing.T) {
	for _, tc := range []struct {
		name        string
		coverName   string
		coverBytes  []byte
		wantWarning string
	}{
		{
			name:        "not-image",
			coverName:   "cover.jpg",
			coverBytes:  []byte("curated sidecar cover"),
			wantWarning: "decode image config",
		},
		{
			name:        "too-large",
			coverName:   "cover.png",
			coverBytes:  testGIFConfig(10000, 10000),
			wantWarning: "exceed",
		},
		{
			name:        "corrupt-pixel-data",
			coverName:   "cover.png",
			coverBytes:  testCorruptPNG(t),
			wantWarning: "decode image",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.name+".epub")
			opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>EPUB With Invalid Sidecar Cover</dc:title>
    <dc:creator>Cover Author</dc:creator>
  </metadata>
</package>`)
			writeEPUB(t, path, opf)
			if err := os.WriteFile(filepath.Join(dir, tc.coverName), tc.coverBytes, 0o644); err != nil {
				t.Fatalf("write sidecar cover: %v", err)
			}

			plan, err := Resolve(context.Background(), Source{Path: path, SidecarDir: dir}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if len(plan.CoverBytes) != 0 {
				t.Fatalf("CoverBytes = %d bytes; want no cover", len(plan.CoverBytes))
			}
			if len(plan.Warnings) != 1 {
				t.Fatalf("Warnings = %+v; want one warning", plan.Warnings)
			}
			warning := plan.Warnings[0].Error()
			if !strings.Contains(warning, "skip invalid cover") || !strings.Contains(warning, tc.wantWarning) {
				t.Fatalf("Warning = %v; want invalid cover warning containing %q", plan.Warnings[0], tc.wantWarning)
			}
			if plan.Title != "EPUB With Invalid Sidecar Cover" {
				t.Fatalf("Title = %q; want metadata despite invalid cover", plan.Title)
			}
		})
	}
}

func testCorruptPNG(t *testing.T) []byte {
	t.Helper()
	src := testCBZPNG(t, color.White)
	idat := bytes.Index(src, []byte("IDAT"))
	if idat < 4 {
		t.Fatal("encoded PNG has no IDAT chunk")
	}
	chunkLen := int(binary.BigEndian.Uint32(src[idat-4 : idat]))
	crc := idat + 4 + chunkLen
	if crc+4 > len(src) {
		t.Fatal("encoded PNG has truncated IDAT checksum")
	}
	src[crc] ^= 0xff
	return src
}

func TestResolveRecoversForbiddenEPUBOPFControl(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken-opf.epub")
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Invalid` + "\x01" + ` OPF</dc:title></metadata>
</package>`
	writeEPUB(t, path, []byte(opf))

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Format != format.FormatEPUB {
		t.Fatalf("Format = %v; want FormatEPUB", plan.Format)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want recovered OPF metadata without a warning", plan.Warnings)
	}
	if plan.Title != "Invalid OPF" {
		t.Fatalf("Title = %q; want recovered OPF title", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Unknown Author" {
		t.Fatalf("Authors = %+v; want Unknown Author fallback", plan.Authors)
	}
}

func TestResolvePrefersCompleteSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sidecar-wins.epub")
	embeddedOPF := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Invalid ` + "\x01" + ` OPF</dc:title></metadata>
</package>`
	writeEPUB(t, path, []byte(embeddedOPF))

	sidecarOPF := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Curated Sidecar Title</dc:title>
    <dc:creator>Curated Author</dc:creator>
    <meta name="calibre:timestamp" content="2013-02-01T10:11:12.345678+00:00"/>
  </metadata>
</package>`)
	if err := os.WriteFile(filepath.Join(dir, "metadata.opf"), sidecarOPF, 0o644); err != nil {
		t.Fatalf("write sidecar opf: %v", err)
	}
	sidecarCover := testCBZPNG(t, color.NRGBA{R: 90, G: 30, B: 60, A: 255})
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), sidecarCover, 0o644); err != nil {
		t.Fatalf("write sidecar cover: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none when complete sidecar metadata is present", plan.Warnings)
	}
	if plan.Title != "Curated Sidecar Title" {
		t.Fatalf("Title = %q; want sidecar title", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Curated Author" {
		t.Fatalf("Authors = %+v; want sidecar author", plan.Authors)
	}
	wantAddedAt := time.Date(2013, time.February, 1, 10, 11, 12, 345678000, time.UTC)
	if !plan.AddedAt.Equal(wantAddedAt) {
		t.Fatalf("AddedAt = %s; want calibre timestamp %s", plan.AddedAt, wantAddedAt)
	}
	if !bytes.Equal(plan.CoverBytes, sidecarCover) {
		t.Fatalf("CoverBytes = %q; want sidecar cover", plan.CoverBytes)
	}
}

func TestResolveMOBIMetadataAndCover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kindle-book.mobi")
	cover := testCBZPNG(t, color.NRGBA{R: 220, G: 80, B: 40, A: 255})
	if err := os.WriteFile(path, testMOBIBytesWithMetadataAndCover(cover), 0o644); err != nil {
		t.Fatalf("write mobi: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Format != format.FormatMOBI {
		t.Fatalf("Format = %v; want FormatMOBI", plan.Format)
	}
	if !plan.CanRead {
		t.Fatalf("CanRead = false; want true for MOBI foliate reader")
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for valid MOBI metadata/cover", plan.Warnings)
	}
	if plan.Title != "MOBI Book" {
		t.Fatalf("Title = %q; want MOBI Book", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Jane Doe" || plan.Authors[0].SortName != "Doe, Jane" {
		t.Fatalf("Authors = %+v; want Jane Doe with sort", plan.Authors)
	}
	if !bytes.Equal(plan.CoverBytes, cover) {
		t.Fatalf("CoverBytes did not come from MOBI cover record")
	}
}

func TestResolveFB2MetadataAndCover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fiction.fb2")
	cover := base64.StdEncoding.EncodeToString(testCBZPNG(t, color.NRGBA{R: 0xff, A: 0xff}))
	src := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      <genre>prose</genre>
      <author><first-name>Jane</first-name><last-name>Author</last-name></author>
      <book-title>FB2 Book</book-title>
      <annotation><p>Imported from FB2.</p></annotation>
      <coverpage><image l:href="#cover.png"/></coverpage>
      <lang>en</lang>
    </title-info>
    <publish-info><isbn>978-0-306-40615-7</isbn></publish-info>
  </description>
  <binary id="cover.png" content-type="image/png">` + cover + `</binary>
</FictionBook>`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Format != format.FormatFB2 {
		t.Fatalf("Format = %v; want FormatFB2", plan.Format)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for valid FB2", plan.Warnings)
	}
	if plan.Title != "FB2 Book" {
		t.Fatalf("Title = %q; want FB2 Book", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Jane Author" || plan.Authors[0].SortName != "Author, Jane" {
		t.Fatalf("Authors = %+v; want Jane Author with sort", plan.Authors)
	}
	if plan.Metadata == nil || plan.Metadata.Identifier != "isbn:978-0-306-40615-7" {
		t.Fatalf("Identifier = %v; want isbn", plan.Metadata)
	}
	if len(plan.CoverBytes) == 0 {
		t.Fatalf("CoverBytes empty; want FB2 embedded cover")
	}
}

func TestResolveFB2IgnoresInvalidEmbeddedCover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken-cover.fb2")
	src := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      <author><first-name>Jane</first-name><last-name>Author</last-name></author>
      <book-title>Broken Cover FB2</book-title>
      <coverpage><image l:href="#cover.png"/></coverpage>
    </title-info>
  </description>
  <binary id="cover.png" content-type="image/png">not-valid-base64</binary>
</FictionBook>`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for invalid embedded cover", plan.Warnings)
	}
	if plan.Title != "Broken Cover FB2" {
		t.Fatalf("Title = %q; want metadata despite invalid cover", plan.Title)
	}
	if len(plan.CoverBytes) != 0 {
		t.Fatalf("CoverBytes = %d bytes; want no cover", len(plan.CoverBytes))
	}
}

func TestResolveZippedFB2MetadataAndCover(t *testing.T) {
	cover := base64.StdEncoding.EncodeToString(testCBZPNG(t, color.NRGBA{B: 0xff, A: 0xff}))
	src := []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      <author><first-name>Zip</first-name><last-name>Author</last-name></author>
      <book-title>Zipped FB2 Book</book-title>
      <coverpage><image l:href="#cover.png"/></coverpage>
      <lang>en</lang>
    </title-info>
  </description>
  <binary id="cover.png" content-type="image/png">` + cover + `</binary>
</FictionBook>`)

	tests := []struct {
		name string
		ext  string
	}{
		{name: "fiction.fb2.zip", ext: ".fb2.zip"},
		{name: "fiction.fbz", ext: ".fbz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			writeFB2Zip(t, path, src)

			plan, err := Resolve(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if plan.Format != format.FormatFB2 {
				t.Fatalf("Format = %v; want FormatFB2", plan.Format)
			}
			if plan.Extension != tt.ext {
				t.Fatalf("Extension = %q; want %q", plan.Extension, tt.ext)
			}
			if plan.Title != "Zipped FB2 Book" {
				t.Fatalf("Title = %q; want Zipped FB2 Book", plan.Title)
			}
			if len(plan.Authors) != 1 || plan.Authors[0].Name != "Zip Author" || plan.Authors[0].SortName != "Author, Zip" {
				t.Fatalf("Authors = %+v; want Zip Author with sort", plan.Authors)
			}
			if len(plan.CoverBytes) == 0 {
				t.Fatalf("CoverBytes empty; want zipped FB2 embedded cover")
			}
		})
	}
}

func TestResolveCBZMetadataAndCover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dark-knight.cbz")
	cover := testCBZPNG(t, color.NRGBA{R: 30, G: 60, B: 90, A: 255})
	writeCBZ(t, path, map[string][]byte{
		"ComicInfo.xml": []byte(`<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Batman: The Dark Knight Returns</Title>
  <Series>Batman</Series>
  <Number>1</Number>
  <Summary>In a bleak future, Bruce Wayne returns.</Summary>
  <Year>1986</Year>
  <Writer>Frank Miller</Writer>
  <Publisher>DC Comics</Publisher>
  <Genre>Superhero, Action</Genre>
  <LanguageISO>en</LanguageISO>
</ComicInfo>`),
		"page001.png": cover,
	})

	plan, err := Resolve(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Format != format.FormatCBZ {
		t.Fatalf("Format = %v; want FormatCBZ", plan.Format)
	}
	if !plan.CanRead {
		t.Fatalf("CanRead = false; want true for CBZ foliate reader")
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for valid CBZ", plan.Warnings)
	}
	if plan.Title != "Batman: The Dark Knight Returns" {
		t.Fatalf("Title = %q; want ComicInfo title", plan.Title)
	}
	if len(plan.Authors) != 1 || plan.Authors[0].Name != "Frank Miller" || plan.Authors[0].SortName != "Miller, Frank" {
		t.Fatalf("Authors = %+v; want Frank Miller with sort", plan.Authors)
	}
	if plan.Metadata == nil || plan.Metadata.Series != "Batman" || plan.Metadata.SeriesIndex != 1 {
		t.Fatalf("Metadata = %+v; want ComicInfo series", plan.Metadata)
	}
	if !bytes.Equal(plan.CoverBytes, cover) {
		t.Fatalf("CoverBytes did not come from first CBZ page")
	}
}

func TestImportGroupElectsReadablePrimary(t *testing.T) {
	newLibrary := func(t *testing.T) (*db.DB, storage.Root) {
		t.Helper()
		dataDir := t.TempDir()
		database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
		if err != nil {
			t.Fatalf("db.Init: %v", err)
		}
		t.Cleanup(func() { database.Close() })
		root := storage.NewRoot(dataDir)
		if err := storage.EnsureLayout(root); err != nil {
			t.Fatalf("EnsureLayout: %v", err)
		}
		return database, root
	}
	writeSources := func(t *testing.T) (string, string) {
		t.Helper()
		sourceDir := t.TempDir()
		docxPath := filepath.Join(sourceDir, "Book.docx")
		epubPath := filepath.Join(sourceDir, "Book.epub")
		writeTestDOCX(t, docxPath)
		writeEPUB(t, epubPath, []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Readable Book</dc:title>
    <dc:creator>Reader Author</dc:creator>
  </metadata>
</package>`))
		return docxPath, epubPath
	}
	assertPrimaryEPUB := func(t *testing.T, database *db.DB, workID string) {
		t.Helper()
		var extension string
		var canRead int
		if err := database.QueryRow(`
			SELECT extension, can_read
			FROM assets
			WHERE work_id = ? AND is_primary = 1
		`, workID).Scan(&extension, &canRead); err != nil {
			t.Fatalf("query primary asset: %v", err)
		}
		if extension != ".epub" || canRead != 1 {
			t.Fatalf("primary asset = extension %q can_read %d; want readable EPUB", extension, canRead)
		}
	}

	t.Run("new grouped work", func(t *testing.T) {
		database, root := newLibrary(t)
		docxPath, epubPath := writeSources(t)

		result, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}, {Path: epubPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("ImportGroup: %v", err)
		}
		assertPrimaryEPUB(t, database, result.WorkID)
	})

	t.Run("readable format added to existing work", func(t *testing.T) {
		database, root := newLibrary(t)
		docxPath, epubPath := writeSources(t)

		initial, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("initial ImportGroup: %v", err)
		}
		result, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}, {Path: epubPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("add-format ImportGroup: %v", err)
		}
		if result.WorkID != initial.WorkID {
			t.Fatalf("work ID = %q; want existing %q", result.WorkID, initial.WorkID)
		}
		assertPrimaryEPUB(t, database, result.WorkID)
	})
}

func TestAddedAtForSources(t *testing.T) {
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	earlier := time.Date(2012, time.March, 4, 5, 6, 7, 0, time.UTC)
	later := time.Date(2019, time.April, 5, 6, 7, 8, 0, time.UTC)
	calibre := time.Date(2015, time.June, 7, 8, 9, 10, 123456000, time.FixedZone("calibre", 2*60*60))

	tests := []struct {
		name      string
		timestamp string
		infos     []sourceInfo
		want      time.Time
	}{
		{
			name:      "calibre timestamp wins over earlier file",
			timestamp: calibre.Format(time.RFC3339Nano),
			infos:     []sourceInfo{{ModTime: earlier}},
			want:      calibre,
		},
		{
			name:      "invalid calibre timestamp falls back to earliest file",
			timestamp: "not-a-timestamp",
			infos:     []sourceInfo{{ModTime: later}, {ModTime: earlier}},
			want:      earlier,
		},
		{
			name:  "implausible file times fall back to now",
			infos: []sourceInfo{{ModTime: time.Unix(0, 0)}, {ModTime: now.Add(time.Hour)}},
			want:  now,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addedAtForSources(tt.timestamp, tt.infos, now)
			if !got.Equal(tt.want) {
				t.Fatalf("addedAtForSources = %s; want %s", got, tt.want)
			}
		})
	}
}

func TestImportGroupStoresEarliestSourceModTimeAsAddedAt(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	root := storage.NewRoot(filepath.Join(dataDir, "books"))
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	sourceDir := t.TempDir()
	laterPath := filepath.Join(sourceDir, "Book.txt")
	earlierPath := filepath.Join(sourceDir, "Book.md")
	if err := os.WriteFile(laterPath, []byte("plain version"), 0o644); err != nil {
		t.Fatalf("write later source: %v", err)
	}
	if err := os.WriteFile(earlierPath, []byte("markdown version"), 0o644); err != nil {
		t.Fatalf("write earlier source: %v", err)
	}
	earlier := time.Date(2011, time.February, 3, 4, 5, 6, 0, time.UTC)
	later := time.Date(2018, time.July, 8, 9, 10, 11, 0, time.UTC)
	if err := os.Chtimes(laterPath, later, later); err != nil {
		t.Fatalf("set later mtime: %v", err)
	}
	if err := os.Chtimes(earlierPath, earlier, earlier); err != nil {
		t.Fatalf("set earlier mtime: %v", err)
	}

	before := time.Now().Unix()
	result, err := ImportGroup(context.Background(), database, root, []Source{{Path: laterPath}, {Path: earlierPath}}, nil, Options{})
	if err != nil {
		t.Fatalf("ImportGroup: %v", err)
	}
	after := time.Now().Unix()

	var createdAt, addedAt int64
	if err := database.QueryRow("SELECT created_at, added_at FROM works WHERE id = ?", result.WorkID).Scan(&createdAt, &addedAt); err != nil {
		t.Fatalf("query work timestamps: %v", err)
	}
	if addedAt != earlier.Unix() {
		t.Fatalf("added_at = %d; want earliest source mtime %d", addedAt, earlier.Unix())
	}
	if createdAt < before || createdAt > after {
		t.Fatalf("created_at = %d; want Polka creation time in [%d, %d]", createdAt, before, after)
	}
}

func TestImportGroupRestoresTrashedWorkOnlyWhenAddingAsset(t *testing.T) {
	newLibrary := func(t *testing.T) (*db.DB, storage.Root) {
		t.Helper()
		dataDir := t.TempDir()
		database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
		if err != nil {
			t.Fatalf("db.Init: %v", err)
		}
		t.Cleanup(func() { database.Close() })
		root := storage.NewRoot(dataDir)
		if err := storage.EnsureLayout(root); err != nil {
			t.Fatalf("EnsureLayout: %v", err)
		}
		return database, root
	}
	writeSources := func(t *testing.T) (string, string) {
		t.Helper()
		sourceDir := t.TempDir()
		docxPath := filepath.Join(sourceDir, "Book.docx")
		epubPath := filepath.Join(sourceDir, "Book.epub")
		writeTestDOCX(t, docxPath)
		writeEPUB(t, epubPath, []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Restored Book</dc:title>
    <dc:creator>Restore Author</dc:creator>
  </metadata>
</package>`))
		return docxPath, epubPath
	}
	trashWork := func(t *testing.T, database *db.DB, workID string) {
		t.Helper()
		if _, err := database.Exec("UPDATE works SET deleted_at = unixepoch() WHERE id = ?", workID); err != nil {
			t.Fatalf("trash work: %v", err)
		}
	}
	assertTrashed := func(t *testing.T, database *db.DB, workID string, want bool) {
		t.Helper()
		var got bool
		if err := database.QueryRow("SELECT deleted_at IS NOT NULL FROM works WHERE id = ?", workID).Scan(&got); err != nil {
			t.Fatalf("query work trash state: %v", err)
		}
		if got != want {
			t.Fatalf("work trashed = %v; want %v", got, want)
		}
	}

	t.Run("plain duplicate stays trashed", func(t *testing.T) {
		database, root := newLibrary(t)
		docxPath, _ := writeSources(t)
		initial, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("initial ImportGroup: %v", err)
		}
		trashWork(t, database, initial.WorkID)

		duplicate, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("duplicate ImportGroup: %v", err)
		}
		if len(duplicate.Results) != 1 || duplicate.Results[0].Status != StatusDuplicate || !duplicate.Results[0].WorkTrashed {
			t.Fatalf("duplicate result = %+v; want duplicate in trashed work", duplicate.Results)
		}
		if duplicate.Restored {
			t.Fatal("plain duplicate reported a restored work")
		}
		assertTrashed(t, database, initial.WorkID, true)
	})

	t.Run("new asset restores work idempotently", func(t *testing.T) {
		database, root := newLibrary(t)
		docxPath, epubPath := writeSources(t)
		initial, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("initial ImportGroup: %v", err)
		}
		trashWork(t, database, initial.WorkID)

		mixed, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}, {Path: epubPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("mixed ImportGroup: %v", err)
		}
		if mixed.WorkID != initial.WorkID {
			t.Fatalf("work ID = %q; want existing %q", mixed.WorkID, initial.WorkID)
		}
		if !mixed.Restored {
			t.Fatal("mixed import did not report the restored work")
		}
		if len(mixed.Results) != 2 || mixed.Results[0].Status != StatusDuplicate || mixed.Results[0].WorkTrashed || mixed.Results[1].Status != StatusImported {
			t.Fatalf("mixed results = %+v; want live duplicate plus imported asset", mixed.Results)
		}
		assertTrashed(t, database, initial.WorkID, false)

		var assets int
		if err := database.QueryRow("SELECT COUNT(*) FROM assets WHERE work_id = ?", initial.WorkID).Scan(&assets); err != nil {
			t.Fatalf("count assets: %v", err)
		}
		if assets != 2 {
			t.Fatalf("asset count = %d; want 2", assets)
		}

		again, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}, {Path: epubPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("repeat ImportGroup: %v", err)
		}
		if again.Restored {
			t.Fatal("repeat import reported a restored work")
		}
		for _, result := range again.Results {
			if result.Status != StatusDuplicate || result.WorkTrashed {
				t.Fatalf("repeat results = %+v; want live duplicates", again.Results)
			}
		}
		if err := database.QueryRow("SELECT COUNT(*) FROM assets WHERE work_id = ?", initial.WorkID).Scan(&assets); err != nil {
			t.Fatalf("count repeated assets: %v", err)
		}
		if assets != 2 {
			t.Fatalf("asset count after repeat = %d; want 2", assets)
		}
	})
}

func TestPersistStoresWorkMetadata(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	root := storage.NewRoot(filepath.Join(dataDir, "books"))
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	source := []byte("metadata mapping source")
	sourcePath := filepath.Join(dataDir, "source.fb2")
	if err := os.WriteFile(sourcePath, source, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	sum := sha256.Sum256(source)
	plan := Plan{
		Source:       Source{Path: sourcePath},
		Size:         int64(len(source)),
		SourceSHA256: hex.EncodeToString(sum[:]),
		Format:       format.FormatFB2,
		Extension:    ".fb2",
		CanRead:      true,
		Metadata: &bookmeta.Metadata{
			Language:    "pt_BR",
			Description: "Mapping description",
			Publisher:   "Mapping Press",
			Date:        "2024-02-29",
			Identifier:  "isbn:978-0-00-000000-1",
			Series:      "Mapping Series",
			SeriesIndex: 2.5,
			Tags:        []string{"mapping", "contract"},
		},
		CoverBytes: []byte("cover bytes"),
		Title:      "Mapped Title",
		SortTitle:  "Title, Mapped",
		Authors: []bookmeta.AuthorMeta{
			{Name: "Primary Author", SortName: "Author, Primary", Role: "aut"},
			{Name: "Second Author", SortName: "Author, Second", Role: "trl"},
		},
	}

	result, err := Persist(context.Background(), database, root, plan, Options{})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}

	type storedMetadata struct {
		title, sortTitle, series string
		seriesIndex              float64
		description, tags        string
		coverVersion             int
		publisher, date          string
		language, identifiers    string
	}
	var got storedMetadata
	if err := database.QueryRow(`
		SELECT title, sort_title, series, series_index, description, tags,
		       cover_version, publisher, published_date,
		       language, identifiers
		FROM works
		WHERE id = ?
	`, result.WorkID).Scan(
		&got.title, &got.sortTitle, &got.series, &got.seriesIndex,
		&got.description, &got.tags, &got.coverVersion, &got.publisher,
		&got.date, &got.language, &got.identifiers,
	); err != nil {
		t.Fatalf("query work metadata: %v", err)
	}
	want := storedMetadata{
		title:        plan.Title,
		sortTitle:    plan.SortTitle,
		series:       plan.Metadata.Series,
		seriesIndex:  plan.Metadata.SeriesIndex,
		description:  plan.Metadata.Description,
		tags:         strings.Join(plan.Metadata.Tags, ", "),
		coverVersion: 1,
		publisher:    plan.Metadata.Publisher,
		date:         plan.Metadata.Date,
		language:     "pt-BR",
		identifiers:  plan.Metadata.Identifier,
	}
	if got != want {
		t.Fatalf("stored work metadata = %+v; want %+v", got, want)
	}

	rows, err := database.Query(`
		SELECT a.name, a.sort_name, COALESCE(wa.role, '')
		FROM work_authors wa
		JOIN authors a ON a.id = wa.author_id
		WHERE wa.work_id = ?
		ORDER BY wa.author_order
	`, result.WorkID)
	if err != nil {
		t.Fatalf("query work authors: %v", err)
	}
	defer rows.Close()
	var authors []bookmeta.AuthorMeta
	for rows.Next() {
		var author bookmeta.AuthorMeta
		if err := rows.Scan(&author.Name, &author.SortName, &author.Role); err != nil {
			t.Fatalf("scan work author: %v", err)
		}
		authors = append(authors, author)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("work authors: %v", err)
	}
	if !slices.Equal(authors, plan.Authors) {
		t.Fatalf("stored work authors = %+v; want %+v", authors, plan.Authors)
	}
}

func TestPersistStoresAssetHashesAndCover(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	root := storage.NewRoot(dataDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	srcBytes := []byte("plain opaque source")
	srcPath := filepath.Join(dataDir, "source.fb2")
	if err := os.WriteFile(srcPath, srcBytes, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	plan, err := Resolve(context.Background(), Source{Path: srcPath}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	plan.CoverBytes = []byte("cover-bytes")

	res, err := Persist(context.Background(), database, root, plan, Options{})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if res.Status != StatusImported {
		t.Fatalf("Status = %q; want imported", res.Status)
	}

	sum := sha256.Sum256(srcBytes)
	wantHash := hex.EncodeToString(sum[:])
	var originalHash, currentHash, storagePath string
	var formatKey string
	var originalSize, currentSize int64
	var isPrimary, canRead int
	if err := database.QueryRow(`
			SELECT original_sha256, current_sha256, original_size, current_size, storage_path, format, is_primary, can_read
			FROM assets
			WHERE id = ?
		`, res.AssetID).Scan(&originalHash, &currentHash, &originalSize, &currentSize, &storagePath, &formatKey, &isPrimary, &canRead); err != nil {
		t.Fatalf("query asset: %v", err)
	}
	if originalHash != wantHash || currentHash != wantHash {
		t.Fatalf("hashes = original %q current %q; want %q", originalHash, currentHash, wantHash)
	}
	if originalSize != int64(len(srcBytes)) || currentSize != int64(len(srcBytes)) {
		t.Fatalf("sizes = original %d current %d; want %d", originalSize, currentSize, len(srcBytes))
	}
	if isPrimary != 1 {
		t.Fatalf("is_primary = %d; want 1", isPrimary)
	}
	if canRead != 1 {
		t.Fatalf("can_read = %d; want 1", canRead)
	}
	if formatKey != format.FormatKey(plan.Format) {
		t.Fatalf("format = %q; want %q", formatKey, format.FormatKey(plan.Format))
	}
	var primaryAuthorSort string
	if err := database.QueryRow("SELECT primary_author_sort FROM works WHERE id = ?", res.WorkID).Scan(&primaryAuthorSort); err != nil {
		t.Fatalf("query primary_author_sort: %v", err)
	}
	if primaryAuthorSort != plan.Authors[0].SortName {
		t.Fatalf("primary_author_sort = %q; want %q", primaryAuthorSort, plan.Authors[0].SortName)
	}
	if _, err := os.Stat(filepath.Join(dataDir, storagePath)); err != nil {
		t.Fatalf("stored asset missing: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dataDir, covers.OriginalPath(res.WorkID))); err != nil {
		t.Fatalf("stored cover missing: %v", err)
	} else if string(got) != "cover-bytes" {
		t.Fatalf("stored cover = %q; want cover-bytes", got)
	}

	dup, err := Import(context.Background(), database, root, Source{Path: srcPath}, nil, Options{})
	if err != nil {
		t.Fatalf("duplicate Import: %v", err)
	}
	if dup.Status != StatusDuplicate || dup.AssetID != res.AssetID || dup.WorkID != res.WorkID {
		t.Fatalf("duplicate result = %+v; want existing asset/work", dup)
	}
}

func TestPersistCanceledContextRollsBackAndCleansStaging(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	root := storage.NewRoot(filepath.Join(dataDir, "books"))
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	srcPath := filepath.Join(dataDir, "source.fb2")
	if err := os.WriteFile(srcPath, []byte("opaque source"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	plan, err := Resolve(context.Background(), Source{Path: srcPath}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Persist(ctx, database, root, plan, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Persist error = %v; want context.Canceled", err)
	}

	var works, assets int
	if err := database.QueryRow("SELECT COUNT(*) FROM works").Scan(&works); err != nil {
		t.Fatalf("count works: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if works != 0 || assets != 0 {
		t.Fatalf("canceled persist left works/assets = %d/%d; want 0/0", works, assets)
	}
	entries, err := os.ReadDir(root.StagingDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled persist left staging files: %v", entries)
	}
}

func TestCanceledPreparationReturnsContextCause(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	srcPath := filepath.Join(dataDir, "source.fb2")
	if err := os.WriteFile(srcPath, []byte("opaque source"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	cause := errors.New("writer lease lost")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)

	operations := map[string]func() error{
		"resolve": func() error {
			_, err := Resolve(ctx, Source{Path: srcPath}, nil)
			return err
		},
		"probe": func() error {
			_, err := ProbeSource(ctx, database, Source{Path: srcPath})
			return err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, cause) {
				t.Fatalf("error = %v; want context cause %v", err, cause)
			}
		})
	}
}

func TestDuplicateImportRestoreUpdatesCurrentHash(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	root := storage.NewRoot(dataDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	srcPath := filepath.Join(dataDir, "source.epub")
	writeEPUB(t, srcPath, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Restore Hash EPUB</dc:title>
    <dc:creator>Restore Author</dc:creator>
  </metadata>
</package>`))
	originalBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	originalSum := sha256.Sum256(originalBytes)
	originalHash := hex.EncodeToString(originalSum[:])

	res, err := Import(context.Background(), database, root, Source{Path: srcPath}, nil, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Status != StatusImported {
		t.Fatalf("Status = %q; want imported", res.Status)
	}

	managedPath := filepath.Join(dataDir, res.StoragePath)
	rewrittenBytes := []byte("future write-back bytes")
	rewrittenSum := sha256.Sum256(rewrittenBytes)
	rewrittenHash := hex.EncodeToString(rewrittenSum[:])
	if err := os.WriteFile(managedPath, rewrittenBytes, 0o644); err != nil {
		t.Fatalf("rewrite managed file: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE assets
		SET current_sha256 = ?, current_size = ?, koreader_hash = 'stale-rewritten-hash', updated_at = unixepoch()
		WHERE id = ?
	`, rewrittenHash, len(rewrittenBytes), res.AssetID); err != nil {
		t.Fatalf("mark rewritten current hash: %v", err)
	}
	if err := os.Remove(managedPath); err != nil {
		t.Fatalf("remove managed file: %v", err)
	}

	dup, err := Import(context.Background(), database, root, Source{Path: srcPath}, nil, Options{})
	if err != nil {
		t.Fatalf("duplicate Import: %v", err)
	}
	if dup.Status != StatusDuplicate || dup.AssetID != res.AssetID || dup.WorkID != res.WorkID {
		t.Fatalf("duplicate result = %+v; want existing asset/work", dup)
	}
	if got, err := os.ReadFile(managedPath); err != nil {
		t.Fatalf("restored file missing: %v", err)
	} else if !bytes.Equal(got, originalBytes) {
		t.Fatalf("restored bytes = %q; want original source bytes", got)
	}

	var originalDBHash, currentDBHash, koReaderHash string
	var originalDBSize, currentDBSize int64
	if err := database.QueryRow(`
		SELECT original_sha256, current_sha256, original_size, current_size,
		       COALESCE(koreader_hash, '')
		FROM assets
		WHERE id = ?
	`, res.AssetID).Scan(&originalDBHash, &currentDBHash, &originalDBSize, &currentDBSize, &koReaderHash); err != nil {
		t.Fatalf("query hashes: %v", err)
	}
	if originalDBHash != originalHash || currentDBHash != originalHash {
		t.Fatalf("hashes = original %q current %q; want %q", originalDBHash, currentDBHash, originalHash)
	}
	if originalDBSize != int64(len(originalBytes)) || currentDBSize != int64(len(originalBytes)) {
		t.Fatalf("sizes = original %d current %d; want %d", originalDBSize, currentDBSize, len(originalBytes))
	}
	if koReaderHash != "" {
		t.Fatalf("restored koreader hash = %q; want empty lazy identity", koReaderHash)
	}
}
