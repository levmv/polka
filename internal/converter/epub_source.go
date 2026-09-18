package converter

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
)

// EPUBSource exposes the source package as interpreted by EPUB conversion.
// OPF is normalized XML; content documents retain their original bytes.
type EPUBSource struct {
	OPFPath string
	OPF     []byte
	archive *zip.Reader
}

// OpenEPUBSource shares package recovery and resource limits with conversion.
// src must remain readable until the caller finishes using the source.
func OpenEPUBSource(ctx context.Context, src io.ReaderAt, size int64) (*EPUBSource, error) {
	zr, err := zip.NewReader(contextReaderAt{ctx: ctx, r: src}, size)
	if err != nil {
		return nil, fmt.Errorf("open EPUB: %w", err)
	}
	if len(zr.File) > maxConversionResourceCount {
		return nil, fmt.Errorf("EPUB exceeds resource-count limit: %w", ErrResourceLimit)
	}
	ctx = withConversionBudget(ctx, &conversionBudget{limits: defaultConversionLimits})
	pkg, err := kepubReadPackage(ctx, zr)
	if err != nil {
		return nil, err
	}
	return &EPUBSource{OPFPath: pkg.opfPath, OPF: pkg.opfBytes, archive: zr}, nil
}

// ContentDocument resolves a manifest item using the converter's content-type
// and archive-path rules. Non-content and missing items return nil.
func (s *EPUBSource) ContentDocument(href, mediaType string) (*zip.File, error) {
	if !isKEPUBContentDocument(kepubManifestItem{Href: href, MediaType: mediaType}) {
		return nil, nil
	}
	return kepubZipFile(s.archive, cleanKEPUBHref(s.OPFPath, href))
}
