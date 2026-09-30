package converter

import (
	"archive/zip"
	"context"
	"io"

	"golang.org/x/net/html"
)

type mobi6Source struct {
	*kindleImages
	styles         map[*zip.File][]mobi6CSSRule
	documentStyles map[*html.Node][]mobi6CSSRule
	cover          int
}

func convertEPUBToMOBI6(ctx context.Context, w io.Writer, src io.ReaderAt, size int64, opts ConversionOptions) error {
	publication, err := readKindleEPUB(ctx, src, size, opts)
	if err != nil {
		return err
	}
	s := &mobi6Source{
		kindleImages: &kindleImages{kindleEPUB: publication, imageIDs: map[*zip.File]int{}},
		styles:       map[*zip.File][]mobi6CSSRule{}, documentStyles: map[*html.Node][]mobi6CSSRule{}, cover: -1,
	}
	if s.coverHref != "" {
		s.cover, err = s.image(s.opfPath, s.coverHref)
		if fatalConversionError(err) {
			return err
		}
		if err != nil {
			s.cover = -1
			opts.warn("Could not include the cover: %v", err)
		}
	}
	for i := range s.documents {
		if err := s.readDocumentStyles(&s.documents[i]); err != nil {
			return err
		}
	}
	book, err := s.render()
	if err != nil {
		return err
	}
	return writeMOBI6(ctx, w, book)
}
