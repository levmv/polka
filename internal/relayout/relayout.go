// Package relayout owns the shared sequencing for work-metadata mutations whose
// path inputs may change: commit catalog/search bookkeeping first, then move
// files to their canonical paths while keeping DB and disk consistent. The
// lost-file recovery in `polka repair` is a different, search-based concern and
// stays separate.
package relayout

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

// Work moves each of a work's assets to its canonical path and then updates
// assets.storage_path. Before and after the operation the DB and disk agree. The
// brief interval after the move but before the update is an accepted tradeoff: a
// concurrent read can fail transiently and succeed on retry; keeping both paths
// live would require disproportionate copying, hard-link portability rules, or
// read/write coordination for this rare metadata-driven move. A failed move
// leaves the old, valid path untouched; a storage_path-update failure after a
// successful move is compensated by moving the file back. Callers should treat
// a returned error as a warning: the metadata change is already durable and the
// DB stays consistent with disk; `polka repair` recovers the rare unrecoverable
// window via the asset-id tag. Returns the number of files relocated.
func Work(database *db.DB, root storage.Root, workID string) (int, error) {
	var title, sortTitle, series, seriesIndex string
	if err := database.QueryRow(`
		SELECT title, COALESCE(sort_title, ''), COALESCE(series, ''),
		       CASE WHEN series_index IS NULL THEN '' ELSE CAST(series_index AS TEXT) END
		FROM works
		WHERE id = ?
	`, workID).Scan(&title, &sortTitle, &series, &seriesIndex); err != nil {
		return 0, fmt.Errorf("load title: %w", err)
	}
	template, err := storage.OpenBookPathTemplate(database)
	if err != nil {
		return 0, err
	}

	primaryAuthor, primaryAuthorSort, err := db.PrimaryAuthor(database, workID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("primary author: %w", err)
	}
	if primaryAuthor == "" {
		primaryAuthor = "Unknown Author"
		primaryAuthorSort = bookmeta.AuthorSort("Unknown Author")
	}

	assets, err := db.AssetsByWorkIDs(database, []string{workID})
	if err != nil {
		return 0, err
	}
	if len(assets) == 0 {
		return 0, nil
	}
	// We hold assets to relayout, so the catalog has books: an empty root here is
	// the dropped-mount shadow-write case, not a fresh library.
	if err := storage.RequireWritableRoot(root, true); err != nil {
		return 0, err
	}

	moved := 0
	for _, a := range assets {
		originalFilename := strings.TrimSpace(a.OriginalFilename)
		if originalFilename == "" {
			originalFilename = filepath.Base(a.StoragePath)
		}
		newPath, err := storage.BookPath(template, storage.BookPathData{
			Title:            title,
			SortTitle:        sortTitle,
			Author:           primaryAuthor,
			AuthorSort:       primaryAuthorSort,
			Series:           series,
			SeriesIndex:      seriesIndex,
			AssetID:          a.ID,
			WorkID:           workID,
			Ext:              a.Extension,
			OriginalFilename: originalFilename,
		})
		if err != nil {
			return moved, err
		}
		if newPath == a.StoragePath {
			continue
		}
		if err := storage.Move(root, a.StoragePath, newPath); err != nil {
			// File still at the old path the DB points at — no divergence.
			return moved, fmt.Errorf("move %s: %w", a.ID, err)
		}
		if _, err := database.Exec("UPDATE assets SET storage_path = ?, filename = ? WHERE id = ?", newPath, filepath.Base(newPath), a.ID); err != nil {
			// Compensate: move the file back so the DB (old path) stays valid.
			if backErr := storage.Move(root, newPath, a.StoragePath); backErr != nil {
				return moved, fmt.Errorf("update storage_path %s failed and rollback move also failed (DB/disk diverged, run `polka repair`): update=%v rollback=%w", a.ID, err, backErr)
			}
			return moved, fmt.Errorf("update storage_path %s: %w", a.ID, err)
		}
		moved++
	}
	return moved, nil
}
