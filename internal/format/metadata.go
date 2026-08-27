package format

import (
	"io"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

type Metadata = bookmeta.Metadata

// ExtractMetadata reads embedded metadata from an already-open book file.
// Cover extraction is operationally separate, not a product statement that the
// cover is outside metadata. Callers often need different fallback behavior,
// byte handling, and error tolerance for embedded images.
func ExtractMetadata(r io.ReaderAt, size int64, kind Format) (*Metadata, error) {
	meta, err := extractMetadata(r, size, kind)
	if meta != nil {
		meta.Language = bookmeta.NormalizeLanguage(meta.Language)
	}
	return meta, err
}

func extractMetadata(r io.ReaderAt, size int64, kind Format) (*Metadata, error) {
	switch kind {
	case FormatEPUB, FormatKEPUB:
		return ExtractEPUBMetadata(r, size)
	case FormatPDF:
		return ExtractPDFMetadataReader(r, size), nil
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
		return ExtractDJVUMetadata(r, size)
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

// ExtractCover reads an embedded cover image from an already-open book file
// when the format has a native cheap cover path. The returned extension is
// normalized with a leading dot. If no cover is found, it returns nil bytes, an
// empty extension, and nil error.
func ExtractCover(r io.ReaderAt, size int64, kind Format) ([]byte, string, error) {
	cover, ext, err := extractCover(r, size, kind)
	return cover, normalizeCoverExtension(ext), err
}

func extractCover(r io.ReaderAt, size int64, kind Format) ([]byte, string, error) {
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
