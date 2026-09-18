package position

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/xmlutil"
)

// KEPUB identifies a Kobo span in an archive member. Path is the decoded
// ZIP member name; Fragment is the span ID, without a leading '#'.
type KEPUB struct {
	Path     string
	Fragment string
}

// CFIToKEPUB maps a source EPUB CFI to a text span produced by Polka's KEPUB
// converter. Ranges use their start; the result identifies a span without a
// character offset. Only the addressed chapter is converted.
func CFIToKEPUB(ctx context.Context, src io.ReaderAt, size int64, cfi string) (KEPUB, error) {
	point, err := parseCFI(cfi)
	if err != nil {
		return KEPUB{}, err
	}
	book, err := readEPUB(ctx, src, size)
	if err != nil {
		return KEPUB{}, err
	}
	ref, err := resolveCFI(book.opf, point.packagePath)
	if err != nil {
		return KEPUB{}, err
	}
	file := book.files[ref]
	if file == nil || book.spineByPath[file.Name] != ref {
		return KEPUB{}, fmt.Errorf("CFI does not identify a unique EPUB spine resource")
	}
	chapter, spans, err := readKEPUBChapter(ctx, file)
	if err != nil {
		return KEPUB{}, err
	}
	n, err := resolveCFI(chapter.root, point.contentPath)
	if err != nil {
		return KEPUB{}, err
	}
	ancestor := n
	for ancestor != nil && ancestor != chapter.body {
		ancestor = ancestor.parent
	}
	if ancestor == nil || n.end == n.start || point.offset > n.end-n.start {
		return KEPUB{}, fmt.Errorf("CFI is outside chapter text")
	}
	offset := n.start + point.offset
	for id, span := range spans {
		if span.start <= offset && offset < span.end && offset < chapter.body.end {
			return KEPUB{Path: file.Name, Fragment: id}, nil
		}
	}
	return KEPUB{}, fmt.Errorf("CFI has no text-bearing Kobo span")
}

// KEPUBToCFI maps a Kobo span to its start in the source EPUB. The span must
// come from Polka's conversion of src. Image-only spans and chapters whose
// text changes during conversion cannot be mapped.
func KEPUBToCFI(ctx context.Context, src io.ReaderAt, size int64, pos KEPUB) (string, error) {
	book, err := readEPUB(ctx, src, size)
	if err != nil {
		return "", err
	}
	ref := book.spineByPath[pos.Path]
	if ref == nil {
		return "", fmt.Errorf("position does not identify a unique EPUB spine resource")
	}
	chapter, spans, err := readKEPUBChapter(ctx, book.files[ref])
	if err != nil {
		return "", err
	}
	span, ok := spans[pos.Fragment]
	if !ok || span.start == span.end {
		return "", fmt.Errorf("position has no text-bearing Kobo span")
	}
	for _, run := range chapter.runs {
		if run.start <= span.start && span.start < run.end {
			return formatCFI(ref, run, span.start-run.start), nil
		}
	}
	return "", fmt.Errorf("Kobo span has no source text location")
}

type kepubTextSpan struct {
	start, end int
}

func readKEPUBChapter(ctx context.Context, file *zip.File) (*epubChapter, map[string]kepubTextSpan, error) {
	raw, chapter, err := readChapter(ctx, file)
	if err != nil {
		return nil, nil, err
	}
	converted, err := converter.RenderKEPUBContent(raw)
	if err != nil {
		return nil, nil, err
	}
	var text strings.Builder
	for _, run := range chapter.runs {
		text.WriteString(run.text)
	}
	spans, err := readKEPUBTextSpans(ctx, converted, text.String())
	if err != nil {
		return nil, nil, err
	}
	return chapter, spans, nil
}

func readKEPUBTextSpans(ctx context.Context, raw []byte, sourceText string) (map[string]kepubTextSpan, error) {
	if len(raw) > maxDocumentBytes {
		return nil, fmt.Errorf("KEPUB chapter exceeds %d bytes", maxDocumentBytes)
	}
	spans := make(map[string]kepubTextSpan)
	remaining := sourceText
	offset, nodes := 0, 0
	inBody := false
	spanID, spanDepth, spanStart := "", 0, 0
	err := xmlutil.WalkXHTML(raw, func(token xml.Token, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			nodes++
			if depth == 2 && t.Name.Local == "body" {
				inBody = true
			}
			if inBody && t.Name.Local == "span" {
				id, class := "", ""
				for _, attr := range t.Attr {
					if attr.Name.Space != "" {
						continue
					}
					switch attr.Name.Local {
					case "id":
						id = attr.Value
					case "class":
						class = attr.Value
					}
				}
				if class == "koboSpan" {
					// The converter generates non-nested Kobo spans.
					spanID, spanDepth, spanStart = id, depth, offset
				}
			}
		case xml.CharData:
			nodes++
			if inBody {
				text := string(t)
				n := min(len(text), len(remaining))
				// HTML parsing moves trailing document whitespace into the body.
				// Only that suffix may differ; every source character keeps its offset.
				if text[:n] != remaining[:n] || strings.Trim(text[n:], " \t\r\n") != "" {
					return fmt.Errorf("chapter text changed; exact mapping unavailable")
				}
				remaining = remaining[n:]
				offset += utf16Len(text)
			}
		case xml.EndElement:
			if depth == spanDepth {
				spans[spanID] = kepubTextSpan{start: spanStart, end: offset}
				spanDepth = 0
			}
			if depth == 2 && t.Name.Local == "body" {
				inBody = false
			}
		}
		if depth > maxDepth || nodes > maxNodes {
			return fmt.Errorf("KEPUB chapter exceeds node/depth limit")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if remaining != "" {
		return nil, fmt.Errorf("chapter text changed; exact mapping unavailable")
	}
	return spans, nil
}
