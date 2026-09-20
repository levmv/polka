package format

import (
	"context"
	"io"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format/djvu"
	"github.com/levmv/polka/internal/format/pdf"
)

type Metadata = bookmeta.Metadata

// ExtractMetadata reads embedded metadata from an already-open book file.
// Format parsers normalize language codes. Covers are extracted separately so
// callers can choose their own image fallbacks and error handling.
func ExtractMetadata(r io.ReaderAt, size int64, kind Format) (*Metadata, error) {
	switch kind {
	case FormatEPUB, FormatKEPUB:
		return ExtractEPUBMetadata(r, size)
	case FormatPDF:
		return pdf.ExtractMetadata(r, size), nil
	case FormatMOBI, FormatAZW, FormatAZW3, FormatAZW4, FormatPRC:
		return ExtractMOBIMetadata(r, size)
	case FormatPDB:
		return ExtractPDBMetadata(r, size)
	case FormatFB2:
		return ExtractFB2Metadata(r, size)
	case FormatCBZ:
		return ExtractCBZMetadata(r, size)
	case FormatCBR:
		return ExtractCBRMetadata(r, size)
	case FormatCB7:
		return ExtractCB7Metadata(r, size)
	case FormatDJVU:
		return djvu.ExtractMetadata(context.Background(), r, size)
	case FormatTXTZ:
		return ExtractTXTZMetadata(r, size)
	case FormatMarkdown:
		return ExtractMarkdownMetadata(r, size)
	case FormatHTML, FormatXHTML:
		return ExtractHTMLMetadata(r, size)
	case FormatHTMLZ:
		return ExtractHTMLZMetadata(r, size)
	case FormatDOCX, FormatDOCM:
		return ExtractDOCXMetadata(r, size)
	case FormatODT:
		return ExtractODTMetadata(r, size)
	case FormatRTF:
		return ExtractRTFMetadata(r, size)
	case FormatCHM:
		return ExtractCHMMetadata(r, size)
	}
	return nil, nil
}

func extractEmbeddedCover(r io.ReaderAt, size int64, kind Format) ([]byte, string, error) {
	switch kind {
	case FormatEPUB, FormatKEPUB:
		return ExtractEPUBCover(r, size)
	case FormatMOBI, FormatAZW, FormatAZW3, FormatAZW4, FormatPRC:
		return ExtractMOBICover(r, size)
	case FormatFB2:
		return ExtractFB2Cover(r, size)
	case FormatCBZ:
		return ExtractCBZCover(r, size)
	case FormatCBR:
		return ExtractCBRCover(r, size)
	case FormatCB7:
		return ExtractCB7Cover(r, size)
	case FormatTXTZ:
		return ExtractTXTZCover(r, size)
	case FormatHTML, FormatXHTML:
		return ExtractHTMLCover(r, size)
	case FormatHTMLZ:
		return ExtractHTMLZCover(r, size)
	case FormatDOCX, FormatDOCM:
		return ExtractDOCXCover(r, size)
	case FormatODT:
		return ExtractODTCover(r, size)
	}
	return nil, "", nil
}

func normalizeCoverExtension(ext string) string {
	ext = strings.TrimSpace(strings.ToLower(ext))
	if ext == "" || strings.HasPrefix(ext, ".") {
		return ext
	}
	return "." + ext
}
