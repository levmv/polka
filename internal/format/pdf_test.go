package format

import (
	"bytes"
	"compress/zlib"
	"io"
	"reflect"
	"strconv"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestExtractPDFMetadataInfoStrings(t *testing.T) {
	utf16Literal := []byte("<< /Title (")
	utf16Literal = append(utf16Literal, pdfUTF16BELiteral("The ebook is 40 (1971-2011)")...)
	utf16Literal = append(utf16Literal, []byte(") /Author (")...)
	utf16Literal = append(utf16Literal, pdfUTF16BELiteral("marie lebert")...)
	utf16Literal = append(utf16Literal, []byte(") >>")...)

	pdfDocEncoding := []byte("<< /Title (Caf")
	pdfDocEncoding = append(pdfDocEncoding, 0xe9, ' ', 0x80, ' ', 0x90, ' ', 0xa0)
	pdfDocEncoding = append(pdfDocEncoding, []byte(") /Author <486578208D417574686F728E> >>")...)

	utf8BOM := append([]byte("<< /Title ("), 0xef, 0xbb, 0xbf)
	utf8BOM = append(utf8BOM, []byte("UTF-8 Caf\xc3\xa9) >>")...)

	utf16Hex := []byte("<< /Title <")
	utf16Hex = append(utf16Hex, pdfUTF16BEHex("Hex UTF16 Title")...)
	utf16Hex = append(utf16Hex, []byte("> /Author <")...)
	utf16Hex = append(utf16Hex, pdfUTF16BEHex("Unicode Author")...)
	utf16Hex = append(utf16Hex, []byte("> >>")...)

	for _, tt := range []struct {
		name       string
		input      []byte
		wantTitle  string
		wantAuthor string
	}{
		{
			name: "plain literal",
			input: []byte(`%PDF-1.7
1 0 obj
<< /Title (Plain Title) /Author (Jane Author) >>
endobj`),
			wantTitle:  "Plain Title",
			wantAuthor: "Jane Author",
		},
		{
			name: "escaped parentheses",
			input: []byte(`<<
	/Title (A \(small\) Book)
	/Author (Jane \) Doe)
>>`),
			wantTitle:  "A (small) Book",
			wantAuthor: "Jane ) Doe",
		},
		{
			name:       "nested literal and escapes",
			input:      []byte("<< /TitleSort (Ignored) /Title(Compact (Nested) Title) /Author (A\\040B\\\nC) >>"),
			wantTitle:  "Compact (Nested) Title",
			wantAuthor: "A BC",
		},
		{
			name:       "UTF-16 literal",
			input:      utf16Literal,
			wantTitle:  "The ebook is 40 (1971-2011)",
			wantAuthor: "marie lebert",
		},
		{
			name:       "PDFDocEncoding",
			input:      pdfDocEncoding,
			wantTitle:  "Caf\u00e9 \u2022 \u2019 \u20ac",
			wantAuthor: "Hex \u201cAuthor\u201d",
		},
		{
			name:      "UTF-8 BOM",
			input:     utf8BOM,
			wantTitle: "UTF-8 Caf\u00e9",
		},
		{
			name:       "ASCII hex",
			input:      []byte("<< /Title <50444620 486578205469746c65> /Author <48657820417574686f72> >>"),
			wantTitle:  "PDF Hex Title",
			wantAuthor: "Hex Author",
		},
		{
			name:       "UTF-16 hex",
			input:      utf16Hex,
			wantTitle:  "Hex UTF16 Title",
			wantAuthor: "Unicode Author",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			meta := testExtractPDFMetadata(t, tt.input)
			if meta.Title != tt.wantTitle {
				t.Fatalf("Title = %q; want %q", meta.Title, tt.wantTitle)
			}
			if !utf8.ValidString(meta.Title) {
				t.Fatalf("Title is not valid UTF-8: %q", meta.Title)
			}
			if tt.wantAuthor == "" {
				if len(meta.Authors) != 0 {
					t.Fatalf("Authors = %+v; want none", meta.Authors)
				}
			} else if len(meta.Authors) != 1 || meta.Authors[0].Name != tt.wantAuthor {
				t.Fatalf("Authors = %+v; want %q", meta.Authors, tt.wantAuthor)
			}
		})
	}
}

func TestExtractPDFMetadataPrefersInfoObjectOverOutlineTitle(t *testing.T) {
	input := []byte(`2 0 obj
<< /Title (Document Title) /Author (Document Author) >>
endobj
3 0 obj
<< /Count 0 /Title <`)
	input = append(input, pdfUTF16BEHex("Introduction")...)
	input = append(input, []byte(`> >>
endobj
trailer
<< /Size 4 /Info 2 0 R >>
startxref
0
%%EOF`)...)

	meta := testExtractPDFMetadata(t, input)

	if meta.Title != "Document Title" {
		t.Fatalf("Title = %q; want Info object title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Document Author" {
		t.Fatalf("Authors = %+v; want Info object author", meta.Authors)
	}
}

func TestExtractPDFMetadataIgnoresEncryptedInfo(t *testing.T) {
	const info = `%PDF-1.7
2 0 obj
<< /Title (Ciphertext Title) /Author (Ciphertext Author) >>
endobj
5 0 obj
<< /Filter /Standard /V 2 >>
endobj
`
	for _, tt := range []struct {
		name string
		xref string
	}{
		{
			name: "classic trailer",
			xref: `trailer
<< /Size 6 /Info 2 0 R /Encrypt 5 0 R >>`,
		},
		{
			name: "cross-reference stream",
			xref: `6 0 obj
<< /Size 7 /Info 2 0 R /Encrypt 5 0 R /Type /XRef /Length 0 >>
stream
endstream
endobj`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			meta := testExtractPDFMetadata(t, []byte(info+tt.xref+`
startxref
0
%%EOF`))
			if meta.Title != "" {
				t.Fatalf("Title = %q; want encrypted Info ignored", meta.Title)
			}
			if len(meta.Authors) != 0 {
				t.Fatalf("Authors = %+v; want encrypted Info ignored", meta.Authors)
			}
		})
	}
}

func TestExtractPDFMetadataXMPFallback(t *testing.T) {
	meta := testExtractPDFMetadata(t, []byte(`%PDF-1.4
4 0 obj
<< /Type /Metadata /Subtype /XML /Length 1 >>
stream
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/">
      <dc:title><rdf:Alt><rdf:li xml:lang="x-default">PDF XMP Only</rdf:li></rdf:Alt></dc:title>
      <dc:creator><rdf:Seq><rdf:li>XMP Author</rdf:li></rdf:Seq></dc:creator>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
endstream
endobj`))

	if meta.Title != "PDF XMP Only" {
		t.Fatalf("Title = %q; want XMP title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "XMP Author" {
		t.Fatalf("Authors = %+v; want XMP Author", meta.Authors)
	}
}

func TestExtractPDFMetadataInfoBeatsXMP(t *testing.T) {
	meta := testExtractPDFMetadata(t, []byte(`%PDF-1.4
2 0 obj
<< /Title (Info Title) /Author (Info Author) >>
endobj
4 0 obj
<< /Type /Metadata /Subtype /XML /Length 1 >>
stream
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/">
      <dc:title><rdf:Alt><rdf:li xml:lang="x-default">XMP Title</rdf:li></rdf:Alt></dc:title>
      <dc:creator><rdf:Seq><rdf:li>XMP Author</rdf:li></rdf:Seq></dc:creator>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
endstream
endobj
trailer
<< /Size 5 /Info 2 0 R >>`))

	if meta.Title != "Info Title" {
		t.Fatalf("Title = %q; want Info title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Info Author" {
		t.Fatalf("Authors = %+v; want Info Author", meta.Authors)
	}
}

func TestExtractPDFMetadataFlateXMPFallback(t *testing.T) {
	xmp := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/">
      <dc:title><rdf:Alt><rdf:li xml:lang="x-default">PDF XMP Flate</rdf:li></rdf:Alt></dc:title>
      <dc:creator><rdf:Seq><rdf:li>Flate Author</rdf:li></rdf:Seq></dc:creator>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>`)
	compressed := zlibCompressPDFTestData(xmp)

	input := []byte(`%PDF-1.4
4 0 obj
<< /Type /Metadata /Subtype /XML /Filter /FlateDecode /Length ` + strconv.Itoa(len(compressed)) + ` >>
stream
`)
	input = append(input, compressed...)
	input = append(input, []byte(`
endstream
endobj`)...)

	meta := testExtractPDFMetadata(t, input)

	if meta.Title != "PDF XMP Flate" {
		t.Fatalf("Title = %q; want Flate XMP title", meta.Title)
	}
	if len(meta.Authors) != 1 || meta.Authors[0].Name != "Flate Author" {
		t.Fatalf("Authors = %+v; want Flate Author", meta.Authors)
	}
}

func TestExtractPDFMetadataKeepsSourceReadsBounded(t *testing.T) {
	const sourceSize = int64(32 << 20)
	reader := &sparsePDFReader{
		size: sourceSize,
		segments: []sparsePDFSegment{
			{
				offset: 1024,
				data:   []byte("\n2 0 obj\n<< /Title (Bounded PDF) /Author (Range Reader) >>\nendobj\n"),
			},
			{
				offset: sourceSize - 128,
				data:   []byte("trailer\n<< /Size 3 /Info 2 0 R >>\nstartxref\n0\n%%EOF"),
			},
		},
	}

	meta, err := ExtractMetadata(reader, sourceSize, FormatPDF)
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
	}
	if meta.Title != "Bounded PDF" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Range Reader" {
		t.Fatalf("metadata = %+v; want bounded Info title and author", meta)
	}
	if reader.maxRead > maxPDFInfoObjectBytes {
		t.Fatalf("largest source read = %d bytes; want at most %d", reader.maxRead, maxPDFInfoObjectBytes)
	}
}

type sparsePDFSegment struct {
	offset int64
	data   []byte
}

type sparsePDFReader struct {
	size     int64
	segments []sparsePDFSegment
	maxRead  int
}

func (r *sparsePDFReader) ReadAt(p []byte, offset int64) (int, error) {
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	if offset >= r.size {
		return 0, io.EOF
	}
	n := len(p)
	if remaining := r.size - offset; int64(n) > remaining {
		n = int(remaining)
	}
	for i := range p[:n] {
		p[i] = ' '
	}
	for _, segment := range r.segments {
		start := max(offset, segment.offset)
		end := min(offset+int64(n), segment.offset+int64(len(segment.data)))
		if start >= end {
			continue
		}
		copy(p[start-offset:end-offset], segment.data[start-segment.offset:end-segment.offset])
	}
	if n != len(p) {
		return n, io.EOF
	}
	return n, nil
}

func pdfUTF16BELiteral(s string) []byte {
	out := []byte{0xFE, 0xFF}
	for _, unit := range utf16.Encode([]rune(s)) {
		out = append(out, byte(unit>>8), byte(unit))
	}
	return out
}

func pdfUTF16BEHex(s string) []byte {
	const digits = "0123456789ABCDEF"
	raw := pdfUTF16BELiteral(s)
	out := make([]byte, 0, len(raw)*2)
	for _, b := range raw {
		out = append(out, digits[b>>4], digits[b&0x0F])
	}
	return out
}

func zlibCompressPDFTestData(data []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func testExtractPDFMetadata(t *testing.T, input []byte) *Metadata {
	t.Helper()
	want := ExtractPDFMetadata(input)
	got := ExtractPDFMetadataReader(bytes.NewReader(input), int64(len(input)))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("seekable metadata = %+v; byte metadata = %+v", got, want)
	}
	return got
}
