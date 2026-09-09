package format

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/nwaples/rardecode/v2"

	"github.com/levmv/polka/internal/comicarchive"
	"github.com/levmv/polka/internal/imagecodec"
)

const (
	maxCBREntries            = 4096
	maxCBRDecodedBytes int64 = 1 << 30
)

func isCBR(r io.ReaderAt, size int64) bool {
	index, err := readCBRIndex(r, size)
	return err == nil && len(index.pages) > 0
}

// ExtractCBRMetadataAndCover reads ComicInfo.xml and the first usable cover.
// Errors preserve any available results, including the count from file headers.
func ExtractCBRMetadataAndCover(r io.ReaderAt, size int64) (*Metadata, []byte, string, error) {
	index, err := readCBRIndex(r, size)
	if err != nil {
		return nil, nil, "", err
	}
	var meta *Metadata
	var metadataErr error
	// Read targets in archive order when possible so a solid decoder can
	// continue forward without replaying the preceding pages.
	if index.comicInfo != nil && (len(index.pages) == 0 || index.comicInfo.order < index.pages[0].order) {
		meta, metadataErr = index.metadata()
	}
	cover, extension, coverErr := index.cover()
	if meta == nil {
		meta, metadataErr = index.metadata()
	}
	return meta, cover, extension, errors.Join(metadataErr, coverErr)
}

// ExtractCBRMetadata extracts ComicInfo.xml metadata and the archive page count.
func ExtractCBRMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	index, err := readCBRIndex(r, size)
	if err != nil {
		return nil, err
	}
	return index.metadata()
}

// ExtractCBRCover returns the first usable cover among entries with supported
// image extensions, in natural path order.
func ExtractCBRCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	index, err := readCBRIndex(r, size)
	if err != nil {
		return nil, "", err
	}
	return index.cover()
}

type cbrEntry struct {
	file  *rardecode.File
	name  string
	order int
}

type cbrIndex struct {
	src       io.ReaderAt
	size      int64
	pages     []cbrEntry
	comicInfo *cbrEntry
	solid     bool
	reader    *rardecode.Reader
	next      int
}

func readCBRIndex(src io.ReaderAt, size int64) (*cbrIndex, error) {
	if src == nil || size <= 0 {
		return nil, fmt.Errorf("CBR archive is empty")
	}
	files, err := comicarchive.ListRAR(src, size)
	if err != nil {
		return nil, fmt.Errorf("open CBR archive: %w", err)
	}
	if len(files) > maxCBREntries {
		return nil, fmt.Errorf("CBR archive has more than %d entries", maxCBREntries)
	}
	index := &cbrIndex{src: src, size: size}
	var declaredBytes int64
	for order, file := range files {
		if !file.UnKnownSize {
			if file.UnPackedSize < 0 || file.UnPackedSize > maxCBRDecodedBytes-declaredBytes {
				return nil, fmt.Errorf("CBR archive expands beyond %d bytes", maxCBRDecodedBytes)
			}
			declaredBytes += file.UnPackedSize
		}
		if file.Encrypted || file.HeaderEncrypted {
			return nil, fmt.Errorf("encrypted CBR entries are not supported")
		}
		index.solid = index.solid || file.Solid
		if !file.Mode().IsRegular() {
			continue
		}
		name := NormalizeZipName(file.Name)
		if name == "" || isIgnoredComicEntry(name) {
			continue
		}
		entry := cbrEntry{file: file, name: name, order: order}
		if isComicInfoName(name) {
			if index.comicInfo == nil || betterComicInfoName(name, index.comicInfo.name) {
				index.comicInfo = &entry
			}
		} else if isComicImageName(name) {
			index.pages = append(index.pages, entry)
		}
	}
	sort.Slice(index.pages, func(i, j int) bool {
		return naturalLess(strings.ToLower(index.pages[i].name), strings.ToLower(index.pages[j].name))
	})
	return index, nil
}

func (index *cbrIndex) metadata() (*Metadata, error) {
	meta := &Metadata{PageCount: len(index.pages)}
	if index.comicInfo == nil {
		return meta, nil
	}
	raw, err := index.readEntry(*index.comicInfo, maxCBZComicInfoBytes)
	if err != nil {
		return meta, err
	}
	parsed, err := parseComicInfoMetadata(raw)
	if err != nil {
		return meta, err
	}
	parsed.PageCount = meta.PageCount
	return parsed, nil
}

func (index *cbrIndex) cover() ([]byte, string, error) {
	for _, entry := range index.pages {
		if !entry.file.UnKnownSize && entry.file.UnPackedSize > maxCBZCoverBytes {
			continue
		}
		raw, err := index.readEntry(entry, maxCBZCoverBytes)
		if err != nil {
			return nil, "", err
		}
		cfg, formatName, err := imagecodec.DecodeConfig(bytes.NewReader(raw))
		if err != nil || !validCBZCoverDimensions(cfg.Width, cfg.Height) {
			continue
		}
		if extension, ok := cbzImageExtension(formatName); ok {
			return raw, extension, nil
		}
	}
	return nil, "", nil
}

func (index *cbrIndex) readEntry(entry cbrEntry, maxBytes int64) ([]byte, error) {
	if !entry.file.UnKnownSize && entry.file.UnPackedSize > maxBytes {
		return nil, fmt.Errorf("read %s: entry exceeds %d bytes", entry.name, maxBytes)
	}
	rc, err := index.openEntry(entry)
	if err != nil {
		return nil, fmt.Errorf("open CBR entry %s: %w", entry.name, err)
	}
	raw, readErr := readAllLimited(rc, "entry", maxBytes)
	closeErr := rc.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read CBR entry %s: %w", entry.name, err)
	}
	return raw, nil
}

func (index *cbrIndex) openEntry(entry cbrEntry) (io.ReadCloser, error) {
	if !index.solid {
		return entry.file.Open()
	}
	// Solid entries depend on preceding data. Use one sequential decoder for
	// selected targets; listing and counting never create this decoder.
	if index.reader == nil || entry.order < index.next {
		reader, err := comicarchive.NewRARReader(index.src, index.size)
		if err != nil {
			return nil, err
		}
		index.reader, index.next = reader, 0
	}
	for index.next <= entry.order {
		if _, err := index.reader.Next(); err != nil {
			return nil, err
		}
		index.next++
	}
	return io.NopCloser(index.reader), nil
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
