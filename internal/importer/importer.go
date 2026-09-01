// Package importer owns the shared "add this file to the library" pipeline.
//
// CLI import and browser upload need the same behavior: inspect the
// source file, extract best-effort metadata/cover data, write the SQLite rows,
// and place the managed file. Keeping that orchestration out of internal/cli
// avoids a second web-specific import path that would drift from the CLI.
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/id"
	"github.com/levmv/polka/internal/pdfcover"
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

// Result summarizes an import attempt. Duplicate imports return the existing
// asset/work ids; if the DB row points at a missing managed file, import restores
// that file from the duplicate source before returning. WorkTrashed reports why
// a duplicate is not visible without making restoration a generic import side
// effect.
type Result struct {
	Status      Status
	WorkID      string
	WorkTrashed bool
	Title       string
	AssetID     string
	Authors     []string
	Format      string
	StoragePath string
	Warnings    []error
}

// GroupResult summarizes importing several files as assets of one logical work.
// Results are in the same order as the input sources.
type GroupResult struct {
	WorkID   string
	Title    string
	Authors  []string
	Restored bool
	Results  []Result
	Warnings []error
}

type SourceProbe struct {
	Duplicate bool
	Existing  Result
	Format    string
}

type Options struct {
	PathTemplate string

	// CoverRoot is where cover originals are staged and placed. Covers live in
	// the app data dir, separate from the books root, so this is normally a
	// storage.Root over the data dir. When unset it falls back to the books root
	// (used only by tests that do not split cover storage).
	CoverRoot storage.Root
}

// coverRoot returns the effective root for cover files: the configured CoverRoot
// when set, otherwise the books root.
func (o Options) coverRoot(booksRoot storage.Root) storage.Root {
	if o.CoverRoot.Path != "" {
		return o.CoverRoot
	}
	return booksRoot
}

// Plan is the resolved source state that is ready to persist. It deliberately
// contains no open file handles so callers can inspect/test it independently of
// the DB transaction and storage placement.
type Plan struct {
	Source       Source
	Size         int64
	SourceSHA256 string
	Format       format.Format
	Extension    string
	CanRead      bool
	Metadata     *bookmeta.Metadata
	CoverBytes   []byte
	Title        string
	SortTitle    string
	Authors      []bookmeta.AuthorMeta
	AddedAt      time.Time
	Warnings     []error
}

type sourceInfo struct {
	Source
	Size         int64
	SourceSHA256 string
	Format       format.Format
	Extension    string
	CanRead      bool
	ModTime      time.Time
}

type storedWork struct {
	id                string
	title             string
	sortTitle         string
	series            string
	seriesIndex       string
	primaryAuthor     string
	primaryAuthorSort string
	authors           []string
}

type preparedAsset struct {
	result Result
	staged storage.StagedFile
	info   sourceInfo
}

type stagedResult struct {
	staged  storage.StagedFile
	relPath string
}

// contextFile makes the importer context authoritative for sequential and
// random source reads without pushing Context variants through every format
// parser. Cancellation is cooperative between file operations.
type contextFile struct {
	ctx  context.Context
	file *os.File
}

func (f contextFile) Read(p []byte) (int, error) {
	if err := importContextError(f.ctx); err != nil {
		return 0, err
	}
	return f.file.Read(p)
}

func (f contextFile) ReadAt(p []byte, offset int64) (int, error) {
	if err := importContextError(f.ctx); err != nil {
		return 0, err
	}
	return f.file.ReadAt(p, offset)
}

func (f contextFile) Seek(offset int64, whence int) (int64, error) {
	if err := importContextError(f.ctx); err != nil {
		return 0, err
	}
	return f.file.Seek(offset, whence)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := importContextError(r.ctx); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func importContextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
		return nil
	}
}

// ImportFile imports srcPath using the file's own name for detection/fallbacks.
func ImportFile(ctx context.Context, database *db.DB, root storage.Root, srcPath string, renderer *pdfcover.Renderer, opts Options) (Result, error) {
	return Import(ctx, database, root, Source{Path: srcPath}, renderer, opts)
}

// Import imports a source into the library, checking for duplicate content
// before expensive metadata/cover work. This is the high-level entry point for
// normal callers. A duplicate of a trashed work stays trashed; an explicit
// single-book surface such as browser upload may use Result.WorkTrashed to
// restore it deliberately.
func Import(ctx context.Context, database *db.DB, root storage.Root, src Source, renderer *pdfcover.Renderer, opts Options) (Result, error) {
	info, err := fingerprintSource(ctx, src)
	if err != nil {
		return Result{}, err
	}

	if existing, found, err := findDuplicate(database, info.SourceSHA256); err != nil {
		return Result{}, err
	} else if found {
		if err := restoreDuplicateAsset(ctx, database, root, info, &existing); err != nil {
			return Result{}, err
		}
		return existing, nil
	}

	plan, err := resolveFromInfo(ctx, info, renderer)
	if err != nil {
		return Result{}, err
	}
	return Persist(ctx, database, root, plan, opts)
}

// ImportGroup imports several concrete files as assets of one work. It is used
// for calibre-shaped folders, where one metadata.opf describes all formats in a
// book directory. If every source is already present, no new work is created
// and a trashed work stays trashed. Attaching at least one new asset restores a
// trashed work in the same transaction, so new bytes never land invisibly.
func ImportGroup(ctx context.Context, database *db.DB, root storage.Root, sources []Source, renderer *pdfcover.Renderer, opts Options) (GroupResult, error) {
	if len(sources) == 0 {
		return GroupResult{}, errors.New("no sources to import")
	}

	infos := make([]sourceInfo, 0, len(sources))
	results := make([]Result, len(sources))
	var newIndexes []int
	var existingWorkID string

	for i, src := range sources {
		if err := importContextError(ctx); err != nil {
			return GroupResult{}, err
		}
		info, err := fingerprintSource(ctx, src)
		if err != nil {
			return GroupResult{}, err
		}
		infos = append(infos, info)

		if existing, found, err := findDuplicate(database, info.SourceSHA256); err != nil {
			return GroupResult{}, err
		} else if found {
			if err := restoreDuplicateAsset(ctx, database, root, info, &existing); err != nil {
				return GroupResult{}, err
			}
			results[i] = existing
			if existingWorkID == "" {
				existingWorkID = existing.WorkID
			} else if existing.WorkID != "" && existing.WorkID != existingWorkID {
				return GroupResult{}, fmt.Errorf("group sources already belong to different works (%s and %s)", existingWorkID, existing.WorkID)
			}
		} else {
			newIndexes = append(newIndexes, i)
		}
	}

	if len(newIndexes) == 0 {
		return GroupResult{WorkID: existingWorkID, Results: results}, nil
	}

	if existingWorkID != "" {
		return addAssetsToExistingWork(ctx, database, root, existingWorkID, infos, newIndexes, results, opts)
	}

	plan, err := resolveFromInfo(ctx, infos[newIndexes[0]], renderer)
	if err != nil {
		return GroupResult{}, err
	}
	plan.AddedAt = addedAtForSources(plan.Metadata.CalibreTimestamp, infos, time.Now())
	return persistNewWorkGroup(ctx, database, root, plan, infos, newIndexes, results, opts)
}

// Resolve reads metadata and cover data from a source without writing the DB or
// storage. Persist can later store the returned plan.
func Resolve(ctx context.Context, src Source, renderer *pdfcover.Renderer) (Plan, error) {
	info, err := fingerprintSource(ctx, src)
	if err != nil {
		return Plan{}, err
	}
	return resolveFromInfo(ctx, info, renderer)
}

// ProbeSource performs the cheap import checks needed by dry-run paths: it
// fingerprints the source, detects the format, and checks for existing content.
// It intentionally avoids metadata extraction and cover rendering.
func ProbeSource(ctx context.Context, database db.Queryer, src Source) (SourceProbe, error) {
	info, err := fingerprintSource(ctx, src)
	if err != nil {
		return SourceProbe{}, err
	}
	existing, found, err := findDuplicate(database, info.SourceSHA256)
	if err != nil {
		return SourceProbe{}, err
	}
	return SourceProbe{
		Duplicate: found,
		Existing:  existing,
		Format:    format.FormatLabel(info.Format),
	}, nil
}

// Persist writes a resolved plan to SQLite and managed storage. It re-checks
// duplicates so callers that use Resolve/Persist directly still get idempotency.
func Persist(ctx context.Context, database *db.DB, root storage.Root, plan Plan, opts Options) (Result, error) {
	if err := importContextError(ctx); err != nil {
		return Result{}, err
	}
	if existing, found, err := findDuplicate(database, plan.SourceSHA256); err != nil {
		return Result{}, err
	} else if found {
		if err := restoreDuplicateAsset(ctx, database, root, planSourceInfo(plan), &existing); err != nil {
			return Result{}, err
		}
		existing.Warnings = plan.Warnings
		return existing, nil
	}

	workID := id.New(id.Work)
	assetID := id.New(id.Asset)
	assetStage, err := stageSource(ctx, root, assetID, planSourceInfo(plan))
	if err != nil {
		return Result{}, err
	}
	staged := []storage.StagedFile{assetStage}
	committed := false
	defer func() {
		cleanupIfUncommitted(committed, staged)
	}()

	coverRoot := opts.coverRoot(root)
	var coverStage storage.StagedFile
	hasCover := false
	if len(plan.CoverBytes) > 0 {
		coverStage, err = stageBytes(ctx, coverRoot, workID+"-cover", plan.CoverBytes)
		if err != nil {
			return Result{}, err
		}
		hasCover = true
		staged = append(staged, coverStage)
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if existing, found, err := findDuplicate(tx, plan.SourceSHA256); err != nil {
		return Result{}, err
	} else if found {
		if err := restoreDuplicateAsset(ctx, tx, root, planSourceInfo(plan), &existing); err != nil {
			return Result{}, err
		}
		if err := tx.Commit(); err != nil {
			return Result{}, fmt.Errorf("commit duplicate restore: %w", err)
		}
		existing.Warnings = plan.Warnings
		return existing, nil
	}

	work, err := insertWork(tx, workID, plan)
	if err != nil {
		return Result{}, err
	}

	asset, err := insertAsset(tx, root, opts.PathTemplate, work, planSourceInfo(plan), assetID, true, assetStage)
	if err != nil {
		return Result{}, err
	}

	if err := db.UpdateSearchIndex(tx, workID); err != nil {
		return Result{}, fmt.Errorf("insert search: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit tx: %w", err)
	}
	committed = true

	finals := []stagedResult{{staged: asset.staged, relPath: asset.result.StoragePath}}
	if err := finalizeStaged(root, finals); err != nil {
		return Result{}, fmt.Errorf("place files: %w", err)
	}
	if hasCover {
		coverFinal := []stagedResult{{staged: coverStage, relPath: covers.OriginalPath(workID)}}
		if err := finalizeStaged(coverRoot, coverFinal); err != nil {
			return Result{}, fmt.Errorf("place cover: %w", err)
		}
	}

	result := asset.result
	result.Title = work.title
	result.Authors = work.authors
	result.Warnings = plan.Warnings
	return result, nil
}

func persistNewWorkGroup(ctx context.Context, database *db.DB, root storage.Root, plan Plan, infos []sourceInfo, newIndexes []int, results []Result, opts Options) (GroupResult, error) {
	workID := id.New(id.Work)
	assets, staged, err := stageAssets(ctx, root, infos, newIndexes)
	if err != nil {
		return GroupResult{}, err
	}
	committed := false
	defer func() {
		cleanupIfUncommitted(committed, staged)
	}()

	coverRoot := opts.coverRoot(root)
	var coverStage storage.StagedFile
	hasCover := false
	if len(plan.CoverBytes) > 0 {
		coverStage, err = stageBytes(ctx, coverRoot, workID+"-cover", plan.CoverBytes)
		if err != nil {
			return GroupResult{}, err
		}
		hasCover = true
		staged = append(staged, coverStage)
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return GroupResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	newIndexes, existingWorkID, err := refreshGroupDuplicates(ctx, tx, root, infos, newIndexes, results, "")
	if err != nil {
		return GroupResult{}, err
	}
	if existingWorkID != "" {
		return GroupResult{}, fmt.Errorf("group source became duplicate of existing work %s during import; retry import", existingWorkID)
	}
	if len(newIndexes) == 0 {
		if err := tx.Commit(); err != nil {
			return GroupResult{}, fmt.Errorf("commit duplicate restore: %w", err)
		}
		return GroupResult{WorkID: existingWorkID, Results: results}, nil
	}

	work, err := insertWork(tx, workID, plan)
	if err != nil {
		return GroupResult{}, err
	}

	finals := make([]stagedResult, 0, len(newIndexes)+1)
	for _, idx := range newIndexes {
		asset := assets[idx]
		asset, err := insertAsset(tx, root, opts.PathTemplate, work, asset.info, asset.result.AssetID, len(finals) == 0, asset.staged)
		if err != nil {
			return GroupResult{}, err
		}
		results[idx] = asset.result
		finals = append(finals, stagedResult{staged: asset.staged, relPath: asset.result.StoragePath})
	}
	if err := db.EnsureReadablePrimaryAsset(tx, workID); err != nil {
		return GroupResult{}, fmt.Errorf("choose primary asset: %w", err)
	}

	if err := db.UpdateSearchIndex(tx, workID); err != nil {
		return GroupResult{}, fmt.Errorf("insert search: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return GroupResult{}, fmt.Errorf("commit tx: %w", err)
	}
	committed = true

	if err := finalizeStaged(root, finals); err != nil {
		return GroupResult{}, fmt.Errorf("place files: %w", err)
	}
	if hasCover {
		coverFinal := []stagedResult{{staged: coverStage, relPath: covers.OriginalPath(workID)}}
		if err := finalizeStaged(coverRoot, coverFinal); err != nil {
			return GroupResult{}, fmt.Errorf("place cover: %w", err)
		}
	}

	return GroupResult{
		WorkID:   workID,
		Title:    work.title,
		Authors:  work.authors,
		Results:  results,
		Warnings: plan.Warnings,
	}, nil
}

func addAssetsToExistingWork(ctx context.Context, database *db.DB, root storage.Root, workID string, infos []sourceInfo, newIndexes []int, results []Result, opts Options) (GroupResult, error) {
	assets, staged, err := stageAssets(ctx, root, infos, newIndexes)
	if err != nil {
		return GroupResult{}, err
	}
	committed := false
	defer func() {
		cleanupIfUncommitted(committed, staged)
	}()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return GroupResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	newIndexes, existingWorkID, err := refreshGroupDuplicates(ctx, tx, root, infos, newIndexes, results, workID)
	if err != nil {
		return GroupResult{}, err
	}
	if existingWorkID != "" && existingWorkID != workID {
		return GroupResult{}, fmt.Errorf("group source became duplicate of another work %s during import; retry import", existingWorkID)
	}
	if len(newIndexes) == 0 {
		if err := tx.Commit(); err != nil {
			return GroupResult{}, fmt.Errorf("commit duplicate restore: %w", err)
		}
		return GroupResult{WorkID: workID, Results: results}, nil
	}

	var title, sortTitle, series, seriesIndex string
	var workTrashed bool
	if err := tx.QueryRow(`
		SELECT title, COALESCE(sort_title, ''), COALESCE(series, ''),
		       CASE WHEN series_index IS NULL THEN '' ELSE CAST(series_index AS TEXT) END,
		       deleted_at IS NOT NULL
		FROM works
		WHERE id = ?
	`, workID).Scan(&title, &sortTitle, &series, &seriesIndex, &workTrashed); err != nil {
		return GroupResult{}, fmt.Errorf("load work: %w", err)
	}
	if workTrashed {
		// A sweep must not resurrect an exact duplicate on every restart. Adding
		// genuinely new bytes is different: keeping the new asset on an invisible
		// work would make a successful import look lost. Restore in the same
		// transaction that attaches the new assets.
		if err := db.RestoreWork(tx, workID); err != nil {
			return GroupResult{}, err
		}
		for i := range results {
			results[i].WorkTrashed = false
		}
	}
	primaryAuthor, primaryAuthorSort, err := db.PrimaryAuthor(tx, workID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GroupResult{}, fmt.Errorf("primary author: %w", err)
	}
	if primaryAuthor == "" {
		primaryAuthor = "Unknown Author"
		primaryAuthorSort = bookmeta.AuthorSort("Unknown Author")
	}

	hasPrimary, err := workHasPrimaryAsset(tx, workID)
	if err != nil {
		return GroupResult{}, err
	}
	work := storedWork{
		id:                workID,
		title:             title,
		sortTitle:         sortTitle,
		series:            series,
		seriesIndex:       seriesIndex,
		primaryAuthor:     primaryAuthor,
		primaryAuthorSort: primaryAuthorSort,
	}
	finals := make([]stagedResult, 0, len(newIndexes))
	for _, idx := range newIndexes {
		asset := assets[idx]
		makePrimary := !hasPrimary && len(finals) == 0
		asset, err := insertAsset(tx, root, opts.PathTemplate, work, asset.info, asset.result.AssetID, makePrimary, asset.staged)
		if err != nil {
			return GroupResult{}, err
		}
		results[idx] = asset.result
		finals = append(finals, stagedResult{staged: asset.staged, relPath: asset.result.StoragePath})
	}
	if err := db.EnsureReadablePrimaryAsset(tx, workID); err != nil {
		return GroupResult{}, fmt.Errorf("choose primary asset: %w", err)
	}

	if err := db.UpdateSearchIndex(tx, workID); err != nil {
		return GroupResult{}, fmt.Errorf("update search: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return GroupResult{}, fmt.Errorf("commit tx: %w", err)
	}
	committed = true

	if err := finalizeStaged(root, finals); err != nil {
		return GroupResult{}, fmt.Errorf("place files: %w", err)
	}

	return GroupResult{WorkID: workID, Title: title, Restored: workTrashed, Results: results}, nil
}

func insertWork(tx *sql.Tx, workID string, plan Plan) (storedWork, error) {
	coverVersion := 0
	if len(plan.CoverBytes) > 0 {
		coverVersion = 1
	}

	meta := plan.Metadata
	if meta == nil {
		meta = &bookmeta.Metadata{}
	}

	// Canonicalize the language code (eng/en_US/en-us -> en/en-US) so stored
	// values are consistent across formats and sources; see bookmeta.NormalizeLanguage.
	language := bookmeta.NormalizeLanguage(meta.Language)

	var addedAt sql.NullInt64
	if !plan.AddedAt.IsZero() {
		addedAt = sql.NullInt64{Int64: plan.AddedAt.Unix(), Valid: true}
	}

	_, err := tx.Exec(`
			INSERT INTO works (id, title, sort_title, series, series_index, description, tags, cover_version, publisher, published_date, language, identifiers, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, unixepoch()))
		`, workID, plan.Title, plan.SortTitle, meta.Series, meta.SeriesIndex, meta.Description, strings.Join(meta.Tags, ", "), coverVersion, meta.Publisher, meta.Date, language, meta.Identifier, addedAt)
	if err != nil {
		return storedWork{}, fmt.Errorf("insert work: %w", err)
	}

	// Resolve authors before laying out the file: reusing an existing author
	// adopts its persisted sort_name, which is what the canonical path buckets on.
	primaryAuthor, primaryAuthorSort, err := db.UpsertWorkAuthors(tx, workID, plan.Authors)
	if err != nil {
		return storedWork{}, fmt.Errorf("link authors: %w", err)
	}
	authorNames := make([]string, len(plan.Authors))
	for i, a := range plan.Authors {
		authorNames[i] = a.Name
	}

	return storedWork{
		id:                workID,
		title:             plan.Title,
		sortTitle:         plan.SortTitle,
		series:            meta.Series,
		seriesIndex:       seriesIndexString(meta.SeriesIndex),
		primaryAuthor:     primaryAuthor,
		primaryAuthorSort: primaryAuthorSort,
		authors:           authorNames,
	}, nil
}

func insertAsset(tx *sql.Tx, root storage.Root, template string, work storedWork, info sourceInfo, assetID string, isPrimary bool, staged storage.StagedFile) (preparedAsset, error) {
	cPath, err := storage.BookPath(template, storage.BookPathData{
		Title:            work.title,
		SortTitle:        work.sortTitle,
		Author:           work.primaryAuthor,
		AuthorSort:       work.primaryAuthorSort,
		Series:           work.series,
		SeriesIndex:      work.seriesIndex,
		AssetID:          assetID,
		WorkID:           work.id,
		Ext:              info.Extension,
		OriginalFilename: filepath.Base(info.sourceName()),
	})
	if err != nil {
		return preparedAsset{}, err
	}
	var existingAsset string
	err = tx.QueryRow("SELECT id FROM assets WHERE storage_path = ? LIMIT 1", cPath).Scan(&existingAsset)
	if err == nil {
		return preparedAsset{}, fmt.Errorf("storage path collision: %s already belongs to %s", cPath, existingAsset)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return preparedAsset{}, fmt.Errorf("check storage path collision: %w", err)
	}
	dstPath, err := root.Resolve(cPath)
	if err != nil {
		return preparedAsset{}, err
	}
	if _, err := os.Stat(dstPath); err == nil {
		return preparedAsset{}, fmt.Errorf("storage path collision: destination exists on disk: %s", cPath)
	} else if !os.IsNotExist(err) {
		return preparedAsset{}, fmt.Errorf("check storage destination: %w", err)
	}
	primary := 0
	if isPrimary {
		primary = 1
	}

	canRead := 0
	if info.CanRead {
		canRead = 1
	}
	_, err = tx.Exec(`
			INSERT INTO assets (id, work_id, storage_path, filename, original_filename, extension, format, is_primary, can_read, original_sha256, current_sha256, original_size, current_size)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, assetID, work.id, cPath, filepath.Base(cPath), filepath.Base(info.sourceName()), info.Extension, format.FormatKey(info.Format), primary, canRead, info.SourceSHA256, info.SourceSHA256, info.Size, info.Size)
	if err != nil {
		return preparedAsset{}, fmt.Errorf("insert asset: %w", err)
	}

	return preparedAsset{
		result: Result{
			Status:      StatusImported,
			WorkID:      work.id,
			AssetID:     assetID,
			Format:      format.FormatLabel(info.Format),
			StoragePath: cPath,
		},
		staged: staged,
		info:   info,
	}, nil
}

func seriesIndexString(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func workHasPrimaryAsset(tx *sql.Tx, workID string) (bool, error) {
	var exists int
	err := tx.QueryRow("SELECT 1 FROM assets WHERE work_id = ? AND is_primary = 1 LIMIT 1", workID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check primary asset: %w", err)
	}
	return true, nil
}

func planSourceInfo(plan Plan) sourceInfo {
	return sourceInfo{
		Source:       plan.Source,
		Size:         plan.Size,
		SourceSHA256: plan.SourceSHA256,
		Format:       plan.Format,
		Extension:    plan.Extension,
		CanRead:      plan.CanRead,
	}
}

func stageAssets(ctx context.Context, root storage.Root, infos []sourceInfo, indexes []int) (map[int]preparedAsset, []storage.StagedFile, error) {
	assets := make(map[int]preparedAsset, len(indexes))
	staged := make([]storage.StagedFile, 0, len(indexes))
	cleanup := true
	defer func() {
		if cleanup {
			for _, s := range staged {
				s.Cleanup()
			}
		}
	}()

	for _, idx := range indexes {
		if err := importContextError(ctx); err != nil {
			return nil, nil, err
		}
		assetID := id.New(id.Asset)
		stagedFile, err := stageSource(ctx, root, assetID, infos[idx])
		if err != nil {
			return nil, nil, err
		}
		staged = append(staged, stagedFile)
		assets[idx] = preparedAsset{
			result: Result{
				Status:  StatusImported,
				AssetID: assetID,
				Format:  format.FormatLabel(infos[idx].Format),
			},
			staged: stagedFile,
			info:   infos[idx],
		}
	}

	cleanup = false
	return assets, staged, nil
}

func stageSource(ctx context.Context, root storage.Root, assetID string, info sourceInfo) (storage.StagedFile, error) {
	f, err := os.Open(info.Path)
	if err != nil {
		return storage.StagedFile{}, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	return storage.Stage(root, fmt.Sprintf("[%s]%s", assetID, info.Extension), contextReader{ctx: ctx, r: f})
}

func stageBytes(ctx context.Context, root storage.Root, label string, data []byte) (storage.StagedFile, error) {
	return storage.Stage(root, label, contextReader{ctx: ctx, r: bytes.NewReader(data)})
}

func refreshGroupDuplicates(ctx context.Context, tx *sql.Tx, root storage.Root, infos []sourceInfo, indexes []int, results []Result, targetWorkID string) ([]int, string, error) {
	refreshed := make([]int, 0, len(indexes))
	existingWorkID := ""
	for _, idx := range indexes {
		if err := importContextError(ctx); err != nil {
			return nil, "", err
		}
		existing, found, err := findDuplicate(tx, infos[idx].SourceSHA256)
		if err != nil {
			return nil, "", err
		}
		if !found {
			refreshed = append(refreshed, idx)
			continue
		}
		if err := restoreDuplicateAsset(ctx, tx, root, infos[idx], &existing); err != nil {
			return nil, "", err
		}
		results[idx] = existing
		if targetWorkID != "" {
			if existing.WorkID != "" && existing.WorkID != targetWorkID {
				return nil, existing.WorkID, nil
			}
			continue
		}
		if existingWorkID == "" {
			existingWorkID = existing.WorkID
		} else if existing.WorkID != "" && existing.WorkID != existingWorkID {
			return nil, "", fmt.Errorf("group sources already belong to different works (%s and %s)", existingWorkID, existing.WorkID)
		}
	}
	return refreshed, existingWorkID, nil
}

// finalizeStaged deliberately has no context: once SQLite commits paths that
// name staged files, cancellation must not interrupt the short DB/filesystem
// convergence step.
func finalizeStaged(root storage.Root, files []stagedResult) error {
	for _, f := range files {
		if err := f.staged.Finalize(root, f.relPath); err != nil {
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

func resolveFromInfo(ctx context.Context, info sourceInfo, renderer *pdfcover.Renderer) (Plan, error) {
	if err := importContextError(ctx); err != nil {
		return Plan{}, err
	}
	f, err := os.Open(info.Path)
	if err != nil {
		return Plan{}, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	plan := Plan{
		Source:       info.Source,
		Size:         info.Size,
		SourceSHA256: info.SourceSHA256,
		Format:       info.Format,
		Extension:    info.Extension,
		CanRead:      info.CanRead,
	}
	if warning := unknownStructuredFormatWarning(info); warning != nil {
		plan.Warnings = append(plan.Warnings, warning)
	}

	contextSource := contextFile{ctx: ctx, file: f}
	coverBytes := readSidecarCover(ctx, info.Source)
	sidecarMeta := readSidecarOPF(ctx, info.Source)
	if err := importContextError(ctx); err != nil {
		return Plan{}, err
	}
	sidecarComplete := sidecarMetadataComplete(sidecarMeta)
	var meta *bookmeta.Metadata
	var metaErr error
	// Combined format extractors avoid decoding the same source twice.
	coverChecked := false
	if sidecarComplete {
		meta = sidecarMeta
	} else {
		switch {
		case format.IsEPUBContainerFormat(info.Format) && len(coverBytes) == 0:
			meta, coverBytes, _, metaErr = format.ExtractEPUBMetadataAndCover(contextSource, info.Size)
			coverChecked = true
		case info.Format == format.FormatFB2 && len(coverBytes) == 0:
			meta, coverBytes, _, metaErr = format.ExtractFB2MetadataAndCover(contextSource, info.Size)
			coverChecked = true
		case info.Format == format.FormatCBR && len(coverBytes) == 0:
			meta, coverBytes, _, metaErr = format.ExtractCBRMetadataAndCover(contextSource, info.Size)
			coverChecked = true
		case info.Format == format.FormatCB7 && len(coverBytes) == 0:
			meta, coverBytes, _, metaErr = format.ExtractCB7MetadataAndCover(contextSource, info.Size)
			coverChecked = true
		default:
			meta, metaErr = format.ExtractMetadata(contextSource, info.Size, info.Format)
		}
		if err := importContextError(ctx); err != nil {
			return Plan{}, err
		}
		if metaErr != nil {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("extract metadata for %s: %w", info.Path, metaErr))
		}
	}
	if len(coverBytes) == 0 && !coverChecked {
		if b, _, err := format.ExtractCover(contextSource, info.Size, info.Format); err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("extract %s cover for %s: %w", format.FormatLabel(info.Format), info.Path, err))
		} else {
			coverBytes = b
		}
		if err := importContextError(ctx); err != nil {
			return Plan{}, err
		}
	}

	if meta == nil {
		meta = &bookmeta.Metadata{}
	}

	// A curated metadata.opf sidecar next to the book file overrides embedded
	// metadata. When the sidecar already has title+author we trust it as the
	// primary metadata source and skip embedded metadata parsing entirely.
	// A sibling cover.jpg/jpeg/png is likewise treated as curated and preferred
	// over embedded cover data.
	if sidecarMeta != nil && meta != sidecarMeta {
		meta.Merge(sidecarMeta)
	}

	if filenameMeta, ok := structuredFilenameMetadata(info.sourceName()); ok {
		if meta.Title == "" && filenameMeta.Title != "" {
			meta.Title = filenameMeta.Title
		}
		if len(meta.Authors) == 0 && len(filenameMeta.Authors) > 0 {
			meta.Authors = filenameMeta.Authors
		}
		if meta.Identifier == "" && filenameMeta.Identifier != "" {
			meta.Identifier = filenameMeta.Identifier
		}
	}

	// PDFs rarely carry an extractable cover; render the first page as a
	// best-effort fallback when nothing better was found. Gated on renderer being
	// supplied so EPUB-only imports never spin up pdfium. Keep the source
	// seekable: pdfium can request the ranges it needs without a second
	// whole-file allocation in both Go and WASM memory.
	if len(coverBytes) == 0 && info.Format == format.FormatPDF && renderer != nil {
		if rendered, rerr := renderer.RenderFirstPageJPEG(ctx, contextSource, info.Size, 0); rerr != nil {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("render PDF cover %s: %w", info.Path, rerr))
		} else {
			coverBytes = rendered
		}
		if err := importContextError(ctx); err != nil {
			return Plan{}, err
		}
	}
	if len(coverBytes) > 0 {
		if err := importContextError(ctx); err != nil {
			return Plan{}, err
		}
		if _, err := covers.Validate(coverBytes); err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("skip invalid cover for %s: %w", info.Path, err))
			coverBytes = nil
		}
		if err := importContextError(ctx); err != nil {
			return Plan{}, err
		}
	}

	title := meta.Title
	if title == "" {
		base := filepath.Base(info.sourceName())
		title = strings.TrimSuffix(base, format.BookExtension(base))
	}

	sortTitle := meta.SortTitle
	if sortTitle == "" {
		sortTitle = title
	}

	authors := meta.Authors
	if len(authors) == 0 {
		authors = []bookmeta.AuthorMeta{{Name: "Unknown Author"}}
	}

	for i := range authors {
		if authors[i].SortName == "" {
			authors[i].SortName = bookmeta.AuthorSort(authors[i].Name)
		}
	}

	plan.Metadata = meta
	plan.CoverBytes = coverBytes
	plan.Title = title
	plan.SortTitle = sortTitle
	plan.Authors = authors
	plan.AddedAt = addedAtForSources(meta.CalibreTimestamp, []sourceInfo{info}, time.Now())
	return plan, nil
}

func addedAtForSources(calibreTimestamp string, infos []sourceInfo, now time.Time) time.Time {
	if addedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(calibreTimestamp)); err == nil && plausibleAddedAt(addedAt, now) {
		return addedAt
	}

	var earliest time.Time
	for _, info := range infos {
		if !plausibleAddedAt(info.ModTime, now) {
			continue
		}
		if earliest.IsZero() || info.ModTime.Before(earliest) {
			earliest = info.ModTime
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
	if len(cleanParts) < 4 || cleanParts[0] == "" || cleanParts[1] == "" {
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
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
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

func fingerprintSource(ctx context.Context, src Source) (sourceInfo, error) {
	if err := importContextError(ctx); err != nil {
		return sourceInfo{}, err
	}
	if src.Path == "" {
		return sourceInfo{}, errors.New("source path is required")
	}

	f, err := os.Open(src.Path)
	if err != nil {
		return sourceInfo{}, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return sourceInfo{}, fmt.Errorf("stat file: %w", err)
	}

	contextSource := contextFile{ctx: ctx, file: f}
	h := sha256.New()
	if _, err := io.Copy(h, contextSource); err != nil {
		return sourceInfo{}, fmt.Errorf("hash file: %w", err)
	}
	fileHash := hex.EncodeToString(h.Sum(nil))

	kind := format.DetectFormat(src.sourceName(), contextSource, stat.Size())
	if err := importContextError(ctx); err != nil {
		return sourceInfo{}, err
	}
	ext := format.BookExtension(src.sourceName())

	return sourceInfo{
		Source:       src,
		Size:         stat.Size(),
		SourceSHA256: fileHash,
		Format:       kind,
		Extension:    ext,
		CanRead:      format.CanRead(kind),
		ModTime:      stat.ModTime(),
	}, nil
}

func findDuplicate(database db.Queryer, fileHash string) (Result, bool, error) {
	var assetID, workID, storagePath string
	var workTrashed bool
	err := database.QueryRow(`
		SELECT a.id, a.work_id, a.storage_path, w.deleted_at IS NOT NULL
		FROM assets a
		JOIN works w ON w.id = a.work_id
		WHERE a.original_sha256 = ? OR a.current_sha256 = ?
		LIMIT 1
	`, fileHash, fileHash).Scan(&assetID, &workID, &storagePath, &workTrashed)
	if err == nil {
		return Result{Status: StatusDuplicate, AssetID: assetID, WorkID: workID, WorkTrashed: workTrashed, StoragePath: storagePath}, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, nil
	}
	return Result{}, false, fmt.Errorf("check duplicate: %w", err)
}

func restoreDuplicateAsset(ctx context.Context, database db.Execer, root storage.Root, info sourceInfo, existing *Result) error {
	if existing.StoragePath == "" {
		return nil
	}
	absPath, err := root.Resolve(existing.StoragePath)
	if err != nil {
		return fmt.Errorf("resolve duplicate asset %s (%s): %w", existing.AssetID, existing.StoragePath, err)
	}
	if stat, err := os.Stat(absPath); err == nil {
		if stat.IsDir() {
			return fmt.Errorf("restore duplicate asset %s: destination is a directory: %s", existing.AssetID, existing.StoragePath)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat duplicate asset %s (%s): %w", existing.AssetID, existing.StoragePath, err)
	}

	f, err := os.Open(info.Source.Path)
	if err != nil {
		return fmt.Errorf("open duplicate source %s: %w", info.Source.Path, err)
	}
	defer f.Close()
	if err := storage.Place(root, existing.StoragePath, contextReader{ctx: ctx, r: f}, nil); err != nil {
		return fmt.Errorf("restore duplicate asset %s (%s): %w", existing.AssetID, existing.StoragePath, err)
	}
	if _, err := database.Exec(`
		UPDATE assets
		SET current_sha256 = ?, current_size = ?, koreader_hash = NULL, updated_at = unixepoch()
		WHERE id = ?
	`, info.SourceSHA256, info.Size, existing.AssetID); err != nil {
		return fmt.Errorf("update restored duplicate asset %s: %w", existing.AssetID, err)
	}
	return nil
}

// IsSupportedBook is the import-folder extension filter. Some accepted
// extensions are currently opaque imports: they store the original file with
// filename/Unknown Author fallback metadata until a real parser exists.
func IsSupportedBook(name string) bool {
	return format.KnownBookExtension(format.BookExtension(name))
}

func CalibreBookSources(dir string) ([]Source, bool, error) {
	if _, err := os.Stat(filepath.Join(dir, "metadata.opf")); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("stat metadata.opf: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, fmt.Errorf("read calibre dir: %w", err)
	}

	var sources []Source
	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() {
			continue
		}
		if !IsSupportedBook(entry.Name()) {
			continue
		}
		sources = append(sources, Source{
			Path:       filepath.Join(dir, entry.Name()),
			SidecarDir: dir,
		})
	}
	if len(sources) == 0 {
		return nil, false, nil
	}
	return sources, true, nil
}

// readSidecarOPF parses a metadata.opf sidecar next to the book file.
// Returns nil when absent or unreadable — the sidecar is a best-effort override.
func readSidecarOPF(ctx context.Context, src Source) *bookmeta.Metadata {
	f, err := os.Open(filepath.Join(src.sidecarDir(), "metadata.opf"))
	if err != nil {
		return nil
	}
	defer f.Close()
	meta, err := format.ParseOPF(contextReader{ctx: ctx, r: f})
	if err != nil {
		return nil
	}
	return meta
}

// readSidecarCover reads a sibling cover.jpg/cover.jpeg/cover.png sidecar.
// Returns nil when none exists.
func readSidecarCover(ctx context.Context, src Source) []byte {
	dir := src.sidecarDir()
	for _, name := range []string{"cover.jpg", "cover.jpeg", "cover.png"} {
		if importContextError(ctx) != nil {
			return nil
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		b, readErr := io.ReadAll(contextReader{ctx: ctx, r: f})
		_ = f.Close()
		if readErr == nil {
			return b
		}
	}
	return nil
}

func (s Source) sourceName() string {
	if s.OriginalName != "" {
		return s.OriginalName
	}
	return s.Path
}

func (s Source) sidecarDir() string {
	if s.SidecarDir != "" {
		return s.SidecarDir
	}
	return filepath.Dir(s.Path)
}
