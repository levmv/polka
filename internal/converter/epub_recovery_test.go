package converter

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/epubtest"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/testfixture"
)

func TestEPUBConversionRecoversResources(t *testing.T) {
	const opf = `<package version="3.0" unique-identifier="uid" xmlns="http://www.idpf.org/2007/opf"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">urn:uuid:11111111-1111-4111-8111-111111111111</dc:identifier><dc:title>Source</dc:title><dc:language>en</dc:language></metadata><manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/><item id="broken" href="broken.xhtml" media-type="application/xhtml+xml"/><item id="font" href="font.ttf" media-type="font/ttf"/><item id="style" href="style.css" media-type="text/css"/></manifest><spine><itemref idref="text"/><itemref idref="broken"/></spine></package>`
	const chapter = `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter</title><link rel="stylesheet" href="style.css"/><style>@font-face {font-family: Embedded;src:url('font.ttf')} p {color:blue}</style></head><body><p>Readable chapter.</p></body></html>`
	encryption := func(algorithm, name string) []byte {
		return []byte(`<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><EncryptedData xmlns="http://www.w3.org/2001/04/xmlenc#"><EncryptionMethod Algorithm="` + algorithm + `"/><CipherData><CipherReference URI="` + name + `"/></CipherData></EncryptedData></encryption>`)
	}
	for _, tc := range []struct {
		name    string
		target  Target
		modify  func(map[string][]byte)
		omitted string
	}{
		{"signature", TargetEPUB, func(files map[string][]byte) { files["META-INF/signatures.xml"] = []byte(`<signatures/>`) }, "META-INF/signatures.xml"},
		{"unsupported font", TargetKEPUB, func(files map[string][]byte) {
			files["META-INF/encryption.xml"] = encryption("unknown", "OEBPS/font.ttf")
		}, "OEBPS/font.ttf"},
		{"missing font key", TargetEPUB, func(files map[string][]byte) {
			files["META-INF/encryption.xml"] = encryption(idpfFontObfuscation, "OEBPS/font.ttf")
			files["OEBPS/content.opf"] = bytes.Replace(files["OEBPS/content.opf"], []byte(`unique-identifier="uid"`), []byte(`unique-identifier="absent"`), 1)
		}, "OEBPS/font.ttf"},
		{"malformed encryption", TargetEPUB, func(files map[string][]byte) { files["META-INF/encryption.xml"] = []byte(`<encryption><broken>`) }, "OEBPS/font.ttf"},
		{"encrypted chapter", TargetMOBI6, func(files map[string][]byte) {
			files["META-INF/encryption.xml"] = encryption("unknown", "OEBPS/broken.xhtml")
		}, "OEBPS/broken.xhtml"},
		{"missing chapter", TargetEPUB, func(files map[string][]byte) { delete(files, "OEBPS/broken.xhtml") }, "OEBPS/broken.xhtml"},
		{"truncated chapter", TargetKEPUB, func(files map[string][]byte) {
			files["OEBPS/broken.xhtml"] = []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p><![CDATA[Second chapter.`)
		}, ""},
		{"broken reading order", TargetKEPUB, func(files map[string][]byte) {
			files["OEBPS/content.opf"] = bytes.Replace(files["OEBPS/content.opf"], []byte(`<spine><itemref idref="text"/><itemref idref="broken"/></spine>`), []byte(`<spine page-progression-direction="rtl"><itemref idref="absent"/></spine>`), 1)
		}, ""},
		{"missing reading order", TargetMOBI6, func(files map[string][]byte) {
			files["OEBPS/content.opf"] = bytes.Replace(files["OEBPS/content.opf"], []byte(`<spine><itemref idref="text"/><itemref idref="broken"/></spine>`), nil, 1)
		}, ""},
		{"unsafe unused path", TargetKEPUB, func(files map[string][]byte) { files["../escape"] = []byte("unused") }, "../escape"},
	} {
		target := tc.target
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{
				"OEBPS/content.opf": []byte(opf), "OEBPS/text.xhtml": []byte(chapter),
				"OEBPS/broken.xhtml": []byte(strings.ReplaceAll(chapter, "Readable chapter.", "Second chapter.")),
				"OEBPS/font.ttf":     []byte("synthetic font"),
				"OEBPS/style.css":    []byte(`/* preserved comment */ @font-face {font-family: Embedded;src: url("font.ttf"); /* } */} @font-face {font-family:Local;src:local("Local")} p {color:red}`),
			}
			tc.modify(files)
			packageXML := files["OEBPS/content.opf"]
			delete(files, "OEBPS/content.opf")
			src := testfixture.EPUB(t, packageXML, files)
			var out bytes.Buffer
			var warnings []string
			err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), target, ConversionOptions{
				Metadata:  &bookmeta.Metadata{Title: "Catalog", Language: "en", Date: "2024"},
				OnWarning: func(message string) { warnings = append(warnings, message) },
			})
			if err != nil || len(warnings) == 0 {
				t.Fatalf("conversion = %v, warnings = %v", err, warnings)
			}
			if target == TargetMOBI6 {
				doc, err := format.ExtractKindleDocument(bytes.NewReader(out.Bytes()), int64(out.Len()), format.FormatMOBI)
				if err != nil || len(doc.Flows) != 1 || !bytes.Contains(doc.Flows[0].Data, []byte("Readable chapter.")) {
					t.Fatalf("lost readable content: %v", err)
				}
				if tc.omitted == "OEBPS/broken.xhtml" && bytes.Contains(doc.Flows[0].Data, []byte("Second chapter.")) {
					t.Fatal("retained encrypted chapter")
				}
				return
			}
			archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range archive.File {
				if file.Name == tc.omitted {
					t.Fatalf("retained unusable resource %s", file.Name)
				}
			}
			if !strings.Contains(zipEntry(t, out.Bytes(), "OEBPS/text.xhtml"), "Readable chapter.") {
				t.Fatal("lost readable chapter")
			}
			if tc.omitted != "OEBPS/broken.xhtml" && !strings.Contains(zipEntry(t, out.Bytes(), "OEBPS/broken.xhtml"), "Second chapter.") {
				t.Fatal("lost second chapter")
			}
			written := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
			if tc.omitted != "OEBPS/broken.xhtml" {
				first, second := strings.Index(written, `idref="text"`), strings.Index(written, `idref="broken"`)
				if first < 0 || second <= first {
					t.Fatal("lost chapter order")
				}
			}
			if bytes.Contains(packageXML, []byte(`page-progression-direction="rtl"`)) && !strings.Contains(written, `page-progression-direction="rtl"`) {
				t.Fatal("lost reading direction")
			}
			if tc.omitted == "OEBPS/broken.xhtml" && (strings.Contains(written, `idref="broken"`) || strings.Contains(written, `href="broken.xhtml"`)) {
				t.Fatal("dangling chapter in manifest/spine")
			}
			if tc.omitted == "OEBPS/font.ttf" {
				if strings.Contains(written, `href="font.ttf"`) {
					t.Fatal("dangling font in manifest")
				}
				css := zipEntry(t, out.Bytes(), "OEBPS/style.css")
				if strings.Contains(css, "font.ttf") || !strings.Contains(css, "p {color:red}") || !strings.Contains(css, "preserved comment") || !strings.Contains(css, `src:local("Local")`) {
					t.Fatalf("font CSS cleanup: %s", css)
				}
				if strings.Contains(zipEntry(t, out.Bytes(), "OEBPS/text.xhtml"), "font.ttf") {
					t.Fatal("dangling inline font declaration")
				}
			}
			if issues, err := epubtest.Check(out.Bytes()); err != nil || len(issues) != 0 {
				t.Fatalf("broken output: %v, %v", issues, err)
			}
		})
	}
}

func TestEPUBConversionKeepsContentWhenMetadataCannotBeWritten(t *testing.T) {
	src := testfixture.EPUB(t, []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="2.0"><manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="text"/></spine></package>`), map[string][]byte{"OEBPS/text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Readable chapter.</p></body></html>`)})
	for _, target := range []Target{TargetEPUB, TargetKEPUB} {
		t.Run(string(target), func(t *testing.T) {
			var out bytes.Buffer
			warned := false
			err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), target, ConversionOptions{Metadata: &bookmeta.Metadata{Title: "Catalog"}, OnWarning: func(string) { warned = true }})
			if err != nil || !warned || !strings.Contains(zipEntry(t, out.Bytes(), "OEBPS/text.xhtml"), "Readable chapter.") {
				t.Fatalf("conversion = %v, warned = %v", err, warned)
			}
		})
	}
}

func TestEPUBConversionRecoversDamagedArchive(t *testing.T) {
	for _, tc := range []struct {
		name        string
		recoverText bool
	}{
		{"bad chapter checksum", true},
		{"unreadable spine and image", false},
	} {
		for _, target := range []Target{TargetEPUB, TargetKEPUB, TargetMOBI6} {
			t.Run(tc.name+"/"+string(target), func(t *testing.T) {
				spine := "broken"
				if tc.recoverText {
					spine = "text"
				}
				src := testfixture.EPUB(t, []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="2.0"><manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/><item id="broken" href="broken.xhtml" media-type="application/xhtml+xml"/><item id="image" href="image.png" media-type="image/png"/></manifest><spine><itemref idref="`+spine+`"/></spine></package>`), map[string][]byte{
					"OEBPS/text.xhtml":   []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Readable chapter.</p></body></html>`),
					"OEBPS/broken.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Other chapter.</p></body></html>`),
					"OEBPS/image.png":    []byte("unreadable image"),
				})
				// Damage ZIP reads independently of the content parser.
				for offset := 0; offset+46 < len(src); offset++ {
					if !bytes.Equal(src[offset:offset+4], []byte{'P', 'K', 1, 2}) {
						continue
					}
					length := int(binary.LittleEndian.Uint16(src[offset+28 : offset+30]))
					if offset+46+length > len(src) {
						continue
					}
					name := string(src[offset+46 : offset+46+length])
					if tc.recoverText && name == "OEBPS/text.xhtml" {
						src[offset+16] ^= 1
					} else if !tc.recoverText && (name == "OEBPS/broken.xhtml" || name == "OEBPS/image.png") {
						binary.LittleEndian.PutUint16(src[offset+10:offset+12], 99)
					}
				}
				var out bytes.Buffer
				warned := false
				err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), target, ConversionOptions{OnWarning: func(string) { warned = true }})
				if err != nil || !warned {
					t.Fatalf("conversion = %v, warned = %v", err, warned)
				}
				if target == TargetMOBI6 {
					doc, err := format.ExtractKindleDocument(bytes.NewReader(out.Bytes()), int64(out.Len()), format.FormatMOBI)
					if err != nil || len(doc.Flows) != 1 || !bytes.Contains(doc.Flows[0].Data, []byte("Readable chapter.")) {
						t.Fatalf("lost readable content: %v", err)
					}
				} else {
					if !strings.Contains(zipEntry(t, out.Bytes(), "OEBPS/text.xhtml"), "Readable chapter.") {
						t.Fatal("lost recovered text")
					}
					opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
					if !strings.Contains(opf, `idref="text"`) {
						t.Fatal("recovered chapter missing from reading order")
					}
					if !tc.recoverText && (strings.Contains(opf, "image.png") || strings.Contains(opf, "broken.xhtml") || strings.Contains(opf, `idref="broken"`)) {
						t.Fatalf("retained references to unreadable entries: %s", opf)
					}
				}
			})
		}
	}
}
