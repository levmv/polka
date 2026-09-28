package importer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/format"
)

type resolvedBook struct {
	Metadata   *bookmeta.Metadata
	CoverBytes []byte
	AddedAt    time.Time
	Warnings   []error
}

func resolvePreparedSource(ctx context.Context, source *preparedSource, extractor *format.Extractor) (resolvedBook, error) {
	f, err := source.staged.Open()
	if err != nil {
		return resolvedBook{}, err
	}
	defer f.Close()

	resolved, pageCount, err := resolveSource(ctx, source.info, f, extractor)
	if err != nil {
		return resolvedBook{}, err
	}
	source.info.PageCount = pageCount
	return resolved, nil
}

// resolveSource accepts partial metadata and missing covers. Extraction errors
// become warnings, but cancellation must still abort the import.
func resolveSource(ctx context.Context, info sourceInfo, content io.ReaderAt, extractor *format.Extractor) (resolvedBook, int, error) {
	if err := context.Cause(ctx); err != nil {
		return resolvedBook{}, 0, err
	}

	resolved := resolvedBook{}
	if warning := unknownStructuredFormatWarning(info); warning != nil {
		resolved.Warnings = append(resolved.Warnings, warning)
	}

	var coverBytes []byte
	sidecarCover, sidecarCoverErr := readSidecarCover(ctx, info.Source)
	if sidecarCoverErr != nil {
		resolved.Warnings = append(resolved.Warnings, fmt.Errorf("skip sidecar cover for %s: %w", info.Path, sidecarCoverErr))
	} else if len(sidecarCover) > 0 {
		if _, err := covers.Validate(sidecarCover); err != nil {
			resolved.Warnings = append(resolved.Warnings, fmt.Errorf("skip invalid cover for %s: %w", info.Path, err))
		} else {
			coverBytes = sidecarCover
		}
	}
	sidecarMeta := readSidecarOPF(ctx, info.Source)
	if err := context.Cause(ctx); err != nil {
		return resolvedBook{}, 0, err
	}
	extracted, extractErr := extractor.Extract(ctx, content, info.Size, info.Format, format.ExtractOptions{
		Metadata:  !sidecarMetadataComplete(sidecarMeta),
		Cover:     len(coverBytes) == 0,
		PageCount: true,
	})
	if err := context.Cause(ctx); err != nil {
		return resolvedBook{}, 0, err
	}
	if extractErr != nil {
		resolved.Warnings = append(resolved.Warnings, fmt.Errorf("%s: %w", info.Path, extractErr))
	}
	if len(coverBytes) == 0 && len(extracted.Cover) > 0 {
		if err := context.Cause(ctx); err != nil {
			return resolvedBook{}, 0, err
		}
		if _, err := covers.Validate(extracted.Cover); err != nil {
			resolved.Warnings = append(resolved.Warnings, fmt.Errorf("skip invalid cover for %s: %w", info.Path, err))
		} else {
			coverBytes = extracted.Cover
		}
		if err := context.Cause(ctx); err != nil {
			return resolvedBook{}, 0, err
		}
	}
	pageCount := selectPageCount(info.Format, extracted.Metadata.PageCount, extracted.Metadata.FixedLayout, sidecarMeta)
	resolved.CoverBytes = coverBytes
	resolved.applyMetadata(info.Source, extracted.Metadata, sidecarMeta)
	resolved.AddedAt = addedAtForSources(resolved.Metadata.CalibreTimestamp, []time.Time{info.ModTime}, time.Now())
	return resolved, pageCount, nil
}

func (r *resolvedBook) applyMetadata(source Source, embedded, sidecar *bookmeta.Metadata) {
	meta := embedded
	if sidecarMetadataComplete(sidecar) {
		meta = sidecar
	}
	if meta == nil {
		meta = &bookmeta.Metadata{}
	}
	if meta != sidecar {
		meta.Merge(sidecar)
	}

	name := source.sourceName()
	if filenameMeta, ok := structuredFilenameMetadata(name); ok {
		if meta.Title == "" {
			meta.Title = filenameMeta.Title
		}
		if len(meta.Authors) == 0 {
			meta.Authors = filenameMeta.Authors
		}
		if meta.Identifier == "" {
			meta.Identifier = filenameMeta.Identifier
		}
	}

	if meta.Title == "" {
		base := filepath.Base(name)
		meta.Title = strings.TrimSuffix(base, format.BookExtension(base))
	}

	if meta.SortTitle == "" {
		// Follow the display title unless the file supplies a sort title;
		// article rules differ by language.
		meta.SortTitle = meta.Title
	}

	for i := range meta.Authors {
		if meta.Authors[i].SortName == "" {
			meta.Authors[i].SortName = bookmeta.AuthorSort(meta.Authors[i].Name)
		}
	}
	r.Metadata = meta
}

func preparedSourcePageCount(ctx context.Context, source preparedSource, extractor *format.Extractor) int {
	f, err := source.staged.Open()
	if err != nil {
		return 0
	}
	defer f.Close()
	return sourcePageCount(ctx, source.info, f, extractor)
}

func sourcePageCount(ctx context.Context, info sourceInfo, content io.ReaderAt, extractor *format.Extractor) int {
	extracted, _ := extractor.Extract(ctx, content, info.Size, info.Format, format.ExtractOptions{PageCount: true})
	if extracted.Metadata == nil {
		return 0
	}
	filePages, fixedLayout := extracted.Metadata.PageCount, extracted.Metadata.FixedLayout
	var sidecarMeta *bookmeta.Metadata
	if !fixedLayout && format.IsPageCountApproximate(info.Format) {
		sidecarMeta = readSidecarOPF(ctx, info.Source)
	}
	return selectPageCount(info.Format, filePages, fixedLayout, sidecarMeta)
}

func selectPageCount(kind format.Format, filePages int, fixedLayout bool, sidecar *bookmeta.Metadata) int {
	// Calibre sidecars can be newer than embedded declarations. Counts from
	// the file's page structure still take precedence.
	if sidecar == nil || sidecar.PageCount == 0 || fixedLayout || !format.IsPageCountApproximate(kind) {
		return filePages
	}
	switch kind {
	case format.FormatEPUB, format.FormatKEPUB, format.FormatFB2:
		return sidecar.PageCount
	}
	if filePages == 0 {
		return sidecar.PageCount
	}
	return filePages
}

func addedAtForSources(calibreTimestamp string, modTimes []time.Time, now time.Time) time.Time {
	if addedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(calibreTimestamp)); err == nil && plausibleAddedAt(addedAt, now) {
		return addedAt
	}

	var earliest time.Time
	for _, modTime := range modTimes {
		if !plausibleAddedAt(modTime, now) {
			continue
		}
		if earliest.IsZero() || modTime.Before(earliest) {
			earliest = modTime
		}
	}
	if !earliest.IsZero() {
		return earliest
	}
	return now
}

func plausibleAddedAt(candidate, now time.Time) bool {
	return candidate.Unix() > 0 && !candidate.After(now)
}

func structuredFilenameMetadata(name string) (*bookmeta.Metadata, bool) {
	base := filepath.Base(name)
	title := strings.TrimSuffix(base, format.BookExtension(base))
	parts := strings.Split(title, " -- ")
	if len(parts) < 4 {
		return nil, false
	}

	cleanParts := make([]string, 0, len(parts))
	for _, part := range parts {
		part = cleanStructuredFilenameField(part)
		if part != "" {
			cleanParts = append(cleanParts, part)
		}
	}
	if len(cleanParts) < 4 {
		return nil, false
	}
	if !structuredFilenameHasBibliographicSignal(cleanParts[2:]) {
		return nil, false
	}

	meta := &bookmeta.Metadata{
		Title:   cleanParts[0],
		Authors: []bookmeta.AuthorMeta{{Name: cleanParts[1]}},
	}
	for _, part := range cleanParts[2:] {
		if id, ok := isbnIdentifierFromFilenamePart(part); ok {
			meta.Identifier = bookmeta.FormatIdentifiers([]bookmeta.Identifier{id})
			break
		}
	}
	return meta, true
}

func structuredFilenameHasBibliographicSignal(parts []string) bool {
	for _, part := range parts {
		if _, ok := isbnIdentifierFromFilenamePart(part); ok {
			return true
		}
		if looksLikeFilenameYear(part) {
			return true
		}
	}
	return false
}

func looksLikeFilenameYear(part string) bool {
	if len(part) != 4 {
		return false
	}
	for _, r := range part {
		if r < '0' || r > '9' {
			return false
		}
	}
	return part >= "1000" && part <= "2099"
}

func cleanStructuredFilenameField(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func isbnIdentifierFromFilenamePart(part string) (bookmeta.Identifier, bool) {
	fields := strings.Fields(part)
	if len(fields) < 2 {
		return bookmeta.Identifier{}, false
	}
	label := strings.Trim(strings.ToLower(fields[0]), ":")
	label = strings.ReplaceAll(label, "-", "")
	switch label {
	case "isbn", "isbn10", "isbn13":
	default:
		return bookmeta.Identifier{}, false
	}
	value := strings.Join(fields[1:], " ")
	if !bookmeta.ValidISBN(value) {
		return bookmeta.Identifier{}, false
	}
	return bookmeta.Identifier{Type: "isbn", Value: value}, true
}

func sidecarMetadataComplete(meta *bookmeta.Metadata) bool {
	return meta != nil && meta.Title != "" && len(meta.Authors) > 0
}

func unknownStructuredFormatWarning(info sourceInfo) error {
	if info.Format != format.FormatUnknown {
		return nil
	}
	if !format.KnownBookExtension(info.Extension) {
		return nil
	}
	return fmt.Errorf("unrecognized %s contents for %s; importing as opaque file", strings.ToLower(info.Extension), info.Path)
}

// readSidecarOPF reads metadata.opf from the source's sidecar directory.
// Missing, unreadable, or invalid sidecars are ignored.
func readSidecarOPF(ctx context.Context, src Source) *bookmeta.Metadata {
	f, err := os.Open(filepath.Join(src.sidecarDir(), "metadata.opf"))
	if err != nil {
		return nil
	}
	defer f.Close()
	meta, err := bookmeta.ParseOPF(contextReader{ctx: ctx, r: f})
	if err != nil {
		return nil
	}
	return meta
}

const maxSidecarCoverBytes int64 = 32 << 20

var sidecarCoverNames = []string{"cover.jpg", "cover.jpeg", "cover.png", "cover.webp", "cover.gif"}

// IsSidecarFile recognizes conventional metadata and cover sidecars for import.
func IsSidecarFile(name string) bool {
	name = strings.ToLower(name)
	return name == "metadata.opf" || slices.Contains(sidecarCoverNames, name)
}

// readSidecarCover reads the first sidecar it can open. Read and size errors
// let the caller warn and fall back to an embedded cover.
func readSidecarCover(ctx context.Context, src Source) ([]byte, error) {
	dir := src.sidecarDir()
	for _, name := range sidecarCoverNames {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()
		stat, statErr := f.Stat()
		if statErr != nil {
			return nil, fmt.Errorf("stat %s: %w", path, statErr)
		}
		if stat.Size() > maxSidecarCoverBytes {
			return nil, fmt.Errorf("%s exceeds %d bytes", path, maxSidecarCoverBytes)
		}
		b, readErr := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: f}, maxSidecarCoverBytes+1))
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		if int64(len(b)) > maxSidecarCoverBytes {
			return nil, fmt.Errorf("%s exceeds %d bytes", path, maxSidecarCoverBytes)
		}
		return b, nil
	}
	return nil, nil
}
