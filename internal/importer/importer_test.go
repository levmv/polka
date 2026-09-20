package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
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
	if err := os.WriteFile(path, testfixture.EPUB(t, opf, binaryFiles), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
}

type resolvedTestSource struct {
	sourceInfo
	resolvedBook
}

func resolveTestSource(ctx context.Context, src Source, extractor *format.Extractor) (resolvedTestSource, error) {
	info, err := fingerprintSource(ctx, src)
	if err != nil {
		return resolvedTestSource{}, err
	}
	f, err := os.Open(src.Path)
	if err != nil {
		return resolvedTestSource{}, fmt.Errorf("open source: %w", err)
	}
	defer f.Close()
	resolved, pageCount, err := resolveSource(ctx, info, f, extractor)
	if err != nil {
		return resolvedTestSource{}, err
	}
	info.PageCount = pageCount
	return resolvedTestSource{sourceInfo: info, resolvedBook: resolved}, nil
}

func stageTestSource(t *testing.T, root storage.Root, info sourceInfo) preparedSource {
	t.Helper()
	data, err := os.ReadFile(info.Source.Path)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	staged, err := storage.Stage(root, fmt.Sprintf("%x%s", info.SourceHash, info.Extension), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("stage source: %v", err)
	}
	t.Cleanup(staged.Cleanup)
	return preparedSource{info: info, staged: staged}
}

func openTestLibrary(t *testing.T, dataDir, rootDir string) (*db.DB, storage.Root) {
	t.Helper()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	root := storage.NewRoot(rootDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	return database, root
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: tmpPath, OriginalName: "Uploaded Book.fb2"}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}

	if resolved.Metadata.Title != "Uploaded Book" {
		t.Fatalf("Title = %q; want %q", resolved.Metadata.Title, "Uploaded Book")
	}
	if resolved.Extension != ".fb2" {
		t.Fatalf("Extension = %q; want .fb2", resolved.Extension)
	}
	if resolved.Format != format.FormatFB2 {
		t.Fatalf("Format = %v; want FormatFB2", resolved.Format)
	}
	if len(resolved.Metadata.Authors) != 0 {
		t.Fatalf("Authors = %+v; want no authors", resolved.Metadata.Authors)
	}
}

func TestStagedImportSnapshotDrivesResolveAndPersist(t *testing.T) {
	dataDir := t.TempDir()
	database, root := openTestLibrary(t, dataDir, filepath.Join(dataDir, "books"))

	original := []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description><title-info><book-title>Staged Original</book-title></title-info></description>
</FictionBook>`)
	sourcePath := filepath.Join(dataDir, "snapshot.fb2")
	if err := os.WriteFile(sourcePath, original, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	prepared, err := prepareSource(t.Context(), root, Source{Path: sourcePath})
	if err != nil {
		t.Fatalf("prepareSource: %v", err)
	}
	defer prepared.staged.Cleanup()
	wantHash := storage.Sum(original)
	entries, err := os.ReadDir(root.StagingDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging = %v, %v; want one source", entries, err)
	}
	label, ok := storage.ParseStagedTempName(entries[0].Name())
	if wantLabel := fmt.Sprintf("%x.fb2", wantHash); !ok || label != wantLabel {
		t.Fatalf("staging label = %q, %v; want %q", label, ok, wantLabel)
	}
	if err := os.WriteFile(sourcePath, []byte("replacement after staging"), 0o644); err != nil {
		t.Fatalf("replace source: %v", err)
	}

	resolved, err := resolvePreparedSource(t.Context(), &prepared, nil)
	if err != nil {
		t.Fatalf("resolvePreparedSource: %v", err)
	}
	if resolved.Metadata.Title != "Staged Original" {
		t.Fatalf("resolved title = %q; want staged original", resolved.Metadata.Title)
	}

	result, err := persistPrepared(t.Context(), database, root, prepared, resolved, Options{})
	if err != nil {
		t.Fatalf("persistPrepared: %v", err)
	}
	got, err := os.ReadFile(root.Abs(result.StoragePath))
	if err != nil {
		t.Fatalf("read managed source: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("managed bytes = %q; want staged original", got)
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Format != format.FormatKEPUB {
		t.Fatalf("Format = %v; want FormatKEPUB", resolved.Format)
	}
	if resolved.Extension != ".kepub.epub" {
		t.Fatalf("Extension = %q; want .kepub.epub", resolved.Extension)
	}
	if !resolved.CanRead {
		t.Fatalf("CanRead = false; want true for KEPUB reader path")
	}
	if resolved.Metadata.Title != "Kobo Book" {
		t.Fatalf("Title = %q; want Kobo Book", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Kobo Author" || resolved.Metadata.Authors[0].SortName != "Author, Kobo" {
		t.Fatalf("Authors = %+v; want Kobo Author with file-as sort", resolved.Metadata.Authors)
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for valid KEPUB", resolved.Warnings)
	}
}

func TestResolveDJVUUsesFilenameFallbacks(t *testing.T) {
	readable, err := os.ReadFile("../testfixture/reader.djvu")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		data  []byte
		cover bool
	}{
		{name: "Scanned Book.djvu", data: testfixture.MinimalDJVU("DJVU")},
		{name: "Multipage Scan.djv", data: readable, cover: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.data, 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}
			if resolved.Format != format.FormatDJVU {
				t.Fatalf("Format = %v; want FormatDJVU", resolved.Format)
			}
			if !resolved.CanRead {
				t.Fatal("DjVu should be readable")
			}
			if (len(resolved.CoverBytes) > 0) != tt.cover || (len(resolved.Warnings) == 0) != tt.cover {
				t.Fatalf("cover bytes=%d, warnings=%v; want cover=%v", len(resolved.CoverBytes), resolved.Warnings, tt.cover)
			}
			wantTitle := strings.TrimSuffix(tt.name, format.BookExtension(tt.name))
			if resolved.Metadata.Title != wantTitle {
				t.Fatalf("Title = %q; want %q", resolved.Metadata.Title, wantTitle)
			}
		})
	}
}

func TestResolveDJVUMetadataAndCover(t *testing.T) {
	original, err := os.ReadFile("../testfixture/metadata.djvu")
	if err != nil {
		t.Fatal(err)
	}
	sidecarCover := testCBZPNG(t, color.NRGBA{R: 90, G: 30, B: 60, A: 255})
	extractor := format.NewExtractor()
	defer extractor.Close()
	for _, tt := range []struct {
		name        string
		cover       bool
		opf         string
		brokenImage bool
	}{
		{name: "embedded metadata and rendered cover"},
		{name: "sidecar cover and embedded metadata", cover: true},
		{name: "complete OPF and rendered cover", opf: "<dc:title>Sidecar</dc:title><dc:creator>Curator</dc:creator>"},
		{name: "complete sidecars", cover: true, opf: "<dc:title>Sidecar</dc:title><dc:creator>Curator</dc:creator>"},
		{name: "partial OPF and sidecar cover", cover: true, opf: "<dc:title>Sidecar</dc:title>"},
		{name: "image failure preserves metadata", brokenImage: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := bytes.Clone(original)
			// Sidecars must bypass damaged embedded data without warnings.
			if strings.Contains(tt.opf, "creator") {
				off := bytes.Index(data, []byte("ANTz"))
				clear(data[off+8 : off+8+int(binary.BigEndian.Uint32(data[off+4:off+8]))])
			}
			if tt.cover || tt.brokenImage {
				off := bytes.Index(data, []byte("Sjbz"))
				clear(data[off+8 : off+8+int(binary.BigEndian.Uint32(data[off+4:off+8]))])
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "Book.djvu")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if tt.cover {
				if err := os.WriteFile(filepath.Join(dir, "cover.png"), sidecarCover, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.opf != "" {
				opf := `<package xmlns="http://www.idpf.org/2007/opf"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/">` + tt.opf + `</metadata></package>`
				if err := os.WriteFile(filepath.Join(dir, "metadata.opf"), []byte(opf), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			resolved, err := resolveTestSource(t.Context(), Source{Path: path}, extractor)
			if err != nil {
				t.Fatal(err)
			}
			title, author := "Annotated DjVu", "Ada Lovelace"
			if tt.opf != "" {
				title = "Sidecar"
			}
			if strings.Contains(tt.opf, "creator") {
				author = "Curator"
			}
			if resolved.Metadata.Title != title || len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != author || resolved.PageCount != 3 {
				t.Fatalf("title=%q, authors=%v, pages=%d", resolved.Metadata.Title, resolved.Metadata.Authors, resolved.PageCount)
			}
			if tt.brokenImage {
				if len(resolved.CoverBytes) != 0 || len(resolved.Warnings) != 1 || !strings.Contains(resolved.Warnings[0].Error(), "render DjVu cover") {
					t.Fatalf("cover bytes=%d, warnings=%v; want only a cover rendering warning", len(resolved.CoverBytes), resolved.Warnings)
				}
				return
			}
			if len(resolved.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %v", resolved.Warnings)
			}
			if len(resolved.CoverBytes) == 0 || tt.cover && !bytes.Equal(resolved.CoverBytes, sidecarCover) {
				t.Fatal("missing or replaced sidecar cover")
			}
		})
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

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}

			if resolved.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", resolved.Format, tt.wantFormat)
			}
			if resolved.CanRead != tt.canRead {
				t.Fatalf("CanRead = %v; want %v", resolved.CanRead, tt.canRead)
			}
			if len(resolved.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized MOBI-family format", resolved.Warnings)
			}
			wantTitle := tt.name[:len(tt.name)-len(filepath.Ext(tt.name))]
			if resolved.Metadata.Title != wantTitle {
				t.Fatalf("Title = %q; want %q", resolved.Metadata.Title, wantTitle)
			}
			if len(resolved.Metadata.Authors) != 0 {
				t.Fatalf("Authors = %+v; want no authors", resolved.Metadata.Authors)
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

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}
			if resolved.Format != format.FormatPDB {
				t.Fatalf("Format = %v; want FormatPDB", resolved.Format)
			}
			if resolved.CanRead {
				t.Fatalf("CanRead = true; want false until PalmDOC reader/export exists")
			}
			if len(resolved.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized PalmDOC", resolved.Warnings)
			}
			if resolved.Metadata.Title != tt.title {
				t.Fatalf("Title = %q; want %q", resolved.Metadata.Title, tt.title)
			}
			if len(resolved.Metadata.Authors) != 0 {
				t.Fatalf("Authors = %+v; want no authors", resolved.Metadata.Authors)
			}
		})
	}
}

func TestResolveComicArchivesUseAvailableCapabilities(t *testing.T) {
	for _, tt := range []struct {
		name        string
		data        []byte
		wantFormat  format.Format
		canRead     bool
		hasCover    bool
		wantTitle   string
		wantAuthors []string
	}{
		{name: "Rar Comic.cbr", data: testRAR4Bytes(), wantFormat: format.FormatCBR, canRead: true, hasCover: true, wantTitle: "Rar Comic"},
		{name: "Seven Zip Comic.cb7", data: testfixture.CB7(), wantFormat: format.FormatCB7, canRead: true, hasCover: true, wantTitle: "CB7 Fixture", wantAuthors: []string{"Fixture Author"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.data, 0o644); err != nil {
				t.Fatalf("write source: %v", err)
			}

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}

			if resolved.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", resolved.Format, tt.wantFormat)
			}
			if resolved.CanRead != tt.canRead {
				t.Fatalf("CanRead = %v; want %v", resolved.CanRead, tt.canRead)
			}
			if (len(resolved.CoverBytes) > 0) != tt.hasCover {
				t.Fatalf("CoverBytes length = %d; has cover want %v", len(resolved.CoverBytes), tt.hasCover)
			}
			if len(resolved.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized comic archive", resolved.Warnings)
			}
			if resolved.Metadata.Title != tt.wantTitle {
				t.Fatalf("Title = %q; want %q", resolved.Metadata.Title, tt.wantTitle)
			}
			var names []string
			for _, a := range resolved.Metadata.Authors {
				names = append(names, a.Name)
			}
			if !slices.Equal(names, tt.wantAuthors) {
				t.Fatalf("Authors = %q; want %q", names, tt.wantAuthors)
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

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}

			if resolved.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", resolved.Format, tt.wantFormat)
			}
			if resolved.CanRead {
				t.Fatalf("CanRead = true; want false until text reader/export exists")
			}
			if len(resolved.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized text format", resolved.Warnings)
			}
			wantTitle := strings.TrimSuffix(tt.name, format.BookExtension(tt.name))
			if resolved.Metadata.Title != wantTitle {
				t.Fatalf("Title = %q; want %q", resolved.Metadata.Title, wantTitle)
			}
			if len(resolved.Metadata.Authors) != 0 {
				t.Fatalf("Authors = %+v; want no authors", resolved.Metadata.Authors)
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}

	if resolved.Format != format.FormatMarkdown {
		t.Fatalf("Format = %v; want Markdown", resolved.Format)
	}
	if resolved.Metadata.Title != "Alice's Adventures in Wonderland" {
		t.Fatalf("Title = %q; want markdown title", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Lewis Carroll" || resolved.Metadata.Authors[0].SortName != "Carroll, Lewis" {
		t.Fatalf("Authors = %+v; want Lewis Carroll with sort", resolved.Metadata.Authors)
	}
	if resolved.Metadata == nil || resolved.Metadata.Date != "1865" {
		t.Fatalf("Metadata = %+v; want year from markdown labels", resolved.Metadata)
	}
}

func TestResolveParsedMetadataDispatch(t *testing.T) {
	for _, tt := range []struct {
		name            string
		wantFormat      format.Format
		wantTitle       string
		wantAuthors     []string
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
			wantAuthors:    []string{"Archive Author"},
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
			wantAuthors:    []string{"HTML Author"},
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
			wantAuthors:    []string{"Doc Author"},
			wantAuthorSort: "Author, Doc",
			wantLanguage:   "en",
			wantPublisher:  "Doc Press",
			write:          writeTestDOCX,
		},
		{
			name:           "Macro Document.docm",
			wantFormat:     format.FormatDOCM,
			wantTitle:      "Document Book",
			wantAuthors:    []string{"Doc Author"},
			wantAuthorSort: "Author, Doc",
			wantLanguage:   "en",
			wantPublisher:  "Doc Press",
			write:          writeTestDOCX,
		},
		{
			name:           "ODT Book.odt",
			wantFormat:     format.FormatODT,
			wantTitle:      "ODT Book",
			wantAuthors:    []string{"ODT Author"},
			wantAuthorSort: "Author, ODT",
			wantLanguage:   "en",
			write:          writeTestODT,
		},
		{
			name:            "RTF Book.rtf",
			wantFormat:      format.FormatRTF,
			wantTitle:       "RTF Book",
			wantAuthors:     []string{"RTF Author"},
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

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}
			if resolved.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", resolved.Format, tt.wantFormat)
			}
			if resolved.CanRead {
				t.Fatalf("CanRead = true; want false for parsed metadata format")
			}
			if len(resolved.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for parsed metadata", resolved.Warnings)
			}
			if resolved.Metadata.Title != tt.wantTitle {
				t.Fatalf("Title = %q; want %q", resolved.Metadata.Title, tt.wantTitle)
			}
			var names []string
			for _, a := range resolved.Metadata.Authors {
				names = append(names, a.Name)
			}
			if !slices.Equal(names, tt.wantAuthors) {
				t.Fatalf("Authors = %q; want %q", names, tt.wantAuthors)
			}
			if tt.wantAuthorSort != "" && resolved.Metadata.Authors[0].SortName != tt.wantAuthorSort {
				t.Fatalf("Author sort = %q; want %q", resolved.Metadata.Authors[0].SortName, tt.wantAuthorSort)
			}
			if tt.wantLanguage != "" && (resolved.Metadata == nil || resolved.Metadata.Language != tt.wantLanguage) {
				t.Fatalf("Metadata = %+v; want language %q", resolved.Metadata, tt.wantLanguage)
			}
			if tt.wantPublisher != "" && (resolved.Metadata == nil || resolved.Metadata.Publisher != tt.wantPublisher) {
				t.Fatalf("Metadata = %+v; want publisher %q", resolved.Metadata, tt.wantPublisher)
			}
			if tt.wantDescription != "" && (resolved.Metadata == nil || resolved.Metadata.Description != tt.wantDescription) {
				t.Fatalf("Metadata = %+v; want description %q", resolved.Metadata, tt.wantDescription)
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Format != format.FormatRTF || resolved.Metadata.Title != "Fallback Title" {
		t.Fatalf("resolved source = format %v title %q; want RTF with filename fallback", resolved.Format, resolved.Metadata.Title)
	}
	if len(resolved.Warnings) != 1 || !strings.Contains(resolved.Warnings[0].Error(), "unsupported RTF code page 932") {
		t.Fatalf("Warnings = %+v; want unsupported code page warning", resolved.Warnings)
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

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}
			if resolved.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", resolved.Format, tt.wantFormat)
			}
			if !bytes.Equal(resolved.CoverBytes, cover) {
				t.Fatalf("CoverBytes = %d bytes; want embedded/container cover", len(resolved.CoverBytes))
			}
			if len(resolved.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for valid cover", resolved.Warnings)
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

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}
			if resolved.Format != tt.wantFormat {
				t.Fatalf("Format = %v; want %v", resolved.Format, tt.wantFormat)
			}
			if resolved.CanRead {
				t.Fatalf("CanRead = true; want false until sanitized HTML reader/export exists")
			}
			if len(resolved.Warnings) != 0 {
				t.Fatalf("Warnings = %+v; want none for recognized HTML", resolved.Warnings)
			}
			if resolved.Metadata.Title != "Saved Page" {
				t.Fatalf("Title = %q; want HTML metadata title", resolved.Metadata.Title)
			}
			if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Web Author" || resolved.Metadata.Authors[0].SortName != "Author, Web" {
				t.Fatalf("Authors = %+v; want Web Author with sort", resolved.Metadata.Authors)
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Format != format.FormatCHM {
		t.Fatalf("Format = %v; want FormatCHM", resolved.Format)
	}
	if resolved.CanRead {
		t.Fatalf("CanRead = true; want false until CHM reader/export exists")
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for recognized CHM", resolved.Warnings)
	}
	if resolved.Metadata.Title != "Technical Manual" {
		t.Fatalf("Title = %q; want filename fallback", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 0 {
		t.Fatalf("Authors = %+v; want no authors", resolved.Metadata.Authors)
	}
}

func TestResolveStructuredFilenameFallbackMetadata(t *testing.T) {
	dir := t.TempDir()
	name := "Structured Title -- Jane Writer -- 2020 -- Example Press -- isbn13 9780306406157 -- source.pdf"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("%PDF-1.4\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Format != format.FormatPDF {
		t.Fatalf("Format = %v; want FormatPDF", resolved.Format)
	}
	if resolved.Metadata.Title != "Structured Title" {
		t.Fatalf("Title = %q; want structured filename title", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Jane Writer" || resolved.Metadata.Authors[0].SortName != "Writer, Jane" {
		t.Fatalf("Authors = %+v; want Jane Writer with sort", resolved.Metadata.Authors)
	}
	if resolved.Metadata == nil || resolved.Metadata.Identifier != "isbn:9780306406157" {
		t.Fatalf("Metadata = %+v; want ISBN from structured filename", resolved.Metadata)
	}
}

func TestResolveIgnoresUnsignaledDelimitedFilename(t *testing.T) {
	dir := t.TempDir()
	name := "One -- Two -- Three -- Four.pdf"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("%PDF-1.4\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Metadata.Title != "One -- Two -- Three -- Four" {
		t.Fatalf("Title = %q; want plain filename fallback", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 0 {
		t.Fatalf("Authors = %+v; want no authors", resolved.Metadata.Authors)
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

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}
			if resolved.Format != format.FormatUnknown {
				t.Fatalf("Format = %v; want FormatUnknown", resolved.Format)
			}
			if resolved.CanRead {
				t.Fatalf("CanRead = true; want false for unrecognized format")
			}
			want := "unrecognized " + strings.ToLower(format.BookExtension(tt.name)) + " contents"
			if len(resolved.Warnings) != 1 || !strings.Contains(resolved.Warnings[0].Error(), want) {
				t.Fatalf("Warnings = %+v; want %q warning", resolved.Warnings, want)
			}
			if tt.wantTitle != "" && resolved.Metadata.Title != tt.wantTitle {
				t.Fatalf("Title = %q; want %q", resolved.Metadata.Title, tt.wantTitle)
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path, SidecarDir: dir}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if !bytes.Equal(resolved.CoverBytes, sidecarCover) {
		t.Fatalf("CoverBytes = %q; want sidecar cover", resolved.CoverBytes)
	}
}

func TestResolveFallsBackFromInvalidSidecarCover(t *testing.T) {
	for _, tc := range []struct {
		name        string
		coverName   string
		coverBytes  []byte
		oversized   bool
		wantWarning string
	}{
		{
			name:        "not-image",
			coverName:   "cover.jpg",
			coverBytes:  []byte("curated sidecar cover"),
			wantWarning: "decode image config",
		},
		{
			name:        "oversized-file",
			coverName:   "cover.jpg",
			oversized:   true,
			wantWarning: "exceeds 33554432 bytes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.name+".epub")
			embeddedCover := testCBZPNG(t, color.NRGBA{R: 12, G: 34, B: 56, A: 255})
			opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>EPUB With Invalid Sidecar Cover</dc:title>
    <dc:creator>Cover Author</dc:creator>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="cover-image" href="images/cover.png" media-type="image/png"/>
  </manifest>
</package>`)
			writeEPUBWithBinaryFiles(t, path, opf, map[string][]byte{"OEBPS/images/cover.png": embeddedCover})
			sidecarPath := filepath.Join(dir, tc.coverName)
			if err := os.WriteFile(sidecarPath, tc.coverBytes, 0o644); err != nil {
				t.Fatalf("write sidecar cover: %v", err)
			}
			if tc.oversized {
				if err := os.Truncate(sidecarPath, maxSidecarCoverBytes+1); err != nil {
					t.Fatalf("grow sidecar cover: %v", err)
				}
			}

			resolved, err := resolveTestSource(context.Background(), Source{Path: path, SidecarDir: dir}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}
			if !bytes.Equal(resolved.CoverBytes, embeddedCover) {
				t.Fatalf("CoverBytes = %d bytes; want embedded cover fallback", len(resolved.CoverBytes))
			}
			if len(resolved.Warnings) != 1 {
				t.Fatalf("Warnings = %+v; want one warning", resolved.Warnings)
			}
			warning := resolved.Warnings[0].Error()
			if !strings.Contains(warning, tc.wantWarning) {
				t.Fatalf("Warning = %v; want invalid cover warning containing %q", resolved.Warnings[0], tc.wantWarning)
			}
			if resolved.Metadata.Title != "EPUB With Invalid Sidecar Cover" {
				t.Fatalf("Title = %q; want metadata despite invalid cover", resolved.Metadata.Title)
			}
		})
	}
}

func TestResolveRecoversForbiddenEPUBOPFControl(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken-opf.epub")
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Invalid` + "\x01" + ` OPF</dc:title></metadata>
</package>`
	writeEPUB(t, path, []byte(opf))

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Format != format.FormatEPUB {
		t.Fatalf("Format = %v; want FormatEPUB", resolved.Format)
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want recovered OPF metadata without a warning", resolved.Warnings)
	}
	if resolved.Metadata.Title != "Invalid OPF" {
		t.Fatalf("Title = %q; want recovered OPF title", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 0 {
		t.Fatalf("Authors = %+v; want no authors", resolved.Metadata.Authors)
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none when complete sidecar metadata is present", resolved.Warnings)
	}
	if resolved.Metadata.Title != "Curated Sidecar Title" {
		t.Fatalf("Title = %q; want sidecar title", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Curated Author" {
		t.Fatalf("Authors = %+v; want sidecar author", resolved.Metadata.Authors)
	}
	wantAddedAt := time.Date(2013, time.February, 1, 10, 11, 12, 345678000, time.UTC)
	if !resolved.AddedAt.Equal(wantAddedAt) {
		t.Fatalf("AddedAt = %s; want calibre timestamp %s", resolved.AddedAt, wantAddedAt)
	}
	if !bytes.Equal(resolved.CoverBytes, sidecarCover) {
		t.Fatalf("CoverBytes = %q; want sidecar cover", resolved.CoverBytes)
	}
}

func TestResolveMOBIMetadataAndCover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kindle-book.mobi")
	cover := testCBZPNG(t, color.NRGBA{R: 220, G: 80, B: 40, A: 255})
	if err := os.WriteFile(path, testMOBIBytesWithMetadataAndCover(cover), 0o644); err != nil {
		t.Fatalf("write mobi: %v", err)
	}

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Format != format.FormatMOBI {
		t.Fatalf("Format = %v; want FormatMOBI", resolved.Format)
	}
	if !resolved.CanRead {
		t.Fatalf("CanRead = false; want true for MOBI foliate reader")
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for valid MOBI metadata/cover", resolved.Warnings)
	}
	if resolved.Metadata.Title != "MOBI Book" {
		t.Fatalf("Title = %q; want MOBI Book", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Jane Doe" || resolved.Metadata.Authors[0].SortName != "Doe, Jane" {
		t.Fatalf("Authors = %+v; want Jane Doe with sort", resolved.Metadata.Authors)
	}
	if !bytes.Equal(resolved.CoverBytes, cover) {
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Format != format.FormatFB2 {
		t.Fatalf("Format = %v; want FormatFB2", resolved.Format)
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for valid FB2", resolved.Warnings)
	}
	if resolved.Metadata.Title != "FB2 Book" {
		t.Fatalf("Title = %q; want FB2 Book", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Jane Author" || resolved.Metadata.Authors[0].SortName != "Author, Jane" {
		t.Fatalf("Authors = %+v; want Jane Author with sort", resolved.Metadata.Authors)
	}
	if resolved.Metadata == nil || resolved.Metadata.Identifier != "isbn:978-0-306-40615-7" {
		t.Fatalf("Identifier = %v; want isbn", resolved.Metadata)
	}
	if len(resolved.CoverBytes) == 0 {
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for invalid embedded cover", resolved.Warnings)
	}
	if resolved.Metadata.Title != "Broken Cover FB2" {
		t.Fatalf("Title = %q; want metadata despite invalid cover", resolved.Metadata.Title)
	}
	if len(resolved.CoverBytes) != 0 {
		t.Fatalf("CoverBytes = %d bytes; want no cover", len(resolved.CoverBytes))
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

			resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
			if err != nil {
				t.Fatalf("resolve source: %v", err)
			}
			if resolved.Format != format.FormatFB2 {
				t.Fatalf("Format = %v; want FormatFB2", resolved.Format)
			}
			if resolved.Extension != tt.ext {
				t.Fatalf("Extension = %q; want %q", resolved.Extension, tt.ext)
			}
			if resolved.Metadata.Title != "Zipped FB2 Book" {
				t.Fatalf("Title = %q; want Zipped FB2 Book", resolved.Metadata.Title)
			}
			if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Zip Author" || resolved.Metadata.Authors[0].SortName != "Author, Zip" {
				t.Fatalf("Authors = %+v; want Zip Author with sort", resolved.Metadata.Authors)
			}
			if len(resolved.CoverBytes) == 0 {
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

	resolved, err := resolveTestSource(context.Background(), Source{Path: path}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if resolved.Format != format.FormatCBZ {
		t.Fatalf("Format = %v; want FormatCBZ", resolved.Format)
	}
	if resolved.PageCount != 1 {
		t.Fatalf("PageCount = %d; want one image page", resolved.PageCount)
	}
	if !resolved.CanRead {
		t.Fatalf("CanRead = false; want true for CBZ foliate reader")
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("Warnings = %+v; want none for valid CBZ", resolved.Warnings)
	}
	if resolved.Metadata.Title != "Batman: The Dark Knight Returns" {
		t.Fatalf("Title = %q; want ComicInfo title", resolved.Metadata.Title)
	}
	if len(resolved.Metadata.Authors) != 1 || resolved.Metadata.Authors[0].Name != "Frank Miller" || resolved.Metadata.Authors[0].SortName != "Miller, Frank" {
		t.Fatalf("Authors = %+v; want Frank Miller with sort", resolved.Metadata.Authors)
	}
	if resolved.Metadata == nil || resolved.Metadata.Series != "Batman" || resolved.Metadata.SeriesIndex != 1 {
		t.Fatalf("Metadata = %+v; want ComicInfo series", resolved.Metadata)
	}
	if !bytes.Equal(resolved.CoverBytes, cover) {
		t.Fatalf("CoverBytes did not come from first CBZ page")
	}
}

func TestImportGroupElectsReadablePrimary(t *testing.T) {
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
	assertPrimaryEPUB := func(t *testing.T, database *db.DB, bookID int64) {
		t.Helper()
		var extension string
		var canRead int
		if err := database.Read(t.Context()).QueryRow(`
			SELECT extension, can_read
			FROM assets
			WHERE book_id = ? AND is_primary = 1
		`, bookID).Scan(&extension, &canRead); err != nil {
			t.Fatalf("query primary asset: %v", err)
		}
		if extension != ".epub" || canRead != 1 {
			t.Fatalf("primary asset = extension %q can_read %d; want readable EPUB", extension, canRead)
		}
	}

	t.Run("new grouped book", func(t *testing.T) {
		dataDir := t.TempDir()
		database, root := openTestLibrary(t, dataDir, dataDir)
		docxPath, epubPath := writeSources(t)

		result, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}, {Path: epubPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("ImportGroup: %v", err)
		}
		assertPrimaryEPUB(t, database, result.BookID)
	})

	t.Run("readable format added to existing book", func(t *testing.T) {
		dataDir := t.TempDir()
		database, root := openTestLibrary(t, dataDir, dataDir)
		docxPath, epubPath := writeSources(t)

		initial, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("initial ImportGroup: %v", err)
		}
		result, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}, {Path: epubPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("add-format ImportGroup: %v", err)
		}
		if result.BookID != initial.BookID {
			t.Fatalf("book ID = %d; want existing %d", result.BookID, initial.BookID)
		}
		assertPrimaryEPUB(t, database, result.BookID)
	})
}

func TestImportGroupDeduplicatesSourceCopies(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new book"
		if existing {
			name = "adding to existing book"
		}
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			database, root := openTestLibrary(t, dataDir, filepath.Join(dataDir, "books"))
			var sources []Source
			for i, name := range []string{"Book.txt", "Book.md", "Book - copy.md"} {
				content := "A short synthetic book.\n"
				if i > 0 {
					content = "# Book\n\nA short synthetic book.\n"
				}
				path := filepath.Join(dataDir, name)
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				sources = append(sources, Source{Path: path})
			}
			if existing {
				if _, err := Import(t.Context(), database, root, sources[0], nil, Options{}); err != nil {
					t.Fatal(err)
				}
			}
			group, err := ImportGroup(t.Context(), database, root, sources, nil, Options{})
			if err != nil {
				t.Fatalf("ImportGroup: %v", err)
			}
			if len(group.Results) != 3 || group.Results[1].Status != StatusImported || group.Results[2].Status != StatusDuplicate || group.Results[1].AssetID != group.Results[2].AssetID {
				t.Fatalf("results = %+v; want one markdown asset shared by both copies", group.Results)
			}
			for i, result := range group.Results {
				want, err := os.ReadFile(sources[i].Path)
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(root.Abs(result.StoragePath))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("managed source %d = %q, %v; want %q", i, got, err, want)
				}
			}
			again, err := ImportGroup(t.Context(), database, root, sources, nil, Options{})
			if err != nil {
				t.Fatalf("repeat ImportGroup: %v", err)
			}
			for _, result := range again.Results {
				if result.Status != StatusDuplicate || result.BookID != group.BookID {
					t.Fatalf("repeat results = %+v; want duplicates of the same book", again.Results)
				}
			}
			var books, assets int
			if err := database.Read(t.Context()).QueryRow("SELECT (SELECT COUNT(*) FROM books), (SELECT COUNT(*) FROM assets)").Scan(&books, &assets); err != nil {
				t.Fatal(err)
			}
			if books != 1 || assets != 2 {
				t.Fatalf("books/assets = %d/%d; want 1/2", books, assets)
			}
			if entries, err := os.ReadDir(root.StagingDir()); err != nil || len(entries) != 0 {
				t.Fatalf("staging = %v, %v; want empty", entries, err)
			}
		})
	}
}

func TestProbeGroupAppliesGroupRules(t *testing.T) {
	t.Run("repeated new source", func(t *testing.T) {
		dataDir := t.TempDir()
		database, _ := openTestLibrary(t, dataDir, filepath.Join(dataDir, "books"))
		dir := t.TempDir()
		first := filepath.Join(dir, "Book.txt")
		second := filepath.Join(dir, "Book.md")
		for _, path := range []string{first, second} {
			if err := os.WriteFile(path, []byte("same source bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		probe, err := ProbeGroup(t.Context(), database.Read(t.Context()), []Source{{Path: first}, {Path: second}})
		if err != nil {
			t.Fatal(err)
		}
		if len(probe) != 2 || probe[0].Duplicate || !probe[1].Duplicate {
			t.Fatalf("probe = %+v; want one import followed by one duplicate", probe)
		}
	})

	t.Run("existing sources from different books", func(t *testing.T) {
		dataDir := t.TempDir()
		database, root := openTestLibrary(t, dataDir, filepath.Join(dataDir, "books"))
		dir := t.TempDir()
		first := filepath.Join(dir, "First.txt")
		second := filepath.Join(dir, "Second.txt")
		if err := os.WriteFile(first, []byte("first source"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(second, []byte("second source"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{first, second} {
			if _, err := Import(t.Context(), database, root, Source{Path: path}, nil, Options{}); err != nil {
				t.Fatal(err)
			}
		}

		_, err := ProbeGroup(t.Context(), database.Read(t.Context()), []Source{{Path: first}, {Path: second}})
		if err == nil || !strings.Contains(err.Error(), "already belong to different books") {
			t.Fatalf("ProbeGroup error = %v; want different-books conflict", err)
		}
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
		modTimes  []time.Time
		want      time.Time
	}{
		{
			name:      "calibre timestamp wins over earlier file",
			timestamp: calibre.Format(time.RFC3339Nano),
			modTimes:  []time.Time{earlier},
			want:      calibre,
		},
		{
			name:      "invalid calibre timestamp falls back to earliest file",
			timestamp: "not-a-timestamp",
			modTimes:  []time.Time{later, earlier},
			want:      earlier,
		},
		{
			name:     "implausible file times fall back to now",
			modTimes: []time.Time{time.Unix(0, 0), now.Add(time.Hour)},
			want:     now,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addedAtForSources(tt.timestamp, tt.modTimes, now)
			if !got.Equal(tt.want) {
				t.Fatalf("addedAtForSources = %s; want %s", got, tt.want)
			}
		})
	}
}

func TestImportGroupStoresEarliestSourceModTimeAsAddedAt(t *testing.T) {
	dataDir := t.TempDir()
	database, root := openTestLibrary(t, dataDir, filepath.Join(dataDir, "books"))

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
	if err := database.Read(t.Context()).QueryRow("SELECT created_at, added_at FROM books WHERE id = ?", result.BookID).Scan(&createdAt, &addedAt); err != nil {
		t.Fatalf("query book timestamps: %v", err)
	}
	if addedAt != earlier.Unix() {
		t.Fatalf("added_at = %d; want earliest source mtime %d", addedAt, earlier.Unix())
	}
	if createdAt < before || createdAt > after {
		t.Fatalf("created_at = %d; want Polka creation time in [%d, %d]", createdAt, before, after)
	}
}

func TestImportGroupRestoresTrashedBookOnlyWhenAddingAsset(t *testing.T) {
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
	trashBook := func(t *testing.T, database *db.DB, bookID int64) {
		t.Helper()
		if _, err := database.Write(t.Context()).Exec("UPDATE books SET deleted_at = unixepoch() WHERE id = ?", bookID); err != nil {
			t.Fatalf("trash book: %v", err)
		}
	}
	assertTrashed := func(t *testing.T, database *db.DB, bookID int64, want bool) {
		t.Helper()
		var got bool
		if err := database.Read(t.Context()).QueryRow("SELECT deleted_at IS NOT NULL FROM books WHERE id = ?", bookID).Scan(&got); err != nil {
			t.Fatalf("query book trash state: %v", err)
		}
		if got != want {
			t.Fatalf("book trashed = %v; want %v", got, want)
		}
	}

	t.Run("plain duplicate stays trashed", func(t *testing.T) {
		dataDir := t.TempDir()
		database, root := openTestLibrary(t, dataDir, dataDir)
		docxPath, _ := writeSources(t)
		initial, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("initial ImportGroup: %v", err)
		}
		trashBook(t, database, initial.BookID)

		duplicate, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("duplicate ImportGroup: %v", err)
		}
		if len(duplicate.Results) != 1 || duplicate.Results[0].Status != StatusDuplicate || !duplicate.Results[0].BookTrashed {
			t.Fatalf("duplicate result = %+v; want duplicate in trashed book", duplicate.Results)
		}
		if duplicate.Restored {
			t.Fatal("plain duplicate reported a restored book")
		}
		assertTrashed(t, database, initial.BookID, true)
	})

	t.Run("new asset restores book idempotently", func(t *testing.T) {
		dataDir := t.TempDir()
		database, root := openTestLibrary(t, dataDir, dataDir)
		docxPath, epubPath := writeSources(t)
		initial, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("initial ImportGroup: %v", err)
		}
		trashBook(t, database, initial.BookID)

		mixed, err := ImportGroup(context.Background(), database, root, []Source{{Path: docxPath}, {Path: epubPath}}, nil, Options{})
		if err != nil {
			t.Fatalf("mixed ImportGroup: %v", err)
		}
		if mixed.BookID != initial.BookID {
			t.Fatalf("book ID = %d; want existing %d", mixed.BookID, initial.BookID)
		}
		if !mixed.Restored {
			t.Fatal("mixed import did not report the restored book")
		}
		if len(mixed.Results) != 2 || mixed.Results[0].Status != StatusDuplicate || mixed.Results[0].BookTrashed || mixed.Results[1].Status != StatusImported {
			t.Fatalf("mixed results = %+v; want live duplicate plus imported asset", mixed.Results)
		}
		assertTrashed(t, database, initial.BookID, false)

		var assets int
		if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM assets WHERE book_id = ?", initial.BookID).Scan(&assets); err != nil {
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
			t.Fatal("repeat import reported a restored book")
		}
		for _, result := range again.Results {
			if result.Status != StatusDuplicate || result.BookTrashed {
				t.Fatalf("repeat results = %+v; want live duplicates", again.Results)
			}
		}
		if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM assets WHERE book_id = ?", initial.BookID).Scan(&assets); err != nil {
			t.Fatalf("count repeated assets: %v", err)
		}
		if assets != 2 {
			t.Fatalf("asset count after repeat = %d; want 2", assets)
		}
	})
}

func TestPersistPreparedStoresBookMetadata(t *testing.T) {
	dataDir := t.TempDir()
	database, root := openTestLibrary(t, dataDir, filepath.Join(dataDir, "books"))

	source := []byte("metadata mapping source")
	sourcePath := filepath.Join(dataDir, "source.fb2")
	if err := os.WriteFile(sourcePath, source, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	sum := storage.Sum(source)
	info := sourceInfo{
		Source:     Source{Path: sourcePath},
		Size:       int64(len(source)),
		SourceHash: sum[:],
		Format:     format.FormatFB2,
		Extension:  ".fb2",
		CanRead:    true,
	}
	resolved := resolvedBook{
		Metadata: &bookmeta.Metadata{
			Title:     "Mapped Title",
			SortTitle: "Title, Mapped",
			Authors: []bookmeta.AuthorMeta{
				{Name: "Primary Author", SortName: "Author, Primary", Role: "aut"},
				{Name: "Second Author", SortName: "Author, Second", Role: "trl"},
			},
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
	}

	result, err := persistPrepared(context.Background(), database, root, stageTestSource(t, root, info), resolved, Options{})
	if err != nil {
		t.Fatalf("persistPrepared: %v", err)
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
	if err := database.Read(t.Context()).QueryRow(`
		SELECT title, sort_title, series, series_index, description, tags,
		       cover_version, publisher, published_date,
		       language, identifiers
		FROM books
		WHERE id = ?
	`, result.BookID).Scan(
		&got.title, &got.sortTitle, &got.series, &got.seriesIndex,
		&got.description, &got.tags, &got.coverVersion, &got.publisher,
		&got.date, &got.language, &got.identifiers,
	); err != nil {
		t.Fatalf("query book metadata: %v", err)
	}
	want := storedMetadata{
		title:        resolved.Metadata.Title,
		sortTitle:    resolved.Metadata.SortTitle,
		series:       resolved.Metadata.Series,
		seriesIndex:  resolved.Metadata.SeriesIndex,
		description:  resolved.Metadata.Description,
		tags:         strings.Join(resolved.Metadata.Tags, ", "),
		coverVersion: 1,
		publisher:    resolved.Metadata.Publisher,
		date:         resolved.Metadata.Date,
		language:     "pt-BR",
		identifiers:  resolved.Metadata.Identifier,
	}
	if got != want {
		t.Fatalf("stored book metadata = %+v; want %+v", got, want)
	}

	rows, err := database.Read(t.Context()).Query(`
		SELECT a.name, a.sort_name, COALESCE(ba.role, '')
		FROM book_authors ba
		JOIN authors a ON a.id = ba.author_id
		WHERE ba.book_id = ?
		ORDER BY ba.author_order
	`, result.BookID)
	if err != nil {
		t.Fatalf("query book authors: %v", err)
	}
	defer rows.Close()
	var authors []bookmeta.AuthorMeta
	for rows.Next() {
		var author bookmeta.AuthorMeta
		if err := rows.Scan(&author.Name, &author.SortName, &author.Role); err != nil {
			t.Fatalf("scan book author: %v", err)
		}
		authors = append(authors, author)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("book authors: %v", err)
	}
	if !slices.Equal(authors, resolved.Metadata.Authors) {
		t.Fatalf("stored book authors = %+v; want %+v", authors, resolved.Metadata.Authors)
	}
}

func TestPersistPreparedStoresAssetHashesAndCover(t *testing.T) {
	dataDir := t.TempDir()
	database, root := openTestLibrary(t, dataDir, dataDir)

	srcBytes := []byte("plain opaque source")
	srcPath := filepath.Join(dataDir, "source.fb2")
	if err := os.WriteFile(srcPath, srcBytes, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	resolved, err := resolveTestSource(context.Background(), Source{Path: srcPath}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	resolved.CoverBytes = []byte("cover-bytes")

	res, err := persistPrepared(context.Background(), database, root, stageTestSource(t, root, resolved.sourceInfo), resolved.resolvedBook, Options{})
	if err != nil {
		t.Fatalf("persistPrepared: %v", err)
	}
	if res.Status != StatusImported {
		t.Fatalf("Status = %q; want imported", res.Status)
	}

	sum := storage.Sum(srcBytes)
	wantHash := sum[:]
	var originalHash, currentHash []byte
	var storagePath string
	var formatKey string
	var originalSize, currentSize int64
	var isPrimary, canRead int
	if err := database.Read(t.Context()).QueryRow(`
			SELECT original_hash, current_hash, original_size, current_size, storage_path, format, is_primary, can_read
			FROM assets
			WHERE id = ?
		`, res.AssetID).Scan(&originalHash, &currentHash, &originalSize, &currentSize, &storagePath, &formatKey, &isPrimary, &canRead); err != nil {
		t.Fatalf("query asset: %v", err)
	}
	if !bytes.Equal(originalHash, wantHash) || !bytes.Equal(currentHash, wantHash) {
		t.Fatalf("hashes = original %x current %x; want %x", originalHash, currentHash, wantHash)
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
	if formatKey != format.FormatKey(resolved.Format) {
		t.Fatalf("format = %q; want %q", formatKey, format.FormatKey(resolved.Format))
	}
	var primaryAuthorSort string
	if err := database.Read(t.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = ?", res.BookID).Scan(&primaryAuthorSort); err != nil {
		t.Fatalf("query primary_author_sort: %v", err)
	}
	wantAuthorSort := ""
	if len(resolved.Metadata.Authors) > 0 {
		wantAuthorSort = resolved.Metadata.Authors[0].SortName
	}
	if primaryAuthorSort != wantAuthorSort {
		t.Fatalf("primary_author_sort = %q; want %q", primaryAuthorSort, wantAuthorSort)
	}
	if _, err := os.Stat(filepath.Join(dataDir, storagePath)); err != nil {
		t.Fatalf("stored asset missing: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dataDir, covers.OriginalPath(res.BookID))); err != nil {
		t.Fatalf("stored cover missing: %v", err)
	} else if string(got) != "cover-bytes" {
		t.Fatalf("stored cover = %q; want cover-bytes", got)
	}

	dup, err := Import(context.Background(), database, root, Source{Path: srcPath}, nil, Options{})
	if err != nil {
		t.Fatalf("duplicate Import: %v", err)
	}
	if dup.Status != StatusDuplicate || dup.AssetID != res.AssetID || dup.BookID != res.BookID {
		t.Fatalf("duplicate result = %+v; want existing asset/book", dup)
	}
}

func TestPersistPreparedCanceledContextRollsBackAndCleansStaging(t *testing.T) {
	dataDir := t.TempDir()
	database, root := openTestLibrary(t, dataDir, filepath.Join(dataDir, "books"))
	srcPath := filepath.Join(dataDir, "source.fb2")
	if err := os.WriteFile(srcPath, []byte("opaque source"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	resolved, err := resolveTestSource(context.Background(), Source{Path: srcPath}, nil)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	prepared := stageTestSource(t, root, resolved.sourceInfo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := persistPrepared(ctx, database, root, prepared, resolved.resolvedBook, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("persistPrepared error = %v; want context.Canceled", err)
	}

	var books, assets int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM books").Scan(&books); err != nil {
		t.Fatalf("count books: %v", err)
	}
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if books != 0 || assets != 0 {
		t.Fatalf("canceled persist left books/assets = %d/%d; want 0/0", books, assets)
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
		"import": func() error {
			_, err := Import(ctx, database, storage.Root{}, Source{Path: srcPath}, nil, Options{})
			return err
		},
		"probe": func() error {
			_, err := ProbeSource(ctx, database.Read(ctx), Source{Path: srcPath})
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
	database, root := openTestLibrary(t, dataDir, dataDir)

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
	originalSum := storage.Sum(originalBytes)
	originalHash := originalSum[:]
	opts := Options{KnownAssetSizes: make(map[int64]struct{})}

	res, err := Import(context.Background(), database, root, Source{Path: srcPath}, nil, opts)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Status != StatusImported {
		t.Fatalf("Status = %q; want imported", res.Status)
	}
	if _, found := opts.KnownAssetSizes[int64(len(originalBytes))]; !found {
		t.Fatal("committed asset size was not added to batch hints")
	}

	managedPath := filepath.Join(dataDir, res.StoragePath)
	rewrittenBytes := []byte("future write-back bytes")
	rewrittenSum := storage.Sum(rewrittenBytes)
	rewrittenHash := rewrittenSum[:]
	if err := os.WriteFile(managedPath, rewrittenBytes, 0o644); err != nil {
		t.Fatalf("rewrite managed file: %v", err)
	}
	if _, err := database.Write(t.Context()).Exec(`
		UPDATE assets
		SET current_hash = ?, current_size = ?, koreader_hash = 'stale-rewritten-hash', updated_at = unixepoch()
		WHERE id = ?
	`, rewrittenHash, len(rewrittenBytes), res.AssetID); err != nil {
		t.Fatalf("mark rewritten current hash: %v", err)
	}
	unchanged, err := Import(context.Background(), database, root, Source{Path: srcPath}, nil, opts)
	if err != nil {
		t.Fatalf("duplicate Import after writeback: %v", err)
	}
	if unchanged.Status != StatusDuplicate || unchanged.AssetID != res.AssetID {
		t.Fatalf("duplicate after writeback = %+v; want existing asset", unchanged)
	}
	if err := os.Remove(managedPath); err != nil {
		t.Fatalf("remove managed file: %v", err)
	}

	dup, err := Import(context.Background(), database, root, Source{Path: srcPath}, nil, opts)
	if err != nil {
		t.Fatalf("duplicate Import: %v", err)
	}
	if dup.Status != StatusDuplicate || dup.AssetID != res.AssetID || dup.BookID != res.BookID {
		t.Fatalf("duplicate result = %+v; want existing asset/book", dup)
	}
	if got, err := os.ReadFile(managedPath); err != nil {
		t.Fatalf("restored file missing: %v", err)
	} else if !bytes.Equal(got, originalBytes) {
		t.Fatalf("restored bytes = %q; want original source bytes", got)
	}

	var originalDBHash, currentDBHash []byte
	var koReaderHash string
	var originalDBSize, currentDBSize int64
	if err := database.Read(t.Context()).QueryRow(`
		SELECT original_hash, current_hash, original_size, current_size,
		       COALESCE(koreader_hash, '')
		FROM assets
		WHERE id = ?
	`, res.AssetID).Scan(&originalDBHash, &currentDBHash, &originalDBSize, &currentDBSize, &koReaderHash); err != nil {
		t.Fatalf("query hashes: %v", err)
	}
	if !bytes.Equal(originalDBHash, originalHash) || !bytes.Equal(currentDBHash, originalHash) {
		t.Fatalf("hashes = original %x current %x; want %x", originalDBHash, currentDBHash, originalHash)
	}
	if originalDBSize != int64(len(originalBytes)) || currentDBSize != int64(len(originalBytes)) {
		t.Fatalf("sizes = original %d current %d; want %d", originalDBSize, currentDBSize, len(originalBytes))
	}
	if koReaderHash != "" {
		t.Fatalf("restored koreader hash = %q; want empty lazy identity", koReaderHash)
	}
}

func TestKnownAssetSizeSkipsStagingOnlyForDuplicate(t *testing.T) {
	dataDir := t.TempDir()
	database, root := openTestLibrary(t, dataDir, dataDir)

	firstPath := filepath.Join(dataDir, "first.txt")
	differentPath := filepath.Join(dataDir, "different.txt")
	firstBytes := []byte("first book\n")
	differentBytes := []byte("other book\n")
	if len(firstBytes) != len(differentBytes) {
		t.Fatal("test sources must have equal sizes")
	}
	if err := os.WriteFile(firstPath, firstBytes, 0o644); err != nil {
		t.Fatalf("write first source: %v", err)
	}

	opts := Options{KnownAssetSizes: make(map[int64]struct{})}
	first, err := Import(t.Context(), database, root, Source{Path: firstPath}, nil, opts)
	if err != nil || first.Status != StatusImported {
		t.Fatalf("first Import = %+v, %v; want imported", first, err)
	}

	if err := os.RemoveAll(root.StagingDir()); err != nil {
		t.Fatalf("remove staging directory: %v", err)
	}
	if err := os.WriteFile(root.StagingDir(), []byte("blocked"), 0o644); err != nil {
		t.Fatalf("block staging path: %v", err)
	}
	duplicate, err := Import(t.Context(), database, root, Source{Path: firstPath}, nil, opts)
	if err != nil || duplicate.Status != StatusDuplicate {
		t.Fatalf("duplicate Import with blocked staging = %+v, %v; want duplicate", duplicate, err)
	}
	if err := os.Remove(root.StagingDir()); err != nil {
		t.Fatalf("unblock staging path: %v", err)
	}
	if err := os.WriteFile(differentPath, differentBytes, 0o644); err != nil {
		t.Fatalf("write different source: %v", err)
	}

	different, err := Import(t.Context(), database, root, Source{Path: differentPath}, nil, opts)
	if err != nil || different.Status != StatusImported {
		t.Fatalf("same-size different Import = %+v, %v; want imported", different, err)
	}
}
