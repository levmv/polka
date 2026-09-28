package cli

import (
	"context"
	"io"
	"os"

	"github.com/levmv/polka/internal/format"
)

func detectAssetFormat(ctx context.Context, detectPath, absPath string) (format.Format, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return format.FormatUnknown, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return format.FormatUnknown, err
	}

	if err := context.Cause(ctx); err != nil {
		return format.FormatUnknown, err
	}
	kind := format.DetectFormat(detectPath, contextReaderAt{ctx: ctx, r: f}, stat.Size())
	return kind, context.Cause(ctx)
}

type contextReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	return r.r.ReadAt(p, offset)
}
