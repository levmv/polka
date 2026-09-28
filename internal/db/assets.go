package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/levmv/polka/internal/format"
)

type AssetRow struct {
	ID               int64
	Extension        string
	Format           format.Format
	StoragePath      string
	OriginalFilename string
	BookID           int64
	IsPrimary        bool
	PageCount        int
	// Size is COALESCE(current_size, original_size, 0) from the row, so UI/list
	// paths report byte sizes without a per-asset os.Stat (matters on a NAS root).
	Size int64
}

type PrimaryAssetRow struct {
	ID          int64
	BookID      int64
	Title       string
	Extension   string
	Format      format.Format
	CurrentHash []byte
}

type AssetWithAuthorRow struct {
	ID               int64
	BookID           int64
	StoragePath      string
	OriginalFilename string
	Extension        string
	Format           format.Format
	OriginalHash     []byte
	CurrentHash      []byte
	OriginalSize     sql.NullInt64
	CurrentSize      sql.NullInt64
	Title            string
	SortTitle        string
	Series           string
	SeriesIndex      string
	AuthorName       string
	AuthorSortName   string
}

func requireAsset(queryer Queryer, assetID int64) error {
	var id int64
	err := queryer.QueryRow("SELECT id FROM assets WHERE id = ?", assetID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAssetNotFound
	}
	if err != nil {
		return fmt.Errorf("check asset: %w", err)
	}
	return nil
}

// RecordAssetRestore keeps write-back acknowledgement only when restoring the
// exact bytes it described. Original import identity is never changed.
func RecordAssetRestore(database Execer, assetID int64, hash []byte, size int64) error {
	_, err := database.Exec(`
		UPDATE assets
		SET writeback_rev = CASE WHEN current_hash = ? THEN writeback_rev ELSE 0 END,
		    writeback_error = NULL,
		    koreader_hash = CASE WHEN current_hash = ? THEN koreader_hash ELSE NULL END,
		    current_hash = ?, current_size = ?, updated_at = unixepoch()
		WHERE id = ?
	`, hash, hash, hash, size, assetID)
	return err
}

// EnsurePreferredPrimaryAsset chooses the preferred browser-reading format.
// Equal priorities keep the current primary, then prefer the oldest asset.
// A catalog default never changes per-user positions or annotations.
func EnsurePreferredPrimaryAsset(tx *Tx, bookID int64) error {
	rows, err := tx.Query(`
		SELECT id, format FROM assets WHERE book_id = ?
		ORDER BY is_primary DESC, created_at ASC, id ASC
	`, bookID)
	if err != nil {
		return fmt.Errorf("select primary asset: %w", err)
	}
	var selectedID int64
	var bestPriority int
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			rows.Close()
			return fmt.Errorf("scan primary asset: %w", err)
		}
		priority := format.ReadingPriority(format.FormatFromKey(key))
		if selectedID == 0 || priority < bestPriority {
			selectedID, bestPriority = id, priority
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("select primary asset: %w", err)
	}
	if selectedID == 0 {
		return nil
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
				SELECT a.book_id, a.id, a.extension, a.format, a.storage_path, a.original_filename, a.is_primary,
				       COALESCE(a.current_size, a.original_size, 0), COALESCE(a.page_count, 0)
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
		SELECT a.book_id, a.id, a.extension, a.format, a.storage_path, a.original_filename, a.is_primary,
		       COALESCE(a.current_size, a.original_size, 0), COALESCE(a.page_count, 0)
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
		var isPrimary int
		if err := rows.Scan(&a.BookID, &a.ID, &a.Extension, &formatKey, &a.StoragePath, &a.OriginalFilename, &isPrimary, &a.Size, &a.PageCount); err != nil {
			return nil, fmt.Errorf("%s scan: %w", operation, err)
		}
		a.Format = format.FormatFromKey(formatKey)
		a.IsPrimary = isPrimary == 1
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
	where, args := scope.AppendBookWhere("b.id = ? AND b.deleted_at IS NULL AND a.is_primary = 1", "b.id", bookID)
	err := queryer.QueryRow(`
			SELECT b.id, b.title, a.id, a.extension, a.format, a.current_hash
			FROM books b
			JOIN assets a ON a.book_id = b.id
			WHERE `+where+`
			LIMIT 1
	`, args...).Scan(&a.BookID, &a.Title, &a.ID, &a.Extension, &formatKey, &a.CurrentHash)
	if err != nil {
		return PrimaryAssetRow{}, err
	}
	a.Format = format.FormatFromKey(formatKey)
	return a, nil
}

func AllAssetsWithPrimaryAuthor(queryer Queryer) ([]AssetWithAuthorRow, error) {
	rows, err := queryer.Query(`
		SELECT a.id, a.book_id, a.storage_path, a.original_filename, a.extension, a.format,
		       a.original_hash, a.current_hash,
		       a.original_size, a.current_size,
		       b.title, b.sort_title, COALESCE(b.series, ''),
		       CASE WHEN b.series_index IS NULL THEN '' ELSE CAST(b.series_index AS TEXT) END,
		       COALESCE((SELECT name FROM authors WHERE id = (SELECT author_id FROM book_authors WHERE book_id = b.id ORDER BY author_order ASC, rowid ASC LIMIT 1)), ''),
		       COALESCE((SELECT sort_name FROM authors WHERE id = (SELECT author_id FROM book_authors WHERE book_id = b.id ORDER BY author_order ASC, rowid ASC LIMIT 1)), '')
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
		if err := rows.Scan(&a.ID, &a.BookID, &a.StoragePath, &a.OriginalFilename, &a.Extension, &formatKey, &a.OriginalHash, &a.CurrentHash, &a.OriginalSize, &a.CurrentSize, &a.Title, &a.SortTitle, &a.Series, &a.SeriesIndex, &a.AuthorName, &a.AuthorSortName); err != nil {
			return nil, fmt.Errorf("scan all assets: %w", err)
		}
		a.Format = format.FormatFromKey(formatKey)
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

// AssetContentSizes returns recorded original and current sizes, including
// assets of trashed books and omitting NULL sizes. Import batches use this set
// only as a prefilter; content hashes determine duplicates.
func AssetContentSizes(q Queryer) (map[int64]struct{}, error) {
	rows, err := q.Query(`SELECT original_size, current_size FROM assets`)
	if err != nil {
		return nil, fmt.Errorf("query asset content sizes: %w", err)
	}
	defer rows.Close()

	sizes := make(map[int64]struct{})
	for rows.Next() {
		var original, current sql.NullInt64
		if err := rows.Scan(&original, &current); err != nil {
			return nil, fmt.Errorf("scan asset content sizes: %w", err)
		}
		if original.Valid {
			sizes[original.Int64] = struct{}{}
		}
		if current.Valid {
			sizes[current.Int64] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("asset content sizes rows: %w", err)
	}
	return sizes, nil
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
