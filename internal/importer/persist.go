package importer

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/storage"
)

type storedBook struct {
	id                int64
	title             string
	sortTitle         string
	series            string
	seriesIndex       string
	primaryAuthor     string
	primaryAuthorSort string
	authors           []string
}

type stagedPlacement struct {
	staged  storage.StagedFile
	relPath string
}

type duplicateMatch struct {
	result      Result
	currentSize sql.NullInt64
}

// persistPrepared takes ownership of source. Failures before commit clean up
// staged files; failures after commit leave them for repair.
func persistPrepared(ctx context.Context, database *db.DB, root storage.Root, source preparedSource, resolved resolvedBook, opts Options) (Result, error) {
	staged := []storage.StagedFile{source.staged}
	committed := false
	defer func() {
		cleanupIfUncommitted(committed, staged)
	}()

	coverRoot := opts.coverRoot(root)
	var coverStage storage.StagedFile
	var err error
	hasCover := false
	if len(resolved.CoverBytes) > 0 {
		coverStage, err = stageBytes(ctx, coverRoot, covers.ImportTempLabel(source.info.SourceHash), resolved.CoverBytes)
		if err != nil {
			return Result{}, err
		}
		hasCover = true
		staged = append(staged, coverStage)
	}

	tx, err := database.BeginWrite(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if existing, found, err := findDuplicate(tx, source.info.SourceHash); err != nil {
		return Result{}, err
	} else if found {
		if err := tx.Rollback(); err != nil {
			return Result{}, err
		}
		if hasCover {
			coverStage.Cleanup()
		}
		staged = nil // restoreDuplicateAsset takes ownership of source.
		if err := restoreDuplicateAsset(ctx, database, root, source, existing); err != nil {
			return Result{}, err
		}
		existing.result.Warnings = resolved.Warnings
		return existing.result, nil
	}

	book, saved, placements, err := insertNewBook(tx, root, resolved, []preparedSource{source}, opts.PathTemplate)
	if err != nil {
		return Result{}, err
	}
	bookID := book.id
	result := saved[0]

	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit tx: %w", err)
	}
	committed = true
	opts.rememberAssetSize(source.info.Size)

	if err := finalizeStaged(root, placements); err != nil {
		return Result{}, fmt.Errorf("place files: %w", err)
	}
	if hasCover {
		if err := coverStage.Finalize(coverRoot, covers.OriginalPath(bookID)); err != nil {
			return Result{}, fmt.Errorf("place cover: %w", err)
		}
	}

	result.Title = book.title
	result.Authors = book.authors
	result.Warnings = resolved.Warnings
	return result, nil
}

func persistNewBookGroup(ctx context.Context, database *db.DB, root storage.Root, resolved resolvedBook, sources []preparedSource, newIndexes []int, results []Result, opts Options) (GroupResult, error) {
	newSources := make([]preparedSource, len(newIndexes))
	staged := make([]storage.StagedFile, 0, len(newIndexes)+1)
	for i, idx := range newIndexes {
		newSources[i] = sources[idx]
		staged = append(staged, newSources[i].staged)
	}
	committed := false
	defer func() {
		cleanupIfUncommitted(committed, staged)
	}()

	coverRoot := opts.coverRoot(root)
	var coverStage storage.StagedFile
	var err error
	hasCover := false
	if len(resolved.CoverBytes) > 0 {
		coverStage, err = stageBytes(ctx, coverRoot, covers.ImportTempLabel(newSources[0].info.SourceHash), resolved.CoverBytes)
		if err != nil {
			return GroupResult{}, err
		}
		hasCover = true
		staged = append(staged, coverStage)
	}

	tx, err := database.BeginWrite(ctx)
	if err != nil {
		return GroupResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := checkNewGroupSources(tx, sources, newIndexes); err != nil {
		return GroupResult{}, err
	}

	book, saved, placements, err := insertNewBook(tx, root, resolved, newSources, opts.PathTemplate)
	if err != nil {
		return GroupResult{}, err
	}
	bookID := book.id
	for i, idx := range newIndexes {
		results[idx] = saved[i]
	}

	if err := tx.Commit(); err != nil {
		return GroupResult{}, fmt.Errorf("commit tx: %w", err)
	}
	committed = true
	for _, source := range newSources {
		opts.rememberAssetSize(source.info.Size)
	}

	if err := finalizeStaged(root, placements); err != nil {
		return GroupResult{}, fmt.Errorf("place files: %w", err)
	}
	if hasCover {
		if err := coverStage.Finalize(coverRoot, covers.OriginalPath(bookID)); err != nil {
			return GroupResult{}, fmt.Errorf("place cover: %w", err)
		}
	}

	return GroupResult{
		BookID:   bookID,
		Title:    book.title,
		Authors:  book.authors,
		Results:  results,
		Warnings: resolved.Warnings,
	}, nil
}

func addAssetsToExistingBook(ctx context.Context, database *db.DB, root storage.Root, bookID int64, sources []preparedSource, newIndexes []int, results []Result, opts Options) (GroupResult, error) {
	committed := false
	defer func() {
		if !committed {
			for _, idx := range newIndexes {
				sources[idx].staged.Cleanup()
			}
		}
	}()

	tx, err := database.BeginWrite(ctx)
	if err != nil {
		return GroupResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := checkNewGroupSources(tx, sources, newIndexes); err != nil {
		return GroupResult{}, err
	}

	var title, sortTitle, series, seriesIndex string
	var bookTrashed bool
	if err := tx.QueryRow(`
		SELECT title, COALESCE(sort_title, ''), COALESCE(series, ''),
		       CASE WHEN series_index IS NULL THEN '' ELSE CAST(series_index AS TEXT) END,
		       deleted_at IS NOT NULL
		FROM books
		WHERE id = ?
	`, bookID).Scan(&title, &sortTitle, &series, &seriesIndex, &bookTrashed); err != nil {
		return GroupResult{}, fmt.Errorf("load book: %w", err)
	}
	if bookTrashed {
		// A sweep must not resurrect an exact duplicate on every restart. Adding
		// genuinely new bytes is different: keeping the new asset on an invisible
		// book would make a successful import look lost. Restore in the same
		// transaction that attaches the new assets.
		if err := db.RestoreBook(tx, bookID); err != nil {
			return GroupResult{}, err
		}
		for i := range results {
			results[i].BookTrashed = false
		}
	}
	primaryAuthor, primaryAuthorSort, err := db.PrimaryAuthor(tx, bookID)
	if err != nil {
		return GroupResult{}, fmt.Errorf("primary author: %w", err)
	}

	hasPrimary, err := bookHasPrimaryAsset(tx, bookID)
	if err != nil {
		return GroupResult{}, err
	}
	book := storedBook{
		id:                bookID,
		title:             title,
		sortTitle:         sortTitle,
		series:            series,
		seriesIndex:       seriesIndex,
		primaryAuthor:     primaryAuthor,
		primaryAuthorSort: primaryAuthorSort,
	}
	placements := make([]stagedPlacement, 0, len(newIndexes))
	for _, idx := range newIndexes {
		source := sources[idx]
		makePrimary := !hasPrimary && len(placements) == 0
		saved, err := insertAsset(tx, root, opts.PathTemplate, book, source.info, makePrimary)
		if err != nil {
			return GroupResult{}, err
		}
		results[idx] = saved
		placements = append(placements, stagedPlacement{staged: source.staged, relPath: saved.StoragePath})
	}
	if err := db.EnsureReadablePrimaryAsset(tx, bookID); err != nil {
		return GroupResult{}, fmt.Errorf("choose primary asset: %w", err)
	}

	if err := db.UpdateSearchIndex(tx, bookID); err != nil {
		return GroupResult{}, fmt.Errorf("update search: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return GroupResult{}, fmt.Errorf("commit tx: %w", err)
	}
	committed = true
	for _, idx := range newIndexes {
		opts.rememberAssetSize(sources[idx].info.Size)
	}

	if err := finalizeStaged(root, placements); err != nil {
		return GroupResult{}, fmt.Errorf("place files: %w", err)
	}

	return GroupResult{BookID: bookID, Title: title, Restored: bookTrashed, Results: results}, nil
}

func insertNewBook(tx *db.Tx, root storage.Root, resolved resolvedBook, sources []preparedSource, pathTemplate string) (storedBook, []Result, []stagedPlacement, error) {
	book, err := insertBookRow(tx, resolved)
	if err != nil {
		return storedBook{}, nil, nil, err
	}

	results := make([]Result, len(sources))
	placements := make([]stagedPlacement, len(sources))
	for i, source := range sources {
		result, err := insertAsset(tx, root, pathTemplate, book, source.info, i == 0)
		if err != nil {
			return storedBook{}, nil, nil, err
		}
		results[i] = result
		placements[i] = stagedPlacement{staged: source.staged, relPath: result.StoragePath}
	}
	if err := db.EnsureReadablePrimaryAsset(tx, book.id); err != nil {
		return storedBook{}, nil, nil, fmt.Errorf("choose primary asset: %w", err)
	}
	if err := db.UpdateSearchIndex(tx, book.id); err != nil {
		return storedBook{}, nil, nil, fmt.Errorf("insert search: %w", err)
	}
	return book, results, placements, nil
}

func insertBookRow(tx *db.Tx, resolved resolvedBook) (storedBook, error) {
	coverVersion := 0
	if len(resolved.CoverBytes) > 0 {
		coverVersion = 1
	}

	meta := resolved.Metadata

	language := bookmeta.NormalizeLanguage(meta.Language)

	var addedAt sql.NullInt64
	if !resolved.AddedAt.IsZero() {
		addedAt = sql.NullInt64{Int64: resolved.AddedAt.Unix(), Valid: true}
	}

	var bookID int64
	err := tx.QueryRow(`
			INSERT INTO books (title, sort_title, series, series_index, description, cover_version, publisher, published_date, language, identifiers, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, unixepoch()))
			RETURNING id
		`, meta.Title, meta.SortTitle, meta.Series, meta.SeriesIndex, meta.Description, coverVersion, meta.Publisher, meta.Date, language, meta.Identifier, addedAt).Scan(&bookID)
	if err != nil {
		return storedBook{}, fmt.Errorf("insert book: %w", err)
	}

	if err := db.SetBookTags(tx, bookID, db.TagKindGenre, bookmeta.NormalizeTags(meta.Genres)); err != nil {
		return storedBook{}, err
	}
	if err := db.SetBookTags(tx, bookID, db.TagKindTag, bookmeta.NormalizeTags(meta.Tags)); err != nil {
		return storedBook{}, err
	}

	// Reuse persisted author sort names so new assets follow the same storage layout.
	primaryAuthor, primaryAuthorSort, err := db.UpsertBookAuthors(tx, bookID, meta.Authors)
	if err != nil {
		return storedBook{}, fmt.Errorf("link authors: %w", err)
	}
	authorNames := make([]string, len(meta.Authors))
	for i, a := range meta.Authors {
		authorNames[i] = a.Name
	}

	return storedBook{
		id:                bookID,
		title:             meta.Title,
		sortTitle:         meta.SortTitle,
		series:            meta.Series,
		seriesIndex:       seriesIndexString(meta.SeriesIndex),
		primaryAuthor:     primaryAuthor,
		primaryAuthorSort: primaryAuthorSort,
		authors:           authorNames,
	}, nil
}

func insertAsset(tx *db.Tx, root storage.Root, template string, book storedBook, info sourceInfo, isPrimary bool) (Result, error) {
	// Render the path after assigning the asset ID, which the template may use.
	// The insert and path update commit together so no empty path becomes visible.
	var assetID int64
	err := tx.QueryRow(`
		INSERT INTO assets (book_id, storage_path, filename, original_filename, extension, format, is_primary, can_read, original_hash, current_hash, original_size, current_size, page_count)
		VALUES (?, '', '', ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0))
		RETURNING id
	`, book.id, filepath.Base(info.sourceName()), info.Extension, format.FormatKey(info.Format), isPrimary, info.CanRead, info.SourceHash, info.SourceHash, info.Size, info.Size, info.PageCount).Scan(&assetID)
	if err != nil {
		return Result{}, fmt.Errorf("insert asset: %w", err)
	}

	storagePath, err := storage.BookPath(template, storage.BookPathData{
		Title:            book.title,
		SortTitle:        book.sortTitle,
		Author:           book.primaryAuthor,
		AuthorSort:       book.primaryAuthorSort,
		Series:           book.series,
		SeriesIndex:      book.seriesIndex,
		AssetID:          assetID,
		BookID:           book.id,
		Ext:              info.Extension,
		OriginalFilename: filepath.Base(info.sourceName()),
	})
	if err != nil {
		return Result{}, err
	}
	var existingAssetID int64
	err = tx.QueryRow("SELECT id FROM assets WHERE storage_path = ? LIMIT 1", storagePath).Scan(&existingAssetID)
	if err == nil {
		return Result{}, fmt.Errorf("storage path collision: %s already belongs to %d", storagePath, existingAssetID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, fmt.Errorf("check storage path collision: %w", err)
	}
	dstPath, err := root.Resolve(storagePath)
	if err != nil {
		return Result{}, err
	}
	if _, err := os.Stat(dstPath); err == nil {
		return Result{}, fmt.Errorf("storage path collision: destination exists on disk: %s", storagePath)
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("check storage destination: %w", err)
	}
	if _, err := tx.Exec("UPDATE assets SET storage_path = ?, filename = ? WHERE id = ?", storagePath, filepath.Base(storagePath), assetID); err != nil {
		return Result{}, fmt.Errorf("set asset path: %w", err)
	}

	return Result{
		Status:      StatusImported,
		BookID:      book.id,
		AssetID:     assetID,
		FormatLabel: format.FormatLabel(info.Format),
		StoragePath: storagePath,
	}, nil
}

func seriesIndexString(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func bookHasPrimaryAsset(tx *db.Tx, bookID int64) (bool, error) {
	var exists int
	err := tx.QueryRow("SELECT 1 FROM assets WHERE book_id = ? AND is_primary = 1 LIMIT 1", bookID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check primary asset: %w", err)
	}
	return true, nil
}

func stageBytes(ctx context.Context, root storage.Root, label string, data []byte) (storage.StagedFile, error) {
	return storage.Stage(root, label, contextReader{ctx: ctx, r: bytes.NewReader(data)})
}

func checkNewGroupSources(tx *db.Tx, sources []preparedSource, indexes []int) error {
	for _, idx := range indexes {
		info := sources[idx].info
		existing, found, err := findDuplicate(tx, info.SourceHash)
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("group source %s became duplicate of book %d during import; retry import", info.sourceName(), existing.result.BookID)
		}
	}
	return nil
}

// finalizeStaged finishes placement after commit even if the caller canceled.
// A placement error leaves the remaining staged files available to repair.
func finalizeStaged(root storage.Root, placements []stagedPlacement) error {
	for _, placement := range placements {
		if err := placement.staged.Finalize(root, placement.relPath); err != nil {
			return err
		}
	}
	return nil
}

func cleanupIfUncommitted(committed bool, files []storage.StagedFile) {
	if committed {
		return
	}
	for _, f := range files {
		f.Cleanup()
	}
}

func findDuplicate(database db.Queryer, fileHash []byte) (duplicateMatch, bool, error) {
	var match duplicateMatch
	err := database.QueryRow(`
		SELECT a.id, a.book_id, a.storage_path, b.deleted_at IS NOT NULL, a.current_size
		FROM assets a
		JOIN books b ON b.id = a.book_id
		WHERE a.original_hash = ? OR a.current_hash = ?
		LIMIT 1
	`, fileHash, fileHash).Scan(
		&match.result.AssetID, &match.result.BookID, &match.result.StoragePath,
		&match.result.BookTrashed, &match.currentSize,
	)
	if err == nil {
		match.result.Status = StatusDuplicate
		return match, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return duplicateMatch{}, false, nil
	}
	return duplicateMatch{}, false, fmt.Errorf("check duplicate: %w", err)
}

func findUnstagedDuplicate(ctx context.Context, database db.Queryer, root storage.Root, source Source, knownSizes map[int64]struct{}) (duplicateMatch, bool, error) {
	fileHash, hashed, err := hashSourceIfKnownSize(ctx, source, knownSizes)
	if err != nil || !hashed {
		return duplicateMatch{}, false, err
	}
	existing, found, err := findDuplicate(database, fileHash)
	if err != nil || !found {
		return duplicateMatch{}, false, err
	}
	needsRestore, err := duplicateNeedsRestore(root, existing)
	if err != nil {
		return duplicateMatch{}, false, err
	}
	if needsRestore {
		// Restoring a missing file needs a staged copy; use the regular import path.
		return duplicateMatch{}, false, nil
	}
	return existing, true, nil
}

func duplicateNeedsRestore(root storage.Root, existing duplicateMatch) (bool, error) {
	absPath, err := root.Resolve(existing.result.StoragePath)
	if err != nil {
		return false, fmt.Errorf("resolve duplicate asset %d (%s): %w", existing.result.AssetID, existing.result.StoragePath, err)
	}
	stat, err := os.Stat(absPath)
	if err == nil {
		if stat.IsDir() {
			return false, fmt.Errorf("restore duplicate asset %d: destination is a directory: %s", existing.result.AssetID, existing.result.StoragePath)
		}
		if existing.currentSize.Valid && stat.Size() != existing.currentSize.Int64 {
			return false, fmt.Errorf(
				"duplicate asset %d (%s) has unexpected size: database records %d bytes, file has %d; run polka check --deep",
				existing.result.AssetID, existing.result.StoragePath, existing.currentSize.Int64, stat.Size(),
			)
		}
		return false, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, fmt.Errorf("stat duplicate asset %d (%s): %w", existing.result.AssetID, existing.result.StoragePath, err)
}

// restoreDuplicateAsset owns the staged copy in source and removes it if unused.
// A failed placement after the DB update leaves that copy for repair.
func restoreDuplicateAsset(ctx context.Context, database *db.DB, root storage.Root, source preparedSource, existing duplicateMatch) error {
	databaseUpdated := false
	defer func() {
		if !databaseUpdated {
			source.staged.Cleanup()
		}
	}()
	needsRestore, err := duplicateNeedsRestore(root, existing)
	if err != nil || !needsRestore {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := db.RecordAssetRestore(database.Write(ctx), existing.result.AssetID, source.info.SourceHash, source.info.Size); err != nil {
		return fmt.Errorf("update restored duplicate asset %d: %w", existing.result.AssetID, err)
	}
	databaseUpdated = true
	if err := source.staged.Finalize(root, existing.result.StoragePath); err != nil {
		return fmt.Errorf("restore duplicate asset %d (%s): %w", existing.result.AssetID, existing.result.StoragePath, err)
	}
	return nil
}
