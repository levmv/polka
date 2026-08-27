package format

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"sort"
	"strings"

	"github.com/bodgit/sevenzip"

	"github.com/levmv/polka/internal/imagecodec"
)

const (
	maxCB7Entries            = 4096
	maxCB7DecodedBytes int64 = 1 << 30
)

func isCB7(r io.ReaderAt, size int64) bool {
	result, err := scanCB7(r, size, true)
	return err == nil && len(result.pages) > 0
}

// ListCB7Pages returns valid image pages in natural archive-path order.
func ListCB7Pages(r io.ReaderAt, size int64) ([]ComicPage, error) {
	result, err := scanCB7(r, size, false)
	if err != nil {
		return nil, err
	}
	return result.pages, nil
}

// ExtractCB7MetadataAndCover scans a 7z archive once for both ComicInfo.xml
// metadata and the first naturally sorted page. A metadata error does not
// discard a cover that was extracted successfully.
func ExtractCB7MetadataAndCover(r io.ReaderAt, size int64) (*Metadata, []byte, string, error) {
	result, err := scanCB7(r, size, false)
	if err != nil {
		return nil, nil, "", err
	}
	meta := &Metadata{}
	if result.comicInfoErr != nil {
		return meta, result.cover, result.coverExtension, result.comicInfoErr
	}
	if result.comicInfo != nil {
		meta, err = parseComicInfoMetadata(result.comicInfo)
		if err != nil {
			return &Metadata{}, result.cover, result.coverExtension, err
		}
	}
	return meta, result.cover, result.coverExtension, nil
}

// ExtractCB7Metadata extracts ComicInfo.xml metadata from a CB7 archive.
func ExtractCB7Metadata(r io.ReaderAt, size int64) (*Metadata, error) {
	meta, _, _, err := ExtractCB7MetadataAndCover(r, size)
	return meta, err
}

// ExtractCB7Cover returns the first valid naturally sorted comic page.
func ExtractCB7Cover(r io.ReaderAt, size int64) ([]byte, string, error) {
	result, err := scanCB7(r, size, false)
	if err != nil {
		return nil, "", err
	}
	return result.cover, result.coverExtension, nil
}

func scanCB7(src io.ReaderAt, size int64, firstPageOnly bool) (comicArchiveScanResult, error) {
	var result comicArchiveScanResult
	if src == nil || size <= 0 {
		return result, fmt.Errorf("CB7 archive is empty")
	}
	zr, err := sevenzip.NewReader(src, size)
	if err != nil {
		return result, cb7ReadError("open CB7 archive", err)
	}
	if err := validateCB7Headers(zr.File); err != nil {
		return result, err
	}

	for _, file := range zr.File {
		if !file.FileInfo().Mode().IsRegular() {
			continue
		}
		name := NormalizeZipName(file.Name)
		if name == "" || isIgnoredComicEntry(name) {
			continue
		}
		if isComicInfoName(name) {
			if betterComicInfoName(name, result.comicInfoName) {
				result.comicInfoName = name
				if file.UncompressedSize > uint64(maxCBZComicInfoBytes) {
					result.comicInfo = nil
					result.comicInfoErr = fmt.Errorf("read %s: entry exceeds %d bytes", name, maxCBZComicInfoBytes)
				} else {
					result.comicInfo, result.comicInfoErr = readCB7FileLimited(file, maxCBZComicInfoBytes)
					if result.comicInfoErr != nil {
						result.comicInfoErr = fmt.Errorf("read %s: %w", name, result.comicInfoErr)
					}
				}
			}
			continue
		}

		page, raw, err := cb7PageFromFile(file, name, betterComicPageName(name, result.coverName))
		if err != nil {
			return result, fmt.Errorf("read CB7 page %s: %w", name, err)
		}
		if page == nil {
			continue
		}
		result.pages = append(result.pages, *page)
		if firstPageOnly {
			return result, nil
		}
		if raw != nil && validCBZCoverDimensions(page.Width, page.Height) && betterComicPageName(name, result.coverName) {
			result.cover = raw
			result.coverExtension = page.Extension
			result.coverName = name
		}
	}

	sort.Slice(result.pages, func(i, j int) bool {
		return naturalLess(strings.ToLower(result.pages[i].Name), strings.ToLower(result.pages[j].Name))
	})
	for i := range result.pages {
		result.pages[i].Index = i
	}
	return result, nil
}

func validateCB7Headers(files []*sevenzip.File) error {
	if len(files) > maxCB7Entries {
		return fmt.Errorf("CB7 archive has more than %d entries", maxCB7Entries)
	}
	var declaredBytes int64
	for _, file := range files {
		if file.UncompressedSize > uint64(maxCB7DecodedBytes-declaredBytes) {
			return fmt.Errorf("CB7 archive expands beyond %d bytes", maxCB7DecodedBytes)
		}
		declaredBytes += int64(file.UncompressedSize)
	}
	return nil
}

func cb7PageFromFile(file *sevenzip.File, name string, coverCandidate bool) (*ComicPage, []byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, nil, cb7ReadError("open entry", err)
	}
	prefix, readErr := readComicEntryPrefix(rc, comicImageHeaderBytes)
	if readErr != nil {
		_ = rc.Close()
		return nil, nil, cb7ReadError("read entry header", readErr)
	}
	if _, _, ok := ComicImageTypeFromBytes(prefix); !ok && !isComicImageName(name) {
		if err := rc.Close(); err != nil {
			return nil, nil, cb7ReadError("close entry", err)
		}
		return nil, nil, nil
	}

	var (
		cfg        image.Config
		formatName string
		raw        []byte
	)
	if coverCandidate && file.UncompressedSize <= uint64(maxCBZCoverBytes) {
		raw, err = readComicEntryLimited(rc, prefix, maxCBZCoverBytes)
		if err == nil {
			cfg, formatName, err = imagecodec.DecodeConfig(bytes.NewReader(raw))
		}
	} else {
		cfg, formatName, err = imagecodec.DecodeConfig(io.LimitReader(io.MultiReader(bytes.NewReader(prefix), rc), maxCBZCoverBytes+1))
	}
	closeErr := rc.Close()
	if err != nil {
		return nil, nil, nil
	}
	if closeErr != nil {
		return nil, nil, cb7ReadError("close entry", closeErr)
	}
	extension, ok := cbzImageExtension(formatName)
	if !ok {
		return nil, nil, nil
	}
	return &ComicPage{
		Name:      name,
		Extension: extension,
		Size:      file.UncompressedSize,
		Width:     cfg.Width,
		Height:    cfg.Height,
	}, raw, nil
}

func readCB7FileLimited(file *sevenzip.File, maxBytes int64) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, cb7ReadError("open entry", err)
	}
	raw, readErr := readComicEntryLimited(rc, nil, maxBytes)
	closeErr := rc.Close()
	if readErr != nil {
		return nil, cb7ReadError("read entry", readErr)
	}
	if closeErr != nil {
		return nil, cb7ReadError("close entry", closeErr)
	}
	return raw, nil
}

func cb7ReadError(action string, err error) error {
	if readErr, ok := errors.AsType[*sevenzip.ReadError](err); ok && readErr.Encrypted {
		return fmt.Errorf("%s: encrypted CB7 archives are not supported: %w", action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}
