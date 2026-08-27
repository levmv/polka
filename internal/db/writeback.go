package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
)

// metadataWritebackFormatSQL restricts asset rows to formats Polka can rewrite.
var metadataWritebackFormatSQL = MetadataWritebackFormatSQL("a.format")

// MetadataWritebackFormatSQL returns a SQL predicate for asset format columns
// that can carry embedded metadata write-back. It is derived from
// format.SupportsMetadataWriteback so DB queries and diagnostics cannot drift
// from the renderer dispatch; the writeback partial index in migration 0001
// mirrors the same key set.
func MetadataWritebackFormatSQL(column string) string {
	return formatKeyInClause(column, format.MetadataWritebackFormatKeys())
}

func formatKeyInClause(column string, keys []string) string {
	quoted := make([]string, len(keys))
	for i, key := range keys {
		quoted[i] = "'" + key + "'"
	}
	return column + " IN (" + strings.Join(quoted, ", ") + ")"
}

// MetadataWritebackAssetRow is one writable asset considered for embedded
// metadata write-back.
type MetadataWritebackAssetRow struct {
	AssetID       string
	WorkID        string
	StoragePath   string
	Format        format.Format
	CurrentSHA256 string
	CurrentSize   sql.NullInt64
	MetadataRev   int64
	WritebackRev  int64
	Error         string
}

type MetadataWritebackSnapshot struct {
	WorkID       string
	MetadataRev  int64
	CoverVersion int
	UpdatedAt    int64
	Metadata     bookmeta.Metadata
}

type MetadataWritebackAttempt struct {
	AssetID      string
	MetadataRev  int64
	StoragePath  string
	TempPath     string
	SHA256       string
	Size         int64
	KOReaderHash string
}

type MetadataWritebackAttemptRow struct {
	MetadataWritebackAttempt
	CurrentStoragePath string
}

// MetadataWritebackCounts summarizes the pending write-back backlog. Failed is
// the dirty subset with a recorded last error.
type MetadataWritebackCounts struct {
	Dirty  int
	Failed int
}

// BumpMetadataRev marks works as needing their current metadata snapshot written
// to writable assets. It also touches updated_at because the bump happens only
// in user-visible metadata mutation paths.
// If cover write-back uses metadata_rev instead of a separate asset marker,
// cover mutation paths must bump this same rev.
func BumpMetadataRev(execer Execer, workIDs []string) error {
	ids := dedupWorkIDs(workIDs)
	if len(ids) == 0 {
		return nil
	}
	placeholders, args := idPlaceholders(ids)
	if _, err := execer.Exec(`
		UPDATE works
		SET metadata_rev = metadata_rev + 1,
		    updated_at = unixepoch()
		WHERE id IN (`+placeholders+`)
	`, args...); err != nil {
		return fmt.Errorf("bump metadata rev: %w", err)
	}
	return nil
}

// CountDirtyMetadataWritebackAssets counts writable live assets whose file
// metadata is behind the current work metadata.
func CountDirtyMetadataWritebackAssets(queryer Queryer, scope VisibilityScope) (MetadataWritebackCounts, error) {
	where, args := metadataWritebackDirtyWhere(scope)
	var counts MetadataWritebackCounts
	err := queryer.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN COALESCE(a.writeback_error, '') <> '' THEN 1 ELSE 0 END), 0)
		FROM assets a
		JOIN works w ON w.id = a.work_id
		WHERE `+where, args...).Scan(&counts.Dirty, &counts.Failed)
	if err != nil {
		return MetadataWritebackCounts{}, fmt.Errorf("count dirty metadata writeback assets: %w", err)
	}
	return counts, nil
}

// WorkWritebackState summarizes one work's writable assets for the book-page
// action: how many assets can carry embedded metadata, and how many of those
// are behind the current work metadata (the enabled/"up to date" signal).
type WorkWritebackState struct {
	Writable int
	Dirty    int
}

// GetWorkWritebackState reports the writable/dirty asset counts for one live
// work. A trashed work reports zero (its detail page 404s anyway).
func GetWorkWritebackState(queryer Queryer, workID string) (WorkWritebackState, error) {
	var st WorkWritebackState
	err := queryer.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN a.writeback_rev < w.metadata_rev THEN 1 ELSE 0 END), 0)
		FROM assets a
		JOIN works w ON w.id = a.work_id
		WHERE a.work_id = ? AND w.deleted_at IS NULL AND `+metadataWritebackFormatSQL,
		workID).Scan(&st.Writable, &st.Dirty)
	if err != nil {
		return WorkWritebackState{}, fmt.Errorf("work writeback state: %w", err)
	}
	return st, nil
}

// GetMetadataWritebackAsset loads the current writable write-back projection for
// one live asset. It is the freshness check before a physical write attempt.
func GetMetadataWritebackAsset(queryer Queryer, assetID string) (MetadataWritebackAssetRow, error) {
	return scanMetadataWritebackAsset(queryer.QueryRow(`
		SELECT a.id, a.work_id, a.storage_path, a.format,
		       COALESCE(a.current_sha256, ''), a.current_size,
		       w.metadata_rev, a.writeback_rev, COALESCE(a.writeback_error, '')
		FROM assets a
		JOIN works w ON w.id = a.work_id
		WHERE a.id = ? AND w.deleted_at IS NULL AND `+metadataWritebackFormatSQL+`
	`, assetID))
}

// ListDirtyMetadataWritebackAssets returns writable live assets ready for the
// future write-back worker or an explicit write-back command.
func ListDirtyMetadataWritebackAssets(queryer Queryer, scope VisibilityScope, limit int) ([]MetadataWritebackAssetRow, error) {
	where, args := metadataWritebackDirtyWhere(scope)
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

// ListAutomaticMetadataWritebackAssets returns one bounded auto-reconciler
// batch. Fresh failures stay out until their assets.updated_at failure timestamp
// reaches failedBefore; clean dirty rows are immediately eligible. The retry
// state is therefore durable across process restarts without another schema
// column or loading the whole backlog for in-memory filtering.
func ListAutomaticMetadataWritebackAssets(queryer Queryer, scope VisibilityScope, failedBefore int64, limit int) ([]MetadataWritebackAssetRow, error) {
	where := "w.deleted_at IS NULL AND a.writeback_rev < w.metadata_rev AND " + metadataWritebackFormatSQL +
		" AND (COALESCE(a.writeback_error, '') = '' OR a.updated_at <= ?)"
	where, args := scope.AppendWorkWhere(where, "w.id", failedBefore)
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

// ListFailedMetadataWritebackAssets returns dirty writable live assets whose
// last write-back attempt failed.
func ListFailedMetadataWritebackAssets(queryer Queryer, scope VisibilityScope, limit int) ([]MetadataWritebackAssetRow, error) {
	where := "w.deleted_at IS NULL AND a.writeback_rev < w.metadata_rev AND COALESCE(a.writeback_error, '') <> '' AND " + metadataWritebackFormatSQL
	where, args := scope.AppendWorkWhere(where, "w.id")
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

// ListAllMetadataWritebackAssets returns every live writable asset, including
// clean rows. It is for explicit maintenance runs such as --all.
func ListAllMetadataWritebackAssets(queryer Queryer, scope VisibilityScope, limit int) ([]MetadataWritebackAssetRow, error) {
	where, args := scope.AppendWorkWhere("w.deleted_at IS NULL AND "+metadataWritebackFormatSQL, "w.id")
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

// ListMetadataWritebackAssetsByWorkIDs returns writable live assets for the
// selected works, regardless of dirty state.
func ListMetadataWritebackAssetsByWorkIDs(queryer Queryer, scope VisibilityScope, workIDs []string, limit int) ([]MetadataWritebackAssetRow, error) {
	ids := dedupWorkIDs(workIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := idPlaceholders(ids)
	where := "w.deleted_at IS NULL AND " + metadataWritebackFormatSQL + " AND w.id IN (" + placeholders + ")"
	where, args = scope.AppendWorkWhere(where, "w.id", args...)
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

func listMetadataWritebackAssets(queryer Queryer, where string, args []any, limit int) ([]MetadataWritebackAssetRow, error) {
	limitClause := ""
	if limit > 0 {
		limitClause = " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := queryer.Query(`
		SELECT a.id, a.work_id, a.storage_path, a.format,
		       COALESCE(a.current_sha256, ''), a.current_size,
		       w.metadata_rev, a.writeback_rev, COALESCE(a.writeback_error, '')
		FROM assets a
		JOIN works w ON w.id = a.work_id
		WHERE `+where+`
		ORDER BY w.updated_at ASC, a.id ASC`+limitClause, args...)
	if err != nil {
		return nil, fmt.Errorf("query dirty metadata writeback assets: %w", err)
	}
	defer rows.Close()

	var out []MetadataWritebackAssetRow
	for rows.Next() {
		row, err := scanMetadataWritebackAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dirty metadata writeback asset rows: %w", err)
	}
	return out, nil
}

func scanMetadataWritebackAsset(row rowScanner) (MetadataWritebackAssetRow, error) {
	var asset MetadataWritebackAssetRow
	var formatKey string
	if err := row.Scan(
		&asset.AssetID, &asset.WorkID, &asset.StoragePath, &formatKey,
		&asset.CurrentSHA256, &asset.CurrentSize,
		&asset.MetadataRev, &asset.WritebackRev, &asset.Error,
	); err != nil {
		return MetadataWritebackAssetRow{}, fmt.Errorf("scan metadata writeback asset: %w", err)
	}
	asset.Format = format.FormatFromKey(formatKey)
	return asset, nil
}

func LoadMetadataWritebackSnapshot(queryer Queryer, workID string) (MetadataWritebackSnapshot, error) {
	var snap MetadataWritebackSnapshot
	var tags string
	err := queryer.QueryRow(`
		SELECT id, title, sort_title, COALESCE(series, ''), COALESCE(series_index, 0),
		       COALESCE(description, ''), COALESCE(tags, ''),
		       COALESCE(publisher, ''), COALESCE(published_date, ''),
		       COALESCE(language, ''), COALESCE(identifiers, ''), metadata_rev, cover_version, updated_at
		FROM works
		WHERE id = ? AND deleted_at IS NULL
	`, workID).Scan(
		&snap.WorkID, &snap.Metadata.Title, &snap.Metadata.SortTitle,
		&snap.Metadata.Series, &snap.Metadata.SeriesIndex,
		&snap.Metadata.Description, &tags, &snap.Metadata.Publisher,
		&snap.Metadata.Date, &snap.Metadata.Language, &snap.Metadata.Identifier,
		&snap.MetadataRev, &snap.CoverVersion, &snap.UpdatedAt,
	)
	if err != nil {
		return MetadataWritebackSnapshot{}, err
	}
	snap.Metadata.Language = bookmeta.NormalizeLanguage(snap.Metadata.Language)
	snap.Metadata.Tags = bookmeta.ParseTagList(tags)

	authors, err := AuthorsByWorkIDs(queryer, []string{snap.WorkID})
	if err != nil {
		return MetadataWritebackSnapshot{}, err
	}
	for _, author := range authors[snap.WorkID] {
		name := strings.TrimSpace(author.Name)
		if name == "" {
			continue
		}
		snap.Metadata.Authors = append(snap.Metadata.Authors, bookmeta.AuthorMeta{
			Name:     name,
			SortName: strings.TrimSpace(author.SortName),
			Role:     strings.TrimSpace(author.Role),
		})
	}
	return snap, nil
}

func UpsertMetadataWritebackAttempt(execer Execer, attempt MetadataWritebackAttempt) error {
	if _, err := execer.Exec(`
		INSERT INTO metadata_writeback_attempts
			(asset_id, metadata_rev, storage_path, temp_path, sha256, size, koreader_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, unixepoch())
		ON CONFLICT(asset_id) DO UPDATE SET
			metadata_rev = excluded.metadata_rev,
			storage_path = excluded.storage_path,
			temp_path = excluded.temp_path,
			sha256 = excluded.sha256,
			size = excluded.size,
			koreader_hash = excluded.koreader_hash,
			created_at = unixepoch()
	`, attempt.AssetID, attempt.MetadataRev, attempt.StoragePath, attempt.TempPath, attempt.SHA256, attempt.Size, attempt.KOReaderHash); err != nil {
		return fmt.Errorf("upsert metadata writeback attempt: %w", err)
	}
	return nil
}

func LoadMetadataWritebackAttempt(queryer Queryer, assetID string) (MetadataWritebackAttempt, bool, error) {
	var attempt MetadataWritebackAttempt
	err := queryer.QueryRow(`
		SELECT asset_id, metadata_rev, storage_path, temp_path, sha256, size, COALESCE(koreader_hash, '')
		FROM metadata_writeback_attempts
		WHERE asset_id = ?
	`, assetID).Scan(
		&attempt.AssetID, &attempt.MetadataRev, &attempt.StoragePath,
		&attempt.TempPath, &attempt.SHA256, &attempt.Size, &attempt.KOReaderHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MetadataWritebackAttempt{}, false, nil
	}
	if err != nil {
		return MetadataWritebackAttempt{}, false, fmt.Errorf("load metadata writeback attempt: %w", err)
	}
	return attempt, true, nil
}

func ListMetadataWritebackAttempts(queryer Queryer) ([]MetadataWritebackAttemptRow, error) {
	rows, err := queryer.Query(`
		SELECT m.asset_id, m.metadata_rev, m.storage_path, m.temp_path, m.sha256,
		       m.size, COALESCE(m.koreader_hash, ''), a.storage_path
		FROM metadata_writeback_attempts m
		JOIN assets a ON a.id = m.asset_id
		ORDER BY m.asset_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list metadata writeback attempts: %w", err)
	}
	defer rows.Close()

	var attempts []MetadataWritebackAttemptRow
	for rows.Next() {
		var row MetadataWritebackAttemptRow
		if err := rows.Scan(
			&row.AssetID, &row.MetadataRev, &row.StoragePath, &row.TempPath,
			&row.SHA256, &row.Size, &row.KOReaderHash, &row.CurrentStoragePath,
		); err != nil {
			return nil, fmt.Errorf("scan metadata writeback attempt: %w", err)
		}
		attempts = append(attempts, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metadata writeback attempts: %w", err)
	}
	return attempts, nil
}

func ClearMetadataWritebackAttempt(execer Execer, assetID string) error {
	if _, err := execer.Exec("DELETE FROM metadata_writeback_attempts WHERE asset_id = ?", assetID); err != nil {
		return fmt.Errorf("clear metadata writeback attempt: %w", err)
	}
	return nil
}

func MarkMetadataWritebackSuccess(tx *sql.Tx, assetID, storagePath, sha256 string, size int64, koReaderHash string, metadataRev int64) error {
	res, err := tx.Exec(`
		UPDATE assets
		SET current_sha256 = ?,
		    current_size = ?,
		    koreader_hash = ?,
		    writeback_rev = ?,
		    writeback_error = NULL,
		    updated_at = unixepoch()
		WHERE id = ? AND storage_path = ?
	`, sha256, size, koReaderHash, metadataRev, assetID, storagePath)
	if err != nil {
		return fmt.Errorf("mark metadata writeback success: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if err := ClearMetadataWritebackAttempt(tx, assetID); err != nil {
		return err
	}
	return nil
}

func MarkMetadataWritebackError(execer Execer, assetID string, writeErr error) error {
	msg := strings.TrimSpace(fmt.Sprint(writeErr))
	if msg == "" {
		msg = "metadata writeback failed"
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if _, err := execer.Exec(`
		UPDATE assets
		SET writeback_error = ?, updated_at = unixepoch()
		WHERE id = ?
	`, msg, assetID); err != nil {
		return fmt.Errorf("mark metadata writeback error: %w", err)
	}
	return nil
}

func metadataWritebackDirtyWhere(scope VisibilityScope) (string, []any) {
	where := "w.deleted_at IS NULL AND a.writeback_rev < w.metadata_rev AND " + metadataWritebackFormatSQL
	return scope.AppendWorkWhere(where, "w.id")
}
