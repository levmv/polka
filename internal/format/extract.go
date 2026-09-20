package format

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/levmv/polka/internal/format/djvu"
	"github.com/levmv/polka/internal/format/pdf"
)

// Extractor retains lazy decoders for one command or import batch.
// Close it after the batch; individual documents are released after extraction.
type Extractor struct {
	pdf  *pdf.Renderer
	djvu *djvu.Decoder
}

func NewExtractor() *Extractor {
	return &Extractor{pdf: pdf.NewRenderer(), djvu: djvu.NewDecoder()}
}

func (e *Extractor) Close() error {
	return errors.Join(e.pdf.Close(), e.djvu.Close())
}

// PDFBackendInfo is empty until this extractor renders a PDF cover.
func (e *Extractor) PDFBackendInfo() pdf.BackendInfo { return e.pdf.BackendInfo() }

type ExtractOptions struct {
	Metadata bool
	Cover    bool
	// PageCount requests declared/native counts, without estimating text layout.
	PageCount bool
}

type ExtractResult struct {
	Metadata       *Metadata
	Cover          []byte
	CoverExtension string
}

// Extract shares parsing between requested operations. An error may accompany
// useful partial results. A nil receiver creates a decoder for this call only.
func (e *Extractor) Extract(ctx context.Context, source io.ReaderAt, size int64, kind Format, need ExtractOptions) (ExtractResult, error) {
	var result ExtractResult
	if err := context.Cause(ctx); err != nil {
		return result, err
	}
	if e == nil {
		e = NewExtractor()
		defer e.Close()
	}
	source = contextReaderAt{ctx: ctx, r: source}
	var extractErr, coverErr error
	switch {
	case kind == FormatDJVU && (need.Metadata || need.Cover):
		var decoded djvu.Result
		decoded, extractErr = e.djvu.Extract(ctx, source, size, djvu.Options{Metadata: need.Metadata, Cover: need.Cover})
		result.Metadata = &decoded.Metadata
		result.Cover = decoded.Cover
		if len(result.Cover) > 0 {
			result.CoverExtension = ".jpg"
		}
		// The native count can still describe a document DjVuTang cannot open.
		if need.PageCount && result.Metadata.PageCount == 0 {
			result.Metadata.PageCount, _ = ReadyPageCount(source, size, kind)
		}
	case need.Cover && (need.Metadata || need.PageCount) && IsEPUBContainerFormat(kind):
		result.Metadata, result.Cover, result.CoverExtension, extractErr = ExtractEPUBMetadataAndCover(source, size)
	case need.Cover && (need.Metadata || need.PageCount) && kind == FormatFB2:
		result.Metadata, result.Cover, result.CoverExtension, extractErr = ExtractFB2MetadataAndCover(source, size)
	// Reading ComicInfo can require decoding an entire solid archive. Only
	// include it when metadata is requested, even if we need a page count.
	case need.Metadata && need.Cover && kind == FormatCBR:
		result.Metadata, result.Cover, result.CoverExtension, extractErr = ExtractCBRMetadataAndCover(source, size)
	case need.Metadata && need.Cover && kind == FormatCB7:
		result.Metadata, result.Cover, result.CoverExtension, extractErr = ExtractCB7MetadataAndCover(source, size)
	default:
		if need.Metadata {
			result.Metadata, extractErr = ExtractMetadata(source, size, kind)
		} else if need.PageCount {
			if IsEPUBContainerFormat(kind) {
				// Keep layout information: a fixed-layout spine count must not
				// be replaced with a sidecar's estimate.
				result.Metadata, _ = ExtractEPUBMetadata(source, size)
			} else {
				pages, _ := ReadyPageCount(source, size, kind)
				result.Metadata = &Metadata{PageCount: pages}
			}
		}
		if need.Cover && context.Cause(ctx) == nil {
			result.Cover, result.CoverExtension, coverErr = extractEmbeddedCover(source, size, kind)
		}
	}
	if err := context.Cause(ctx); err != nil {
		return result, err
	}
	if extractErr != nil {
		extractErr = fmt.Errorf("extract %s metadata/cover: %w", FormatLabel(kind), extractErr)
	}
	if coverErr != nil {
		coverErr = fmt.Errorf("extract %s cover: %w", FormatLabel(kind), coverErr)
	}
	if result.Metadata == nil {
		result.Metadata = &Metadata{}
	}

	if kind == FormatPDF {
		// SectionReader keeps PDFium's input seekable without copying the file.
		pdfSource := io.NewSectionReader(source, 0, size)
		if need.Cover {
			var pages int
			result.Cover, pages, coverErr = e.pdf.RenderCoverJPEG(ctx, pdfSource, size, 0)
			if pages > 0 {
				result.Metadata.PageCount = pages
			}
			if coverErr != nil {
				coverErr = fmt.Errorf("render PDF cover: %w", coverErr)
			} else {
				result.CoverExtension = ".jpg"
			}
		}
		if need.PageCount && result.Metadata.PageCount == 0 && context.Cause(ctx) == nil {
			result.Metadata.PageCount, _ = e.pdf.CountPages(ctx, pdfSource, size)
		}
	}
	if err := context.Cause(ctx); err != nil {
		return result, err
	}
	result.CoverExtension = normalizeCoverExtension(result.CoverExtension)
	return result, errors.Join(extractErr, coverErr)
}
