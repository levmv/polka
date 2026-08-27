package format

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"golang.org/x/image/bmp"
)

func TestDetectFormatPalmDOC(t *testing.T) {
	data := testPalmDOCFile("Libmobi test sample")
	for _, name := range []string{"book.pdb", "book.mobi", "book.prc"} {
		t.Run(name, func(t *testing.T) {
			r := bytes.NewReader(data)
			if got := DetectFormat(name, r, r.Size()); got != FormatPDB {
				t.Fatalf("DetectFormat = %v; want FormatPDB", got)
			}
		})
	}
}

func TestDetectFormatPDBRejectsNonPalmDOC(t *testing.T) {
	encrypted := testPalmDOCRecord0(2)
	binary.BigEndian.PutUint16(encrypted[12:14], 1)
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "not a palm database", data: []byte("not a pdb")},
		{name: "wrong type creator", data: testPalmDBFile("Calendar", "DATAAPP1", testPalmDOCRecord0(2))},
		{name: "bad compression", data: testPalmDBFile("Broken", "TEXtREAd", testPalmDOCRecord0(5))},
		{name: "encrypted", data: testPalmDBFile("Encrypted", "TEXtREAd", encrypted)},
		{name: "wrong extension", data: testPalmDOCFile("Palm Doc")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			filename := "book.pdb"
			if tt.name == "wrong extension" {
				filename = "book.txt"
			}
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(filename, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}

func TestExtractPDBMetadata(t *testing.T) {
	data := testPalmDOCFile("Lem Stanislaw - Solaris")
	r := bytes.NewReader(data)
	meta, err := ExtractPDBMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractPDBMetadata: %v", err)
	}
	if meta.Title != "Lem Stanislaw - Solaris" {
		t.Fatalf("Title = %q; want Palm database name", meta.Title)
	}
}

func TestExtractPDBMetadataUsesEmbeddedOEBBlock(t *testing.T) {
	text := []byte(`<HTML><HEAD><metadata><dc-metadata xmlns:dc="http://purl.org/metadata/dublin_core">
<dc:Title>Embedded Palm Title</dc:Title><dc:Creator>Ada Writer</dc:Creator>
<dc:Language>en-us</dc:Language><dc:Publisher>Palm House</dc:Publisher>
</dc-metadata></metadata></HEAD><BODY><p>Book text.</p></BODY></HTML>`)
	data := testPalmDOCFileWithText("Database Fallback", mobiCompressionPalmDOC, [][]byte{text}, uint32(len(text)))
	r := bytes.NewReader(data)

	meta, err := ExtractPDBMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractPDBMetadata: %v", err)
	}
	if meta.Title != "Embedded Palm Title" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Ada Writer" {
		t.Fatalf("embedded title/authors = %q / %+v", meta.Title, meta.Authors)
	}
	if meta.Language != "en-US" || meta.Publisher != "Palm House" {
		t.Fatalf("embedded language/publisher = %q / %q", meta.Language, meta.Publisher)
	}
}

func testPalmDOCFile(title string) []byte {
	return testPalmDBFile(title, "TEXtREAd", testPalmDOCRecord0(2))
}

func testPalmDOCFileWithText(title string, compression uint16, textRecords [][]byte, textLength uint32) []byte {
	return testPalmDOCFileWithTextAndResources(title, compression, textRecords, textLength, nil)
}

func testPalmDOCFileWithTextAndResources(title string, compression uint16, textRecords [][]byte, textLength uint32, resources [][]byte) []byte {
	record0 := testPalmDOCRecord0(compression)
	binary.BigEndian.PutUint32(record0[4:8], textLength)
	binary.BigEndian.PutUint16(record0[8:10], uint16(len(textRecords)))
	records := append([][]byte{record0}, textRecords...)
	records = append(records, resources...)
	return testPalmDBFileWithRecords(title, "TEXtREAd", records)
}

func testPalmDOCBMP(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	img.Set(1, 0, color.NRGBA{B: 0xff, A: 0xff})
	var out bytes.Buffer
	if err := bmp.Encode(&out, img); err != nil {
		t.Fatalf("encode PalmDOC BMP: %v", err)
	}
	return out.Bytes()
}

func testPalmDOCRecord0(compression uint16) []byte {
	record0 := make([]byte, palmDOCHeader)
	binary.BigEndian.PutUint16(record0[0:2], compression)
	binary.BigEndian.PutUint32(record0[4:8], 1024)
	binary.BigEndian.PutUint16(record0[8:10], 1)
	binary.BigEndian.PutUint16(record0[10:12], 4096)
	return record0
}

func testPalmDBFile(name, typeCreator string, record0 []byte) []byte {
	return testPalmDBFileWithRecords(name, typeCreator, [][]byte{record0})
}

func testPalmDBFileWithRecords(name, typeCreator string, records [][]byte) []byte {
	tableSize := len(records) * palmDBRecordSize
	data := make([]byte, palmDBHeaderSize+tableSize)
	copy(data[:palmDBNameBytes], []byte(name))
	copy(data[60:68], []byte(typeCreator))
	binary.BigEndian.PutUint16(data[76:78], uint16(len(records)))
	offset := palmDBHeaderSize + tableSize
	for i, record := range records {
		binary.BigEndian.PutUint32(data[palmDBHeaderSize+i*palmDBRecordSize:palmDBHeaderSize+i*palmDBRecordSize+4], uint32(offset))
		offset += len(record)
	}
	for _, record := range records {
		data = append(data, record...)
	}
	return data
}
