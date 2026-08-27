package format

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"maps"
	"testing"
)

func TestDetectFormatDOCXFamily(t *testing.T) {
	for _, tt := range []struct {
		name string
		want Format
	}{
		{name: "book.docx", want: FormatDOCX},
		{name: "book.docm", want: FormatDOCM},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := testDOCXZip(t, docxFixture{})
			r := bytes.NewReader(data)
			if got := DetectFormat(tt.name, r, r.Size()); got != tt.want {
				t.Fatalf("DetectFormat = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFormatDOCXRejectsExtensionOnlyFiles(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries map[string][]byte
	}{
		{
			name: "plain bytes",
			entries: map[string][]byte{
				"book.txt": []byte("not a docx"),
			},
		},
		{
			name: "presentation ooxml",
			entries: map[string][]byte{
				"[Content_Types].xml": []byte(`<Types/>`),
				"_rels/.rels": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`),
				"ppt/presentation.xml": []byte(`<p:presentation/>`),
			},
		},
		{
			name: "missing content types",
			entries: map[string][]byte{
				"_rels/.rels":       []byte(docxRelsXML()),
				"word/document.xml": []byte(`<w:document/>`),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := writeTestZip(t, tt.entries)
			r := bytes.NewReader(data)
			if got := DetectFormat("book.docx", r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}

func TestDetectFormatDOCXAcceptsAlternateMainDocumentPart(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"[Content_Types].xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/main.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`),
		"_rels/.rels": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/main.xml"/>
</Relationships>`),
		"word/main.xml": []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`),
	})
	r := bytes.NewReader(data)
	if got := DetectFormat("book.docx", r, r.Size()); got != FormatDOCX {
		t.Fatalf("DetectFormat = %v; want FormatDOCX", got)
	}
}

func TestExtractDOCXMetadata(t *testing.T) {
	data := testDOCXZip(t, docxFixture{
		core: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
  xmlns:dc="http://purl.org/dc/elements/1.1/"
  xmlns:dcterms="http://purl.org/dc/terms/">
  <dc:title>Document Book</dc:title>
  <dc:creator>Ada Lovelace; Charles Babbage</dc:creator>
  <dc:subject>Computing, History</dc:subject>
  <dc:description>Notes_x000d_
with line break.</dc:description>
  <cp:keywords>Math; Engines</cp:keywords>
  <dc:language>en-US</dc:language>
  <dcterms:created>1843-01-01T00:00:00Z</dcterms:created>
</cp:coreProperties>`,
		app: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties">
  <Company>Analytical Press</Company>
</Properties>`,
	})
	r := bytes.NewReader(data)
	meta, err := ExtractDOCXMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDOCXMetadata: %v", err)
	}
	if meta.Title != "Document Book" {
		t.Fatalf("Title = %q; want Document Book", meta.Title)
	}
	if len(meta.Authors) != 2 || meta.Authors[0].Name != "Ada Lovelace" || meta.Authors[1].Name != "Charles Babbage" {
		t.Fatalf("Authors = %+v; want Ada Lovelace and Charles Babbage", meta.Authors)
	}
	if meta.Description != "Notes\nwith line break." {
		t.Fatalf("Description = %q; want Word newline marker stripped", meta.Description)
	}
	if meta.Language != "en-US" {
		t.Fatalf("Language = %q; want en-US", meta.Language)
	}
	if meta.Date != "1843-01-01" {
		t.Fatalf("Date = %q; want 1843-01-01", meta.Date)
	}
	if meta.Publisher != "Analytical Press" {
		t.Fatalf("Publisher = %q; want Analytical Press", meta.Publisher)
	}
	wantTags := []string{"Computing", "History", "Math", "Engines"}
	if !equalStrings(meta.Tags, wantTags) {
		t.Fatalf("Tags = %+v; want %+v", meta.Tags, wantTags)
	}
}

func TestExtractDOCXMetadataFallsBackToStyleLanguage(t *testing.T) {
	data := testDOCXZip(t, docxFixture{
		core: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
  xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:title>Style Language</dc:title>
</cp:coreProperties>`,
		styles: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:docDefaults><w:rPrDefault><w:rPr><w:lang w:val="fr-FR"/></w:rPr></w:rPrDefault></w:docDefaults>
</w:styles>`,
	})
	r := bytes.NewReader(data)
	meta, err := ExtractDOCXMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDOCXMetadata: %v", err)
	}
	if meta.Language != "fr-FR" {
		t.Fatalf("Language = %q; want fr-FR from styles.xml", meta.Language)
	}
}

func TestExtractDOCXMetadataFollowsRelocatedStrictRelationships(t *testing.T) {
	data := writeTestZip(t, map[string][]byte{
		"[Content_Types].xml": []byte(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`),
		"_rels/.rels": []byte(`<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="externalCore" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="https://example.invalid/core.xml" TargetMode="External"/>
  <Relationship Id="main" Type="http://purl.oclc.org/ooxml/officeDocument/relationships/officeDocument" Target="/word/main.xml"/>
  <Relationship Id="core" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="metadata/book-core.xml"/>
  <Relationship Id="app" Type="http://purl.oclc.org/ooxml/officeDocument/relationships/extended-properties" Target="metadata/book-app.xml"/>
</Relationships>`),
		"word/main.xml": []byte(`<w:document xmlns:w="http://purl.oclc.org/ooxml/wordprocessingml/main"><w:body/></w:document>`),
		"word/_rels/main.xml.rels": []byte(`<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="externalStyles" Type="http://purl.oclc.org/ooxml/officeDocument/relationships/styles" Target="https://example.invalid/styles.xml" TargetMode="External"/>
  <Relationship Id="styles" Type="http://purl.oclc.org/ooxml/officeDocument/relationships/styles" Target="../metadata/book-styles.xml"/>
</Relationships>`),
		"metadata/book-core.xml": []byte(`<?xml version="1.0"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:title>Relationship Title</dc:title>
</cp:coreProperties>`),
		"metadata/book-app.xml": []byte(`<?xml version="1.0"?>
<Properties xmlns="http://purl.oclc.org/ooxml/officeDocument/extended-properties">
  <Company>Relationship Press</Company>
</Properties>`),
		"metadata/book-styles.xml": []byte(`<?xml version="1.0"?>
<w:styles xmlns:w="http://purl.oclc.org/ooxml/wordprocessingml/main">
  <w:docDefaults><w:rPrDefault><w:rPr><w:lang w:val="de-DE"/></w:rPr></w:rPrDefault></w:docDefaults>
</w:styles>`),
		"docProps/core.xml": []byte(`<coreProperties><title>Conventional Decoy</title></coreProperties>`),
		"docProps/app.xml":  []byte(`<Properties><Company>Decoy Press</Company></Properties>`),
		"word/styles.xml":   []byte(`<styles><lang val="fr-FR"/></styles>`),
	})

	r := bytes.NewReader(data)
	meta, err := ExtractDOCXMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDOCXMetadata: %v", err)
	}
	if meta.Title != "Relationship Title" || meta.Publisher != "Relationship Press" || meta.Language != "de-DE" {
		t.Fatalf("relationship-selected metadata = %+v", meta)
	}
}

func TestExtractDOCXCover(t *testing.T) {
	icon := testSizedPNG(t, 50, 50, color.NRGBA{R: 200, G: 20, B: 20, A: 255})
	cover := testSizedPNG(t, 400, 600, color.NRGBA{R: 20, G: 80, B: 140, A: 255})
	data := testDOCXZip(t, docxFixture{
		document: `<?xml version="1.0" encoding="UTF-8"?>
<w:document
  xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
  xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
  xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body>
    <w:p><w:r><w:drawing><a:blip r:embed="rIcon"/></w:drawing></w:r></w:p>
    <w:p><w:r><w:drawing><a:blip r:embed="rCover"/></w:drawing></w:r></w:p>
  </w:body>
</w:document>`,
		documentRels: `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIcon" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/icon.png"/>
  <Relationship Id="rCover" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/cover.png"/>
</Relationships>`,
		extra: map[string][]byte{
			"word/media/icon.png":  icon,
			"word/media/cover.png": cover,
		},
	})

	r := bytes.NewReader(data)
	got, ext, err := ExtractDOCXCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDOCXCover: %v", err)
	}
	if ext != ".png" {
		t.Fatalf("cover ext = %q; want .png", ext)
	}
	if !bytes.Equal(got, cover) {
		t.Fatalf("cover bytes = %d bytes; want large embedded DOCX image", len(got))
	}
}

func TestExtractDOCXCoverUsesMarkupCompatibilityFallback(t *testing.T) {
	choice := testSizedPNG(t, 400, 600, color.NRGBA{R: 180, G: 20, B: 20, A: 255})
	fallback := testSizedPNG(t, 400, 600, color.NRGBA{R: 20, G: 80, B: 180, A: 255})
	data := testDOCXZip(t, docxFixture{
		document: `<?xml version="1.0" encoding="UTF-8"?>
<w:document
  xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
  xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"
  xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
  xmlns:v="urn:schemas-microsoft-com:vml"
  xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body><w:p><w:r><mc:AlternateContent>
    <mc:Choice Requires="wps"><w:drawing><a:blip r:embed="rChoice"/></w:drawing></mc:Choice>
    <mc:Fallback><w:pict><v:imagedata r:id="rFallback"/></w:pict></mc:Fallback>
  </mc:AlternateContent></w:r></w:p></w:body>
</w:document>`,
		documentRels: `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rChoice" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/choice.png"/>
  <Relationship Id="rFallback" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/fallback.png"/>
</Relationships>`,
		extra: map[string][]byte{
			"word/media/choice.png":   choice,
			"word/media/fallback.png": fallback,
		},
	})

	r := bytes.NewReader(data)
	got, ext, err := ExtractDOCXCover(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractDOCXCover: %v", err)
	}
	if ext != ".png" || !bytes.Equal(got, fallback) {
		t.Fatalf("cover ext=%q bytes=%d; want markup-compatibility fallback image", ext, len(got))
	}
}

type docxFixture struct {
	core         string
	app          string
	styles       string
	document     string
	documentRels string
	extra        map[string][]byte
}

func testDOCXZip(t *testing.T, fixture docxFixture) []byte {
	t.Helper()
	entries := map[string][]byte{
		"[Content_Types].xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
  <Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>
</Types>`),
		"_rels/.rels":       []byte(docxRelsXML()),
		"word/document.xml": []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body/></w:document>`),
	}
	if fixture.document != "" {
		entries["word/document.xml"] = []byte(fixture.document)
	}
	if fixture.documentRels != "" {
		entries["word/_rels/document.xml.rels"] = []byte(fixture.documentRels)
	}
	if fixture.core != "" {
		entries["docProps/core.xml"] = []byte(fixture.core)
	}
	if fixture.app != "" {
		entries["docProps/app.xml"] = []byte(fixture.app)
	}
	if fixture.styles != "" {
		entries["word/styles.xml"] = []byte(fixture.styles)
	}
	maps.Copy(entries, fixture.extra)
	return writeTestZip(t, entries)
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

func docxRelsXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`
}
