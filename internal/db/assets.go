package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/levmv/polka/internal/format"
)

type AssetRow struct {
	ID               string
	Extension        string
	Format           format.Format
	StoragePath      string
	OriginalFilename string
	BookID           int64
	IsPrimary        bool
	CanRead          bool
	// Size is COALESCE(current_size, original_size, 0) from the row, so UI/list
	// paths report byte sizes without a per-asset os.Stat (matters on a NAS root).
	Size int64
}

type PrimaryAssetRow struct {
	AssetRow
	Filename      string
	Title         string
	CurrentSHA256 string
}

type AssetWithAuthorRow struct {
	ID               string
	BookID           int64
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

// RecordAssetRestore keeps write-back acknowledgement only when restoring the
// exact bytes it described. Original import identity is never changed.
func RecordAssetRestore(database Execer, assetID, sha256 string, size int64) error {
	_, err := database.Exec(`
		UPDATE assets
		SET writeback_rev = CASE WHEN current_sha256 = ? THEN writeback_rev ELSE 0 END,
		    writeback_error = NULL,
		    current_sha256 = ?, current_size = ?, koreader_hash = NULL, updated_at = unixepoch()
		WHERE id = ?
	`, sha256, sha256, size, assetID)
	return err
}

// EnsureReadablePrimaryAsset keeps one primary asset for a book while
// preferring something the browser can actually open. An existing readable
// primary is stable; an unreadable primary is replaced only when a readable
// candidate exists. If every asset is unreadable, the existing primary remains
// the least surprising download/default-format choice.
func EnsureReadablePrimaryAsset(tx *Tx, bookID int64) error {
	var selectedID string
	err := tx.QueryRow(`
		SELECT id
		FROM assets
		WHERE book_id = ?
		ORDER BY can_read DESC, is_primary DESC, created_at ASC, id ASC
		LIMIT 1
	`, bookID).Scan(&selectedID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("select primary asset: %w", err)
	}

	if _, err := tx.Exec(`
		UPDATE assets
		SET is_primary = 0, updated_at = unixepoch()
		WHERE book_id = ? AND is_primary = 1 AND id <> ?
	`, bookID, selectedID); err != nil {
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

func AssetsByBookIDs(queryer Queryer, bookIDs []int64) ([]AssetRow, error) {
	if len(bookIDs) == 0 {
		return nil, nil
	}

	placeholders, args := idPlaceholders(bookIDs)

	query := `
				SELECT a.book_id, a.id, a.extension, a.format, a.storage_path, COALESCE(a.original_filename, ''), a.is_primary, a.can_read,
				       COALESCE(a.current_size, a.original_size, 0)
			FROM assets a
			WHERE a.book_id IN (` + placeholders + `)
			ORDER BY a.book_id, a.is_primary DESC, a.extension COLLATE NOCASE ASC, a.id ASC
		`
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("assets by books query: %w", err)
	}
	return scanAssetRows(rows, "assets by books")
}

// AssetsForTrashedBooks returns every asset whose book is currently in Trash.
// Empty-trash uses this dedicated projection so its SQL shape stays constant at
// large-library scale instead of expanding one host parameter per book.
func AssetsForTrashedBooks(queryer Queryer) ([]AssetRow, error) {
	rows, err := queryer.Query(`
		SELECT a.book_id, a.id, a.extension, a.format, a.storage_path, COALESCE(a.original_filename, ''), a.is_primary, a.can_read,
		       COALESCE(a.current_size, a.original_size, 0)
		FROM assets a
		JOIN books b ON b.id = a.book_id
		WHERE b.deleted_at IS NOT NULL
		ORDER BY a.book_id, a.is_primary DESC, a.extension COLLATE NOCASE ASC, a.id ASC
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
		if err := rows.Scan(&a.BookID, &a.ID, &a.Extension, &formatKey, &a.StoragePath, &a.OriginalFilename, &isPrimary, &canRead, &a.Size); err != nil {
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

func PrimaryAssetForBook(queryer Queryer, scope VisibilityScope, bookID int64) (PrimaryAssetRow, error) {
	var a PrimaryAssetRow
	var formatKey string
	var isPrimary, canRead int
	where, args := scope.AppendBookWhere("b.id = ? AND b.deleted_at IS NULL AND a.is_primary = 1", "b.id", bookID)
	err := queryer.QueryRow(`
			SELECT b.id, b.title, a.id, a.extension, a.format, a.storage_path, a.filename, a.is_primary, a.can_read,
			       COALESCE(a.current_sha256, '')
			FROM books b
			JOIN assets a ON a.book_id = b.id
			WHERE `+where+`
			LIMIT 1
	`, args...).Scan(&a.BookID, &a.Title, &a.ID, &a.Extension, &formatKey, &a.StoragePath, &a.Filename, &isPrimary, &canRead, &a.CurrentSHA256)
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
		SELECT a.id, a.book_id, a.storage_path, COALESCE(a.original_filename, ''), a.extension, COALESCE(a.format, ''), COALESCE(a.can_read, 0),
		       COALESCE(a.original_sha256, ''), COALESCE(a.current_sha256, ''),
		       a.original_size, a.current_size,
		       b.title, COALESCE(b.sort_title, ''), COALESCE(b.series, ''),
		       CASE WHEN b.series_index IS NULL THEN '' ELSE CAST(b.series_index AS TEXT) END,
		       (SELECT name FROM authors WHERE id = (SELECT author_id FROM book_authors WHERE book_id = b.id ORDER BY author_order ASC, rowid ASC LIMIT 1)) as author_name,
		       (SELECT sort_name FROM authors WHERE id = (SELECT author_id FROM book_authors WHERE book_id = b.id ORDER BY author_order ASC, rowid ASC LIMIT 1)) as author_sort_name
		FROM assets a
		JOIN books b ON a.book_id = b.id
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
		if err := rows.Scan(&a.ID, &a.BookID, &a.StoragePath, &a.OriginalFilename, &a.Extension, &formatKey, &canRead, &a.OriginalSHA256, &a.CurrentSHA256, &a.OriginalSize, &a.CurrentSize, &a.Title, &a.SortTitle, &a.Series, &a.SeriesIndex, &a.AuthorName, &a.AuthorSortName); err != nil {
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

// LibraryStorageStats reports the number of live books and the total on-disk
// size of their asset files, for the Storage settings health line. Size uses
// the current bytes, falling back to the imported size for assets never
// rewritten; trashed books are excluded because their files are pending purge.
func LibraryStorageStats(q Queryer) (books int, sizeBytes int64, err error) {
	row := q.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM books WHERE deleted_at IS NULL),
			(SELECT COALESCE(SUM(COALESCE(a.current_size, a.original_size, 0)), 0)
			   FROM assets a
			   JOIN books b ON b.id = a.book_id
			  WHERE b.deleted_at IS NULL)`)
	if err := row.Scan(&books, &sizeBytes); err != nil {
		return 0, 0, fmt.Errorf("library storage stats: %w", err)
	}
	return books, sizeBytes, nil
}
