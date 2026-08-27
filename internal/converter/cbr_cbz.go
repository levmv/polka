package converter

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/levmv/polka/internal/comicarchive"
	"github.com/levmv/polka/internal/format"
)

const cbrConversionHeaderBytes = 512

func convertCBRToCBZ(ctx context.Context, w io.Writer, src io.ReaderAt, size int64) error {
	rr, err := comicarchive.NewRARReader(src, size)
	if err != nil {
		return fmt.Errorf("open CBR archive: %w", err)
	}
	zw := zip.NewWriter(w)
	pageCount := 0

	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		header, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read CBR archive: %w", err)
		}
		if header.IsDir {
			continue
		}
		if header.Encrypted || header.HeaderEncrypted {
			return fmt.Errorf("encrypted CBR entries are not supported")
		}

		name := format.NormalizeZipName(header.Name)
		if name == "" {
			if err := copyContext(ctx, io.Discard, rr); err != nil {
				return fmt.Errorf("discard unsafe CBR entry: %w", err)
			}
			continue
		}
		prefix, err := readAllContextLimited(
			ctx,
			io.LimitReader(rr, cbrConversionHeaderBytes),
			cbrConversionHeaderBytes,
			"CBR entry header "+name,
		)
		if err != nil {
			return err
		}
		isPage, err := writeComicCBZEntry(ctx, zw, name, prefix, rr)
		if err != nil {
			return err
		}
		if isPage {
			pageCount++
		}
	}
	if pageCount == 0 {
		return fmt.Errorf("CBR archive contains no supported comic pages")
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("close CBZ archive: %w", err)
	}
	return nil
}

func writeComicCBZEntry(ctx context.Context, zw *zip.Writer, name string, prefix []byte, src io.Reader) (bool, error) {
	isPage := false
	if _, imageExt, ok := format.ComicImageTypeFromBytes(prefix); ok {
		name = comicPageOutputName(name, imageExt)
		isPage = true
	}
	if err := claimConversionResources(ctx, 1, "comic archive entries"); err != nil {
		return false, err
	}
	entry, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return false, fmt.Errorf("create CBZ entry %s: %w", name, err)
	}
	if _, err := entry.Write(prefix); err != nil {
		return false, fmt.Errorf("write CBZ entry %s: %w", name, err)
	}
	if err := copyContext(ctx, entry, src); err != nil {
		return false, fmt.Errorf("write CBZ entry %s: %w", name, err)
	}
	return isPage, nil
}

func comicPageOutputName(name, detectedExtension string) string {
	detectedExtension = strings.ToLower(detectedExtension)
	current := strings.ToLower(path.Ext(name))
	if current == detectedExtension || (detectedExtension == ".jpg" && current == ".jpeg") {
		return name
	}
	return strings.TrimSuffix(name, path.Ext(name)) + detectedExtension
}
