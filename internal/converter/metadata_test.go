package converter

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/testfixture"
)

func TestGeneratedBooksRetainGenresAndTags(t *testing.T) {
	source := []byte("Chapter one.\n\nSome text.")
	for _, target := range []Target{TargetEPUB, TargetKEPUB, TargetMOBI6} {
		t.Run(string(target), func(t *testing.T) {
			meta := &bookmeta.Metadata{Title: "Book", Genres: []string{"Fiction"}, Tags: []string{"Favourite", "Fiction"}}
			input, from := source, format.FormatTXT
			if target == TargetMOBI6 {
				var epub bytes.Buffer
				if err := ConvertContextWithOptions(t.Context(), &epub, bytes.NewReader(source), from, int64(len(source)), TargetEPUB, ConversionOptions{Metadata: meta}); err != nil {
					t.Fatal(err)
				}
				input, from = epub.Bytes(), format.FormatEPUB
			}
			var out bytes.Buffer
			if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(input), from, int64(len(input)), target, ConversionOptions{Metadata: meta}); err != nil {
				t.Fatal(err)
			}
			kind := format.FormatEPUB
			wantGenres, wantTags := meta.Genres, meta.Tags
			if target == TargetMOBI6 {
				kind = format.FormatMOBI
				wantGenres, wantTags = []string{"Fiction", "Favourite"}, nil
			}
			got, err := format.ExtractMetadata(bytes.NewReader(out.Bytes()), int64(out.Len()), kind)
			if err != nil || !slices.Equal(got.Genres, wantGenres) || !slices.Equal(got.Tags, wantTags) {
				t.Fatalf("converted metadata = %+v, %v; want %v / %v", got, err, wantGenres, wantTags)
			}
		})
	}
}

func TestConvertEPUBCatalogMetadata(t *testing.T) {
	const primary = `<dc:date id="publication" opf:event="publication">2001-06-01T12:00:00Z</dc:date>`
	const unqualified = `<dc:date>1990</dc:date>`
	const original = `<dc:date opf:event="original-publication">1975</dc:date>`
	const electronic = `<dc:date opf:event="ops-publication">2002</dc:date>`
	const technical = `<dc:date opf:event="creation">2020</dc:date>`
	const refinement = `<meta refines="#publication" property="source-of">Publication note</meta>`
	opf := `<package version="3.0" unique-identifier="book-id" xmlns="http://www.idpf.org/2007/opf" xmlns:opf="http://www.idpf.org/2007/opf">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="book-id">urn:uuid:11111111-1111-4111-8111-111111111111</dc:identifier>
<dc:identifier>urn:isbn:9780306406157</dc:identifier>
<dc:title>Source title</dc:title><dc:creator>Source author</dc:creator><dc:language>en</dc:language>
<dc:publisher>Source publisher</dc:publisher><dc:subject>Source tag</dc:subject>
<meta name="calibre:series" content="Source series"/><meta name="calibre:series_index" content="2"/>
<meta property="schema:numberOfPages">17</meta>
` + primary + unqualified + original + electronic + technical + refinement + `
</metadata><manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
<spine><itemref idref="text"/></spine></package>`
	src := testfixture.EPUB(t, []byte(opf), map[string][]byte{
		"OEBPS/text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter</title></head><body><p>Preserved chapter.</p></body></html>`),
	})
	for _, tc := range []struct {
		target Target
		date   string
	}{
		{TargetEPUB, "2001-06-01"},
		{TargetKEPUB, ""},
		{TargetMOBI6, "2024-03"},
	} {
		target, date := tc.target, tc.date
		t.Run(string(target), func(t *testing.T) {
			// The omitted descriptive fields are deliberately cleared values.
			opts := ConversionOptions{
				Metadata: &bookmeta.Metadata{Title: "Catalog title", Date: date, Identifier: "isbn:9780140328721", PageCount: 999},
				Modified: time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC),
			}
			convert := func() []byte {
				t.Helper()
				var out bytes.Buffer
				if err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), target, opts); err != nil {
					t.Fatal(err)
				}
				return out.Bytes()
			}
			out := convert()
			kind := format.FormatEPUB
			if target == TargetMOBI6 {
				kind = format.FormatMOBI
			}
			got, err := format.ExtractMetadata(bytes.NewReader(out), int64(len(out)), kind)
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "Catalog title" || got.Language != "" || got.Date != date || len(got.Authors) != 0 || got.Publisher != "" || len(got.Genres) != 0 || len(got.Tags) != 0 || got.Series != "" || got.SeriesIndex != 0 || !strings.Contains(got.Identifier, "9780140328721") || strings.Contains(got.Identifier, "9780306406157") {
				t.Fatalf("catalog edits were not carried into output: %+v", got)
			}
			if target != TargetMOBI6 {
				written := zipEntry(t, out, "OEBPS/content.opf")
				for _, record := range []struct {
					xml  string
					keep bool
				}{
					{primary, date == "2001-06-01"}, {unqualified, date == "2001-06-01"},
					{refinement, date == "2001-06-01"},
					{original, date != ""}, {electronic, date != ""}, {technical, true},
				} {
					if strings.Contains(written, record.xml) != record.keep {
						t.Fatalf("date record retained != %v: %s\n%s", record.keep, record.xml, written)
					}
				}
				if got.PageCount != 17 || !strings.Contains(written, `property="dcterms:modified">2026-09-24T12:30:00Z</meta>`) || !strings.Contains(written, `<dc:language>und</dc:language>`) {
					t.Fatalf("wrong asset count, modification time or unknown language: %s", written)
				}
			}
			if target == TargetKEPUB && !bytes.Equal(out, convert()) {
				t.Fatal("same catalog snapshot produced different bytes")
			}
		})
	}
}

func TestConvertCatalogMetadataProtectsObfuscatedFontKey(t *testing.T) {
	src := testfixture.EPUB(t, []byte(`<package version="2.0" unique-identifier="book-id" xmlns="http://www.idpf.org/2007/opf">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Source</dc:title><dc:language>en</dc:language><dc:identifier id="book-id">urn:isbn:9780306406157</dc:identifier></metadata>
<manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/><item id="font" href="font.ttf" media-type="font/ttf"/></manifest><spine><itemref idref="text"/></spine></package>`), map[string][]byte{
		"OEBPS/text.xhtml":        []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter</title></head><body><p>Text.</p></body></html>`),
		"OEBPS/font.ttf":          []byte("synthetic obfuscated font bytes"),
		"META-INF/encryption.xml": []byte(`<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><EncryptedData xmlns="http://www.w3.org/2001/04/xmlenc#"><EncryptionMethod Algorithm="http://www.idpf.org/2008/embedding"/><CipherData><CipherReference URI="OEBPS/font.ttf"/></CipherData></EncryptedData></encryption>`),
	})
	for _, target := range []Target{TargetEPUB, TargetKEPUB} {
		for _, identifier := range []string{"isbn:9780306406157", "isbn:9780140328721"} {
			t.Run(string(target)+"/"+identifier, func(t *testing.T) {
				var out bytes.Buffer
				var warnings []string
				err := ConvertContextWithOptions(t.Context(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), target, ConversionOptions{
					Metadata:  &bookmeta.Metadata{Title: "Catalog", Language: "en", Date: "2024", Identifier: identifier + ", doi:10.1000/catalog"},
					OnWarning: func(message string) { warnings = append(warnings, message) },
				})
				if err != nil {
					t.Fatal(err)
				}
				if zipEntry(t, out.Bytes(), "OEBPS/font.ttf") != "synthetic obfuscated font bytes" {
					t.Fatal("font bytes changed")
				}
				meta, err := format.ExtractEPUBMetadata(bytes.NewReader(out.Bytes()), int64(out.Len()))
				if err != nil || meta.Title != "Catalog" || meta.Date != "2024" || !strings.Contains(meta.Identifier, "9780306406157") || !strings.Contains(meta.Identifier, "doi:10.1000/catalog") || strings.Contains(meta.Identifier, "9780140328721") {
					t.Fatalf("metadata = %+v, %v; want old font key and other catalog edits", meta, err)
				}
				if (len(warnings) > 0) != (identifier != "isbn:9780306406157") {
					t.Fatalf("warnings = %v", warnings)
				}
			})
		}
	}
}
