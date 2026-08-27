package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	nethtml "golang.org/x/net/html"
	"golang.org/x/text/encoding/charmap"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
)

func TestTargetSpecsForFormat(t *testing.T) {
	specs := TargetSpecsForFormat(format.FormatAZW4)
	if len(specs) != 1 || specs[0].Target != TargetPDF || specs[0].Label != "PDF" || specs[0].Extension != ".pdf" || specs[0].MediaType != "application/pdf" {
		t.Fatalf("TargetSpecsForFormat(AZW4) = %+v; want PDF spec", specs)
	}

	specs[0].Target = TargetEPUB
	specsAgain := TargetSpecsForFormat(format.FormatAZW4)
	if len(specsAgain) != 1 || specsAgain[0].Target != TargetPDF {
		t.Fatalf("TargetSpecsForFormat returned mutable backing slice: %+v", specsAgain)
	}

	if specs := TargetSpecsForFormat(format.FormatEPUB); len(specs) != 2 || specs[0].Target != TargetEPUB || specs[0].Label != "Repaired EPUB" || specs[1].Target != TargetKEPUB {
		t.Fatalf("TargetSpecsForFormat(EPUB) = %+v; want repaired EPUB, KEPUB", specs)
	}
	if specs := TargetSpecsForFormat(format.FormatCBR); len(specs) != 1 || specs[0].Target != TargetCBZ || specs[0].Label != "CBZ" {
		t.Fatalf("TargetSpecsForFormat(CBR) = %+v; want CBZ", specs)
	}
	if specs := TargetSpecsForFormat(format.FormatCB7); len(specs) != 1 || specs[0].Target != TargetCBZ || specs[0].Label != "CBZ" {
		t.Fatalf("TargetSpecsForFormat(CB7) = %+v; want CBZ", specs)
	}
	for _, tt := range []format.Format{
		format.FormatFB2,
		format.FormatMOBI,
		format.FormatAZW,
		format.FormatAZW3,
		format.FormatPRC,
	} {
		specs := TargetSpecsForFormat(tt)
		if len(specs) != 2 || specs[0].Target != TargetEPUB || specs[1].Target != TargetKEPUB {
			t.Fatalf("TargetSpecsForFormat(%s) = %+v; want EPUB, KEPUB", format.FormatLabel(tt), specs)
		}
	}
	for _, tt := range []format.Format{
		format.FormatPDB,
		format.FormatTXT,
		format.FormatTXTZ,
		format.FormatMarkdown,
		format.FormatHTML,
		format.FormatHTMLZ,
		format.FormatXHTML,
	} {
		specs := TargetSpecsForFormat(tt)
		if len(specs) != 1 || specs[0].Target != TargetEPUB {
			t.Fatalf("TargetSpecsForFormat(%s) = %+v; want EPUB", format.FormatLabel(tt), specs)
		}
	}

	supported := SupportedTargetSpecs()
	if len(supported) != 4 || supported[0].Target != TargetPDF || supported[1].Target != TargetEPUB || supported[2].Target != TargetKEPUB || supported[3].Target != TargetCBZ {
		t.Fatalf("SupportedTargetSpecs = %+v; want PDF, EPUB, KEPUB, CBZ", supported)
	}
	supported[0].Target = TargetEPUB
	if again := SupportedTargetSpecs(); len(again) != 4 || again[0].Target != TargetPDF {
		t.Fatalf("SupportedTargetSpecs returned mutable backing slice: %+v", again)
	}
}

func TestConvertContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	src := []byte("plain text\n")
	var out bytes.Buffer
	err := ConvertContext(ctx, &out, bytes.NewReader(src), format.FormatTXT, int64(len(src)), TargetEPUB)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ConvertContext error = %v; want context.Canceled", err)
	}
	if out.Len() != 0 {
		t.Fatalf("ConvertContext wrote %d bytes after cancellation", out.Len())
	}
}

func TestConvertContextStopsRandomReadsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := testZip(t, map[string][]byte{
		"mimetype": []byte("application/epub+zip"),
	})
	reader := &cancelAfterFirstReaderAt{
		reader: bytes.NewReader(src),
		cancel: cancel,
	}

	var out bytes.Buffer
	err := ConvertContext(ctx, &out, reader, format.FormatEPUB, int64(len(src)), TargetEPUB)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ConvertContext error = %v; want context.Canceled", err)
	}
	if reader.reads != 1 {
		t.Fatalf("source reads = %d; want exactly the read that triggered cancellation", reader.reads)
	}
}

type cancelAfterFirstReaderAt struct {
	reader io.ReaderAt
	cancel context.CancelFunc
	reads  int
}

func (r *cancelAfterFirstReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	n, err := r.reader.ReadAt(p, offset)
	r.reads++
	if r.reads == 1 {
		r.cancel()
	}
	return n, err
}

func TestReadAllContextLimitedRejectsOversizedInput(t *testing.T) {
	_, err := readAllContextLimited(context.Background(), strings.NewReader("abcd"), 3, "test source")
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("readAllContextLimited error = %v; want ErrInputTooLarge", err)
	}
}

func TestConversionAggregateBudgets(t *testing.T) {
	t.Run("decoded bytes", func(t *testing.T) {
		limits := defaultConversionLimits
		limits.decodedBytes = 3
		src := []byte("four")
		var out bytes.Buffer
		err := convertContextWithLimits(context.Background(), &out, bytes.NewReader(src), format.FormatTXT, int64(len(src)), TargetEPUB, ConversionOptions{}, limits)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("conversion error = %v; want ErrResourceLimit", err)
		}
		if out.Len() != 0 {
			t.Fatalf("conversion wrote %d bytes after decoded-data budget failure", out.Len())
		}
	})

	t.Run("resource count", func(t *testing.T) {
		src := testZip(t, map[string][]byte{
			"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
			"OEBPS/content.opf":      []byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="text"/></spine></package>`),
			"OEBPS/text.xhtml":       []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`),
			"OEBPS/extra.bin":        []byte("extra"),
		})
		limits := defaultConversionLimits
		limits.resources = 3
		var out bytes.Buffer
		err := convertContextWithLimits(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB, ConversionOptions{}, limits)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("conversion error = %v; want ErrResourceLimit", err)
		}
		if out.Len() != 0 {
			t.Fatalf("conversion wrote %d bytes after resource-count budget failure", out.Len())
		}
	})

	t.Run("output bytes", func(t *testing.T) {
		pdf := []byte("%PDF-1.7\nbody\n%%EOF")
		src := testMOBIWithPayload(pdf)
		limits := defaultConversionLimits
		limits.outputBytes = int64(len(pdf) - 1)
		var out bytes.Buffer
		err := convertContextWithLimits(context.Background(), &out, bytes.NewReader(src), format.FormatAZW4, int64(len(src)), TargetPDF, ConversionOptions{}, limits)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("conversion error = %v; want ErrResourceLimit", err)
		}
	})

	t.Run("composed resource count", func(t *testing.T) {
		src := testFB2ForEPUB()
		limits := defaultConversionLimits
		// The FB2 stage claims one image and produces a six-entry EPUB. A
		// shared budget of six therefore admits either stage alone, but not
		// both stages together.
		limits.resources = 6
		var out bytes.Buffer
		err := convertContextWithLimits(context.Background(), &out, bytes.NewReader(src), format.FormatFB2, int64(len(src)), TargetKEPUB, ConversionOptions{}, limits)
		if !errors.Is(err, ErrResourceLimit) || !strings.Contains(err.Error(), "intermediate EPUB to KEPUB") {
			t.Fatalf("composed conversion error = %v; want shared resource-count failure in KEPUB stage", err)
		}
	})

	t.Run("composed intermediate output", func(t *testing.T) {
		src := testFB2ForEPUB()
		limits := defaultConversionLimits
		limits.outputBytes = 8
		var out bytes.Buffer
		err := convertContextWithLimits(context.Background(), &out, bytes.NewReader(src), format.FormatFB2, int64(len(src)), TargetKEPUB, ConversionOptions{}, limits)
		if !errors.Is(err, ErrResourceLimit) || !strings.Contains(err.Error(), "create intermediate EPUB") {
			t.Fatalf("composed conversion error = %v; want bounded intermediate EPUB failure", err)
		}
	})
}

func TestConvertAZW4ToPDF(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "print-replica.azw4")
	dst := filepath.Join(dir, "print-replica.pdf")
	pdf := []byte("%PDF-1.7\nbody\n%%EOF")
	writeFile(t, src, testMOBIWithPayload(pdf))

	if err := ConvertFile(context.Background(), src, dst, TargetPDF, false); err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(pdf) {
		t.Fatalf("output = %q; want %q", got, pdf)
	}
}

func TestConvertAZW4ToPDFDoesNotOverwriteByDefault(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "print-replica.azw4")
	dst := filepath.Join(dir, "print-replica.pdf")
	writeFile(t, src, testMOBIWithPayload([]byte("%PDF-1.7\nbody\n%%EOF")))
	writeFile(t, dst, []byte("existing"))

	err := ConvertFile(context.Background(), src, dst, TargetPDF, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("ConvertFile error = %v; want output exists error", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "existing" {
		t.Fatalf("output was overwritten: %q", got)
	}
}

func TestConvertAZW4ToPDFOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "print-replica.azw4")
	dst := filepath.Join(dir, "print-replica.pdf")
	pdf := []byte("%PDF-1.7\nbody\n%%EOF")
	writeFile(t, src, testMOBIWithPayload(pdf))
	writeFile(t, dst, []byte("existing"))

	if err := ConvertFile(context.Background(), src, dst, TargetPDF, true); err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(pdf) {
		t.Fatalf("output = %q; want %q", got, pdf)
	}
}

func TestConvertRejectsMOBIToPDF(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "legacy.mobi")
	dst := filepath.Join(dir, "legacy.pdf")
	writeFile(t, src, testMOBIWithPayload([]byte("%PDF-1.7\nbody\n%%EOF")))

	err := ConvertFile(context.Background(), src, dst, TargetPDF, false)
	if err == nil || !strings.Contains(err.Error(), "unsupported conversion from MOBI to pdf") {
		t.Fatalf("ConvertFile error = %v; want unsupported MOBI conversion", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("output exists after rejected conversion: %v", err)
	}
}

func TestConvertTXTToEPUB(t *testing.T) {
	src := []byte("First & line\ncontinues\n\nSecond <paragraph>\n")
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatTXT, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert TXT to EPUB: %v", err)
	}

	if got := zipEntry(t, out.Bytes(), "mimetype"); got != "application/epub+zip" {
		t.Fatalf("mimetype = %q", got)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, "<p>First &amp; line continues</p>") {
		t.Fatalf("text.xhtml missing escaped first paragraph:\n%s", xhtml)
	}
	if !strings.Contains(xhtml, "<p>Second &lt;paragraph&gt;</p>") {
		t.Fatalf("text.xhtml missing escaped second paragraph:\n%s", xhtml)
	}
}

func TestConvertTXTToEPUBUsesFallbackMetadata(t *testing.T) {
	src := []byte("Body.\n")
	var out bytes.Buffer
	opts := ConversionOptions{
		Metadata: &bookmeta.Metadata{
			Title:      "Library Title",
			Authors:    []bookmeta.AuthorMeta{{Name: "Library Author"}},
			Language:   "en",
			Identifier: "isbn:978-0-306-40615-7",
		},
		SourceName: "fallback-title.txt",
	}
	if err := ConvertContextWithOptions(context.Background(), &out, bytes.NewReader(src), format.FormatTXT, int64(len(src)), TargetEPUB, opts); err != nil {
		t.Fatalf("Convert TXT to EPUB: %v", err)
	}

	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		"<dc:title>Library Title</dc:title>",
		"<dc:creator>Library Author</dc:creator>",
		"<dc:language>en</dc:language>",
		"<dc:identifier id=\"pub-id\">urn:isbn:978-0-306-40615-7</dc:identifier>",
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("content.opf missing %q:\n%s", want, opf)
		}
	}
}

func TestConvertMarkdownToEPUBEmbeddedMetadataWinsOverFallback(t *testing.T) {
	src := []byte("# Title: Embedded Title\n\n## Author: Embedded Author\n## Language: fr\n\nBody.\n")
	var out bytes.Buffer
	opts := ConversionOptions{
		Metadata: &bookmeta.Metadata{
			Title:    "Library Title",
			Authors:  []bookmeta.AuthorMeta{{Name: "Library Author"}},
			Language: "en",
		},
	}
	if err := ConvertContextWithOptions(context.Background(), &out, bytes.NewReader(src), format.FormatMarkdown, int64(len(src)), TargetEPUB, opts); err != nil {
		t.Fatalf("Convert Markdown to EPUB: %v", err)
	}

	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		"<dc:title>Embedded Title</dc:title>",
		"<dc:creator>Embedded Author</dc:creator>",
		"<dc:language>fr</dc:language>",
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("content.opf missing %q:\n%s", want, opf)
		}
	}
	for _, forbidden := range []string{"Library Title", "Library Author", "<dc:language>en</dc:language>"} {
		if strings.Contains(opf, forbidden) {
			t.Fatalf("content.opf used fallback over embedded metadata %q:\n%s", forbidden, opf)
		}
	}
}

func TestConvertFileUsesSourceFilenameAsEPUBTitleFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Plain Source Title.txt")
	dst := filepath.Join(dir, "out.epub")
	writeFile(t, src, []byte("Body.\n"))

	if err := ConvertFile(context.Background(), src, dst, TargetEPUB, false); err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}
	epub, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	opf := zipEntry(t, epub, "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>Plain Source Title</dc:title>") {
		t.Fatalf("content.opf missing filename fallback title:\n%s", opf)
	}
}

func TestConvertTXTToEPUBDecodesLegacySingleByteText(t *testing.T) {
	src := converterWindows1251(t, "\u041f\u0435\u0440\u0432\u0430\u044f \u0441\u0442\u0440\u043e\u043a\u0430.\n\n\u0412\u0442\u043e\u0440\u0430\u044f \u0441\u0442\u0440\u043e\u043a\u0430.\n")
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatTXT, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert TXT to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, "<p>\u041f\u0435\u0440\u0432\u0430\u044f \u0441\u0442\u0440\u043e\u043a\u0430.</p>") {
		t.Fatalf("text.xhtml missing decoded Windows-1251 paragraph:\n%s", xhtml)
	}
	if strings.Contains(xhtml, "\uFFFD") || strings.Contains(xhtml, "\u00cf\u00e5\u00f0\u00e2\u00e0\u00ff") {
		t.Fatalf("text.xhtml contains replacement or cp1252 mojibake:\n%s", xhtml)
	}
}

func TestCleanTextForEPUBStripsControlsAndCollapsesBlankRuns(t *testing.T) {
	got := cleanTextForEPUB("a\x00b\x08\n\n\n\n\nc")
	want := "ab\n\n\nc"
	if got != want {
		t.Fatalf("cleanTextForEPUB = %q; want %q", got, want)
	}
}

func TestConvertTXTToEPUBCleansControlsAndPreservesIndentation(t *testing.T) {
	src := []byte("  Indented <line>\x01\ncontinues\x02\n")
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatTXT, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert TXT to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, "<p>&#160;&#160;Indented &lt;line&gt; continues</p>") {
		t.Fatalf("text.xhtml missing cleaned indented paragraph:\n%s", xhtml)
	}
	for _, forbidden := range []string{"\x01", "\x02"} {
		if strings.Contains(xhtml, forbidden) {
			t.Fatalf("text.xhtml retained control char %q:\n%s", forbidden, xhtml)
		}
	}
}

func TestConvertTXTToEPUBFallsBackToLineParagraphs(t *testing.T) {
	src := []byte(strings.Join([]string{
		"First short paragraph.",
		"Second short paragraph.",
		"Third short paragraph.",
		"Fourth short paragraph.",
		"Fifth short paragraph.",
		"Sixth short paragraph.",
	}, "\n") + "\n")
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatTXT, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert TXT to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if strings.Contains(xhtml, "<p>First short paragraph. Second short paragraph.") {
		t.Fatalf("text.xhtml collapsed line paragraphs into one paragraph:\n%s", xhtml)
	}
	for _, want := range []string{
		"<p>First short paragraph.</p>",
		"<p>Sixth short paragraph.</p>",
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
}

func TestConvertMarkdownToEPUB(t *testing.T) {
	src := []byte("# Heading **One** & [Two](https://example.test)\n\nBody **stays bold** and <unsafe> stays escaped.\n")
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatMarkdown, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert Markdown to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<h1 id="heading-one-two">Heading <strong>One</strong> &amp; <a href="https://example.test">Two</a></h1>`) {
		t.Fatalf("text.xhtml missing escaped heading:\n%s", xhtml)
	}
	if !strings.Contains(xhtml, "<p>Body <strong>stays bold</strong> and &lt;unsafe&gt; stays escaped.</p>") {
		t.Fatalf("text.xhtml missing paragraph:\n%s", xhtml)
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#heading-one-two">Heading One &amp; Two</a></li>`) {
		t.Fatalf("nav.xhtml missing markdown heading link:\n%s", nav)
	}
}

func TestConvertMarkdownBlocksAndInlineMarkupToEPUB(t *testing.T) {
	src := []byte(`# Heading

Body with **bold**, *emphasis*, ` + "`code <x>`" + `, [site](https://example.test/path?q=1&x=2), [chapter](#chapter-1), [bad](javascript:evil()), and ![Alt text](https://example.invalid/pic.png).

- First **item**
- Second item

1. One
2. Two

> Quote **line**
> next line

---
`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatMarkdown, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert Markdown to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		"<p>Body with <strong>bold</strong>, <em>emphasis</em>, <code>code &lt;x&gt;</code>, " +
			`<a href="https://example.test/path?q=1&amp;x=2">site</a>, <a href="#chapter-1">chapter</a>, bad, and Alt text.</p>`,
		"<ul>",
		"<li>First <strong>item</strong></li>",
		"<li>Second item</li>",
		"<ol>",
		"<li>One</li>",
		"<li>Two</li>",
		"<blockquote>",
		"<p>Quote <strong>line</strong> next line</p>",
		"<hr/>",
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	for _, forbidden := range []string{"javascript:", "evil", "![", "**bold**", "`code"} {
		if strings.Contains(xhtml, forbidden) {
			t.Fatalf("text.xhtml retained raw/unsafe markdown fragment %q:\n%s", forbidden, xhtml)
		}
	}
}

func TestConvertMarkdownHeadingMetadataToEPUB(t *testing.T) {
	src := []byte("# Title: Alice's Adventures in Wonderland\n\n## Author: Lewis Carroll\n## Year: 1865\n\n---\n\n## Chapter 1\nBody.\n")
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatMarkdown, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert Markdown to EPUB: %v", err)
	}

	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>Alice&#39;s Adventures in Wonderland</dc:title>") {
		t.Fatalf("content.opf missing markdown title:\n%s", opf)
	}
	if !strings.Contains(opf, "<dc:creator>Lewis Carroll</dc:creator>") {
		t.Fatalf("content.opf missing markdown author:\n%s", opf)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<h1 id="title-alice-s-adventures-in-wonderland">Title: Alice&#39;s Adventures in Wonderland</h1>`) {
		t.Fatalf("text.xhtml missing original title heading:\n%s", xhtml)
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#chapter-1">Chapter 1</a></li>`) {
		t.Fatalf("nav.xhtml missing chapter heading link:\n%s", nav)
	}
	for _, forbidden := range []string{"Title: Alice", "Author: Lewis Carroll", "Year: 1865"} {
		if strings.Contains(nav, forbidden) {
			t.Fatalf("nav.xhtml contains leading markdown metadata heading %q:\n%s", forbidden, nav)
		}
	}
}

func TestConvertTXTZToEPUB(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"book.md": []byte("# TXTZ Heading\n\nArchive body.\n"),
		"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Archived Text</dc:title>
    <dc:creator>Text Author</dc:creator>
    <dc:language>en</dc:language>
  </metadata>
</package>`),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatTXTZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert TXTZ to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<h1 id="txtz-heading">TXTZ Heading</h1>`) {
		t.Fatalf("text.xhtml missing markdown heading:\n%s", xhtml)
	}
	if !strings.Contains(xhtml, "<p>Archive body.</p>") {
		t.Fatalf("text.xhtml missing archive body:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>Archived Text</dc:title>") {
		t.Fatalf("content.opf missing TXTZ title:\n%s", opf)
	}
	if !strings.Contains(opf, "<dc:creator>Text Author</dc:creator>") {
		t.Fatalf("content.opf missing TXTZ creator:\n%s", opf)
	}
	if !strings.Contains(opf, "<dc:language>en</dc:language>") {
		t.Fatalf("content.opf missing TXTZ language:\n%s", opf)
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#txtz-heading">TXTZ Heading</a></li>`) {
		t.Fatalf("nav.xhtml missing TXTZ heading link:\n%s", nav)
	}
}

func TestConvertTXTZToEPUBConcatenatesTextEntries(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"02.md": []byte("## Chapter Two\n\nSecond body.\n"),
		"01.md": []byte("# Chapter One\n\nFirst body.\n"),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatTXTZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert TXTZ to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<h1 id="chapter-one">Chapter One</h1>`,
		"<p>First body.</p>",
		`<h2 id="chapter-two">Chapter Two</h2>`,
		"<p>Second body.</p>",
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	if strings.Index(xhtml, "Chapter One") > strings.Index(xhtml, "Chapter Two") {
		t.Fatalf("text.xhtml chapter order is not deterministic archive order:\n%s", xhtml)
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if strings.Index(nav, "text.xhtml#chapter-one") > strings.Index(nav, "text.xhtml#chapter-two") {
		t.Fatalf("nav.xhtml chapter order is not deterministic archive order:\n%s", nav)
	}
}

func TestConvertTXTZToEPUBUsesTextFormattingHint(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"book.txt": []byte("# Hint Heading\n\nBody with **bold**.\n"),
		"metadata.opf": []byte(`<?xml version='1.0' encoding='utf-8'?>
<metadata>
  <text-formatting>markdown</text-formatting>
</metadata>`),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatTXTZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert TXTZ to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<h1 id="hint-heading">Hint Heading</h1>`,
		"<p>Body with <strong>bold</strong>.</p>",
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
}

func TestConvertTXTZTextileToEPUBAsPlainText(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"book.textile": []byte("h1. Textile Heading\n\nBody with <markup>.\n"),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatTXTZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert TXTZ textile to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		"<p>h1. Textile Heading</p>",
		"<p>Body with &lt;markup&gt;.</p>",
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
}

func TestConvertHTMLToEPUB(t *testing.T) {
	src := []byte(`<!doctype html>
<html lang="en">
  <head>
    <title>HTML Book</title>
    <meta name="author" content="Web Writer">
    <meta name="dc.language" content="en">
    <script>alert("head")</script>
  </head>
  <body>
    <h1 onclick="evil()">Heading &amp; One</h1>
    <p>Body <strong>stays bold</strong> and <em>emphasized</em>.</p>
    <script>alert("body")</script>
  </body>
</html>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTML, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTML to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<h1 id="heading-1">Heading &amp; One</h1>`,
		"<p>Body <strong>stays bold</strong> and <em>emphasized</em>.</p>",
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#heading-1">Heading &amp; One</a></li>`) {
		t.Fatalf("nav.xhtml missing generated heading link:\n%s", nav)
	}
	for _, forbidden := range []string{"script", "onclick", "alert"} {
		if strings.Contains(xhtml, forbidden) {
			t.Fatalf("text.xhtml contains unsafe source fragment %q:\n%s", forbidden, xhtml)
		}
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>HTML Book</dc:title>") {
		t.Fatalf("content.opf missing HTML title:\n%s", opf)
	}
	if !strings.Contains(opf, "<dc:creator>Web Writer</dc:creator>") {
		t.Fatalf("content.opf missing HTML creator:\n%s", opf)
	}
	if !strings.Contains(opf, "<dc:language>en</dc:language>") {
		t.Fatalf("content.opf missing HTML language:\n%s", opf)
	}
}

func TestConvertHTMLToEPUBPreservesSafeSemanticAttributes(t *testing.T) {
	src := []byte(`<!doctype html>
<html>
  <head><title>Semantic HTML</title></head>
  <body>
    <section class="chapter main bad<script" role="doc-chapter" dir="rtl" lang="fr" style="color:red" onclick="evil()">
      <h2 class="title">Titre</h2>
      <p class="first child" data-drop="x">Bonjour <a class="noteref" role="doc-noteref" href="#note">1</a></p>
      <img class="cover-art bad<script" src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(converterTinyPNG) + `" alt="cover">
      <p id="note" xml:lang="en-US">Note.</p>
    </section>
  </body>
</html>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTML, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTML to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<div class="chapter main" role="doc-chapter" dir="rtl" lang="fr">`,
		`<h2 id="heading-1" class="title">Titre</h2>`,
		`<p class="first child">Bonjour <a href="#note" class="noteref" role="doc-noteref">1</a></p>`,
		`<img src="images/img1.png" alt="cover" class="cover-art"/>`,
		`<p id="note" xml:lang="en-US">Note.</p>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	for _, forbidden := range []string{"bad<script", "style=", "onclick=", "data-drop="} {
		if strings.Contains(xhtml, forbidden) {
			t.Fatalf("text.xhtml contains unsafe source fragment %q:\n%s", forbidden, xhtml)
		}
	}
}

func TestConvertHTMLToEPUBNormalizesDocumentStructure(t *testing.T) {
	src := []byte(`<!doctype html>
<html>
  <head><title>Messy HTML</title></head>
  <body>
    <h2><blockquote><span>Warm-ups</span></blockquote></h2>
    <div id="repeat"><p>First target.</p></div>
    <div id="repeat"><p>Second target.</p></div>
    <figure><figcaption>Caption</figcaption><div>Extra</div></figure>
    <strong><span>Before <div id="block-anchor" class="marker"><em>block text</em></div> after.</span></strong>
    <p><a href="#block-anchor">Jump to block</a></p>
  </body>
</html>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTML, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTML to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<h2 id="heading-1"><span>Warm-ups</span></h2>`,
		`<div id="repeat"><p>First target.</p>`,
		`<div id="repeat-2"><p>Second target.</p>`,
		`<div><div>Caption</div>`,
		`<div>Extra</div>`,
		`<strong><span>Before <span id="block-anchor" class="marker"><em>block text</em></span> after.</span></strong>`,
		`<a href="#block-anchor">Jump to block</a>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	for _, forbidden := range []string{
		`<h2 id="heading-1"><blockquote>`,
		`id="repeat"><p>Second target.`,
		`<figure>`,
		`<figcaption>`,
		`<span>Before <div`,
	} {
		if strings.Contains(xhtml, forbidden) {
			t.Fatalf("text.xhtml retained invalid or duplicate fragment %q:\n%s", forbidden, xhtml)
		}
	}
}

func TestConvertHTMLToEPUBUsesFallbackMetadataWhenMissing(t *testing.T) {
	src := []byte(`<!doctype html><html><body><p>Body.</p></body></html>`)
	var out bytes.Buffer
	opts := ConversionOptions{
		Metadata: &bookmeta.Metadata{
			Title:    "Library HTML",
			Authors:  []bookmeta.AuthorMeta{{Name: "Web Curator"}},
			Language: "en",
		},
	}
	if err := ConvertContextWithOptions(context.Background(), &out, bytes.NewReader(src), format.FormatHTML, int64(len(src)), TargetEPUB, opts); err != nil {
		t.Fatalf("Convert HTML to EPUB: %v", err)
	}

	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		"<dc:title>Library HTML</dc:title>",
		"<dc:creator>Web Curator</dc:creator>",
		"<dc:language>en</dc:language>",
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("content.opf missing %q:\n%s", want, opf)
		}
	}
}

func TestConvertHTMLToEPUBDecodesDeclaredCharset(t *testing.T) {
	title := "\u041a\u043d\u0438\u0433\u0430"
	author := "\u0410\u0432\u0442\u043e\u0440"
	body := "\u0422\u0435\u043a\u0441\u0442 \u0441\u0442\u0440\u0430\u043d\u0438\u0446\u044b"
	src := converterWindows1251(t, `<!doctype html>
<html>
  <head>
    <meta charset="windows-1251">
    <title>`+title+`</title>
    <meta name="author" content="`+author+`">
  </head>
  <body><p>`+body+`</p></body>
</html>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTML, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTML to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, "<p>"+body+"</p>") {
		t.Fatalf("text.xhtml missing decoded body:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>"+title+"</dc:title>") {
		t.Fatalf("content.opf missing decoded title:\n%s", opf)
	}
	if !strings.Contains(opf, "<dc:creator>"+author+"</dc:creator>") {
		t.Fatalf("content.opf missing decoded author:\n%s", opf)
	}
}

func TestConvertHTMLToEPUBPreservesSafeLinksAndAnchors(t *testing.T) {
	src := []byte(`<!doctype html>
<html>
  <head><title>Linked HTML</title></head>
  <body>
    <nav>
      <a href="#chapter-1">Chapter</a>
      <a href="https://example.test/path?q=1&amp;x=2">External</a>
      <a href="javascript:alert(1)">Bad</a>
      <a href="notes.html#note-1">Missing local file</a>
    </nav>
    <h1 id="chapter-1" onclick="evil()">Chapter One</h1>
    <p><a name="note-1"></a>Footnote target.</p>
  </body>
</html>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTML, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTML to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<a href="#chapter-1">Chapter</a>`,
		`<a href="https://example.test/path?q=1&amp;x=2">External</a>`,
		`<h1 id="chapter-1">Chapter One</h1>`,
		`<p><a id="note-1"></a>Footnote target.</p>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	for _, forbidden := range []string{"javascript:", "alert", "notes.html", "onclick"} {
		if strings.Contains(xhtml, forbidden) {
			t.Fatalf("text.xhtml contains unsafe or unsupported link fragment %q:\n%s", forbidden, xhtml)
		}
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#chapter-1">Chapter One</a></li>`) {
		t.Fatalf("nav.xhtml missing existing-anchor heading link:\n%s", nav)
	}
}

func TestConvertHTMLToEPUBBuildsNavFromInternalLinks(t *testing.T) {
	src := []byte(`<!doctype html>
<html>
  <head><title>Linked Contents</title></head>
  <body>
    <nav>
      <a href="#part-2">Part Two</a>
      <a href="#part-1">Part One</a>
      <a href="#missing">Missing</a>
      <a href="#part-2">Duplicate Target</a>
      <a href="#part-3"><img alt="Image Label"></a>
    </nav>
    <div id="part-1"><p>First section.</p></div>
    <div id="part-2"><p>Second section.</p></div>
    <div id="part-3"><p>Third section.</p></div>
  </body>
</html>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTML, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTML to EPUB: %v", err)
	}

	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	for _, want := range []string{
		`<li><a href="text.xhtml#part-1">Part One</a></li>`,
		`<li><a href="text.xhtml#part-2">Part Two</a></li>`,
		`<li><a href="text.xhtml#part-3">Image Label</a></li>`,
	} {
		if !strings.Contains(nav, want) {
			t.Fatalf("nav.xhtml missing %q:\n%s", want, nav)
		}
	}
	for _, forbidden := range []string{"Missing", "Duplicate Target"} {
		if strings.Contains(nav, forbidden) {
			t.Fatalf("nav.xhtml contains unsupported duplicate/missing link %q:\n%s", forbidden, nav)
		}
	}
	partOne := strings.Index(nav, `text.xhtml#part-1`)
	partTwo := strings.Index(nav, `text.xhtml#part-2`)
	partThree := strings.Index(nav, `text.xhtml#part-3`)
	if partOne < 0 || partTwo < 0 || partThree < 0 || partOne > partTwo || partTwo > partThree {
		t.Fatalf("nav.xhtml did not order link fallback by target reading order:\n%s", nav)
	}
}

func TestConvertHTMLToEPUBKeepsDataURIImages(t *testing.T) {
	src := []byte(`<!doctype html>
<html>
  <head>
    <title>Image HTML</title>
    <link rel="cover" href="data:image/png;base64,` + base64.StdEncoding.EncodeToString(converterTinyPNG) + `">
  </head>
  <body>
    <p>Inline <img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(converterTinyPNG) + `" alt="Picture"></p>
    <p>Remote <img src="https://example.invalid/pic.png" alt="Remote"></p>
  </body>
</html>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTML, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTML to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<img src="images/img1.png" alt="Picture"/>`) {
		t.Fatalf("text.xhtml missing embedded data image:\n%s", xhtml)
	}
	if strings.Contains(xhtml, "example.invalid") {
		t.Fatalf("text.xhtml retained external image reference:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, `<meta name="cover" content="img1"/>`) {
		t.Fatalf("content.opf missing HTML cover meta:\n%s", opf)
	}
	if !strings.Contains(opf, `<item id="img1" href="images/img1.png" media-type="image/png" properties="cover-image"/>`) {
		t.Fatalf("content.opf missing data image asset:\n%s", opf)
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/images/img1.png"); !bytes.Equal(got, converterTinyPNG) {
		t.Fatalf("HTML data image bytes = %d; want copied PNG", len(got))
	}
}

func TestConvertXHTMLToEPUB(t *testing.T) {
	src := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en">
  <head><title>XHTML Book</title></head>
  <body>
    <section>
      <h1>XHTML Heading</h1>
      <p>Body with <strong>safe inline markup</strong>.</p>
      <style>body { color: red; }</style>
    </section>
  </body>
</html>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatXHTML, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert XHTML to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<h1 id="heading-1">XHTML Heading</h1>`,
		"<p>Body with <strong>safe inline markup</strong>.</p>",
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#heading-1">XHTML Heading</a></li>`) {
		t.Fatalf("nav.xhtml missing XHTML heading link:\n%s", nav)
	}
	if strings.Contains(xhtml, "color: red") {
		t.Fatalf("text.xhtml retained stripped XHTML style:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>XHTML Book</dc:title>") {
		t.Fatalf("content.opf missing XHTML title:\n%s", opf)
	}
}

func TestConvertHTMLZToEPUB(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"index.html": []byte(`<!doctype html>
<html>
  <head><title>Fallback Title</title></head>
  <body>
    <h1>HTMLZ Heading</h1>
    <p>Body with <img src="images/pic.png" alt="Picture"> and <img src="https://example.invalid/remote.jpg" alt="remote">.</p>
  </body>
</html>`),
		"images/pic.png": converterTinyPNG,
		"metadata.opf": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Archived HTML</dc:title>
    <dc:creator>HTMLZ Author</dc:creator>
    <dc:language>en</dc:language>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="cover-image" href="cover.gif" media-type="image/gif"/>
  </manifest>
</package>`),
		"cover.gif": converterTinyGIF,
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<h1 id="heading-1">HTMLZ Heading</h1>`,
		`<img src="images/image1.png" alt="Picture"/>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#heading-1">HTMLZ Heading</a></li>`) {
		t.Fatalf("nav.xhtml missing HTMLZ heading link:\n%s", nav)
	}
	if strings.Contains(xhtml, "remote.jpg") || strings.Contains(xhtml, "https://") {
		t.Fatalf("text.xhtml retained remote image reference:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		"<dc:title>Archived HTML</dc:title>",
		"<dc:creator>HTMLZ Author</dc:creator>",
		"<dc:language>en</dc:language>",
		`<item id="img1" href="images/image1.png" media-type="image/png"/>`,
		`<meta name="cover" content="cover-image"/>`,
		`<item id="cover-image" href="images/cover.gif" media-type="image/gif" properties="cover-image"/>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("content.opf missing %q:\n%s", want, opf)
		}
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/images/image1.png"); !bytes.Equal(got, converterTinyPNG) {
		t.Fatalf("HTMLZ image bytes = %d; want copied PNG", len(got))
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/images/cover.gif"); !bytes.Equal(got, converterTinyGIF) {
		t.Fatalf("HTMLZ cover bytes = %d; want copied GIF", len(got))
	}
}

func TestConvertHTMLZToEPUBCopiesSVGImageResource(t *testing.T) {
	svg := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`)
	src := testZip(t, map[string][]byte{
		"index.html": []byte(`<!doctype html>
<html>
  <head><title>SVG HTMLZ</title></head>
  <body>
    <h1>Diagram</h1>
    <p><img src="images/diagram.svg" alt="Diagram"></p>
  </body>
</html>`),
		"images/diagram.svg": svg,
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<img src="images/image1.svg" alt="Diagram"/>`) {
		t.Fatalf("text.xhtml missing SVG image reference:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, `<item id="img1" href="images/image1.svg" media-type="image/svg+xml"/>`) {
		t.Fatalf("content.opf missing SVG manifest item:\n%s", opf)
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/images/image1.svg"); !bytes.Equal(got, svg) {
		t.Fatalf("HTMLZ SVG bytes = %d; want copied SVG", len(got))
	}
}

func TestConvertHTMLZToEPUBOmitsAmbiguousImageResource(t *testing.T) {
	src := testZipWithHeaders(t, []testZipEntry{
		{name: "index.html", data: []byte(`<!doctype html><html><head><title>Ambiguous image</title></head><body><p><img src="Images/pic.png" alt="Ambiguous"></p></body></html>`)},
		{name: "images/pic.png", data: converterTinyPNG},
		{name: "IMAGES/PIC.PNG", data: converterTinyPNG},
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	if zipHasEntry(t, out.Bytes(), "OEBPS/images/image1.png") {
		t.Fatal("ambiguous HTMLZ image was selected arbitrarily")
	}
	if strings.Contains(zipEntry(t, out.Bytes(), "OEBPS/text.xhtml"), "<img") {
		t.Fatal("converted document retained a reference to an unresolved ambiguous image")
	}
}

func TestConvertHTMLZToEPUBSanitizesSVGImageResource(t *testing.T) {
	svg := []byte(`<?xml version="1.0" standalone="no"?>
<!DOCTYPE svg PUBLIC "-//W3C/DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">
<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">
<defs>
<path id="glyph" d="M 0 0 L 1 1"/>
<path id="glyph" d="M 0 0 L 1 1"/>
</defs>
<use href="#glyph"/>
</svg>`)
	src := testZip(t, map[string][]byte{
		"index.html": []byte(`<!doctype html>
<html>
  <head><title>Sanitized SVG HTMLZ</title></head>
  <body><p><img src="images/diagram.svg" alt="Diagram"></p></body>
</html>`),
		"images/diagram.svg": svg,
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	got := string(zipEntryBytes(t, out.Bytes(), "OEBPS/images/image1.svg"))
	if strings.Contains(strings.ToLower(got), "<!doctype") {
		t.Fatalf("SVG resource kept DOCTYPE:\n%s", got)
	}
	if count := strings.Count(got, `id="glyph"`); count != 1 {
		t.Fatalf("SVG resource has %d glyph ids; want duplicate definitions removed:\n%s", count, got)
	}
}

func TestConvertHTMLZToEPUBBuildsNavFromIndexLinks(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"index.html": []byte(`<!doctype html>
<html>
  <head><title>HTMLZ Linked Contents</title></head>
  <body>
    <nav>
      <a href="index.html#alpha">Alpha</a>
      <a href="./index.html#beta">Beta</a>
      <a href="other.html#alpha">Other Page</a>
    </nav>
    <div id="alpha"><p>Alpha body.</p></div>
    <div id="beta"><p>Beta body.</p></div>
  </body>
</html>`),
		"other.html": []byte(`<html><body><div id="alpha">Other</div></body></html>`),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	for _, want := range []string{
		`<li><a href="text.xhtml#alpha">Alpha</a></li>`,
		`<li><a href="text.xhtml#beta">Beta</a></li>`,
	} {
		if !strings.Contains(nav, want) {
			t.Fatalf("nav.xhtml missing %q:\n%s", want, nav)
		}
	}
	if strings.Contains(nav, "Other Page") {
		t.Fatalf("nav.xhtml included link outside selected HTMLZ document:\n%s", nav)
	}
}

func TestConvertHTMLZToEPUBPrefersHeadingsOverLinkFallback(t *testing.T) {
	var html strings.Builder
	html.WriteString(`<!doctype html><html><head><title>Footnotes</title></head><body><h1>Chapter One</h1><p>`)
	for i := 1; i <= 8; i++ {
		html.WriteString(`<a href="#fn`)
		html.WriteByte(byte('0' + i))
		html.WriteString(`">Footnote `)
		html.WriteByte(byte('0' + i))
		html.WriteString(`</a> `)
	}
	html.WriteString(`</p>`)
	for i := 1; i <= 8; i++ {
		html.WriteString(`<p id="fn`)
		html.WriteByte(byte('0' + i))
		html.WriteString(`">Footnote text</p>`)
	}
	html.WriteString(`</body></html>`)

	src := testZip(t, map[string][]byte{
		"index.html": []byte(html.String()),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#heading-1">Chapter One</a></li>`) {
		t.Fatalf("nav.xhtml missing heading navigation:\n%s", nav)
	}
	if strings.Contains(nav, "Footnote") {
		t.Fatalf("nav.xhtml used link fallback despite heading navigation:\n%s", nav)
	}
}

func TestConvertHTMLZToEPUBBuildsNavFromPackageNav(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"index.html": []byte(`<!doctype html>
<html>
  <head><title>Package Nav</title></head>
  <body>
    <div id="alpha"><p>Alpha body.</p></div>
    <div id="beta"><p>Beta body.</p></div>
  </body>
</html>`),
		"content.opf": []byte(`<?xml version="1.1" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Package Nav</dc:title>
  </metadata>
  <manifest>
    <item id="nav" href="toc.xhtml" media-type="application/xhtml+xml" properties="nav"/>
  </manifest>
</package>`),
		"toc.xhtml": []byte(`<!doctype html>
<html>
  <body>
    <nav epub:type="toc">
      <ol>
        <li><a href="index.html#alpha">Alpha Chapter</a></li>
        <li><a href="./index.html#beta">Beta Chapter</a></li>
        <li><a href="other.html#gamma">Other Page</a></li>
      </ol>
    </nav>
  </body>
</html>`),
		"other.html": []byte(`<html><body><div id="gamma">Other</div></body></html>`),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	for _, want := range []string{
		`<li><a href="text.xhtml#alpha">Alpha Chapter</a></li>`,
		`<li><a href="text.xhtml#beta">Beta Chapter</a></li>`,
	} {
		if !strings.Contains(nav, want) {
			t.Fatalf("nav.xhtml missing %q:\n%s", want, nav)
		}
	}
	if strings.Contains(nav, "Other Page") {
		t.Fatalf("nav.xhtml included package TOC link outside selected HTMLZ document:\n%s", nav)
	}
}

func TestConvertHTMLZToEPUBBuildsNavFromPackageNCX(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"index.html": []byte(`<!doctype html>
<html>
  <head><title>Package NCX</title></head>
  <body>
    <div id="one"><p>One body.</p></div>
    <div id="two"><p>Two body.</p></div>
  </body>
</html>`),
		"content.opf": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Package NCX</dc:title>
  </metadata>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
  </manifest>
  <spine toc="ncx"/>
</package>`),
		"toc.ncx": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/">
  <navMap>
    <navPoint id="nav2" playOrder="2">
      <navLabel><text>Two NCX</text></navLabel>
      <content src="index.html#two"/>
    </navPoint>
    <navPoint id="nav1" playOrder="1">
      <navLabel><text>One NCX</text></navLabel>
      <content src="index.html#one"/>
    </navPoint>
  </navMap>
</ncx>`),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	for _, want := range []string{
		`<li><a href="text.xhtml#one">One NCX</a></li>`,
		`<li><a href="text.xhtml#two">Two NCX</a></li>`,
	} {
		if !strings.Contains(nav, want) {
			t.Fatalf("nav.xhtml missing %q:\n%s", want, nav)
		}
	}
	one := strings.Index(nav, `text.xhtml#one`)
	two := strings.Index(nav, `text.xhtml#two`)
	if one < 0 || two < 0 || one > two {
		t.Fatalf("nav.xhtml did not honor NCX playOrder:\n%s", nav)
	}
}

func TestConvertHTMLZToEPUBIgnoresAmbiguousPackageNavigation(t *testing.T) {
	src := testZipWithHeaders(t, []testZipEntry{
		{name: "index.html", data: []byte(`<!doctype html><html><head><title>Ambiguous nav</title></head><body><div id="chapter">Readable body.</div></body></html>`)},
		{name: "content.opf", data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Ambiguous nav</dc:title></metadata>
  <manifest><item id="nav" href="TOC.xhtml" media-type="application/xhtml+xml" properties="nav"/></manifest>
</package>`)},
		{name: "toc.xhtml", data: []byte(`<html><body><nav><ol><li><a href="index.html#chapter">First arbitrary nav</a></li></ol></nav></body></html>`)},
		{name: "Toc.xhtml", data: []byte(`<html><body><nav><ol><li><a href="index.html#chapter">Second arbitrary nav</a></li></ol></nav></body></html>`)},
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}

	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if strings.Contains(nav, "arbitrary nav") {
		t.Fatalf("ambiguous HTMLZ navigation member was selected:\n%s", nav)
	}
}

func TestConvertHTMLZToEPUBIgnoresMalformedOptionalMetadata(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"index.html":   []byte(`<html><head><title>HTML Fallback</title></head><body><p>Readable body.</p></body></html>`),
		"metadata.opf": []byte(`<package><metadata>`),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ to EPUB: %v", err)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>HTML Fallback</dc:title>") {
		t.Fatalf("content.opf did not fall back to HTML metadata:\n%s", opf)
	}
}

func TestHTMLZMetadataForEPUBHonorsCancellation(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"index.html": []byte(`<html><head><title>Fallback Title</title></head><body><p>Body.</p></body></html>`),
		"metadata.opf": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>OPF Title</dc:title>
  </metadata>
</package>`),
	})
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatalf("open HTMLZ zip: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = htmlZMetadataForEPUB(ctx, zr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("htmlZMetadataForEPUB error = %v; want context.Canceled", err)
	}
}

func TestConvertHTMLZXHTMLIndexToEPUB(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"index.xhtml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <head><title>Archived XHTML</title></head>
  <body><p>Readable XHTMLZ body.</p></body>
</html>`),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatHTMLZ, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTMLZ XHTML to EPUB: %v", err)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, "<p>Readable XHTMLZ body.</p>") {
		t.Fatalf("text.xhtml missing XHTMLZ body:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>Archived XHTML</dc:title>") {
		t.Fatalf("content.opf missing XHTML title:\n%s", opf)
	}
}

func TestConvertEPUBToKEPUB(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"mimetype": []byte("application/epub+zip"),
		"META-INF/container.xml": []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`),
		"OEBPS/content.opf": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Kobo Source</dc:title>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="text" href="text.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover-image" href="cover.png" media-type="image/png"/>
  </manifest>
  <spine><itemref idref="text"/></spine>
</package>`),
		"OEBPS/text.xhtml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <head><title>Kobo Source</title></head>
  <body><p>First sentence. Second sentence.</p><p><img src="cover.png" alt="cover"/></p></body>
</html>`),
		"OEBPS/cover.png":                converterTinyPNG,
		"META-INF/calibre_bookmarks.txt": []byte("old bookmarks"),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB); err != nil {
		t.Fatalf("Convert EPUB to KEPUB: %v", err)
	}

	if got := zipEntry(t, out.Bytes(), "mimetype"); got != "application/epub+zip" {
		t.Fatalf("mimetype = %q", got)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, `id="cover-image" href="cover.png" media-type="image/png" properties="cover-image"`) {
		t.Fatalf("content.opf did not mark cover-image manifest item:\n%s", opf)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`id="book-columns"`,
		`id="book-inner"`,
		`id="kobostylehacks"`,
		`class="koboSpan" id="kobo.1.1">First sentence. </span>`,
		`class="koboSpan" id="kobo.1.2">Second sentence.</span>`,
		`class="koboSpan" id="kobo.2.1"><img src="cover.png" alt="cover"/></span>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/cover.png"); !bytes.Equal(got, converterTinyPNG) {
		t.Fatalf("cover image bytes = %d; want copied PNG", len(got))
	}
	if zipHasEntry(t, out.Bytes(), "META-INF/calibre_bookmarks.txt") {
		t.Fatalf("KEPUB retained calibre_bookmarks.txt")
	}
	assertKEPUBConversionContract(t, src, out.Bytes(), "OEBPS/content.opf")

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB); err != nil {
		t.Fatalf("repeat EPUB to KEPUB conversion: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("same EPUB source produced different KEPUB archive bytes")
	}
}

func TestConvertEPUBToKEPUBNormalizesCompatibleOPFVariants(t *testing.T) {
	legacyOPF := converterWindows1251(t, `<?xml version="1.0" encoding="windows-1251"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Книга</dc:title></metadata>
  <manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`)
	xml11OPF := []byte(`<?xml version="1.1" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>XML 1.1 Book</dc:title></metadata>
  <manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`)

	for _, tt := range []struct {
		name  string
		opf   []byte
		title string
	}{
		{name: "legacy encoding", opf: legacyOPF, title: "Книга"},
		{name: "XML 1.1 declaration", opf: xml11OPF, title: "XML 1.1 Book"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := testZip(t, map[string][]byte{
				"mimetype":               []byte("application/epub+zip"),
				"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
				"OEBPS/content.opf":      tt.opf,
				"OEBPS/text.xhtml":       []byte(`<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><body><p>Readable text.</p></body></html>`),
			})
			var out bytes.Buffer
			if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB); err != nil {
				t.Fatalf("Convert EPUB to KEPUB: %v", err)
			}
			opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
			if !strings.Contains(opf, tt.title) || strings.Contains(opf, `version="1.1"`) || !strings.Contains(opf, `encoding="UTF-8"`) {
				t.Fatalf("normalized KEPUB OPF is wrong:\n%s", opf)
			}
		})
	}
}

func TestConvertEPUBToEPUBRepairsPackageWithoutChangingContent(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chapter"/></spine>
</package>
trailing producer junk`)
	chapter := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Keep this text.</p></body></html>`)
	unknown := []byte{0x00, 0x01, 0x02, 0xff}

	for _, tt := range []struct {
		name      string
		container []byte
	}{
		{name: "missing container"},
		{name: "stale rootfile", container: []byte(`<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles><rootfile full-path="package.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entries := []testZipEntry{
				{name: "OPS/chapter.xhtml", data: chapter},
				{name: "mimetype", data: []byte("wrong producer marker")},
				{name: "OPS/package.opf", data: opf},
				{name: "OPS/unknown.bin", data: unknown},
				{name: "__MACOSX/._package.opf", data: []byte("debris")},
			}
			if tt.container != nil {
				entries = append(entries, testZipEntry{name: "META-INF/container.xml", data: tt.container})
			}
			src := testZipWithHeaders(t, entries)

			var out bytes.Buffer
			if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
				t.Fatalf("rebuild EPUB: %v", err)
			}
			zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatalf("open rebuilt EPUB: %v", err)
			}
			if len(zr.File) < 2 || zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store || zr.File[1].Name != "META-INF/container.xml" {
				t.Fatalf("rebuilt EPUB prefix = %+v; want stored mimetype then container.xml", zr.File[:min(len(zr.File), 2)])
			}
			if got := zipEntry(t, out.Bytes(), "mimetype"); got != "application/epub+zip" {
				t.Fatalf("mimetype = %q", got)
			}
			if got := zipEntry(t, out.Bytes(), "META-INF/container.xml"); !strings.Contains(got, `full-path="OPS/package.opf"`) {
				t.Fatalf("container.xml does not point to recovered OPF:\n%s", got)
			}
			if got := zipEntry(t, out.Bytes(), "OPS/package.opf"); strings.Contains(got, "producer junk") || !strings.HasSuffix(got, "</package>\n") {
				t.Fatalf("rebuilt OPF retained trailing junk or unstable ending:\n%s", got)
			}
			if got := zipEntryBytes(t, out.Bytes(), "OPS/chapter.xhtml"); !bytes.Equal(got, chapter) {
				t.Fatal("rebuilt EPUB changed chapter bytes")
			}
			if got := zipEntryBytes(t, out.Bytes(), "OPS/unknown.bin"); !bytes.Equal(got, unknown) {
				t.Fatal("rebuilt EPUB changed unrelated entry bytes")
			}
			if zipHasEntry(t, out.Bytes(), "__MACOSX/._package.opf") {
				t.Fatal("rebuilt EPUB retained known packaging debris")
			}

			var repeated bytes.Buffer
			if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
				t.Fatalf("repeat EPUB rebuild: %v", err)
			}
			if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
				t.Fatal("second EPUB rebuild changed archive bytes")
			}
		})
	}
}

func TestConvertEPUBToEPUBNormalizesDeclaredOPFEncoding(t *testing.T) {
	opf := converterWindows1251(t, `<?xml version="1.0" encoding="windows-1251"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Рђ</dc:title></metadata>
  <manifest>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="declared-debris" href="Thumbs.DB" media-type="application/octet-stream"/>
  </manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`),
		"OPS/THUMBS.DB":          []byte("manifest-declared resource"),
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild Windows-1251 EPUB: %v", err)
	}
	got := zipEntry(t, out.Bytes(), "OPS/package.opf")
	if !strings.Contains(got, `encoding="UTF-8"`) || !strings.Contains(got, "Рђ") || strings.Contains(got, "А") || strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("rebuilt OPF encoding was not normalized safely:\n%s", got)
	}
	if got := zipEntry(t, out.Bytes(), "OPS/THUMBS.DB"); got != "manifest-declared resource" {
		t.Fatalf("rebuilt EPUB lost manifest-declared resource: %q", got)
	}
}

func TestConvertEPUBToEPUBNormalizesXML11Package(t *testing.T) {
	opf := []byte(`<?xml version="1.1" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`)
	chapter := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <head><title>Preserved chapter</title></head>
  <body><p>Preserved text.</p><data value="unchanged">Preserved data.</data></body>
</html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild XML 1.1 EPUB: %v", err)
	}
	gotOPF := zipEntry(t, out.Bytes(), "OPS/package.opf")
	if !strings.Contains(gotOPF, `version="1.0"`) || strings.Contains(gotOPF, `version="1.1"`) {
		t.Fatalf("rebuilt OPF did not normalize XML version:\n%s", gotOPF)
	}
	if gotChapter := zipEntryBytes(t, out.Bytes(), "OPS/chapter.xhtml"); !bytes.Equal(gotChapter, chapter) {
		t.Fatalf("XML 1.1 package normalization changed unrelated chapter bytes:\n%s", gotChapter)
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat XML 1.1 EPUB rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second XML 1.1 EPUB rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBRepairsAdeptResourceMetadata(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`)
	chapter := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <head>
    <title>Producer metadata</title>
    <meta name="Adept.resource" value="urn:uuid:test"/>
    <meta name="Adept.resource" content="already-canonical" value="unchanged"/>
    <meta name="unrelated" value="unchanged"/>
  </head>
  <body><p>Preserved text.</p><data value="unchanged">Preserved data.</data></body>
</html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB with Adept metadata: %v", err)
	}
	gotChapter := zipEntry(t, out.Bytes(), "OPS/chapter.xhtml")
	if !strings.Contains(gotChapter, `<meta name="Adept.resource" content="urn:uuid:test"/>`) {
		t.Fatalf("rebuilt XHTML did not repair Adept.resource metadata:\n%s", gotChapter)
	}
	for _, preserved := range []string{
		`<meta name="Adept.resource" content="already-canonical" value="unchanged"/>`,
		`<meta name="unrelated" value="unchanged"/>`,
		`<data value="unchanged">Preserved data.</data>`,
	} {
		if !strings.Contains(gotChapter, preserved) {
			t.Fatalf("rebuilt XHTML changed unrelated value attribute %q:\n%s", preserved, gotChapter)
		}
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat Adept metadata EPUB rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second Adept metadata EPUB rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBRemovesRedundantEPUB3PageMapPointer(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="legacy-pages" href="page-map.xml" media-type="application/oebps-page-map+xml"/>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
  </manifest>
  <spine page-map="legacy-pages" toc="legacy-toc"><itemref idref="chapter"/></spine>
</package>`)
	chapter := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Preserved text.</p></body></html>`)
	pageMap := []byte(`<page-map xmlns="http://www.idpf.org/2007/opf"><page name="1" href="chapter.xhtml"/></page-map>`)
	nav := []byte(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="page-list"><ol><li><a href="chapter.xhtml">1</a></li></ol></nav></body></html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
		"OPS/page-map.xml":       pageMap,
		"OPS/nav.xhtml":          nav,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB3 with legacy page-map: %v", err)
	}
	gotOPF := zipEntry(t, out.Bytes(), "OPS/package.opf")
	if strings.Contains(gotOPF, `page-map="legacy-pages"`) {
		t.Fatalf("rebuilt EPUB3 retained legacy spine page-map pointer:\n%s", gotOPF)
	}
	for _, preserved := range []string{
		`<item id="legacy-pages" href="page-map.xml" media-type="application/oebps-page-map+xml"/>`,
		`<spine toc="legacy-toc"><itemref idref="chapter"/></spine>`,
	} {
		if !strings.Contains(gotOPF, preserved) {
			t.Fatalf("rebuilt EPUB3 changed package content %q:\n%s", preserved, gotOPF)
		}
	}
	if got := zipEntryBytes(t, out.Bytes(), "OPS/page-map.xml"); !bytes.Equal(got, pageMap) {
		t.Fatal("rebuilt EPUB3 changed the preserved legacy page-map resource")
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat EPUB3 page-map rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second EPUB3 page-map rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBPreservesNonredundantPageMapPointer(t *testing.T) {
	for _, tt := range []struct {
		name       string
		version    string
		navType    string
		navHasLink bool
	}{
		{name: "EPUB2", version: "2.0", navType: "page-list", navHasLink: true},
		{name: "EPUB3 without page list", version: "3.0", navType: "toc", navHasLink: true},
		{name: "EPUB3 with empty page list", version: "3.0", navType: "page-list"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opf := []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="%s">
  <manifest>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="legacy-pages" href="page-map.xml" media-type="application/oebps-page-map+xml"/>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
  </manifest>
  <spine page-map="legacy-pages"><itemref idref="chapter"/></spine>
</package>`, tt.version))
			navContent := "<ol></ol>"
			if tt.navHasLink {
				navContent = `<ol><li><a href="chapter.xhtml">1</a></li></ol>`
			}
			nav := []byte(fmt.Sprintf(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="%s">%s</nav></body></html>`, tt.navType, navContent))
			src := testZip(t, map[string][]byte{
				"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
				"OPS/package.opf":        opf,
				"OPS/chapter.xhtml":      []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`),
				"OPS/page-map.xml":       []byte(`<page-map xmlns="http://www.idpf.org/2007/opf"><page name="1" href="chapter.xhtml"/></page-map>`),
				"OPS/nav.xhtml":          nav,
			})

			var out bytes.Buffer
			if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
				t.Fatalf("rebuild EPUB with nonredundant page-map: %v", err)
			}
			if got := zipEntryBytes(t, out.Bytes(), "OPS/package.opf"); !bytes.Equal(got, opf) {
				t.Fatalf("rebuilt EPUB changed nonredundant page-map package:\n%s", got)
			}
		})
	}
}

func TestConvertEPUBToEPUBRepairsEPUB3ContentDeclarations(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest>
    <item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>
    <item id="dedication" href="dedication.xhtml" media-type="application/xhtml+xml"/>
    <item id="glossary" href="glossary.xhtml" media-type="application/xhtml+xml"/>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
  </manifest>
  <spine><itemref idref="cover"/><itemref idref="dedication"/><itemref idref="glossary"/><itemref idref="chapter"/></spine>
</package>`)
	cover := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body epub:type="cover" role="doc-cover" class="preserved"><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><title>Preserved cover</title></svg></body></html>`)
	dedication := []byte(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body epub:type="dedication" role="doc-dedication"><p>For everyone.</p></body></html>`)
	glossary := []byte(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body epub:type="glossary" role="doc-glossary"><p>Term.</p></body></html>`)
	chapter := []byte(`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body epub:type="chapter" role="doc-cover"><p role="doc-glossary">Preserved unmatched roles.</p></body></html>`)
	nav := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><ol><li><a href="cover.xhtml">Cover</a></li></ol></nav></body></html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/cover.xhtml":        cover,
		"OPS/dedication.xhtml":   dedication,
		"OPS/glossary.xhtml":     glossary,
		"OPS/chapter.xhtml":      chapter,
		"OPS/nav.xhtml":          nav,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB3 content declarations: %v", err)
	}
	gotOPF := zipEntry(t, out.Bytes(), "OPS/package.opf")
	if !strings.Contains(gotOPF, `<item id="cover" href="cover.xhtml" media-type="application/xhtml+xml" properties="svg"/>`) {
		t.Fatalf("rebuilt EPUB3 did not declare inline SVG use:\n%s", gotOPF)
	}
	for _, entry := range []string{"cover.xhtml", "dedication.xhtml", "glossary.xhtml"} {
		got := zipEntry(t, out.Bytes(), "OPS/"+entry)
		if strings.Contains(got, ` role="doc-`) {
			t.Fatalf("rebuilt EPUB3 retained redundant body role in %s:\n%s", entry, got)
		}
	}
	gotCover := zipEntry(t, out.Bytes(), "OPS/cover.xhtml")
	for _, preserved := range []string{`epub:type="cover"`, `class="preserved"`, `<title>Preserved cover</title>`} {
		if !strings.Contains(gotCover, preserved) {
			t.Fatalf("rebuilt EPUB3 changed cover content %q:\n%s", preserved, gotCover)
		}
	}
	if gotChapter := zipEntryBytes(t, out.Bytes(), "OPS/chapter.xhtml"); !bytes.Equal(gotChapter, chapter) {
		t.Fatalf("rebuilt EPUB3 changed unmatched roles:\n%s", gotChapter)
	}
	gotNav := zipEntry(t, out.Bytes(), "OPS/nav.xhtml")
	if !strings.Contains(gotNav, "\n<!DOCTYPE html>\n") || strings.Contains(gotNav, "XHTML 1.0 Strict") {
		t.Fatalf("rebuilt EPUB3 did not normalize the nav doctype:\n%s", gotNav)
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat EPUB3 content-declaration rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second EPUB3 content-declaration rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBPreservesEPUB2ContentDeclarations(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`)
	chapter := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body epub:type="cover" role="doc-cover"><svg xmlns="http://www.w3.org/2000/svg"><title>EPUB2</title></svg></body></html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB2 content declarations: %v", err)
	}
	if got := zipEntryBytes(t, out.Bytes(), "OPS/package.opf"); !bytes.Equal(got, opf) {
		t.Fatalf("rebuilt EPUB2 changed package declarations:\n%s", got)
	}
	if got := zipEntryBytes(t, out.Bytes(), "OPS/chapter.xhtml"); !bytes.Equal(got, chapter) {
		t.Fatalf("rebuilt EPUB2 changed content declarations:\n%s", got)
	}
}

func TestConvertEPUBToEPUBRemovesOnlyMissingPresentationReferences(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <manifest>
    <!-- keep literal <item id="missing-font" href="comment.ttf" media-type="application/x-font-ttf"/> note -->
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="style" href="style.css" media-type="text/css"/>
    <item id="missing-style" href="missing.css" media-type="text/css"/>
    <item id="missing-font" href="missing.ttf" media-type="application/x-font-ttf"/>
    <item id="missing-image" href="missing.png" media-type="image/png"/>
  </manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`)
	chapter := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <head><title>Missing resources</title><meta name="keep" value="unchanged"></meta><link rel="stylesheet" href="style.css"></link><link rel="stylesheet" href="missing.css"></link></head>
  <body><p>Preserved text.</p><img src="missing.png" alt="missing"/></body>
</html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
		"OPS/style.css":          []byte("p { color: black; }"),
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB with missing presentation resources: %v", err)
	}
	gotOPF := zipEntry(t, out.Bytes(), "OPS/package.opf")
	for _, removed := range []string{`id="missing-style"`, `href="missing.ttf"`} {
		if strings.Contains(gotOPF, removed) {
			t.Fatalf("rebuilt OPF retained %s:\n%s", removed, gotOPF)
		}
	}
	for _, preserved := range []string{`id="style"`, `id="missing-image"`} {
		if !strings.Contains(gotOPF, preserved) {
			t.Fatalf("rebuilt OPF lost %s:\n%s", preserved, gotOPF)
		}
	}
	if !strings.Contains(gotOPF, `keep literal <item id="missing-font"`) {
		t.Fatalf("rebuilt OPF changed a comment that resembles a manifest item:\n%s", gotOPF)
	}
	gotChapter := zipEntry(t, out.Bytes(), "OPS/chapter.xhtml")
	if strings.Contains(gotChapter, `href="missing.css"`) {
		t.Fatalf("rebuilt XHTML retained missing stylesheet link:\n%s", gotChapter)
	}
	for _, preserved := range []string{`href="style.css"`, `src="missing.png"`, `name="keep" value="unchanged"`} {
		if !strings.Contains(gotChapter, preserved) {
			t.Fatalf("rebuilt XHTML lost %s:\n%s", preserved, gotChapter)
		}
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat missing-resource EPUB rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second missing-resource EPUB rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBRemovesOnlyMissingAdobePageTemplateLinks(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <manifest>
    <item id="chapter" href="text/chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="page-template" href="styles/present.xpgt" media-type="application/vnd.adobe-page-template+xml"/>
  </manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`)
	missingLink := `<link rel="stylesheet" type="application/vnd.adobe-page-template+xml" href="../styles/missing-head.xpgt"/>`
	chapter := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <head>
    <title>Adobe page templates</title>
    ` + missingLink + `
    <link rel="alternate stylesheet" type="application/vnd.adobe-page-template+xml" href="../styles/present.xpgt"/>
    <link rel="stylesheet" type="text/css" href="../styles/missing.css"/>
    <link rel="alternate" type="application/vnd.adobe-page-template+xml" href="../styles/missing-alternate.xpgt"/>
    <link rel="stylesheet" type="application/vnd.adobe-page-template+xml" href="https://example.com/remote.xpgt"/>
  </head>
  <body>
    <p>Preserved text.</p>
    <link rel="stylesheet" type="application/vnd.adobe-page-template+xml" href="../styles/missing-body.xpgt"/>
  </body>
</html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml":  []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":         opf,
		"OPS/text/chapter.xhtml":  chapter,
		"OPS/styles/present.xpgt": []byte("present page template"),
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB with missing Adobe page template: %v", err)
	}
	wantChapter := strings.Replace(string(chapter), missingLink, "", 1)
	if got := zipEntry(t, out.Bytes(), "OPS/text/chapter.xhtml"); got != wantChapter {
		t.Fatalf("rebuilt XHTML changed more than the missing Adobe page-template link:\n%s", got)
	}
	if got := zipEntry(t, out.Bytes(), "OPS/styles/present.xpgt"); got != "present page template" {
		t.Fatalf("rebuilt EPUB changed the present Adobe page template: %q", got)
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat missing Adobe page-template rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second missing Adobe page-template rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBRejectsAmbiguousAdobePageTemplateTarget(t *testing.T) {
	opf := []byte(`<package xmlns="http://www.idpf.org/2007/opf" version="2.0"><manifest><item id="chapter" href="text/chapter.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="chapter"/></spine></package>`)
	chapter := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><link rel="stylesheet" type="application/vnd.adobe-page-template+xml" href="../styles/template.xpgt"/></head><body><p>Text.</p></body></html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml":   []byte(`<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":          opf,
		"OPS/text/chapter.xhtml":   chapter,
		"OPS/styles/Template.xpgt": []byte("first page template"),
		"OPS/styles/TEMPLATE.XPGT": []byte("second page template"),
	})

	var out bytes.Buffer
	err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB)
	if err == nil || !strings.Contains(err.Error(), "multiple matching archive members") {
		t.Fatalf("ambiguous Adobe page-template rebuild error = %v; want ambiguous target", err)
	}
	if out.Len() != 0 {
		t.Fatalf("ambiguous Adobe page-template rebuild wrote %d bytes before failing", out.Len())
	}
}

func TestConvertEPUBToEPUBRepairsInvalidDateAndNCXIdentifier(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="bookid" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">urn:isbn:123</dc:identifier>
    <dc:date>NONE</dc:date>
    <dc:date>2020-01-02</dc:date>
  </metadata>
  <manifest>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
  </manifest>
  <spine toc="ncx"><itemref idref="chapter"/></spine>
</package>`)
	ncx := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head><meta name="dtb:uid" content="wrong-id"/><meta name="dtb:depth" content="1"/></head>
  <docTitle><text>NCX title</text></docTitle><navMap/>
</ncx>`)
	chapter := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Preserved text.</p></body></html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
		"OPS/toc.ncx":            ncx,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB with invalid date and NCX identifier: %v", err)
	}
	gotOPF := zipEntry(t, out.Bytes(), "OPS/package.opf")
	if strings.Contains(gotOPF, `<dc:date>NONE</dc:date>`) || !strings.Contains(gotOPF, `<dc:date>2020-01-02</dc:date>`) {
		t.Fatalf("rebuilt OPF did not remove only the invalid date sentinel:\n%s", gotOPF)
	}
	gotNCX := zipEntry(t, out.Bytes(), "OPS/toc.ncx")
	if !strings.Contains(gotNCX, `name="dtb:uid" content="urn:isbn:123"`) || !strings.Contains(gotNCX, `name="dtb:depth" content="1"`) {
		t.Fatalf("rebuilt NCX did not synchronize only dtb:uid:\n%s", gotNCX)
	}
	if gotChapter := zipEntryBytes(t, out.Bytes(), "OPS/chapter.xhtml"); !bytes.Equal(gotChapter, chapter) {
		t.Fatal("rebuilt EPUB changed chapter bytes")
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat date/NCX EPUB rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second date/NCX EPUB rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBNormalizesMicrosoftImageGuide(t *testing.T) {
	opf := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Vendor guide</dc:title></metadata>
  <manifest>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover-image" href="cover.png" media-type="image/png"/>
    <item id="thumbnail" href="thumb.png" media-type="image/png"/>
  </manifest>
  <spine><itemref idref="chapter"/></spine>
  <guide>
    <reference type="other.ms-coverimage-standard" href="cover.png"/>
    <reference type="other.ms-thumbimage-standard" href="thumb.png"/>
    <reference type="other" href="cover.png"/>
    <reference type="text" href="chapter.xhtml"/>
  </guide>
</package>`)
	chapter := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Preserved text.</p></body></html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
		"OPS/cover.png":          converterTinyPNG,
		"OPS/thumb.png":          converterTinyPNG,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild EPUB with Microsoft image guide: %v", err)
	}
	gotOPF := zipEntry(t, out.Bytes(), "OPS/package.opf")
	if !strings.Contains(gotOPF, `<meta name="cover" content="cover-image"/>`) {
		t.Fatalf("rebuilt OPF did not add standard cover metadata:\n%s", gotOPF)
	}
	for _, removed := range []string{"other.ms-coverimage-standard", "other.ms-thumbimage-standard"} {
		if strings.Contains(gotOPF, removed) {
			t.Fatalf("rebuilt OPF retained %s guide reference:\n%s", removed, gotOPF)
		}
	}
	for _, preserved := range []string{`type="other" href="cover.png"`, `type="text" href="chapter.xhtml"`} {
		if !strings.Contains(gotOPF, preserved) {
			t.Fatalf("rebuilt OPF lost unrelated guide reference %s:\n%s", preserved, gotOPF)
		}
	}
	reader := bytes.NewReader(out.Bytes())
	cover, ext, err := format.ExtractEPUBCover(reader, reader.Size())
	if err != nil || ext != ".png" || !bytes.Equal(cover, converterTinyPNG) {
		t.Fatalf("rebuilt EPUB cover = ext %q bytes %d err %v", ext, len(cover), err)
	}

	var repeated bytes.Buffer
	if err := ConvertContext(context.Background(), &repeated, bytes.NewReader(out.Bytes()), format.FormatEPUB, int64(out.Len()), TargetEPUB); err != nil {
		t.Fatalf("repeat Microsoft-guide EPUB rebuild: %v", err)
	}
	if !bytes.Equal(out.Bytes(), repeated.Bytes()) {
		t.Fatal("second Microsoft-guide EPUB rebuild changed archive bytes")
	}
}

func TestConvertEPUBToEPUBRemovesInvalidOPFXMLControlCharacters(t *testing.T) {
	opf := append([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Invalid`), 0x01)
	opf = append(opf, []byte(` OPF</dc:title></metadata>
  <manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`)...)
	chapter := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Preserved text.</p></body></html>`)
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
		"OPS/package.opf":        opf,
		"OPS/chapter.xhtml":      chapter,
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("rebuild invalid-control EPUB: %v", err)
	}
	got := zipEntry(t, out.Bytes(), "OPS/package.opf")
	if strings.ContainsRune(got, '\x01') || !strings.Contains(got, "Invalid OPF") {
		t.Fatalf("rebuilt OPF did not remove only the invalid control character:\n%s", got)
	}
	if gotChapter := zipEntryBytes(t, out.Bytes(), "OPS/chapter.xhtml"); !bytes.Equal(gotChapter, chapter) {
		t.Fatal("rebuilt EPUB changed chapter bytes")
	}
}

func TestConvertEPUBToEPUBRefusesAmbiguousOrUnsafePackage(t *testing.T) {
	opf := func(chapter string) []byte {
		return []byte(fmt.Sprintf(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><manifest><item id="chapter" href="%s" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="chapter"/></spine></package>`, chapter))
	}
	chapter := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`)

	for _, tt := range []struct {
		name    string
		entries map[string][]byte
		want    string
	}{
		{name: "ambiguous OPF discovery", entries: map[string][]byte{
			"OPS/a.opf": opf("a.xhtml"), "OPS/a.xhtml": chapter,
			"OPS/b.opf": opf("b.xhtml"), "OPS/b.xhtml": chapter,
		}, want: "package is ambiguous"},
		{name: "signed package", entries: map[string][]byte{
			"META-INF/container.xml":  []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/a.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
			"META-INF/signatures.xml": []byte(`<signatures/>`),
			"OPS/a.opf":               opf("a.xhtml"), "OPS/a.xhtml": chapter,
		}, want: "signatures.xml"},
		{name: "unknown encryption", entries: map[string][]byte{
			"META-INF/container.xml":  []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/a.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
			"META-INF/encryption.xml": []byte(`<encryption><EncryptedData><EncryptionMethod Algorithm="urn:unknown"/></EncryptedData></encryption>`),
			"OPS/a.opf":               opf("a.xhtml"), "OPS/a.xhtml": chapter,
		}, want: "unsupported encryption algorithm"},
		{name: "missing spine resource", entries: map[string][]byte{
			"OPS/a.opf": opf("missing.xhtml"),
		}, want: "no coherent OPF package"},
		{name: "markup after package root", entries: map[string][]byte{
			"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/a.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
			"OPS/a.opf":              append(opf("a.xhtml"), []byte(`<extra/>`)...),
			"OPS/a.xhtml":            chapter,
		}, want: "markup follows the closed package root"},
		{name: "unsafe ZIP entry path", entries: map[string][]byte{
			"META-INF/container.xml": []byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OPS/a.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`),
			"OPS/a.opf":              opf("a.xhtml"),
			"OPS/a.xhtml":            chapter,
			"../escape":              []byte("unsafe"),
		}, want: "unsafe ZIP entry path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := testZip(t, tt.entries)
			var out bytes.Buffer
			err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetEPUB)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("rebuild error = %v; want %q", err, tt.want)
			}
			if out.Len() != 0 {
				t.Fatalf("failed rebuild wrote %d bytes", out.Len())
			}
		})
	}
}

func TestTransformKEPUBOPFDoesNotMarkXHTMLCoverPageAsCoverImage(t *testing.T) {
	raw := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata><meta name="cover" content="cover"/></metadata>
  <manifest><item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/></manifest>
</package>`)

	got, err := transformKEPUBOPF(raw)
	if err != nil {
		t.Fatalf("transform KEPUB OPF: %v", err)
	}
	if strings.Contains(string(got), `properties="cover-image"`) {
		t.Fatalf("XHTML cover page was marked as a cover image:\n%s", got)
	}
}

func TestConvertEPUBToKEPUBSanitizesMimetypeHeader(t *testing.T) {
	src := testZipWithHeaders(t, []testZipEntry{
		{
			name:      "mimetype",
			data:      []byte("application/epub+zip"),
			method:    zip.Store,
			methodSet: true,
			extra:     []byte{0x55, 0x54, 0x05, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00},
		},
		{name: "META-INF/container.xml", data: []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest>
    <item id="text" href="text.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="text"/></spine>
</package>`)},
		{name: "OEBPS/text.xhtml", data: []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`)},
	})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB); err != nil {
		t.Fatalf("Convert EPUB to KEPUB: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("open converted KEPUB: %v", err)
	}
	if len(zr.File) == 0 {
		t.Fatal("converted KEPUB has no ZIP entries")
	}
	if zr.File[0].Name != "mimetype" {
		t.Fatalf("first KEPUB entry = %q; want mimetype", zr.File[0].Name)
	}
	mimetype := zr.File[0]
	if mimetype.Method != zip.Store {
		t.Fatalf("mimetype method = %d; want Store", mimetype.Method)
	}
	if len(mimetype.Extra) != 0 {
		t.Fatalf("mimetype extra field length = %d; want 0", len(mimetype.Extra))
	}
	if got := zipEntry(t, out.Bytes(), "mimetype"); got != "application/epub+zip" {
		t.Fatalf("mimetype = %q", got)
	}
}

func TestConvertEPUBToKEPUBMarksSelectedUTF8EntryNames(t *testing.T) {
	src := testZipWithHeaders(t, []testZipEntry{
		{name: "mimetype", data: []byte("application/epub+zip"), method: zip.Store, methodSet: true},
		{name: "META-INF/container.xml", data: []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest>
    <item id="text" href="章.xhtml" media-type="application/xhtml+xml"/>
    <item id="image" href="表紙.png" media-type="image/png"/>
  </manifest>
  <spine><itemref idref="text"/></spine>
</package>`)},
		{name: "OEBPS/章.xhtml", data: []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`), nonUTF8: true},
		{name: "OEBPS/表紙.png", data: converterTinyPNG, nonUTF8: true},
		{name: "OEBPS/資料.bin", data: []byte("unrelated"), nonUTF8: true},
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB); err != nil {
		t.Fatalf("Convert EPUB to KEPUB: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("open converted KEPUB: %v", err)
	}
	entries := make(map[string]*zip.File, len(zr.File))
	for _, file := range zr.File {
		entries[file.Name] = file
	}
	for _, name := range []string{"OEBPS/章.xhtml", "OEBPS/表紙.png"} {
		file := entries[name]
		if file == nil {
			t.Fatalf("converted KEPUB missing %s", name)
		}
		if file.NonUTF8 || file.Flags&0x800 == 0 {
			t.Fatalf("converted KEPUB entry %s flags = %#x, NonUTF8 = %v; want UTF-8 language flag", name, file.Flags, file.NonUTF8)
		}
	}
	unrelated := entries["OEBPS/資料.bin"]
	if unrelated == nil {
		t.Fatal("converted KEPUB missing unrelated entry")
	}
	if !unrelated.NonUTF8 || unrelated.Flags&0x800 != 0 {
		t.Fatalf("unrelated entry flags = %#x, NonUTF8 = %v; want source encoding flag preserved", unrelated.Flags, unrelated.NonUTF8)
	}
}

func TestConvertEPUBToKEPUBNormalizesRootfileMediaType(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"mimetype": []byte("application/epub+zip"),
		"META-INF/container.xml": []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/x-oebps-package+xml"/>
  </rootfiles>
</container>`),
		"OEBPS/content.opf": []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`),
		"OEBPS/text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Readable fallback.</p></body></html>`),
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB); err != nil {
		t.Fatalf("Convert EPUB to KEPUB: %v", err)
	}
	if xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml"); !strings.Contains(xhtml, `class="koboSpan"`) {
		t.Fatalf("content document was not transformed:\n%s", xhtml)
	}
	container := zipEntry(t, out.Bytes(), "META-INF/container.xml")
	if !strings.Contains(container, `media-type="application/oebps-package+xml"`) || strings.Contains(container, `application/x-oebps-package+xml`) {
		t.Fatalf("container.xml did not normalize the selected rootfile media type:\n%s", container)
	}
}

func TestConvertEPUBToKEPUBPrefersExactEntryAmongCaseCollisions(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`),
		"OEBPS/content.opf": []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest><item id="text" href="Text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`),
		"OEBPS/Text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body>first</body></html>`),
		"oebps/text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body>second</body></html>`),
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB); err != nil {
		t.Fatalf("Convert EPUB to KEPUB: %v", err)
	}
	if exact := zipEntry(t, out.Bytes(), "OEBPS/Text.xhtml"); !strings.Contains(exact, `class="koboSpan"`) {
		t.Fatalf("exact content entry was not transformed:\n%s", exact)
	}
	if other := zipEntry(t, out.Bytes(), "oebps/text.xhtml"); other != `<html xmlns="http://www.w3.org/1999/xhtml"><body>second</body></html>` {
		t.Fatalf("unreferenced case-colliding entry changed:\n%s", other)
	}
}

func TestConvertEPUBToKEPUBRefusesOnlyAmbiguousRequiredEntry(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"META-INF/container.xml": []byte(`<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`),
		"OEBPS/content.opf": []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`),
		"OEBPS/Text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body>first</body></html>`),
		"oebps/text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body>second</body></html>`),
	})

	var out bytes.Buffer
	err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB)
	if err == nil || !strings.Contains(err.Error(), "multiple matching archive members") {
		t.Fatalf("Convert EPUB to KEPUB error = %v; want required-entry ambiguity", err)
	}
	if out.Len() != 0 {
		t.Fatalf("Convert EPUB to KEPUB wrote %d bytes before resolving required entries", out.Len())
	}
}

func TestConvertEPUBToKEPUBCanonicalizesEquivalentMimetypeName(t *testing.T) {
	src := testZip(t, map[string][]byte{
		"MIMETYPE":   []byte("application/epub+zip"),
		"mime%74ype": []byte("preserve unrelated escaped name"),
		"META-INF/container.xml": []byte(`<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`),
		"OEBPS/content.opf": []byte(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`),
		"OEBPS/text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Text.</p></body></html>`),
	})

	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB); err != nil {
		t.Fatalf("Convert EPUB to KEPUB: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("open converted KEPUB: %v", err)
	}
	var markers []string
	for _, file := range zr.File {
		if strings.EqualFold(file.Name, "mimetype") {
			markers = append(markers, file.Name)
		}
	}
	if !slices.Equal(markers, []string{"mimetype"}) {
		t.Fatalf("converted KEPUB mimetype entries = %v; want one canonical marker", markers)
	}
	if got := zipEntry(t, out.Bytes(), "mime%74ype"); got != "preserve unrelated escaped name" {
		t.Fatalf("escaped source entry = %q; want preserved bytes", got)
	}
}

func TestTransformKEPUBContentKeepsRestrictedEPUBContentModelsValid(t *testing.T) {
	out, err := transformKEPUBContent([]byte(`<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
  <head><title>Title</title></head>
  <body>
    <section>
      <hgroup epub:type="fulltitle">
        <h2 epub:type="title">Title</h2>
        <p role="doc-subtitle" epub:type="subtitle">Subtitle</p>
      </hgroup>
      <p>Published in <time>1818</time>.</p>
    </section>
  </body>
</html>`))
	if err != nil {
		t.Fatalf("transform KEPUB content: %v", err)
	}
	xhtml := string(out)
	for _, bad := range []string{"<hgroup", "</hgroup>", "<time><span"} {
		if strings.Contains(xhtml, bad) {
			t.Fatalf("transformed XHTML contains %q:\n%s", bad, xhtml)
		}
	}
	for _, want := range []string{
		`<div epub:type="fulltitle">`,
		`<p role="doc-subtitle" epub:type="subtitle"><span class="koboSpan"`,
		`<span class="koboSpan" id="kobo.3.2"><time>1818</time></span>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("transformed XHTML missing %q:\n%s", want, xhtml)
		}
	}
}

func TestTransformKEPUBContentProducesXMLCompatibleXHTML(t *testing.T) {
	out, err := transformKEPUBContent([]byte(`<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
  <head>
    <title>XML-sensitive content</title>
    <style>.note::before { content: "a &lt; b"; }</style>
    <script>if (a &lt; b &amp;&amp; c &gt; d) { window.ok = true; }</script>
  </head>
  <body>
    <!-- preserved comment -->
    <p epub:type="note">A&#160;note.</p>
    <svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="#shape"/></svg>
  </body>
</html>`))
	if err != nil {
		t.Fatalf("transform KEPUB content: %v", err)
	}
	if err := validateKEPUBXHTML(out); err != nil {
		t.Fatalf("validate transformed XHTML: %v\n%s", err, out)
	}
	xhtml := string(out)
	for _, want := range []string{
		`xmlns:epub="http://www.idpf.org/2007/ops"`,
		`epub:type="note"`,
		`xmlns:xlink="http://www.w3.org/1999/xlink"`,
		`xlink:href="#shape"`,
		`<!-- preserved comment -->`,
		`if (a &lt; b &amp;&amp; c &gt; d)`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("transformed XHTML missing %q:\n%s", want, xhtml)
		}
	}
}

func TestTransformKEPUBContentRepairsRecoverableXMLDefects(t *testing.T) {
	tests := []struct {
		name   string
		source []byte
		want   string
	}{
		{
			name: "truncated start tag",
			source: []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Broken attribute</title></head><body>
<h1>Chapter</h1><p
</p></body></html>`),
			want: "Chapter",
		},
		{
			name:   "invalid UTF-8 text",
			source: bytes.Join([][]byte{[]byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Encoding</title></head><body><p>Hello `), {0xff, 0xff}, []byte(` world.</p></body></html>`)}, nil),
			want:   "Hello � world.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := transformKEPUBContent(tt.source)
			if err != nil {
				t.Fatalf("transformKEPUBContent: %v", err)
			}
			if err := validateKEPUBXHTML(out); err != nil {
				t.Fatalf("validate transformed XHTML: %v\n%s", err, out)
			}
			doc, err := nethtml.Parse(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("parse transformed XHTML: %v", err)
			}
			if text := strings.Join(strings.Fields(testHTMLText(firstHTMLElement(doc, "body"))), " "); !strings.Contains(text, tt.want) {
				t.Fatalf("reading text = %q; want to contain %q\n%s", text, tt.want, out)
			}
			if strings.Contains(string(out), ` p=""`) {
				t.Fatalf("transformed XHTML retained a tokenizer artifact:\n%s", out)
			}
		})
	}
}

func TestTransformKEPUBContentRejectsNonXMLRawText(t *testing.T) {
	_, err := transformKEPUBContent([]byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><script>if (a < b && c > d) {}</script></head><body><p>Text.</p></body></html>`))
	if err == nil || !strings.Contains(err.Error(), "not XML-compatible") {
		t.Fatalf("transformKEPUBContent error = %v; want XML compatibility diagnostic", err)
	}
}

func TestTransformKEPUBContentRefusesExistingKoboSpanMarkup(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "partially marked",
			body: `<p><span class="koboSpan" id="kobo.1.1">Marked.</span> Still plain.</p>`,
		},
		{
			name: "fully marked",
			body: `<div id="book-columns"><div id="book-inner"><p><span id="kobo.1.1">Marked.</span></p></div></div>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := transformKEPUBContent([]byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head></head><body>` + tc.body + `</body></html>`))
			if err == nil || !strings.Contains(err.Error(), "already contains Kobo span markup") {
				t.Fatalf("transformKEPUBContent error = %v; want existing-markup refusal", err)
			}
		})
	}
}

func TestTransformKEPUBContentMultilingualSpansAreDeterministic(t *testing.T) {
	sourceText := "Latin. Кириллица! 中文第一句。中文第二句！ Emoji 😀?\u00a0Done."
	source := []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Languages</title></head><body><p>` + sourceText + `</p></body></html>`)
	first, err := transformKEPUBContent(source)
	if err != nil {
		t.Fatalf("transformKEPUBContent: %v", err)
	}
	second, err := transformKEPUBContent(source)
	if err != nil {
		t.Fatalf("transformKEPUBContent second run: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same source produced different KEPUB span markup")
	}

	spans := testKEPUBSpanSnapshots(t, first)
	wantText := []string{
		"Latin. ",
		"Кириллица! ",
		"中文第一句。",
		"中文第二句！ ",
		"Emoji 😀?\u00a0",
		"Done.",
	}
	if len(spans) != len(wantText) {
		t.Fatalf("spans = %#v; want %d multilingual segments", spans, len(wantText))
	}
	for i, want := range wantText {
		wantID := fmt.Sprintf("kobo.1.%d", i+1)
		if spans[i].ID != wantID || spans[i].Text != want {
			t.Fatalf("span[%d] = %#v; want id=%q text=%q", i, spans[i], wantID, want)
		}
	}
	var reconstructed strings.Builder
	for _, span := range spans {
		reconstructed.WriteString(span.Text)
	}
	if reconstructed.String() != sourceText {
		t.Fatalf("span text = %q; want source reading text %q", reconstructed.String(), sourceText)
	}
	if _, err := transformKEPUBContent(first); err == nil || !strings.Contains(err.Error(), "already contains Kobo span markup") {
		t.Fatalf("repeated transform error = %v; want existing-markup refusal", err)
	}
}

func TestConvertEPUBToKEPUBRejectsEncryptedEntries(t *testing.T) {
	src := testZipWithHeaders(t, []testZipEntry{
		{name: "mimetype", data: []byte("application/epub+zip"), method: zip.Store, methodSet: true},
		{name: "META-INF/container.xml", data: []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)},
		{name: "OEBPS/content.opf", data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <manifest>
    <item id="text" href="text.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="text"/></spine>
</package>`)},
		{name: "OEBPS/text.xhtml", data: []byte(`<html><body><p>Encrypted entry.</p></body></html>`), flags: 0x1},
	})
	var out bytes.Buffer
	err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatEPUB, int64(len(src)), TargetKEPUB)
	if err == nil {
		t.Fatal("Convert EPUB to KEPUB succeeded for encrypted entry")
	}
	if !strings.Contains(err.Error(), "OEBPS/text.xhtml is encrypted") {
		t.Fatalf("Convert EPUB to KEPUB error = %v; want encrypted entry error", err)
	}
}

type testKEPUBSpanSnapshot struct {
	ID   string
	Text string
}

func assertKEPUBConversionContract(t *testing.T, source, output []byte, opfPath string) {
	t.Helper()
	sourceOPF := zipEntryBytes(t, source, opfPath)
	outputOPF := zipEntryBytes(t, output, opfPath)
	sourceDocs, err := kepubContentDocuments(opfPath, sourceOPF)
	if err != nil {
		t.Fatalf("source content documents: %v", err)
	}
	outputDocs, err := kepubContentDocuments(opfPath, outputOPF)
	if err != nil {
		t.Fatalf("output content documents: %v", err)
	}
	if !slices.Equal(outputDocs, sourceDocs) {
		t.Fatalf("output content hrefs = %#v; want %#v", outputDocs, sourceDocs)
	}
	if sourceSpine, outputSpine := testKEPUBSpineIDs(t, sourceOPF), testKEPUBSpineIDs(t, outputOPF); !slices.Equal(outputSpine, sourceSpine) {
		t.Fatalf("output spine = %#v; want %#v", outputSpine, sourceSpine)
	}
	for _, name := range sourceDocs {
		sourceText := testKEPUBReadingText(t, zipEntryBytes(t, source, name))
		outputText := testKEPUBReadingText(t, zipEntryBytes(t, output, name))
		if outputText != sourceText {
			t.Fatalf("output reading text for %s = %q; want %q", name, outputText, sourceText)
		}
	}
}

func testKEPUBSpineIDs(t *testing.T, opf []byte) []string {
	t.Helper()
	var doc struct {
		Spine struct {
			Items []struct {
				IDRef string `xml:"idref,attr"`
			} `xml:"itemref"`
		} `xml:"spine"`
	}
	if err := xml.Unmarshal(opf, &doc); err != nil {
		t.Fatalf("parse KEPUB OPF spine: %v", err)
	}
	ids := make([]string, 0, len(doc.Spine.Items))
	for _, item := range doc.Spine.Items {
		ids = append(ids, item.IDRef)
	}
	return ids
}

func testKEPUBReadingText(t *testing.T, raw []byte) string {
	t.Helper()
	doc, err := nethtml.Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse KEPUB reading text: %v", err)
	}
	body := firstHTMLElement(doc, "body")
	if body == nil {
		t.Fatal("KEPUB content has no body")
	}
	return strings.Join(strings.Fields(testHTMLText(body)), " ")
}

func testKEPUBSpanSnapshots(t *testing.T, raw []byte) []testKEPUBSpanSnapshot {
	t.Helper()
	doc, err := nethtml.Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse transformed KEPUB content: %v", err)
	}
	var spans []testKEPUBSpanSnapshot
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode && strings.EqualFold(n.Data, "span") && containsKEPUBToken(attrValue(n, "class"), kepubSpanClass) {
			spans = append(spans, testKEPUBSpanSnapshot{ID: attrValue(n, "id"), Text: testHTMLText(n)})
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return spans
}

func testHTMLText(n *nethtml.Node) string {
	if n.Type == nethtml.TextNode {
		return n.Data
	}
	var text strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		text.WriteString(testHTMLText(child))
	}
	return text.String()
}

func TestConvertFB2ToEPUB(t *testing.T) {
	src := testFB2ForEPUB()
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatFB2, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert FB2 to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<h1 id="fb2-heading-1">Chapter One</h1>`,
		`<h2 id="fb2-heading-2">* * *</h2>`,
		"<p>Text with <strong>bold</strong> and <em>emphasis</em>.</p>",
		"<p>One <strong>two</strong> <em>three</em>.</p>",
		`<p><a href="https://example.test/path?q=1&amp;x=2">External</a>, <a href="#chapter-1">Chapter</a>, <span>Bad</span>, <span>Local</span>, <a href="#note-1" epub:type="noteref">Note</a>, <a href="#comment-1" epub:type="noteref"><sup>Comment</sup></a>.</p>`,
		`<aside id="note-1" epub:type="footnote"><header><h1 id="fb2-heading-3">1</h1>`,
		`<aside id="comment-1" epub:type="footnote"><header><h1 id="fb2-heading-4">A</h1>`,
		`<img src="images/img1.png" alt=""/>`,
		`xmlns:epub="http://www.idpf.org/2007/ops"`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#fb2-heading-1">Chapter One</a></li>`) {
		t.Fatalf("nav.xhtml missing FB2 chapter link:\n%s", nav)
	}
	if strings.Contains(nav, "* * *") {
		t.Fatalf("nav.xhtml contains decorative FB2 subtitle:\n%s", nav)
	}
	for _, noteTitle := range []string{
		`text.xhtml#fb2-heading-3`,
		`text.xhtml#fb2-heading-4`,
	} {
		if strings.Contains(nav, noteTitle) {
			t.Fatalf("nav.xhtml contains supplementary FB2 note heading %q:\n%s", noteTitle, nav)
		}
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		"<dc:identifier id=\"pub-id\">urn:isbn:978-0-306-40615-7</dc:identifier>",
		"<dc:title>FB2 Export</dc:title>",
		"<dc:creator>FB2 Author</dc:creator>",
		"<dc:language>en</dc:language>",
		"<dc:publisher>Mir</dc:publisher>",
		"<dc:date>1972-01-01</dc:date>",
		"<dc:description>A short classic annotation.</dc:description>",
		"<dc:subject>sf_history</dc:subject>",
		"<dc:subject>adventure</dc:subject>",
		`<meta property="belongs-to-collection" id="series-1">Noon Universe</meta>`,
		`<meta refines="#series-1" property="group-position">3</meta>`,
		`<meta name="cover" content="img1"/>`,
		`<item id="img1" href="images/img1.png" media-type="image/png" properties="cover-image"/>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("content.opf missing %q:\n%s", want, opf)
		}
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/images/img1.png"); !bytes.Equal(got, converterTinyPNG) {
		t.Fatalf("embedded FB2 image = %d bytes; want tiny PNG", len(got))
	}
}

func TestConvertFB2ToKEPUB(t *testing.T) {
	src := testFB2ForEPUB()
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatFB2, int64(len(src)), TargetKEPUB); err != nil {
		t.Fatalf("Convert FB2 to KEPUB: %v", err)
	}

	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>FB2 Export</dc:title>") {
		t.Fatalf("content.opf lost FB2 metadata:\n%s", opf)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`id="book-columns"`,
		`class="koboSpan"`,
		"Chapter One",
		"Text with",
		`epub:type="noteref" href="#note-1"`,
		`id="note-1" epub:type="footnote"`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("composed KEPUB text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/images/img1.png"); !bytes.Equal(got, converterTinyPNG) {
		t.Fatalf("composed KEPUB image = %d bytes; want original PNG", len(got))
	}
	problems, err := checkEPUBInternalLinks(out.Bytes())
	if err != nil {
		t.Fatalf("check composed KEPUB links: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("composed KEPUB internal-link problems: %v", problems)
	}
}

func TestConvertFB2ToEPUBCombinesMultilineTitleNavigation(t *testing.T) {
	src := []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <book-title>Multiline FB2</book-title>
      <lang>en</lang>
    </title-info>
  </description>
  <body>
    <section>
      <title>
        <p>First Line</p>
        <p>Second Line</p>
      </title>
      <p>Body.</p>
    </section>
  </body>
</FictionBook>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatFB2, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert FB2 to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<h1 id="fb2-heading-1">First Line</h1>`,
		`<h1 id="fb2-heading-2">Second Line</h1>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<li><a href="text.xhtml#fb2-heading-1">First Line Second Line</a></li>`) {
		t.Fatalf("nav.xhtml missing combined title link:\n%s", nav)
	}
	if strings.Count(nav, "text.xhtml#fb2-heading-") != 1 {
		t.Fatalf("nav.xhtml split multiline title into multiple entries:\n%s", nav)
	}
}

func TestConvertZippedFB2ToEPUB(t *testing.T) {
	src := testZip(t, map[string][]byte{"book.fb2": testFB2ForEPUB()})
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatFB2, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert zipped FB2 to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<h1 id="fb2-heading-1">Chapter One</h1>`) {
		t.Fatalf("text.xhtml missing FB2 body from zipped source:\n%s", xhtml)
	}
}

func TestConvertFB2ToEPUBToleratesNULByte(t *testing.T) {
	src := []byte(strings.Replace(string(testFB2ForEPUB()), "<body>", "<body>\x00", 1))
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatFB2, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert FB2 with NUL to EPUB: %v", err)
	}

	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<h1 id="fb2-heading-1">Chapter One</h1>`) {
		t.Fatalf("text.xhtml missing FB2 body after NUL repair:\n%s", xhtml)
	}
}

func TestConvertFB2ToEPUBFallsBackFromWrongUTF8Declaration(t *testing.T) {
	title := "\u041a\u043d\u0438\u0433\u0430"
	body := "\u0422\u0435\u043a\u0441\u0442 \u0433\u043b\u0430\u0432\u044b"
	src := converterWindows1251(t, `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <book-title>`+title+`</book-title>
      <lang>ru</lang>
    </title-info>
  </description>
  <body><section><p>`+body+`</p></section></body>
</FictionBook>`)
	var out bytes.Buffer
	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatFB2, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert FB2 to EPUB: %v", err)
	}

	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, "<dc:title>"+title+"</dc:title>") {
		t.Fatalf("content.opf missing decoded title:\n%s", opf)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, "<p>"+body+"</p>") {
		t.Fatalf("text.xhtml missing decoded body:\n%s", xhtml)
	}
}

func TestConvertMOBI6ToEPUB(t *testing.T) {
	text := []byte(`<html><body><p>Hello from MOBI6</p></body></html>`)
	src := testMOBI6PalmDOCFile("Native MOBI", text)
	var out bytes.Buffer

	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatMOBI, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert MOBI6 to EPUB: %v", err)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, `<p>Hello from MOBI6</p>`) {
		t.Fatalf("text.xhtml missing MOBI body:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, `<dc:title>Native MOBI</dc:title>`) {
		t.Fatalf("content.opf missing MOBI title:\n%s", opf)
	}
}

func TestConvertPalmDOCToEPUB(t *testing.T) {
	text := []byte("Palm paragraph\n\nCaf\xe9 & <line>\n")
	src := testPalmDOCFileForConverter("Palm Export", 1, [][]byte{text}, uint32(len(text)))
	var out bytes.Buffer

	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatPDB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert PalmDOC to EPUB: %v", err)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<p>Palm paragraph</p>`,
		`<p>Café &amp; &lt;line&gt;</p>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	if !strings.Contains(opf, `<dc:title>Palm Export</dc:title>`) {
		t.Fatalf("content.opf missing PalmDOC title:\n%s", opf)
	}
}

func TestConvertPalmDOCOEBHTMLToEPUB(t *testing.T) {
	text := []byte(`<HTML><HEAD><metadata><dc-metadata xmlns:dc="http://purl.org/metadata/dublin_core">
<dc:Title>Structured Palm Export</dc:Title><dc:Creator>Example Author</dc:Creator><dc:Language>en</dc:Language>
</dc-metadata></metadata></HEAD><BODY><p>Structured <b>PalmDOC</b>.</p><img src="BMP" recindex="00001"></BODY></HTML>`)
	src := testPalmDOCFileForConverterWithResources("Database Fallback", 1, [][]byte{text}, uint32(len(text)), [][]byte{converterTinyPNG})
	var out bytes.Buffer

	if err := ConvertContext(context.Background(), &out, bytes.NewReader(src), format.FormatPDB, int64(len(src)), TargetEPUB); err != nil {
		t.Fatalf("Convert HTML PalmDOC to EPUB: %v", err)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<p>Structured <strong>PalmDOC</strong>.</p>`,
		`<img src="images/00002.png" alt=""/>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	if strings.Contains(xhtml, "&lt;HTML&gt;") {
		t.Fatalf("text.xhtml escaped PalmDOC HTML as plain text:\n%s", xhtml)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		`<dc:title>Structured Palm Export</dc:title>`,
		`<dc:creator>Example Author</dc:creator>`,
		`<item id="res-00002" href="images/00002.png" media-type="image/png"/>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("content.opf missing %q:\n%s", want, opf)
		}
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/images/00002.png"); !bytes.Equal(got, converterTinyPNG) {
		t.Fatalf("PalmDOC image bytes = %d; want copied PNG", len(got))
	}
}

func TestConvertKindleDocumentToEPUB(t *testing.T) {
	video := []byte{0, 0, 0, 20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	audio := []byte("ID3\x04\x00\x00tiny mp3")
	doc := &format.KindleDocument{
		Metadata: &format.Metadata{
			Title:    "Kindle Export",
			Language: "en",
			Authors:  []bookmeta.AuthorMeta{{Name: "Jane Doe"}},
		},
		Flows: []format.KindleTextFlow{{
			ID:        "flow-0001",
			Href:      "text/flow-0001.html",
			MediaType: "text/html",
			Data:      []byte("<html><body><p>Chapter \x12body</p><p><a filepos=0000000012>Go</a><img src=\"BMP\" recindex=\"00001\"><img src=\"kindle:embed:0001?mime=image/png\" alt=\"embed\"><img src=\"kindle:flow:0003?mime=image/svg+xml\" alt=\"svg\"></p><video src=\"kindle:embed:0003?mime=video/mpeg\" title=\"Test video\">Video fallback</video><audio mediarecindex=\"00004\" title=\"Test audio\">Audio fallback</audio></body></html>"),
		}},
		Resources: []format.KindleResource{{
			ID:         "res-00001",
			Href:       "images/00001.png",
			MediaType:  "image/png",
			Data:       converterTinyPNG,
			EmbedIndex: 1,
			Cover:      true,
		}, {
			ID:         "font-00002",
			Href:       "fonts/00002.ttf",
			MediaType:  "font/ttf",
			Data:       []byte("\x00\x01\x00\x00fake font"),
			EmbedIndex: 2,
		}, {
			ID:         "video-00003",
			Href:       "media/00003.mp4",
			MediaType:  "video/mp4",
			Data:       video,
			EmbedIndex: 3,
		}, {
			ID:         "audio-00004",
			Href:       "media/00004.mp3",
			MediaType:  "audio/mpeg",
			Data:       audio,
			EmbedIndex: 4,
		}, {
			ID:        "svg-0003",
			Href:      "images/flow-0003.svg",
			MediaType: "image/svg+xml",
			Data:      []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>`),
			FlowIndex: 3,
		}, {
			ID:        "style-0001",
			Href:      "styles/flow-0001.css",
			MediaType: "text/css",
			Data:      []byte("@font-face { src: url(kindle:embed:0002); }\n.figure { background: url(\"kindle:flow:0003?mime=image/svg+xml\"); }\np { color: red; }\n"),
		}},
		Navigation: []format.KindleNavItem{{
			Label: "Chapter",
			Href:  "text/flow-0001.html#filepos12",
		}},
	}

	var out bytes.Buffer
	if err := convertKindleDocumentToEPUB(context.Background(), &out, doc, ConversionOptions{}); err != nil {
		t.Fatalf("convertKindleDocumentToEPUB: %v", err)
	}
	xhtml := zipEntry(t, out.Bytes(), "OEBPS/text.xhtml")
	for _, want := range []string{
		`<link rel="stylesheet" href="styles/flow-0001.css"/>`,
		`<p id="filepos12">Chapter body</p>`,
		`<a href="#filepos12">Go</a>`,
		`<img src="images/00001.png" alt=""/>`,
		`<img src="images/00001.png" alt="embed"/>`,
		`<img src="images/flow-0003.svg" alt="svg"/>`,
		`<video src="media/00003.mp4" controls="controls" title="Test video">Video fallback</video>`,
		`<audio src="media/00004.mp3" controls="controls" title="Test audio">Audio fallback</audio>`,
	} {
		if !strings.Contains(xhtml, want) {
			t.Fatalf("text.xhtml missing %q:\n%s", want, xhtml)
		}
	}
	nav := zipEntry(t, out.Bytes(), "OEBPS/nav.xhtml")
	if !strings.Contains(nav, `<a href="text.xhtml#filepos12">Chapter</a>`) {
		t.Fatalf("nav.xhtml missing Kindle navigation item:\n%s", nav)
	}
	opf := zipEntry(t, out.Bytes(), "OEBPS/content.opf")
	for _, want := range []string{
		`<dc:title>Kindle Export</dc:title>`,
		`<dc:creator>Jane Doe</dc:creator>`,
		`<item id="res-00001" href="images/00001.png" media-type="image/png" properties="cover-image"/>`,
		`<item id="font-00002" href="fonts/00002.ttf" media-type="font/ttf"/>`,
		`<item id="video-00003" href="media/00003.mp4" media-type="video/mp4"/>`,
		`<item id="audio-00004" href="media/00004.mp3" media-type="audio/mpeg"/>`,
		`<item id="svg-0003" href="images/flow-0003.svg" media-type="image/svg+xml"/>`,
		`<item id="style-0001" href="styles/flow-0001.css" media-type="text/css"/>`,
	} {
		if !strings.Contains(opf, want) {
			t.Fatalf("content.opf missing %q:\n%s", want, opf)
		}
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/images/00001.png"); !bytes.Equal(got, converterTinyPNG) {
		t.Fatalf("Kindle image bytes = %d; want copied PNG", len(got))
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/fonts/00002.ttf"); !bytes.Equal(got, []byte("\x00\x01\x00\x00fake font")) {
		t.Fatalf("Kindle font bytes = %d; want copied TTF", len(got))
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/media/00003.mp4"); !bytes.Equal(got, video) {
		t.Fatalf("Kindle video bytes = %d; want copied MP4", len(got))
	}
	if got := zipEntryBytes(t, out.Bytes(), "OEBPS/media/00004.mp3"); !bytes.Equal(got, audio) {
		t.Fatalf("Kindle audio bytes = %d; want copied MP3", len(got))
	}
	if got := zipEntry(t, out.Bytes(), "OEBPS/images/flow-0003.svg"); got != `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>` {
		t.Fatalf("Kindle SVG asset = %q; want copied SVG", got)
	}
	if strings.Contains(xhtml, "\x12") {
		t.Fatalf("text.xhtml retained an XML 1.0-forbidden source character:\n%s", xhtml)
	}
	if got := zipEntry(t, out.Bytes(), "OEBPS/styles/flow-0001.css"); got != "@font-face { src: url(../fonts/00002.ttf); }\n.figure { background: url(\"../images/flow-0003.svg\"); }\np { color: red; }\n" {
		t.Fatalf("Kindle CSS asset = %q; want copied stylesheet", got)
	}
}

func zipEntry(t *testing.T, data []byte, name string) string {
	t.Helper()
	return string(zipEntryBytes(t, data, name))
}

func zipEntryBytes(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", name, err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read zip entry %s: %v", name, err)
		}
		return body
	}
	t.Fatalf("zip entry %s not found", name)
	return nil
}

func zipHasEntry(t *testing.T, data []byte, name string) bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

func testZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	zipped := make([]testZipEntry, 0, len(entries))
	for name, data := range entries {
		zipped = append(zipped, testZipEntry{name: name, data: data})
	}
	return testZipWithHeaders(t, zipped)
}

type testZipEntry struct {
	name      string
	data      []byte
	method    uint16
	methodSet bool
	flags     uint16
	extra     []byte
	nonUTF8   bool
}

func testZipWithHeaders(t *testing.T, entries []testZipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		method := zip.Deflate
		if entry.methodSet {
			method = entry.method
		}
		header := &zip.FileHeader{
			Name:    entry.name,
			Method:  method,
			Flags:   entry.flags,
			Extra:   entry.extra,
			NonUTF8: entry.nonUTF8,
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func testMOBIWithPayload(payload []byte) []byte {
	const record0Offset = 78 + 8

	data := make([]byte, record0Offset+32)
	copy(data[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(data[76:78], 1)
	binary.BigEndian.PutUint32(data[78:82], record0Offset)
	copy(data[record0Offset+16:record0Offset+20], "MOBI")
	return append(data, payload...)
}

func testMOBI6PalmDOCFile(title string, text []byte) []byte {
	const (
		palmDBHeaderSize        = 78
		palmDBRecordSize        = 8
		mobiHeaderLength uint32 = 0xe8
		mobiNoImageIndex uint32 = 0xffffffff
	)
	exth := testConverterEXTH([]testConverterEXTHRecord{
		{typ: 501, value: []byte("EBOK")},
	})
	titleOffset := 16 + int(mobiHeaderLength) + len(exth)
	titleBytes := []byte(title)
	record0 := make([]byte, titleOffset+len(titleBytes))
	binary.BigEndian.PutUint16(record0[0:2], 2)
	binary.BigEndian.PutUint32(record0[4:8], uint32(len(text)))
	binary.BigEndian.PutUint16(record0[8:10], 1)
	binary.BigEndian.PutUint16(record0[10:12], 4096)
	copy(record0[16:20], "MOBI")
	binary.BigEndian.PutUint32(record0[20:24], mobiHeaderLength)
	binary.BigEndian.PutUint32(record0[28:32], 65001)
	binary.BigEndian.PutUint32(record0[0x54:0x58], uint32(titleOffset))
	binary.BigEndian.PutUint32(record0[0x58:0x5c], uint32(len(titleBytes)))
	binary.BigEndian.PutUint32(record0[0x5c:0x60], 0x09)
	binary.BigEndian.PutUint32(record0[0x68:0x6c], 6)
	binary.BigEndian.PutUint32(record0[0x6c:0x70], mobiNoImageIndex)
	binary.BigEndian.PutUint32(record0[0x80:0x84], 0x40)
	copy(record0[16+mobiHeaderLength:], exth)
	copy(record0[titleOffset:], titleBytes)

	records := [][]byte{record0, text}
	header := make([]byte, palmDBHeaderSize)
	copy(header[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(header[76:78], uint16(len(records)))
	table := make([]byte, len(records)*palmDBRecordSize)
	offset := palmDBHeaderSize + len(table)
	for i, record := range records {
		binary.BigEndian.PutUint32(table[i*palmDBRecordSize:i*palmDBRecordSize+4], uint32(offset))
		offset += len(record)
	}
	out := append(header, table...)
	for _, record := range records {
		out = append(out, record...)
	}
	return out
}

func testPalmDOCFileForConverter(title string, compression uint16, textRecords [][]byte, textLength uint32) []byte {
	return testPalmDOCFileForConverterWithResources(title, compression, textRecords, textLength, nil)
}

func testPalmDOCFileForConverterWithResources(title string, compression uint16, textRecords [][]byte, textLength uint32, resources [][]byte) []byte {
	record0 := make([]byte, 16)
	binary.BigEndian.PutUint16(record0[0:2], compression)
	binary.BigEndian.PutUint32(record0[4:8], textLength)
	binary.BigEndian.PutUint16(record0[8:10], uint16(len(textRecords)))
	binary.BigEndian.PutUint16(record0[10:12], 4096)
	records := append([][]byte{record0}, textRecords...)
	records = append(records, resources...)
	return testPalmDBFileForConverter(title, "TEXtREAd", records)
}

func testPalmDBFileForConverter(name, typeCreator string, records [][]byte) []byte {
	const (
		palmDBHeaderSize = 78
		palmDBRecordSize = 8
		palmDBNameBytes  = 32
	)
	header := make([]byte, palmDBHeaderSize)
	copy(header[:palmDBNameBytes], []byte(name))
	copy(header[60:68], []byte(typeCreator))
	binary.BigEndian.PutUint16(header[76:78], uint16(len(records)))
	table := make([]byte, len(records)*palmDBRecordSize)
	offset := palmDBHeaderSize + len(table)
	for i, record := range records {
		binary.BigEndian.PutUint32(table[i*palmDBRecordSize:i*palmDBRecordSize+4], uint32(offset))
		offset += len(record)
	}
	out := append(header, table...)
	for _, record := range records {
		out = append(out, record...)
	}
	return out
}

type testConverterEXTHRecord struct {
	typ   uint32
	value []byte
}

func testConverterEXTH(records []testConverterEXTHRecord) []byte {
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

func converterWindows1251(t *testing.T, src string) []byte {
	t.Helper()
	encoded, err := charmap.Windows1251.NewEncoder().String(src)
	if err != nil {
		t.Fatalf("encode windows-1251 fixture: %v", err)
	}
	return []byte(encoded)
}

var converterTinyPNG = []byte{
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

var converterTinyGIF = []byte{
	'G', 'I', 'F', '8', '9', 'a', 0x01, 0x00, 0x01, 0x00, 0x80, 0x00,
	0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01,
	0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
}

func testFB2ForEPUB() []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      <author><nickname>FB2 Author</nickname></author>
      <book-title>FB2 Export</book-title>
      <genre>sf_history</genre>
      <genre>adventure</genre>
      <date value="1972-01-01">1972</date>
      <lang>en</lang>
      <annotation><p>A short classic annotation.</p></annotation>
      <coverpage><image l:href="#cover.png"/></coverpage>
      <sequence name="Noon Universe" number="3"/>
    </title-info>
    <publish-info>
      <publisher>Mir</publisher>
      <isbn>978-0-306-40615-7</isbn>
    </publish-info>
  </description>
  <body>
    <section id="chapter-1">
      <title><p>Chapter One</p></title>
      <subtitle>* * *</subtitle>
      <p>Text with <strong>bold</strong> and <emphasis>emphasis</emphasis>.</p>
      <p>One <strong>two</strong> <emphasis>three</emphasis>.</p>
      <p><a l:href="https://example.test/path?q=1&amp;x=2">External</a>, <a l:href="#chapter-1">Chapter</a>, <a l:href="javascript:alert(1)">Bad</a>, <a l:href="notes.html#note-1">Local</a>, <a l:href="#note-1" type="note">Note</a>, <a l:href="#comment-1"><sup>Comment</sup></a>.</p>
      <image l:href="#cover.png"/>
    </section>
  </body>
  <body name="notes">
    <section id="note-1"><title><p>1</p></title><p>Note text.</p></section>
  </body>
  <body name="comments">
    <section id="comment-1"><title><p>A</p></title><p>Comment text.</p></section>
  </body>
  <binary id="cover.png" content-type="image/png">` + base64.StdEncoding.EncodeToString(converterTinyPNG) + `</binary>
</FictionBook>`)
}
