package converter

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/bodgit/sevenzip"

	"github.com/levmv/polka/internal/format"
)

const maxCB7ConversionEntries = 4096

func convertCB7ToCBZ(ctx context.Context, w io.Writer, src io.ReaderAt, size int64) error {
	zr, err := sevenzip.NewReader(src, size)
	if err != nil {
		return cb7ConversionError("open CB7 archive", err)
	}
	if len(zr.File) > maxCB7ConversionEntries {
		return fmt.Errorf("CB7 archive has more than %d entries", maxCB7ConversionEntries)
	}
	zw := zip.NewWriter(w)
	pageCount := 0

	for _, file := range zr.File {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if !file.FileInfo().Mode().IsRegular() {
			continue
		}
		name := format.NormalizeZipName(file.Name)
		if name == "" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return cb7ConversionError("open CB7 entry "+name, err)
		}
		prefix, readErr := readAllContextLimited(
			ctx,
			io.LimitReader(rc, cbrConversionHeaderBytes),
			cbrConversionHeaderBytes,
			"CB7 entry header "+name,
		)
		if readErr != nil {
			_ = rc.Close()
			return cb7ConversionError("read CB7 entry "+name, readErr)
		}
		isPage, writeErr := writeComicCBZEntry(ctx, zw, name, prefix, rc)
		closeErr := rc.Close()
		if writeErr != nil {
			return cb7ConversionError("convert CB7 entry "+name, writeErr)
		}
		if closeErr != nil {
			return cb7ConversionError("close CB7 entry "+name, closeErr)
		}
		if isPage {
			pageCount++
		}
	}
	if pageCount == 0 {
		return fmt.Errorf("CB7 archive contains no supported comic pages")
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("close CBZ archive: %w", err)
	}
	return nil
}

func cb7ConversionError(action string, err error) error {
	if readErr, ok := errors.AsType[*sevenzip.ReadError](err); ok && readErr.Encrypted {
		return fmt.Errorf("%s: encrypted CB7 archives are not supported: %w", action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
