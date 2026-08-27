package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/format"
)

type AssetRow struct {
	ID               string
	Extension        string
	Format           format.Format
	StoragePath      string
	OriginalFilename string
	WorkID           string
	IsPrimary        bool
	CanRead          bool
	// Size is COALESCE(current_size, original_size, 0) from the row, so UI/list
	// paths report byte sizes without a per-asset os.Stat (matters on a NAS root).
	Size int64
}

type PrimaryAssetRow struct {
	AssetRow
	Filename string
	Title    string
}

type AssetWithAuthorRow struct {
	ID               string
	WorkID           string
	StoragePath      string
	OriginalFilename string
	Extension        string
	Format           format.Format
	CanRead          bool
	OriginalSHA256   string
	CurrentSHA256    string
	OriginalSize     sql.NullInt64
	CurrentSize      sql.NullInt64
	Title            string
	SortTitle        string
	Series           string
	SeriesIndex      string
	AuthorName       string
	AuthorSortName   string
}

// EnsureReadablePrimaryAsset keeps one primary asset for a work while
// preferring something the browser can actually open. An existing readable
// primary is stable; an unreadable primary is replaced only when a readable
// candidate exists. If every asset is unreadable, the existing primary remains
// the least surprising download/default-format choice.
func EnsureReadablePrimaryAsset(tx *sql.Tx, workID string) error {
	var selectedID string
	err := tx.QueryRow(`
		SELECT id
		FROM assets
		WHERE work_id = ?
		ORDER BY can_read DESC, is_primary DESC, created_at ASC, id ASC
		LIMIT 1
	`, workID).Scan(&selectedID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("select primary asset: %w", err)
	}

	if _, err := tx.Exec(`
		UPDATE assets
		SET is_primary = 0, updated_at = unixepoch()
		WHERE work_id = ? AND is_primary = 1 AND id <> ?
	`, workID, selectedID); err != nil {
		return fmt.Errorf("demote old primary asset: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE assets
		SET is_primary = 1, updated_at = unixepoch()
		WHERE id = ? AND is_primary = 0
	`, selectedID); err != nil {
		return fmt.Errorf("promote primary asset: %w", err)
	}
	return nil
}

func AssetsByWorkIDs(queryer Queryer, workIDs []string) ([]AssetRow, error) {
	if len(workIDs) == 0 {
		return nil, nil
	}

	placeholders := strings.Repeat("?,", len(workIDs))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]any, len(workIDs))
	for i, id := range workIDs {
		args[i] = id
	}

	query := `
				SELECT a.work_id, a.id, a.extension, a.format, a.storage_path, COALESCE(a.original_filename, ''), a.is_primary, a.can_read,
				       COALESCE(a.current_size, a.original_size, 0)
			FROM assets a
			WHERE a.work_id IN (` + placeholders + `)
			ORDER BY a.work_id, a.is_primary DESC, a.extension COLLATE NOCASE ASC, a.id ASC
		`
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("assets by works query: %w", err)
	}
	return scanAssetRows(rows, "assets by works")
}

// AssetsForTrashedWorks returns every asset whose work is currently in Trash.
// Empty-trash uses this dedicated projection so its SQL shape stays constant at
// large-library scale instead of expanding one host parameter per work.
func AssetsForTrashedWorks(queryer Queryer) ([]AssetRow, error) {
	rows, err := queryer.Query(`
		SELECT a.work_id, a.id, a.extension, a.format, a.storage_path, COALESCE(a.original_filename, ''), a.is_primary, a.can_read,
		       COALESCE(a.current_size, a.original_size, 0)
		FROM assets a
		JOIN works w ON w.id = a.work_id
		WHERE w.deleted_at IS NOT NULL
		ORDER BY a.work_id, a.is_primary DESC, a.extension COLLATE NOCASE ASC, a.id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("trashed assets query: %w", err)
	}
	return scanAssetRows(rows, "trashed assets")
}

func scanAssetRows(rows *sql.Rows, operation string) ([]AssetRow, error) {
	defer rows.Close()

	var assets []AssetRow
	for rows.Next() {
		var a AssetRow
		var formatKey string
		var isPrimary, canRead int
		if err := rows.Scan(&a.WorkID, &a.ID, &a.Extension, &formatKey, &a.StoragePath, &a.OriginalFilename, &isPrimary, &canRead, &a.Size); err != nil {
			return nil, fmt.Errorf("%s scan: %w", operation, err)
		}
		a.Format = format.FormatFromKey(formatKey)
		a.IsPrimary = isPrimary == 1
		a.CanRead = canRead == 1
		assets = append(assets, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s rows: %w", operation, err)
	}
	return assets, nil
}

func PrimaryAssetForWork(queryer Queryer, scope VisibilityScope, workID string) (PrimaryAssetRow, error) {
	var a PrimaryAssetRow
	var formatKey string
	var isPrimary, canRead int
	where, args := scope.AppendWorkWhere("w.id = ? AND w.deleted_at IS NULL AND a.is_primary = 1", "w.id", workID)
	err := queryer.QueryRow(`
			SELECT w.id, w.title, a.id, a.extension, a.format, a.storage_path, a.filename, a.is_primary, a.can_read
			FROM works w
			JOIN assets a ON a.work_id = w.id
			WHERE `+where+`
			LIMIT 1
	`, args...).Scan(&a.WorkID, &a.Title, &a.ID, &a.Extension, &formatKey, &a.StoragePath, &a.Filename, &isPrimary, &canRead)
	if err != nil {
		return PrimaryAssetRow{}, err
	}
	a.Format = format.FormatFromKey(formatKey)
	a.IsPrimary = isPrimary == 1
	a.CanRead = canRead == 1
	return a, nil
}

func AllAssetsWithPrimaryAuthor(queryer Queryer) ([]AssetWithAuthorRow, error) {
	rows, err := queryer.Query(`
		SELECT a.id, a.work_id, a.storage_path, COALESCE(a.original_filename, ''), a.extension, COALESCE(a.format, ''), COALESCE(a.can_read, 0),
		       COALESCE(a.original_sha256, ''), COALESCE(a.current_sha256, ''),
		       a.original_size, a.current_size,
		       w.title, COALESCE(w.sort_title, ''), COALESCE(w.series, ''),
		       CASE WHEN w.series_index IS NULL THEN '' ELSE CAST(w.series_index AS TEXT) END,
		       (SELECT name FROM authors WHERE id = (SELECT author_id FROM work_authors WHERE work_id = w.id ORDER BY author_order ASC, rowid ASC LIMIT 1)) as author_name,
		       (SELECT sort_name FROM authors WHERE id = (SELECT author_id FROM work_authors WHERE work_id = w.id ORDER BY author_order ASC, rowid ASC LIMIT 1)) as author_sort_name
		FROM assets a
		JOIN works w ON a.work_id = w.id
		ORDER BY a.id
	`)
	if err != nil {
		return nil, fmt.Errorf("query all assets: %w", err)
	}
	defer rows.Close()

	var assets []AssetWithAuthorRow
	for rows.Next() {
		var a AssetWithAuthorRow
		var formatKey string
		var canRead int
		if err := rows.Scan(&a.ID, &a.WorkID, &a.StoragePath, &a.OriginalFilename, &a.Extension, &formatKey, &canRead, &a.OriginalSHA256, &a.CurrentSHA256, &a.OriginalSize, &a.CurrentSize, &a.Title, &a.SortTitle, &a.Series, &a.SeriesIndex, &a.AuthorName, &a.AuthorSortName); err != nil {
			return nil, fmt.Errorf("scan all assets: %w", err)
		}
		a.Format = format.FormatFromKey(formatKey)
		a.CanRead = canRead == 1
		assets = append(assets, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows all assets: %w", err)
	}
	return assets, nil
}

func HasAnyAsset(q Queryer) (bool, error) {
	var exists bool
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM assets)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check for assets: %w", err)
	}
	return exists, nil
}

// LibraryStorageStats reports the number of live works and the total on-disk
// size of their asset files, for the Storage settings health line. Size uses
// the current bytes, falling back to the imported size for assets never
// rewritten; trashed works are excluded because their files are pending purge.
func LibraryStorageStats(q Queryer) (books int, sizeBytes int64, err error) {
	row := q.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM works WHERE deleted_at IS NULL),
			(SELECT COALESCE(SUM(COALESCE(a.current_size, a.original_size, 0)), 0)
			   FROM assets a
			   JOIN works w ON w.id = a.work_id
			  WHERE w.deleted_at IS NULL)`)
	if err := row.Scan(&books, &sizeBytes); err != nil {
		return 0, 0, fmt.Errorf("library storage stats: %w", err)
	}
	return books, sizeBytes, nil
}
