// Package importer prepares and imports books for CLI, uploads, folder imports,
// and Incoming, sharing metadata extraction, duplicate handling, and persistence.
package importer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/storage"
)

type Status string

const (
	StatusImported  Status = "imported"
	StatusDuplicate Status = "duplicate"
)

// Source names a file to import. Path is the local file to read. OriginalName is
// optional and is used for extension/title fallback when Path is a temporary
// upload path. SidecarDir is optional; when empty, sidecars are read next to Path.
type Source struct {
	Path         string
	OriginalName string
	SidecarDir   string
}

// Result summarizes an import. Duplicates refer to the existing book and asset;
// a missing managed file is restored before success is reported.
// BookTrashed reports whether the duplicate belongs to a book in Trash.
type Result struct {
	Status      Status
	BookID      int64
	BookTrashed bool
	Title       string
	AssetID     int64
	Authors     []string
	FormatLabel string
	StoragePath string
	Warnings    []error
}

// GroupResult summarizes importing several files as assets of one logical book.
// Results are in the same order as the input sources.
type GroupResult struct {
	BookID   int64
	Title    string
	Authors  []string
	Restored bool // Attaching new assets restored the book from Trash.
	Results  []Result
	Warnings []error
}

type SourceProbe struct {
	Duplicate bool
	// Existing is populated only for a duplicate already in the catalog,
	// not for a repeated source within a new group.
	Existing    Result
	FormatLabel string
}

type Options struct {
	PathTemplate string

	// KnownAssetSizes is a batch-scoped hint for avoiding staging writes when a
	// source may already exist. A non-nil set enables the fast path, and imports
	// add newly committed asset sizes for later sources in the same batch.
	// Missing sizes never bypass duplicate detection.
	KnownAssetSizes map[int64]struct{}

	// CoverRoot is where cover originals are staged and placed. Covers live in
	// the app data dir, separate from the books root, so this is normally a
	// storage.Root over the data dir. When unset it falls back to the books root
	// (used only by tests that do not split cover storage).
	CoverRoot storage.Root
}

func (o Options) coverRoot(booksRoot storage.Root) storage.Root {
	if o.CoverRoot.Path != "" {
		return o.CoverRoot
	}
	return booksRoot
}

func (o Options) rememberAssetSize(size int64) {
	if o.KnownAssetSizes != nil {
		o.KnownAssetSizes[size] = struct{}{}
	}
}

// ImportFile imports srcPath using the file's own name for detection/fallbacks.
func ImportFile(ctx context.Context, database *db.DB, root storage.Root, srcPath string, extractor *format.Extractor, opts Options) (Result, error) {
	return Import(ctx, database, root, Source{Path: srcPath}, extractor, opts)
}

// Import imports a source, checking for duplicate content before extracting
// metadata and covers. A duplicate of a trashed book stays trashed; callers such
// as browser upload may use Result.BookTrashed to restore it explicitly.
func Import(ctx context.Context, database *db.DB, root storage.Root, src Source, extractor *format.Extractor, opts Options) (Result, error) {
	if existing, found, err := findUnstagedDuplicate(ctx, database.Read(ctx), root, src, opts.KnownAssetSizes); err != nil {
		return Result{}, err
	} else if found {
		return existing.result, nil
	}

	prepared, err := prepareSource(ctx, root, src)
	if err != nil {
		return Result{}, err
	}
	owned := true
	defer func() {
		if owned {
			prepared.staged.Cleanup()
		}
	}()

	if existing, found, err := findDuplicate(database.Read(ctx), prepared.info.SourceHash); err != nil {
		return Result{}, err
	} else if found {
		owned = false // restoreDuplicateAsset takes ownership.
		if err := restoreDuplicateAsset(ctx, database, root, prepared, existing); err != nil {
			return Result{}, err
		}
		return existing.result, nil
	}

	resolved, err := resolvePreparedSource(ctx, &prepared, extractor)
	if err != nil {
		return Result{}, err
	}
	owned = false // persistPrepared takes ownership, including on error.
	return persistPrepared(ctx, database, root, prepared, resolved, opts)
}

// ImportGroup imports several files as assets of one book. It is used
// for Calibre-shaped folders, where one metadata.opf describes all formats in a
// book directory. If every source is already present, no new book is created
// and a trashed book stays trashed. Attaching at least one new asset restores a
// trashed book in the same transaction, so new bytes never land invisibly.
func ImportGroup(ctx context.Context, database *db.DB, root storage.Root, sources []Source, extractor *format.Extractor, opts Options) (GroupResult, error) {
	if len(sources) == 0 {
		return GroupResult{}, errors.New("no sources to import")
	}

	prepared := make([]preparedSource, len(sources))
	results := make([]Result, len(sources))
	cleanup := true
	defer func() {
		if cleanup {
			for _, source := range prepared {
				source.staged.Cleanup()
			}
		}
	}()
	sourceHashes := make(map[[16]byte]int, len(sources))
	repeatedSources := make(map[int]int)
	var newIndexes []int
	var existingBookID int64

	for i, src := range sources {
		if err := context.Cause(ctx); err != nil {
			return GroupResult{}, err
		}
		if existing, found, err := findUnstagedDuplicate(ctx, database.Read(ctx), root, src, opts.KnownAssetSizes); err != nil {
			return GroupResult{}, err
		} else if found {
			results[i] = existing.result
			existingBookID, err = groupExistingBookID(existingBookID, existing.result)
			if err != nil {
				return GroupResult{}, err
			}
			continue
		}
		source, err := prepareSource(ctx, root, src)
		if err != nil {
			return GroupResult{}, err
		}
		prepared[i].info = source.info
		hash := [16]byte(source.info.SourceHash)
		if first, found := sourceHashes[hash]; found {
			source.staged.Cleanup()
			repeatedSources[i] = first
			continue
		}
		sourceHashes[hash] = i

		if existing, found, err := findDuplicate(database.Read(ctx), source.info.SourceHash); err != nil {
			source.staged.Cleanup()
			return GroupResult{}, err
		} else if found {
			if err := restoreDuplicateAsset(ctx, database, root, source, existing); err != nil {
				return GroupResult{}, err
			}
			results[i] = existing.result
			existingBookID, err = groupExistingBookID(existingBookID, existing.result)
			if err != nil {
				return GroupResult{}, err
			}
		} else {
			prepared[i].staged = source.staged
			newIndexes = append(newIndexes, i)
		}
	}

	var group GroupResult
	var err error
	switch {
	case len(newIndexes) == 0:
		group = GroupResult{BookID: existingBookID, Results: results}
	case existingBookID != 0:
		for _, idx := range newIndexes {
			prepared[idx].info.PageCount = preparedSourcePageCount(ctx, prepared[idx], extractor)
		}
		cleanup = false // addAssetsToExistingBook takes ownership of the new sources.
		group, err = addAssetsToExistingBook(ctx, database, root, existingBookID, prepared, newIndexes, results, opts)
	default:
		resolved, resolveErr := resolvePreparedSource(ctx, &prepared[newIndexes[0]], extractor)
		if resolveErr != nil {
			return GroupResult{}, resolveErr
		}
		modTimes := make([]time.Time, len(prepared))
		for i := range prepared {
			modTimes[i] = prepared[i].info.ModTime
		}
		resolved.AddedAt = addedAtForSources(resolved.Metadata.CalibreTimestamp, modTimes, time.Now())
		for _, idx := range newIndexes[1:] {
			prepared[idx].info.PageCount = preparedSourcePageCount(ctx, prepared[idx], extractor)
		}
		cleanup = false // persistNewBookGroup takes ownership of the new sources.
		group, err = persistNewBookGroup(ctx, database, root, resolved, prepared, newIndexes, results, opts)
	}
	if err != nil {
		return GroupResult{}, err
	}
	for i, first := range repeatedSources {
		group.Results[i] = group.Results[first]
		group.Results[i].Status = StatusDuplicate
	}
	return group, nil
}

// ProbeSource performs the per-source checks needed by dry-run paths: it
// fingerprints the source, detects the format, and checks for existing content.
// It intentionally avoids metadata extraction and cover rendering.
func ProbeSource(ctx context.Context, database db.Queryer, src Source) (SourceProbe, error) {
	info, err := fingerprintSource(ctx, src)
	if err != nil {
		return SourceProbe{}, err
	}
	return probeSourceInfo(database, info)
}

// ProbeGroup performs the dry-run checks that depend on sources being imported
// as one logical book. It does not extract metadata or write any files.
func ProbeGroup(ctx context.Context, database db.Queryer, sources []Source) ([]SourceProbe, error) {
	if len(sources) == 0 {
		return nil, errors.New("no sources to import")
	}

	probes := make([]SourceProbe, len(sources))
	sourceHashes := make(map[[16]byte]int, len(sources))
	var existingBookID int64
	for i, src := range sources {
		info, err := fingerprintSource(ctx, src)
		if err != nil {
			return nil, err
		}
		hash := [16]byte(info.SourceHash)
		if first, found := sourceHashes[hash]; found {
			probe := probes[first]
			probe.Duplicate = true
			probes[i] = probe
			continue
		}
		sourceHashes[hash] = i

		probe, err := probeSourceInfo(database, info)
		if err != nil {
			return nil, err
		}
		probes[i] = probe
		if !probe.Duplicate {
			continue
		}
		existingBookID, err = groupExistingBookID(existingBookID, probe.Existing)
		if err != nil {
			return nil, err
		}
	}
	return probes, nil
}

func probeSourceInfo(database db.Queryer, info sourceInfo) (SourceProbe, error) {
	existing, found, err := findDuplicate(database, info.SourceHash)
	if err != nil {
		return SourceProbe{}, err
	}
	return SourceProbe{
		Duplicate:   found,
		Existing:    existing.result,
		FormatLabel: format.FormatLabel(info.Format),
	}, nil
}

func groupExistingBookID(current int64, existing Result) (int64, error) {
	if current == 0 {
		return existing.BookID, nil
	}
	if existing.BookID != current {
		return 0, fmt.Errorf("group sources already belong to different books (%d and %d)", current, existing.BookID)
	}
	return current, nil
}
