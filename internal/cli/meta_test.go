package cli

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/testfixture"
)

var metaTinyPNG = []byte{
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

func TestRunMetaTreatsArgumentsAfterDoubleDashAsFilenames(t *testing.T) {
	t.Chdir(t.TempDir())
	name := "--data=notes.txt"
	if err := os.WriteFile(name, []byte("A small synthetic book.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := captureStdout(t, func() error { return RunContext(t.Context(), []string{"meta", "--json", "--", name}) })
	if err != nil {
		t.Fatal(err)
	}
	var reports []metaFileReport
	if err := json.Unmarshal([]byte(output), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Path != name || reports[0].Format != "txt" {
		t.Fatalf("reports = %+v; want the literal filename %q", reports, name)
	}
}

func TestRunMetaJSONEPUB(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.epub")
	writeEPUB(t, src, "Meta Book", "Jane Doe", "Doe, Jane")

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len = %d; want 1", len(reports))
	}
	report := reports[0]
	if report.Path != src || report.Format != "epub" || report.FormatLabel != "EPUB" || report.MediaType != "application/epub+zip" {
		t.Fatalf("unexpected format report: %+v", report)
	}
	if report.Metadata == nil || report.Metadata.Title != "Meta Book" {
		t.Fatalf("metadata = %+v; want title Meta Book", report.Metadata)
	}
	if len(report.Metadata.Authors) != 1 || report.Metadata.Authors[0].Name != "Jane Doe" || report.Metadata.Authors[0].SortName != "Doe, Jane" {
		t.Fatalf("authors = %+v; want Jane Doe with sort name", report.Metadata.Authors)
	}
}

func TestRunMetaJSONUnknownKnownExtensionUsesOctetStream(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "broken.epub")
	if err := os.WriteFile(src, []byte("not an epub package"), 0o644); err != nil {
		t.Fatalf("write broken epub: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len = %d; want 1", len(reports))
	}
	report := reports[0]
	if report.Format != "unknown" || report.MediaType != "application/octet-stream" {
		t.Fatalf("unexpected report for broken EPUB: %+v", report)
	}
}

func TestRunMetaJSONIncludesFormatDetails(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.fb2")
	writeMetaFB2(t, src, "FB2 Meta")

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len = %d; want 1", len(reports))
	}
	report := reports[0]
	if report.Metadata == nil || report.Format != "fb2" || report.Metadata.Title != "FB2 Meta" {
		t.Fatalf("unexpected FB2 report: %+v", report)
	}
	if report.Details == nil || report.Details.FB2Container != "plain" {
		t.Fatalf("details = %+v; want plain FB2 container", report.Details)
	}
	if len(report.ConversionTargets) != 2 || report.ConversionTargets[0].Target != "epub" || report.ConversionTargets[1].Target != "kepub" {
		t.Fatalf("conversion targets = %+v; want epub, kepub", report.ConversionTargets)
	}
}

func TestRunMetaReportsToleratedFormatWarnings(t *testing.T) {
	dir := t.TempDir()
	epubPath := filepath.Join(dir, "fallback.epub")
	fb2Path := filepath.Join(dir, "compressed-but-mislabeled.fb2")
	kindlePath := filepath.Join(dir, "unknown-codepage.mobi")
	writeMetaEPUBWithFallbackPaths(t, epubPath)
	writeMetaFB2Zip(t, fb2Path, "Rock & Roll")
	kindle := testfixture.MOBI(0)
	const record0Offset = 78 + 2*8
	binary.BigEndian.PutUint32(kindle[record0Offset+28:record0Offset+32], 932)
	if err := os.WriteFile(kindlePath, kindle, 0o644); err != nil {
		t.Fatalf("write Kindle fixture: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", epubPath, fb2Path, kindlePath})
	})
	if err != nil {
		t.Fatalf("runMeta JSON: %v", err)
	}
	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	byPath := metaReportsByPath(reports)
	wants := map[string]string{
		epubPath:   "epub.nonstandard_mimetype,epub.normalized_container_path,epub.normalized_package_path,epub.rootfile_media_type_fallback,epub.package_candidate_fallback",
		fb2Path:    "fb2.content_detected_container,fb2.unescaped_ampersand_repaired",
		kindlePath: "kindle.codepage_fallback",
	}
	for path, want := range wants {
		report := byPath[path]
		if report == nil {
			t.Fatalf("missing report for %s", path)
		}
		var codes []string
		for _, warning := range report.Warnings {
			if warning.Message == "" {
				t.Fatalf("%s warning %q has no message", path, warning.Code)
			}
			codes = append(codes, warning.Code)
		}
		if got := strings.Join(codes, ","); got != want {
			t.Fatalf("%s warning codes = %q, want %q; warnings: %+v", path, got, want, report.Warnings)
		}
	}

	human, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{fb2Path})
	})
	if err != nil {
		t.Fatalf("runMeta human: %v", err)
	}
	if !strings.Contains(human, "  warnings:\n    - fb2.content_detected_container:") || !strings.Contains(human, "    - fb2.unescaped_ampersand_repaired:") {
		t.Fatalf("human meta output missing structured warnings:\n%s", human)
	}
}

func TestRunMetaJSONNormalizesExtractedLanguage(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.fb2")
	if err := os.WriteFile(src, []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>Language Meta</book-title>
      <lang>rus</lang>
    </title-info>
  </description>
  <body><section><p>Text.</p></section></body>
</FictionBook>`), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 || reports[0].Metadata == nil {
		t.Fatalf("reports = %+v; want one metadata report", reports)
	}
	if reports[0].Metadata.Language != "ru" {
		t.Fatalf("Language = %q; want normalized ru", reports[0].Metadata.Language)
	}
}

func TestRunMetaJSONFB2ZipKeepsContainerMediaType(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.fb2.zip")
	writeMetaFB2Zip(t, src, "Zipped FB2")

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len = %d; want 1", len(reports))
	}
	report := reports[0]
	if report.Format != "fb2" || report.MediaType != "application/zip" {
		t.Fatalf("unexpected report for FB2 zip: %+v", report)
	}
}

func TestRunMetaJSONIncludesFB2ContainerDetails(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "zipped.fb2.zip")
	gzipPath := filepath.Join(dir, "gzipped.fb2.gz")
	writeMetaFB2Zip(t, zipPath, "Zipped FB2")
	writeMetaFB2Gzip(t, gzipPath, "Gzipped FB2")

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", zipPath, gzipPath})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	byPath := metaReportsByPath(reports)
	for path, want := range map[string]string{
		zipPath:  "zip",
		gzipPath: "gzip",
	} {
		report := byPath[path]
		if report == nil || report.Format != "fb2" || report.Details == nil || report.Details.FB2Container != want {
			t.Fatalf("%s report = %+v; want FB2 container %s", path, report, want)
		}
		if len(report.ConversionTargets) != 2 || report.ConversionTargets[0].Target != "epub" || report.ConversionTargets[1].Target != "kepub" {
			t.Fatalf("%s conversion targets = %+v; want epub, kepub", path, report.ConversionTargets)
		}
	}
}

func TestRunMetaJSONIncludesCBZPageCount(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "comic.cbz")
	writeMetaCBZ(t, src, map[string][]byte{
		"page10.png": metaTinyPNG,
		"page2.png":  metaTinyPNG,
	})

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len = %d; want 1", len(reports))
	}
	report := reports[0]
	if report.Format != "cbz" || report.FormatLabel != "CBZ" || report.MediaType != "application/vnd.comicbook+zip" {
		t.Fatalf("unexpected CBZ format report: %+v", report)
	}
	if report.Details == nil || report.Details.CBZPageCount != 2 {
		t.Fatalf("details = %+v; want two CBZ pages", report.Details)
	}
}

func TestRunMetaJSONIncludesArchivePageCount(t *testing.T) {
	for _, tt := range []struct {
		format    string
		data      []byte
		pageCount func(*metaFormatDetail) int
		wantPages int
	}{
		{format: "cbr", data: testfixture.CBR5(), pageCount: func(d *metaFormatDetail) int { return d.CBRPageCount }, wantPages: 2},
		{format: "cb7", data: testfixture.CB7(), pageCount: func(d *metaFormatDetail) int { return d.CB7PageCount }, wantPages: 1},
	} {
		t.Run(tt.format, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "comic."+tt.format)
			if err := os.WriteFile(src, tt.data, 0o644); err != nil {
				t.Fatalf("write %s: %v", tt.format, err)
			}

			out, err := captureStdout(t, func() error {
				return runMeta(t.Context(), []string{"--json", src})
			})
			if err != nil {
				t.Fatalf("runMeta: %v", err)
			}

			var reports []metaFileReport
			if err := json.Unmarshal([]byte(out), &reports); err != nil {
				t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
			}
			if len(reports) != 1 {
				t.Fatalf("reports len = %d; want 1", len(reports))
			}
			report := reports[0]
			if report.Format != tt.format || report.Details == nil || tt.pageCount(report.Details) != tt.wantPages {
				t.Fatalf("%s report = %+v; want %d pages", tt.format, report, tt.wantPages)
			}
			if len(report.ConversionTargets) != 1 || report.ConversionTargets[0].Target != "cbz" {
				t.Fatalf("%s conversion targets = %+v; want CBZ", tt.format, report.ConversionTargets)
			}
		})
	}
}

func TestRunMetaJSONIncludesPalmDBSubtype(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.pdb")
	if err := os.WriteFile(src, metaPalmDOCFile("PalmDOC Meta"), 0o644); err != nil {
		t.Fatalf("write PalmDOC: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len = %d; want 1", len(reports))
	}
	report := reports[0]
	if report.Format != "pdb" || report.Metadata == nil || report.Metadata.Title != "PalmDOC Meta" {
		t.Fatalf("unexpected PalmDOC report: %+v", report)
	}
	if report.Details == nil || report.Details.MOBIKind != "palmdoc" {
		t.Fatalf("details = %+v; want PalmDOC subtype", report.Details)
	}
	if report.Details.Kindle == nil || report.Details.Kindle.SourceClass != "palmdoc" || report.Details.Kindle.Container != "palmdoc" || report.Details.Kindle.Compression != "palmdoc" {
		t.Fatalf("kindle details = %+v; want PalmDOC classification", report.Details.Kindle)
	}
	if len(report.ConversionTargets) != 1 || report.ConversionTargets[0].Target != "epub" || report.ConversionTargets[0].Label != "EPUB" {
		t.Fatalf("conversion targets = %+v; want EPUB for PalmDOC", report.ConversionTargets)
	}
}

func TestRunMetaJSONRecognizesEncryptedPalmDOC(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "encrypted.mobi")
	if err := os.WriteFile(src, metaPalmDOCFileWithEncryption("DRM PalmDOC", 1), 0o644); err != nil {
		t.Fatalf("write encrypted PalmDOC: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}

	var reports []metaFileReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len = %d; want 1", len(reports))
	}
	report := reports[0]
	if report.Format != "pdb" || len(report.ConversionTargets) != 1 || report.ConversionTargets[0].Target != "epub" {
		t.Fatalf("encrypted PalmDOC report = %+v", report)
	}
	if report.Details == nil || report.Details.Kindle == nil {
		t.Fatalf("details = %+v; want Kindle diagnostic block", report.Details)
	}
	if report.Details.Kindle.SourceClass != "encrypted-palmdoc" || !report.Details.Kindle.Encrypted {
		t.Fatalf("kindle details = %+v; want encrypted PalmDOC classification", report.Details.Kindle)
	}
	if report.Details.MOBIKind != "palmdoc" {
		t.Fatalf("MOBIKind = %q; want palmdoc", report.Details.MOBIKind)
	}
}

func TestAZW4MetadataAndPDFConversion(t *testing.T) {
	pdf := bytes.TrimSpace(testfixture.MinimalPDF())
	header := testfixture.PalmDBRecordBodies(t, testfixture.MOBI(0))[0][:64]
	for _, tt := range []struct {
		name, pdfState, reason string
		data                   []byte
	}{
		{"print-replica", "present", "", append(testfixture.MOBI(0), pdf...)},
		{"empty", "missing", "no embedded PDF", testfixture.MOBI(0)},
		{"short-header", "present", "", testfixture.PalmDBWithRecords("Print Replica", "BOOKMOBI", [][]byte{header, pdf})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, tt.name+".azw4"), filepath.Join(dir, "export.pdf")
			if err := os.WriteFile(src, tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := captureStdout(t, func() error { return runMeta(t.Context(), []string{"--json", src}) })
			if err != nil {
				t.Fatal(err)
			}
			var reports []metaFileReport
			if err := json.Unmarshal([]byte(out), &reports); err != nil || len(reports) != 1 {
				t.Fatalf("meta JSON: %v\n%s", err, out)
			}
			report := reports[0]
			if report.Format != "azw4" || report.Details == nil || report.Details.AZW4PDF != tt.pdfState {
				t.Fatalf("report = %+v; want AZW4 PDF %s", report, tt.pdfState)
			}
			if tt.pdfState == "present" && (report.Details.Kindle == nil || report.Details.Kindle.SourceClass != "azw4-pdf-wrapper" || !report.Details.Kindle.AZW4PDF) {
				t.Fatalf("kindle details = %+v; want AZW4 PDF wrapper", report.Details.Kindle)
			}
			if len(report.ConversionTargets) != 1 || report.ConversionTargets[0].Target != "pdf" {
				t.Fatalf("conversion targets = %+v; want pdf", report.ConversionTargets)
			}
			_, err = captureStdout(t, func() error {
				return RunContext(t.Context(), []string{"convert", "--to", "pdf", src, dst})
			})
			if tt.reason != "" {
				if !errors.Is(err, converter.ErrUnsupportedContent) || !strings.Contains(err.Error(), tt.reason) {
					t.Fatalf("conversion error = %v; want refusal with %q", err, tt.reason)
				}
				if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed conversion left an output file: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(dst); err != nil || !bytes.Equal(got, pdf) {
					t.Fatalf("PDF export changed the payload: %v", err)
				}
			}
		})
	}
}

func TestRunMetaSetEPUBOverlaysPassedFields(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.epub")
	writeEPUB(t, src, "Old Title", "Jane Doe", "Doe, Jane")

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{
			"set", src,
			"--title", "New Title",
			"--authors", "Alice Example; Bob Co",
			"--series", "Saga",
			"--series-index", "2.5",
			"--genres", "Fiction, fiction",
			"--tags", "Sci-Fi, Classic, sci-fi",
			"--description", "New description",
			"--publisher", "Polka Press",
			"--date", "July 7, 2026",
			"--language", "eng",
			"--identifiers", "ISBN 978-0-306-40615-7, https://example.test/book",
		})
	})
	if err != nil {
		t.Fatalf("runMeta set: %v", err)
	}
	if !strings.Contains(out, "Updated metadata: "+src) {
		t.Fatalf("meta set output = %q; want update line", out)
	}

	report := inspectMetaFile(t.Context(), nil, src, "", false)
	if report.Error != "" {
		t.Fatalf("inspect rewritten file: %s", report.Error)
	}
	meta := report.Metadata
	if meta == nil {
		t.Fatal("rewritten metadata missing")
	}
	if meta.Title != "New Title" || meta.Series != "Saga" || meta.SeriesIndex != 2.5 {
		t.Fatalf("metadata title/series = %+v", meta)
	}
	if meta.Language != "en" || meta.Publisher != "Polka Press" || meta.Date != "2026-07-07" || meta.Description != "New description" {
		t.Fatalf("metadata simple fields = %+v", meta)
	}
	if len(meta.Authors) != 2 || meta.Authors[0].Name != "Alice Example" || meta.Authors[0].SortName != "Example, Alice" || meta.Authors[1].Name != "Bob Co" {
		t.Fatalf("authors = %+v; want normalized authors from flag", meta.Authors)
	}
	if strings.Join(meta.Genres, "|") != "Fiction" || strings.Join(meta.Tags, "|") != "Sci-Fi|Classic" {
		t.Fatalf("genres/tags = %v / %v; want independently deduplicated lists", meta.Genres, meta.Tags)
	}
	if !strings.Contains(meta.Identifier, "isbn:978-0-306-40615-7") || !strings.Contains(meta.Identifier, "url:https://example.test/book") {
		t.Fatalf("identifiers = %q; want normalized isbn and url", meta.Identifier)
	}
}

func TestRunMetaSetExplicitEmptyClearsFields(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.fb2")
	writeMetaFB2(t, src, "FB2 Meta")

	if err := runMeta(t.Context(), []string{"set", src, "--series", "Roadside", "--series-index", "4", "--genres", "sf", "--tags", "one, two"}); err != nil {
		t.Fatalf("runMeta set seed fields: %v", err)
	}
	if err := runMeta(t.Context(), []string{"set", src, "--authors", "", "--series", "", "--series-index", "", "--genres", "", "--tags", ""}); err != nil {
		t.Fatalf("runMeta set clear fields: %v", err)
	}

	report := inspectMetaFile(t.Context(), nil, src, "", false)
	if report.Error != "" {
		t.Fatalf("inspect rewritten file: %s", report.Error)
	}
	meta := report.Metadata
	if meta == nil {
		t.Fatal("rewritten metadata missing")
	}
	if len(meta.Authors) != 0 || meta.Series != "" || meta.SeriesIndex != 0 || len(meta.Genres) != 0 || len(meta.Tags) != 0 {
		t.Fatalf("cleared metadata = %+v; want empty authors/series/genres/tags", meta)
	}
}

func TestRunMetaSetRejectsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	before := []byte("plain text\n")
	if err := os.WriteFile(src, before, 0o644); err != nil {
		t.Fatalf("write text source: %v", err)
	}

	err := runMeta(t.Context(), []string{"set", src, "--title", "New Title"})
	if err == nil || !strings.Contains(err.Error(), "unsupported metadata write-back format: txt") {
		t.Fatalf("runMeta set error = %v; want unsupported txt", err)
	}
	after, readErr := os.ReadFile(src)
	if readErr != nil {
		t.Fatalf("read text source after failed set: %v", readErr)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("unsupported file changed: got %q, want %q", after, before)
	}
}

func TestRunMetaPrintsAZW4PDFDetail(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "print-replica.azw4")
	if err := os.WriteFile(src, append(testfixture.MOBI(0), []byte("%PDF-1.7\nbody\n%%EOF")...), 0o644); err != nil {
		t.Fatalf("write AZW4: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}
	if !strings.Contains(out, "azw4_pdf: present") {
		t.Fatalf("human meta output missing AZW4 PDF detail:\n%s", out)
	}
	if !strings.Contains(out, "kindle_class: azw4-pdf-wrapper") {
		t.Fatalf("human meta output missing Kindle class detail:\n%s", out)
	}
}

func TestRunMetaWritesCover(t *testing.T) {
	for _, src := range coverSourceFiles(t) {
		t.Run(filepath.Ext(src), func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "cover.bin")

			out, err := captureStdout(t, func() error {
				return runMeta(t.Context(), []string{"--json", "--cover", dst, src})
			})
			if err != nil {
				t.Fatalf("runMeta: %v", err)
			}

			got, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("read cover output: %v", err)
			}
			if filepath.Ext(src) == ".epub" && !bytes.Equal(got, metaTinyPNG) {
				t.Fatalf("cover output mismatch: got %d bytes; want %d", len(got), len(metaTinyPNG))
			}

			var reports []metaFileReport
			if err := json.Unmarshal([]byte(out), &reports); err != nil {
				t.Fatalf("unmarshal meta JSON: %v\n%s", err, out)
			}
			if len(reports) != 1 {
				t.Fatalf("reports len = %d; want 1", len(reports))
			}
			if reports[0].CoverError != "" {
				t.Fatalf("cover error = %q; want none", reports[0].CoverError)
			}
			ext := ".jpg"
			if filepath.Ext(src) == ".epub" {
				ext = ".png"
			}
			if _, err := covers.Validate(got); err != nil {
				t.Fatal(err)
			}
			if reports[0].Cover == nil || reports[0].Cover.Path != dst || reports[0].Cover.Extension != ext || reports[0].Cover.Bytes != len(got) {
				t.Fatalf("cover report = %+v; want %s %s %d bytes", reports[0].Cover, dst, ext, len(got))
			}
		})
	}
}

func TestRunMetaPreservesPartialDJVUResults(t *testing.T) {
	original, err := os.ReadFile("../testfixture/metadata.djvu")
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"ANTz", "Sjbz"} {
		t.Run(chunk, func(t *testing.T) {
			data := bytes.Clone(original)
			offset := bytes.Index(data, []byte(chunk))
			clear(data[offset+8 : offset+8+int(binary.BigEndian.Uint32(data[offset+4:offset+8]))])
			dir := t.TempDir()
			path, cover := filepath.Join(dir, "book.djvu"), filepath.Join(dir, "cover.jpg")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := captureStdout(t, func() error {
				return runMeta(t.Context(), []string{"--json", "--cover", cover, path})
			})
			if !errors.Is(err, errMetaFileErrors) {
				t.Fatalf("inspection error = %v", err)
			}
			var reports []metaFileReport
			if err := json.Unmarshal([]byte(output), &reports); err != nil {
				t.Fatal(err)
			}
			if len(reports) != 1 || reports[0].Error == "" {
				t.Fatalf("missing extraction error: %+v", reports)
			}
			if chunk == "ANTz" {
				data, err := os.ReadFile(cover)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := covers.Validate(data); err != nil {
					t.Fatal(err)
				}
			} else if reports[0].Metadata == nil || reports[0].Metadata.Title != "Annotated DjVu" {
				t.Fatalf("lost readable metadata: %+v", reports[0])
			}
		})
	}
}

func TestRunMetaCoverMissingIsFileError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.epub")
	writeEPUB(t, src, "No Cover", "Jane Doe", "Doe, Jane")
	dst := filepath.Join(dir, "cover.png")

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--cover", dst, src})
	})
	if !errors.Is(err, errMetaFileErrors) {
		t.Fatalf("runMeta error = %v; want errMetaFileErrors", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("cover output stat error = %v; want not exist", statErr)
	}
	if !strings.Contains(out, "cover_error: extract cover: no cover found") {
		t.Fatalf("cover error not printed:\n%s", out)
	}
	if !strings.Contains(out, "format: epub (EPUB)") {
		t.Fatalf("metadata report missing despite cover error:\n%s", out)
	}
}

func TestRunMetaContinuesAfterFileError(t *testing.T) {
	dir := t.TempDir()
	okPath := filepath.Join(dir, "notes.txt")
	missingPath := filepath.Join(dir, "missing.epub")
	if err := os.WriteFile(okPath, []byte("plain text\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{missingPath, okPath})
	})
	if !errors.Is(err, errMetaFileErrors) {
		t.Fatalf("runMeta error = %v; want errMetaFileErrors", err)
	}
	if !strings.Contains(out, missingPath+"\n  error: open source:") {
		t.Fatalf("missing file error not printed:\n%s", out)
	}
	if !strings.Contains(out, okPath+"\n  format: txt (TXT)") {
		t.Fatalf("valid file was not inspected after error:\n%s", out)
	}
}

func coverSourceFiles(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	epub := filepath.Join(dir, "book.epub")
	writeMetaEPUBWithCover(t, epub, metaTinyPNG)
	djvu, err := os.ReadFile("../testfixture/metadata.djvu")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{epub}
	for _, file := range []struct {
		name string
		data []byte
	}{{"book.pdf", testfixture.MinimalPDF()}, {"book.djvu", djvu}} {
		path := filepath.Join(dir, file.name)
		if err := os.WriteFile(path, file.data, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

func writeMetaEPUBWithCover(t *testing.T, path string, cover []byte) {
	t.Helper()
	opf := []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Cover Book</dc:title>
    <dc:creator>Jane Doe</dc:creator>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="cover-image" href="images/cover.png" media-type="image/png"/>
  </manifest>
</package>`)
	data := testfixture.EPUB(t, opf, map[string][]byte{"OEBPS/images/cover.png": cover})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
}

func writeMetaEPUBWithFallbackPaths(t *testing.T, path string) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	entries := []struct {
		name string
		body string
	}{
		{"mimetype", "application/epub+zip"},
		{"meta-inf/CONTAINER.XML", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/broken.opf" media-type="application/oebps-package+xml"/><rootfile full-path="oebps/content.opf" media-type="text/xml"/></rootfiles></container>`},
		{"OEBPS/broken.opf", `<package><broken>`},
		{"OEBPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Fallback EPUB</dc:title></metadata><manifest/></package>`},
	}
	for _, entry := range entries {
		f, err := w.Create(entry.name)
		if err != nil {
			t.Fatalf("create EPUB entry %s: %v", entry.name, err)
		}
		if _, err := f.Write([]byte(entry.body)); err != nil {
			t.Fatalf("write EPUB entry %s: %v", entry.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fallback EPUB: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write fallback EPUB: %v", err)
	}
}

func writeMetaFB2(t *testing.T, path, title string) {
	t.Helper()
	if err := os.WriteFile(path, metaFB2Bytes(title), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}
}

func writeMetaFB2Zip(t *testing.T, path, title string) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	f, err := w.Create("book.fb2")
	if err != nil {
		t.Fatalf("create fb2 zip entry: %v", err)
	}
	if _, err := f.Write(metaFB2Bytes(title)); err != nil {
		t.Fatalf("write fb2 zip entry: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fb2 zip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write fb2 zip: %v", err)
	}
}

func writeMetaFB2Gzip(t *testing.T, path, title string) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := gzip.NewWriter(buf)
	if _, err := w.Write(metaFB2Bytes(title)); err != nil {
		t.Fatalf("write fb2 gzip body: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fb2 gzip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write fb2 gzip: %v", err)
	}
}

func metaFB2Bytes(title string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>` + title + `</book-title>
      <author><first-name>Arkady</first-name><last-name>Strugatsky</last-name></author>
    </title-info>
  </description>
  <body><section><p>Text.</p></section></body>
</FictionBook>`)
}

func writeMetaCBZ(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	for name, body := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("create cbz entry %s: %v", name, err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatalf("write cbz entry %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close cbz: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write cbz: %v", err)
	}
}

func metaReportsByPath(reports []metaFileReport) map[string]*metaFileReport {
	out := make(map[string]*metaFileReport, len(reports))
	for i := range reports {
		out[reports[i].Path] = &reports[i]
	}
	return out
}

func metaPalmDOCFile(title string) []byte {
	return metaPalmDOCFileWithEncryption(title, 0)
}

func metaPalmDOCFileWithEncryption(title string, encryption uint16) []byte {
	record0 := testfixture.PalmDOCHeader(2)
	binary.BigEndian.PutUint32(record0[4:8], 4)
	binary.BigEndian.PutUint16(record0[12:14], encryption)
	return testfixture.PalmDBWithRecords(title, "TEXtREAd", [][]byte{record0, []byte("Text")})
}

// A format with no metadata extractor still reports an (empty) metadata object;
// only a file that could not be inspected at all omits the key. Callers use that
// difference to tell "nothing embedded" from "inspection failed".
func TestRunMetaJSONDistinguishesEmptyMetadataFromFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(src, []byte("Just a plain text book.\n"), 0o644); err != nil {
		t.Fatalf("write plain text: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runMeta: %v", err)
	}
	if !strings.Contains(out, `"metadata": {}`) {
		t.Fatalf("inspected file omits its empty metadata object:\n%s", out)
	}

	missing, err := captureStdout(t, func() error {
		return runMeta(t.Context(), []string{"--json", filepath.Join(dir, "absent.txt")})
	})
	if err == nil {
		t.Fatal("runMeta on a missing file: want error")
	}
	if strings.Contains(missing, `"metadata"`) {
		t.Fatalf("uninspectable file reports a metadata object:\n%s", missing)
	}
}
