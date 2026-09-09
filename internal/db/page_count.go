package db

import (
	"fmt"

	"github.com/levmv/polka/internal/format"
)

type PageCountAsset struct {
	AssetID     int64
	StoragePath string
	Format      format.Format
	SHA256      []byte
	Size        int64
	PageCount   int
}

func PrimaryPageCountAsset(queryer Queryer, scope VisibilityScope, bookID int64) (PageCountAsset, error) {
	var asset PageCountAsset
	var key string
	where, args := scope.AppendBookWhere("b.id = ? AND b.deleted_at IS NULL AND a.is_primary = 1", "b.id", bookID)
	err := queryer.QueryRow(`
		SELECT a.id, a.storage_path, a.format, a.current_sha256,
		       COALESCE(a.current_size, a.original_size, 0), COALESCE(a.page_count, 0)
		FROM assets a JOIN books b ON b.id = a.book_id WHERE `+where, args...).Scan(
		&asset.AssetID, &asset.StoragePath, &key, &asset.SHA256, &asset.Size, &asset.PageCount)
	asset.Format = format.FormatFromKey(key)
	return asset, err
}

// StorePageCount fills a missing count only for the bytes that were measured.
// The next metadata writeback includes it; counting alone does not advance the
// book's revision or schedule a file update.
func StorePageCount(execer Execer, assetID int64, sha256 []byte, pages int) (bool, error) {
	if pages <= 0 {
		return false, fmt.Errorf("page count must be positive")
	}
	result, err := execer.Exec(`
		UPDATE assets SET page_count = ?
		WHERE id = ? AND current_sha256 = ? AND page_count IS NULL
		  AND EXISTS (SELECT 1 FROM books b WHERE b.id = assets.book_id AND b.deleted_at IS NULL)
	`, pages, assetID, sha256)
	if err != nil {
		return false, fmt.Errorf("store page count: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}
