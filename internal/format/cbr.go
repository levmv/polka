package format

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"sort"
	"strings"

	"github.com/nwaples/rardecode/v2"

	"github.com/levmv/polka/internal/comicarchive"
	"github.com/levmv/polka/internal/imagecodec"
)

const (
	maxCBREntries               = 4096
	maxCBRDecodedBytes    int64 = 1 << 30
	comicImageHeaderBytes       = 512
)

type comicArchiveScanResult struct {
	pages          []ComicPage
	comicInfo      []byte
	comicInfoName  string
	comicInfoErr   error
	cover          []byte
	coverExtension string
	coverName      string
}

func isCBR(r io.ReaderAt, size int64) bool {
	result, err := scanCBR(r, size, true)
	return err == nil && len(result.pages) > 0
}

// ListCBRPages returns valid image pages in natural archive-path order.
func ListCBRPages(r io.ReaderAt, size int64) ([]ComicPage, error) {
	result, err := scanCBR(r, size, false)
	if err != nil {
		return nil, err
	}
	return result.pages, nil
}

// ExtractCBRMetadataAndCover scans a RAR archive once for both ComicInfo.xml
// metadata and the first naturally sorted page. A metadata error does not
// discard a cover that was extracted successfully.
func ExtractCBRMetadataAndCover(r io.ReaderAt, size int64) (*Metadata, []byte, string, error) {
	result, err := scanCBR(r, size, false)
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

// ExtractCBRMetadata extracts ComicInfo.xml metadata from a CBR archive.
func ExtractCBRMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	meta, _, _, err := ExtractCBRMetadataAndCover(r, size)
	return meta, err
}

// ExtractCBRCover returns the first valid naturally sorted comic page.
func ExtractCBRCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	result, err := scanCBR(r, size, false)
	if err != nil {
		return nil, "", err
	}
	return result.cover, result.coverExtension, nil
}

func scanCBR(src io.ReaderAt, size int64, firstPageOnly bool) (comicArchiveScanResult, error) {
	var result comicArchiveScanResult
	if src == nil || size <= 0 {
		return result, fmt.Errorf("CBR archive is empty")
	}
	rr, err := comicarchive.NewRARReader(src, size)
	if err != nil {
		return result, fmt.Errorf("open CBR archive: %w", err)
	}

	var declaredBytes int64
	entryCount := 0
	for {
		header, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, fmt.Errorf("read CBR archive: %w", err)
		}
		entryCount++
		if entryCount > maxCBREntries {
			return result, fmt.Errorf("CBR archive has more than %d entries", maxCBREntries)
		}
		if !header.UnKnownSize {
			if header.UnPackedSize < 0 || header.UnPackedSize > maxCBRDecodedBytes-declaredBytes {
				return result, fmt.Errorf("CBR archive expands beyond %d bytes", maxCBRDecodedBytes)
			}
			declaredBytes += header.UnPackedSize
		}
		if header.IsDir {
			continue
		}
		if header.Encrypted || header.HeaderEncrypted {
			return result, fmt.Errorf("encrypted CBR entries are not supported")
		}

		name := NormalizeZipName(header.Name)
		if name == "" || isIgnoredComicEntry(name) {
			continue
		}
		if isComicInfoName(name) {
			if betterComicInfoName(name, result.comicInfoName) {
				result.comicInfoName = name
				result.comicInfo, result.comicInfoErr = readComicEntryLimited(rr, nil, maxCBZComicInfoBytes)
				if result.comicInfoErr != nil {
					result.comicInfoErr = fmt.Errorf("read %s: %w", name, result.comicInfoErr)
				}
			}
			continue
		}

		prefix, err := readComicEntryPrefix(rr, comicImageHeaderBytes)
		if err != nil {
			return result, fmt.Errorf("read CBR entry %s: %w", name, err)
		}
		if _, _, ok := ComicImageTypeFromBytes(prefix); !ok && !isComicImageName(name) {
			continue
		}

		page, raw, err := cbrPageFromEntry(rr, header, name, prefix)
		if err != nil {
			return result, fmt.Errorf("read CBR page %s: %w", name, err)
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

func cbrPageFromEntry(rr io.Reader, header *rardecode.FileHeader, name string, prefix []byte) (*ComicPage, []byte, error) {
	var (
		cfg        image.Config
		formatName string
		raw        []byte
		err        error
	)
	if !header.UnKnownSize && header.UnPackedSize <= maxCBZCoverBytes {
		raw, err = readComicEntryLimited(rr, prefix, maxCBZCoverBytes)
		if err != nil {
			return nil, nil, err
		}
		cfg, formatName, err = imagecodec.DecodeConfig(bytes.NewReader(raw))
	} else {
		cfg, formatName, err = imagecodec.DecodeConfig(io.LimitReader(io.MultiReader(bytes.NewReader(prefix), rr), maxCBZCoverBytes+1))
	}
	if err != nil {
		return nil, nil, nil
	}
	extension, ok := cbzImageExtension(formatName)
	if !ok {
		return nil, nil, nil
	}
	size := uint64(0)
	if !header.UnKnownSize && header.UnPackedSize >= 0 {
		size = uint64(header.UnPackedSize)
	} else if raw != nil {
		size = uint64(len(raw))
	}
	return &ComicPage{
		Name:      name,
		Extension: extension,
		Size:      size,
		Width:     cfg.Width,
		Height:    cfg.Height,
	}, raw, nil
}

func readComicEntryPrefix(r io.Reader, maxBytes int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxBytes))
}

func readComicEntryLimited(r io.Reader, prefix []byte, maxBytes int64) ([]byte, error) {
	if int64(len(prefix)) > maxBytes {
		return nil, fmt.Errorf("entry exceeds %d bytes", maxBytes)
	}
	rest, err := io.ReadAll(io.LimitReader(r, maxBytes-int64(len(prefix))+1))
	if err != nil {
		return nil, err
	}
	if int64(len(prefix)+len(rest)) > maxBytes {
		return nil, fmt.Errorf("entry exceeds %d bytes", maxBytes)
	}
	return append(prefix, rest...), nil
}

func betterComicInfoName(candidate, current string) bool {
	if current == "" {
		return true
	}
	candidateRank, currentRank := comicInfoRank(candidate), comicInfoRank(current)
	if candidateRank != currentRank {
		return candidateRank < currentRank
	}
	return naturalLess(strings.ToLower(candidate), strings.ToLower(current))
}

func betterComicPageName(candidate, current string) bool {
	return current == "" || naturalLess(strings.ToLower(candidate), strings.ToLower(current))
}
