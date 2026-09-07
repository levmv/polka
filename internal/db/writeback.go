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
	AssetID       int64
	BookID        int64
	StoragePath   string
	Format        format.Format
	CurrentSHA256 []byte
	CurrentSize   sql.NullInt64
	MetadataRev   int64
	WritebackRev  int64
	Error         string
}

type MetadataWritebackSnapshot struct {
	BookID       int64
	MetadataRev  int64
	CoverVersion int
	UpdatedAt    int64
	Metadata     bookmeta.Metadata
}

type MetadataWritebackAttempt struct {
	AssetID      int64
	MetadataRev  int64
	StoragePath  string
	TempPath     string
	SHA256       []byte
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

// BumpMetadataRev marks books' writable assets as needing metadata write-back
// and touches updated_at. Text metadata and cover changes share this revision.
func BumpMetadataRev(tx *Tx, bookIDs []int64) error {
	if len(bookIDs) == 0 {
		return nil
	}
	placeholders, args := idPlaceholders(bookIDs)
	if _, err := tx.Exec(`
		UPDATE books
		SET metadata_rev = metadata_rev + 1,
		    updated_at = unixepoch()
		WHERE id IN (`+placeholders+`)
	`, args...); err != nil {
		return fmt.Errorf("bump metadata rev: %w", err)
	}
	return nil
}

// CountDirtyMetadataWritebackAssets counts writable live assets whose file
// metadata is behind the current book metadata.
func CountDirtyMetadataWritebackAssets(queryer Queryer, scope VisibilityScope) (MetadataWritebackCounts, error) {
	where, args := metadataWritebackDirtyWhere(scope)
	var counts MetadataWritebackCounts
	err := queryer.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN COALESCE(a.writeback_error, '') <> '' THEN 1 ELSE 0 END), 0)
		FROM assets a
		JOIN books b ON b.id = a.book_id
		WHERE `+where, args...).Scan(&counts.Dirty, &counts.Failed)
	if err != nil {
		return MetadataWritebackCounts{}, fmt.Errorf("count dirty metadata writeback assets: %w", err)
	}
	return counts, nil
}

// BookWritebackState summarizes one book's writable assets for the book-page
// action: how many assets can carry embedded metadata, and how many of those
// are behind the current book metadata (the enabled/"up to date" signal).
type BookWritebackState struct {
	Writable int
	Dirty    int
}

// GetBookWritebackState reports the writable/dirty asset counts for one live
// book. A trashed book reports zero (its detail page 404s anyway).
func GetBookWritebackState(queryer Queryer, bookID int64) (BookWritebackState, error) {
	var st BookWritebackState
	err := queryer.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN a.writeback_rev < b.metadata_rev THEN 1 ELSE 0 END), 0)
		FROM assets a
		JOIN books b ON b.id = a.book_id
		WHERE a.book_id = ? AND b.deleted_at IS NULL AND `+metadataWritebackFormatSQL,
		bookID).Scan(&st.Writable, &st.Dirty)
	if err != nil {
		return BookWritebackState{}, fmt.Errorf("book writeback state: %w", err)
	}
	return st, nil
}

// GetMetadataWritebackAsset loads the current writable write-back projection for
// one live asset. It is the freshness check before a physical write attempt.
func GetMetadataWritebackAsset(queryer Queryer, assetID int64) (MetadataWritebackAssetRow, error) {
	return scanMetadataWritebackAsset(queryer.QueryRow(`
		SELECT a.id, a.book_id, a.storage_path, a.format,
		       a.current_sha256, a.current_size,
		       b.metadata_rev, a.writeback_rev, COALESCE(a.writeback_error, '')
		FROM assets a
		JOIN books b ON b.id = a.book_id
		WHERE a.id = ? AND b.deleted_at IS NULL AND `+metadataWritebackFormatSQL+`
	`, assetID))
}

// ListDirtyMetadataWritebackAssets returns writable live assets whose embedded
// metadata is behind the catalog revision.
func ListDirtyMetadataWritebackAssets(queryer Queryer, scope VisibilityScope, limit int) ([]MetadataWritebackAssetRow, error) {
	where, args := metadataWritebackDirtyWhere(scope)
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

// ListAutomaticMetadataWritebackAssets returns a bounded automatic write-back
// batch. Failed assets are eligible when assets.updated_at <= failedBefore;
// dirty assets without a recorded failure are eligible immediately.
func ListAutomaticMetadataWritebackAssets(queryer Queryer, scope VisibilityScope, failedBefore int64, limit int) ([]MetadataWritebackAssetRow, error) {
	where := "b.deleted_at IS NULL AND a.writeback_rev < b.metadata_rev AND " + metadataWritebackFormatSQL +
		" AND (COALESCE(a.writeback_error, '') = '' OR a.updated_at <= ?)"
	where, args := scope.AppendBookWhere(where, "b.id", failedBefore)
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

// ListFailedMetadataWritebackAssets returns dirty writable live assets whose
// last write-back attempt failed.
func ListFailedMetadataWritebackAssets(queryer Queryer, scope VisibilityScope, limit int) ([]MetadataWritebackAssetRow, error) {
	where := "b.deleted_at IS NULL AND a.writeback_rev < b.metadata_rev AND COALESCE(a.writeback_error, '') <> '' AND " + metadataWritebackFormatSQL
	where, args := scope.AppendBookWhere(where, "b.id")
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

// ListAllMetadataWritebackAssets returns every live writable asset, including
// clean rows. It is for explicit maintenance runs such as --all.
func ListAllMetadataWritebackAssets(queryer Queryer, scope VisibilityScope, limit int) ([]MetadataWritebackAssetRow, error) {
	where, args := scope.AppendBookWhere("b.deleted_at IS NULL AND "+metadataWritebackFormatSQL, "b.id")
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

// ListMetadataWritebackAssetsByBookIDs returns writable live assets for the
// selected books, regardless of dirty state.
func ListMetadataWritebackAssetsByBookIDs(queryer Queryer, scope VisibilityScope, bookIDs []int64, limit int) ([]MetadataWritebackAssetRow, error) {
	ids := DedupBookIDs(bookIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := idPlaceholders(ids)
	where := "b.deleted_at IS NULL AND " + metadataWritebackFormatSQL + " AND b.id IN (" + placeholders + ")"
	where, args = scope.AppendBookWhere(where, "b.id", args...)
	return listMetadataWritebackAssets(queryer, where, args, limit)
}

func listMetadataWritebackAssets(queryer Queryer, where string, args []any, limit int) ([]MetadataWritebackAssetRow, error) {
	limitClause := ""
	if limit > 0 {
		limitClause = " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := queryer.Query(`
		SELECT a.id, a.book_id, a.storage_path, a.format,
		       a.current_sha256, a.current_size,
		       b.metadata_rev, a.writeback_rev, COALESCE(a.writeback_error, '')
		FROM assets a
		JOIN books b ON b.id = a.book_id
		WHERE `+where+`
		ORDER BY b.updated_at ASC, a.id ASC`+limitClause, args...)
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
		&asset.AssetID, &asset.BookID, &asset.StoragePath, &formatKey,
		&asset.CurrentSHA256, &asset.CurrentSize,
		&asset.MetadataRev, &asset.WritebackRev, &asset.Error,
	); err != nil {
		return MetadataWritebackAssetRow{}, fmt.Errorf("scan metadata writeback asset: %w", err)
	}
	asset.Format = format.FormatFromKey(formatKey)
	return asset, nil
}

func LoadMetadataWritebackSnapshot(queryer Queryer, bookID int64) (MetadataWritebackSnapshot, error) {
	var snap MetadataWritebackSnapshot
	var tags string
	err := queryer.QueryRow(`
		SELECT id, title, sort_title, COALESCE(series, ''), COALESCE(series_index, 0),
		       COALESCE(description, ''), COALESCE(tags, ''),
		       COALESCE(publisher, ''), COALESCE(published_date, ''),
		       COALESCE(language, ''), COALESCE(identifiers, ''), metadata_rev, cover_version, updated_at
		FROM books
		WHERE id = ? AND deleted_at IS NULL
	`, bookID).Scan(
		&snap.BookID, &snap.Metadata.Title, &snap.Metadata.SortTitle,
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

	authors, err := AuthorsByBookIDs(queryer, []int64{snap.BookID})
	if err != nil {
		return MetadataWritebackSnapshot{}, err
	}
	for _, author := range authors[snap.BookID] {
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

func LoadMetadataWritebackAttempt(queryer Queryer, assetID int64) (MetadataWritebackAttempt, bool, error) {
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

func ClearMetadataWritebackAttempt(execer Execer, assetID int64) error {
	if _, err := execer.Exec("DELETE FROM metadata_writeback_attempts WHERE asset_id = ?", assetID); err != nil {
		return fmt.Errorf("clear metadata writeback attempt: %w", err)
	}
	return nil
}

func MarkMetadataWritebackSuccess(tx *Tx, assetID int64, storagePath string, sha256 []byte, size int64, koReaderHash string, metadataRev int64) error {
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

func MarkMetadataWritebackError(execer Execer, assetID int64, writeErr error) error {
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
	where := "b.deleted_at IS NULL AND a.writeback_rev < b.metadata_rev AND " + metadataWritebackFormatSQL
	return scope.AppendBookWhere(where, "b.id")
}
