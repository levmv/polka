package writeback

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"time"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/koreader"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/workslot"
)

const maxMetadataWritebackInputBytes int64 = 512 << 20

var ErrInputTooLarge = errors.New("metadata write-back input exceeds size limit")

type Options struct {
	All        bool
	DryRun     bool
	FailedOnly bool
	Limit      int
	Scope      db.VisibilityScope
	WorkIDs    []string
	// CoverRoot points at the app data dir, where covers/<work_id> originals
	// live. When unset, Run falls back to root for package-level tests.
	CoverRoot storage.Root
	WorkQueue *workslot.Queue
}

type Status string

const (
	StatusWouldWrite Status = "would_write"
	StatusWritten    Status = "written"
	StatusUnchanged  Status = "unchanged"
	StatusSkipped    Status = "skipped"
	StatusFailed     Status = "failed"
)

type Result struct {
	AssetID     string
	WorkID      string
	StoragePath string
	Status      Status
	Error       string
}

type Summary struct {
	Planned    int
	WouldWrite int
	Written    int
	Unchanged  int
	Skipped    int
	Failed     int
	Results    []Result
}

func Run(ctx context.Context, database *db.DB, root storage.Root, opts Options) (Summary, error) {
	selectedModes := 0
	if opts.All {
		selectedModes++
	}
	if opts.FailedOnly {
		selectedModes++
	}
	if len(opts.WorkIDs) > 0 {
		selectedModes++
	}
	if selectedModes > 1 {
		return Summary{}, fmt.Errorf("write-back scope options cannot be combined")
	}
	if opts.Scope.ContentScope == "" {
		opts.Scope = db.FullVisibilityScope()
	}

	rows, err := planAssets(database, opts)
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{Planned: len(rows), Results: make([]Result, 0, len(rows))}
	if opts.DryRun {
		for _, row := range rows {
			summary.WouldWrite++
			summary.Results = append(summary.Results, Result{
				AssetID:     row.AssetID,
				WorkID:      row.WorkID,
				StoragePath: row.StoragePath,
				Status:      StatusWouldWrite,
			})
		}
		return summary, nil
	}

	for _, row := range rows {
		if err := context.Cause(ctx); err != nil {
			return summary, err
		}
		result, err := writeAssetQueued(ctx, database, root, row.AssetID, opts)
		if result.Status != "" {
			appendResult(&summary, result)
		}
		if err != nil {
			return summary, fmt.Errorf("write back asset %s: %w", row.AssetID, err)
		}
	}
	return summary, nil
}

func writeAssetQueued(ctx context.Context, database *db.DB, root storage.Root, assetID string, opts Options) (Result, error) {
	releaseWork := func() {}
	if opts.WorkQueue != nil {
		release, err := opts.WorkQueue.Acquire(ctx)
		if err != nil {
			return Result{}, err
		}
		releaseWork = release
	}
	defer releaseWork()
	return writeAsset(ctx, database, root, assetID, opts)
}

func appendResult(summary *Summary, result Result) {
	switch result.Status {
	case StatusWritten:
		summary.Written++
	case StatusUnchanged:
		summary.Unchanged++
	case StatusSkipped:
		summary.Skipped++
	case StatusFailed:
		summary.Failed++
	}
	summary.Results = append(summary.Results, result)
}

func planAssets(database *db.DB, opts Options) ([]db.MetadataWritebackAssetRow, error) {
	if len(opts.WorkIDs) > 0 {
		return db.ListMetadataWritebackAssetsByWorkIDs(database, opts.Scope, opts.WorkIDs, opts.Limit)
	}
	if opts.All {
		return db.ListAllMetadataWritebackAssets(database, opts.Scope, opts.Limit)
	}
	if opts.FailedOnly {
		return db.ListFailedMetadataWritebackAssets(database, opts.Scope, opts.Limit)
	}
	return db.ListDirtyMetadataWritebackAssets(database, opts.Scope, opts.Limit)
}

func writeAsset(ctx context.Context, database *db.DB, root storage.Root, assetID string, opts Options) (Result, error) {
	if err := context.Cause(ctx); err != nil {
		return Result{}, err
	}
	row, err := db.GetMetadataWritebackAsset(database, assetID)
	if err != nil {
		result := Result{AssetID: assetID, Status: StatusFailed, Error: err.Error()}
		if errors.Is(err, sql.ErrNoRows) {
			return result, nil
		}
		return fail(ctx, database, result, err)
	}

	result := Result{AssetID: row.AssetID, WorkID: row.WorkID, StoragePath: row.StoragePath}
	force := opts.All || len(opts.WorkIDs) > 0
	if !force && row.WritebackRev >= row.MetadataRev {
		result.Status = StatusSkipped
		return result, nil
	}
	if !format.SupportsMetadataWriteback(row.Format) {
		return fail(ctx, database, result, fmt.Errorf("unsupported write-back format: %s", format.FormatKey(row.Format)))
	}

	fullPath, err := root.Resolve(row.StoragePath)
	if err != nil {
		return fail(ctx, database, result, err)
	}
	src, err := os.Open(fullPath)
	if err != nil {
		return fail(ctx, database, result, fmt.Errorf("open asset file: %w", err))
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return fail(ctx, database, result, fmt.Errorf("stat asset file: %w", err))
	}
	if info.IsDir() {
		return fail(ctx, database, result, fmt.Errorf("asset path is a directory: %s", row.StoragePath))
	}
	currentSize := info.Size()
	if currentSize > maxMetadataWritebackInputBytes {
		return fail(ctx, database, result, fmt.Errorf("%s exceeds metadata write-back input limit (%d bytes): %w", row.AssetID, maxMetadataWritebackInputBytes, ErrInputTooLarge))
	}
	currentHash, err := fileSHA256(ctx, src)
	if err != nil {
		return fail(ctx, database, result, fmt.Errorf("hash asset file: %w", err))
	}
	if err := validateCurrentBytes(row, currentHash, currentSize); err != nil {
		return fail(ctx, database, result, err)
	}

	snapshot, err := db.LoadMetadataWritebackSnapshot(database, row.WorkID)
	if err != nil {
		return fail(ctx, database, result, fmt.Errorf("load metadata snapshot: %w", err))
	}
	// Cover write-back is EPUB-only today; FB2 keeps its embedded cover linked
	// but never receives new cover bytes here (see internal/format).
	var coverBytes []byte
	if format.IsEPUBContainerFormat(row.Format) {
		coverBytes, err = loadWritebackCover(opts.coverRoot(root), snapshot)
		if err != nil {
			return fail(ctx, database, result, err)
		}
	}
	// EPUB3 requires dcterms:modified; use the durable work timestamp so
	// repeated --all write-back passes render byte-identical metadata.
	modified := time.Unix(snapshot.UpdatedAt, 0).UTC()

	renderedHash := sha256.New()
	renderedSize := &countingWriter{}
	tempRel, err := storage.WriteAdjacentTempWith(root, row.StoragePath, fmt.Sprintf("%s-rev%d", row.AssetID, snapshot.MetadataRev), func(w io.Writer) error {
		return renderWritebackAsset(io.MultiWriter(w, renderedHash, renderedSize), row.Format, src, currentSize, snapshot.Metadata, modified, coverBytes)
	})
	if err != nil {
		return fail(ctx, database, result, err)
	}
	pendingRecorded := false
	defer func() {
		if !pendingRecorded {
			_ = os.Remove(root.Abs(tempRel))
		}
	}()
	if err := validateRenderedWritebackAsset(root, tempRel, row.Format); err != nil {
		return fail(ctx, database, result, fmt.Errorf("validate rendered metadata: %w", err))
	}
	renderedHashHex := hashHex(renderedHash)
	renderedKOReaderHash, err := koreader.PartialMD5File(root.Abs(tempRel))
	if err != nil {
		return fail(ctx, database, result, fmt.Errorf("compute KOReader hash: %w", err))
	}
	if err := context.Cause(ctx); err != nil {
		return result, err
	}

	if renderedHashHex == currentHash && renderedSize.N == currentSize {
		if err := markSuccess(ctx, database, row, currentHash, currentSize, renderedKOReaderHash, snapshot.MetadataRev); err != nil {
			return fail(ctx, database, result, err)
		}
		result.Status = StatusUnchanged
		return result, nil
	}

	previousAttempt, hasPreviousAttempt, err := db.LoadMetadataWritebackAttempt(database, row.AssetID)
	if err != nil {
		return fail(ctx, database, result, err)
	}
	attempt := db.MetadataWritebackAttempt{
		AssetID:      row.AssetID,
		MetadataRev:  snapshot.MetadataRev,
		StoragePath:  row.StoragePath,
		TempPath:     tempRel,
		SHA256:       renderedHashHex,
		Size:         renderedSize.N,
		KOReaderHash: renderedKOReaderHash,
	}
	if err := database.Transact(ctx, func(tx *sql.Tx) error {
		return db.UpsertMetadataWritebackAttempt(tx, attempt)
	}); err != nil {
		return fail(ctx, database, result, err)
	}
	pendingRecorded = true
	// The attempt is recorded before the overwrite: repair can either apply the
	// temp if the final file was not replaced, or finalize success if the final
	// already matches this recorded hash/size and markSuccess failed afterward.
	if hasPreviousAttempt && previousAttempt.TempPath != tempRel {
		if err := removeWritebackTemp(root, previousAttempt.TempPath); err != nil {
			return fail(ctx, database, result, err)
		}
	}
	if err := context.Cause(ctx); err != nil {
		return result, err
	}

	if err := storage.ReplaceWithStaged(root, tempRel, row.StoragePath); err != nil {
		return fail(ctx, database, result, err)
	}
	if err := markSuccess(ctx, database, row, renderedHashHex, renderedSize.N, renderedKOReaderHash, snapshot.MetadataRev); err != nil {
		return fail(ctx, database, result, err)
	}
	result.Status = StatusWritten
	return result, nil
}

// renderWritebackAsset dispatches to the per-format embedded-metadata renderer.
// coverBytes is honored only by the EPUB container renderer; FB2 carries no
// cover write-back yet.
func renderWritebackAsset(w io.Writer, kind format.Format, src io.ReaderAt, size int64, meta format.Metadata, modified time.Time, coverBytes []byte) error {
	switch {
	case format.IsEPUBContainerFormat(kind):
		return format.RewriteEPUBMetadataAndCoverTo(w, src, size, meta, modified, coverBytes)
	case kind == format.FormatFB2:
		return format.RewriteFB2MetadataTo(w, src, size, meta)
	default:
		return fmt.Errorf("unsupported write-back format: %s", format.FormatKey(kind))
	}
}

func validateRenderedWritebackAsset(root storage.Root, relPath string, kind format.Format) error {
	fullPath, err := root.Resolve(relPath)
	if err != nil {
		return err
	}
	f, err := os.Open(fullPath)
	if err != nil {
		return fmt.Errorf("open rendered asset: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat rendered asset: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("rendered asset path is a directory: %s", relPath)
	}
	meta, err := format.ExtractMetadata(f, info.Size(), kind)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("metadata not found in rendered %s", format.FormatKey(kind))
	}
	return nil
}

func (opts Options) coverRoot(fallback storage.Root) storage.Root {
	if opts.CoverRoot.Path != "" {
		return opts.CoverRoot
	}
	return fallback
}

func loadWritebackCover(root storage.Root, snapshot db.MetadataWritebackSnapshot) ([]byte, error) {
	if snapshot.CoverVersion <= 0 {
		return nil, nil
	}
	fullPath, err := root.Resolve(covers.OriginalPath(snapshot.WorkID))
	if err != nil {
		return nil, err
	}
	coverBytes, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read cover original for %s: %w", snapshot.WorkID, err)
	}
	if _, err := covers.Validate(coverBytes); err != nil {
		return nil, fmt.Errorf("validate cover original for %s: %w", snapshot.WorkID, err)
	}
	return coverBytes, nil
}

type countingWriter struct {
	N int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.N += int64(len(p))
	return len(p), nil
}

func fileSHA256(ctx context.Context, r io.Reader) (string, error) {
	h := sha256.New()
	buf := make([]byte, 128<<10)
	for {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hashHex(h), nil
}

func validateCurrentBytes(row db.MetadataWritebackAssetRow, currentHash string, currentSize int64) error {
	if row.CurrentSHA256 != "" && row.CurrentSHA256 != currentHash {
		return fmt.Errorf("current file drift for %s: db sha256=%s disk sha256=%s", row.AssetID, row.CurrentSHA256, currentHash)
	}
	if row.CurrentSize.Valid && row.CurrentSize.Int64 != currentSize {
		return fmt.Errorf("current file drift for %s: db size=%d disk size=%d", row.AssetID, row.CurrentSize.Int64, currentSize)
	}
	return nil
}

func markSuccess(ctx context.Context, database *db.DB, row db.MetadataWritebackAssetRow, hash string, size int64, koReaderHash string, metadataRev int64) error {
	return database.Transact(ctx, func(tx *sql.Tx) error {
		return db.MarkMetadataWritebackSuccess(tx, row.AssetID, row.StoragePath, hash, size, koReaderHash, metadataRev)
	})
}

func fail(ctx context.Context, database *db.DB, result Result, err error) (Result, error) {
	result.Status = StatusFailed
	result.Error = err.Error()
	if recordErr := recordWritebackError(ctx, database, result.AssetID, err); recordErr != nil {
		combined := errors.Join(err, recordErr)
		result.Error = combined.Error()
		return result, combined
	}
	return result, nil
}

func recordWritebackError(ctx context.Context, database *db.DB, assetID string, err error) error {
	if recordErr := database.Transact(ctx, func(tx *sql.Tx) error {
		return db.MarkMetadataWritebackError(tx, assetID, err)
	}); recordErr != nil {
		return fmt.Errorf("record metadata write-back failure for %s: %w", assetID, recordErr)
	}
	return nil
}

func removeWritebackTemp(root storage.Root, relPath string) error {
	fullPath, err := root.Resolve(relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous write-back temp: %w", err)
	}
	return nil
}

func hashHex(h hash.Hash) string {
	return hex.EncodeToString(h.Sum(nil))
}
