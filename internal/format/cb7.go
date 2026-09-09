package format

import (
	"bytes"
	"errors"
	"fmt"
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
	index, err := readCB7Index(r, size)
	return err == nil && len(index.pages) > 0
}

// ExtractCB7MetadataAndCover reads ComicInfo.xml and the first usable cover
// through one archive reader. Errors preserve any available results,
// including the page count from the archive index.
func ExtractCB7MetadataAndCover(r io.ReaderAt, size int64) (*Metadata, []byte, string, error) {
	index, err := readCB7Index(r, size)
	if err != nil {
		return nil, nil, "", err
	}
	var meta *Metadata
	var metadataErr error
	// Read the earlier target first so a solid block can continue forward
	// instead of being decompressed again for the other entry.
	if index.comicInfo != nil && (len(index.pages) == 0 || index.comicInfo.order < index.pages[0].order) {
		meta, metadataErr = index.metadata()
	}
	cover, extension, coverErr := index.cover()
	if meta == nil {
		meta, metadataErr = index.metadata()
	}
	return meta, cover, extension, errors.Join(metadataErr, coverErr)
}

// ExtractCB7Metadata extracts ComicInfo.xml metadata from a CB7 archive.
func ExtractCB7Metadata(r io.ReaderAt, size int64) (*Metadata, error) {
	index, err := readCB7Index(r, size)
	if err != nil {
		return nil, err
	}
	return index.metadata()
}

// ExtractCB7Cover returns the first usable cover among entries with supported
// image extensions, in natural path order.
func ExtractCB7Cover(r io.ReaderAt, size int64) ([]byte, string, error) {
	index, err := readCB7Index(r, size)
	if err != nil {
		return nil, "", err
	}
	return index.cover()
}

type cb7Entry struct {
	file  *sevenzip.File
	name  string
	order int
}

type cb7Index struct {
	pages     []cb7Entry
	comicInfo *cb7Entry
}

func readCB7Index(src io.ReaderAt, size int64) (*cb7Index, error) {
	if src == nil || size <= 0 {
		return nil, fmt.Errorf("CB7 archive is empty")
	}
	zr, err := sevenzip.NewReader(src, size)
	if err != nil {
		return nil, cb7ReadError("open CB7 archive", err)
	}
	if len(zr.File) > maxCB7Entries {
		return nil, fmt.Errorf("CB7 archive has more than %d entries", maxCB7Entries)
	}

	index := &cb7Index{}
	var declaredBytes int64
	// In a solid archive, reaching an image header can decompress every preceding
	// page. Collect pages by name to keep import and counting cheap.
	for order, file := range zr.File {
		if file.UncompressedSize > uint64(maxCB7DecodedBytes-declaredBytes) {
			return nil, fmt.Errorf("CB7 archive expands beyond %d bytes", maxCB7DecodedBytes)
		}
		declaredBytes += int64(file.UncompressedSize)
		if !file.FileInfo().Mode().IsRegular() {
			continue
		}
		name := NormalizeZipName(file.Name)
		if name == "" || isIgnoredComicEntry(name) {
			continue
		}
		entry := cb7Entry{file: file, name: name, order: order}
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

func (index *cb7Index) metadata() (*Metadata, error) {
	meta := &Metadata{PageCount: len(index.pages)}
	if index.comicInfo == nil {
		return meta, nil
	}
	entry := index.comicInfo
	if entry.file.UncompressedSize > uint64(maxCBZComicInfoBytes) {
		return meta, fmt.Errorf("read %s: entry exceeds %d bytes", entry.name, maxCBZComicInfoBytes)
	}
	raw, err := readCB7FileLimited(entry.file, maxCBZComicInfoBytes)
	if err != nil {
		return meta, fmt.Errorf("read %s: %w", entry.name, err)
	}
	parsed, err := parseComicInfoMetadata(raw)
	if err != nil {
		return meta, err
	}
	parsed.PageCount = meta.PageCount
	return parsed, nil
}

func (index *cb7Index) cover() ([]byte, string, error) {
	for _, entry := range index.pages {
		if entry.file.UncompressedSize > uint64(maxCBZCoverBytes) {
			continue
		}
		raw, err := readCB7FileLimited(entry.file, maxCBZCoverBytes)
		if err != nil {
			return nil, "", fmt.Errorf("read CB7 cover %s: %w", entry.name, err)
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

func readCB7FileLimited(file *sevenzip.File, maxBytes int64) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, cb7ReadError("open entry", err)
	}
	raw, readErr := readAllLimited(rc, "entry", maxBytes)
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
