package converter

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/levmv/polka/internal/format"
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
	if !isEPUBContentDocument(epubManifestItem{Href: href, MediaType: mediaType}) {
		return nil, nil
	}
	return epubZipFile(s.archive, packageResourcePath(s.OPFPath, href))
}

type epubManifestItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

func isEPUBContentDocument(item epubManifestItem) bool {
	mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if mediaType == "application/xhtml+xml" || mediaType == "text/html" {
		return true
	}
	switch strings.ToLower(path.Ext(item.Href)) {
	case ".xhtml", ".xhtm", ".html", ".htm":
		return true
	default:
		return false
	}
}

func epubZipFile(zr *zip.Reader, name string) (*zip.File, error) {
	file, ambiguous := format.ResolveZIPEntry(zr, name)
	if ambiguous {
		return nil, fmt.Errorf("entry %q has multiple matching archive members", name)
	}
	return file, nil
}
