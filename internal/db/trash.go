package db

import (
	"database/sql"
	"fmt"
)

// SoftDeleteBook marks a live book as trashed: it drops out of every normal
// projection (browse / search / cleanup / shelves — all filter
// deleted_at IS NULL) while its row and files stay on disk until an admin purge.
// deletedBy records who trashed it, for a legible "Deleted by X — Restore?"
// trash view. Returns sql.ErrNoRows when no *live* book has this id (unknown id
// or already trashed), so the handler can answer 404 / no-op uniformly.
func SoftDeleteBook(execer Execer, bookID int64, deletedBy int64) error {
	res, err := execer.Exec(`
		UPDATE books SET deleted_at = unixepoch(), deleted_by = ?
		WHERE id = ? AND deleted_at IS NULL
	`, deletedBy, bookID)
	if err != nil {
		return fmt.Errorf("soft delete book: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RestoreBook clears the trash flags, returning the book to the live catalog.
// Returns sql.ErrNoRows when no *trashed* book has this id.
func RestoreBook(execer Execer, bookID int64) error {
	res, err := execer.Exec(`
		UPDATE books SET deleted_at = NULL, deleted_by = NULL
		WHERE id = ? AND deleted_at IS NOT NULL
	`, bookID)
	if err != nil {
		return fmt.Errorf("restore book: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// TrashedBookRow is a trashed book for the trash listing: the same summary
// fields the library grid renders, plus when it was trashed and the display name
// of whoever trashed it (empty if that user has since been removed).
type TrashedBookRow struct {
	BookSummaryRow
	DeletedAt     int64
	DeletedByName string
}

// ListTrashedBooks returns the soft-deleted books, most-recently-trashed first.
func ListTrashedBooks(queryer Queryer, scope VisibilityScope) ([]TrashedBookRow, error) {
	where, args := scope.AppendBookWhere("b.deleted_at IS NOT NULL", "b.id")
	rows, err := queryer.Query(fmt.Sprintf(`
		SELECT %s, b.deleted_at, COALESCE(u.username, '')
		FROM books b
		LEFT JOIN users u ON u.id = b.deleted_by
		WHERE %s
		ORDER BY b.deleted_at DESC
	`, bookSummaryColumns, where), args...)
	if err != nil {
		return nil, fmt.Errorf("list trashed books query: %w", err)
	}
	defer rows.Close()

	var books []TrashedBookRow
	for rows.Next() {
		var t TrashedBookRow
		if err := rows.Scan(&t.ID, &t.Title, &t.Series, &t.SeriesIndex,
			&t.Tags, &t.CoverVersion, &t.Date,
			&t.DeletedAt, &t.DeletedByName); err != nil {
			return nil, fmt.Errorf("list trashed books scan: %w", err)
		}
		books = append(books, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list trashed books rows: %w", err)
	}
	return books, nil
}

// ListTrashedBookIDs returns just the ids of trashed books. With no bookIDs it
// selects the whole trash; with ids it filters to the trashed subset of that
// explicit selection. Purge uses this inside its deletion transaction so a
// concurrent restore cannot change the selected set between inspection and
// commit.
func ListTrashedBookIDs(queryer Queryer, bookIDs ...int64) ([]int64, error) {
	query := `SELECT id FROM books WHERE deleted_at IS NOT NULL`
	var args []any
	if len(bookIDs) > 0 {
		var placeholders string
		placeholders, args = idPlaceholders(bookIDs)
		query += ` AND id IN (` + placeholders + `)`
	}
	query += ` ORDER BY id`
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list trashed book ids query: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list trashed book ids scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PurgeBooks permanently deletes the trashed subset of bookIDs in one batch.
// Search rows are explicit because FTS is not covered by foreign-key cascades;
// orphan authors are swept once after the whole set, not once per book. The
// caller captures file paths before this call and unlinks only after commit.
func PurgeBooks(tx *Tx, bookIDs []int64) (int, error) {
	if len(bookIDs) == 0 {
		return 0, nil
	}
	trashedIDs, err := ListTrashedBookIDs(tx, bookIDs...)
	if err != nil {
		return 0, err
	}
	if len(trashedIDs) == 0 {
		return 0, nil
	}

	placeholders, args := idPlaceholders(trashedIDs)
	if _, err := tx.Exec(`DELETE FROM search WHERE rowid IN (`+placeholders+`)`, args...); err != nil {
		return 0, fmt.Errorf("purge search rows: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM books WHERE deleted_at IS NOT NULL AND id IN (`+placeholders+`)`, args...); err != nil {
		return 0, fmt.Errorf("purge books: %w", err)
	}
	if _, err := DeleteOrphanAuthors(tx); err != nil {
		return 0, fmt.Errorf("purge orphan authors: %w", err)
	}
	return len(trashedIDs), nil
}

// PurgeAllTrashedBooks permanently deletes every trashed book without
// expanding their ids into SQL host parameters. Search is deleted first because
// its FTS5 rows are not covered by foreign-key cascades and the book subquery is
// no longer available after the authoritative rows are removed.
func PurgeAllTrashedBooks(tx *Tx) (int, error) {
	if _, err := tx.Exec(`
		DELETE FROM search
		WHERE rowid IN (SELECT id FROM books WHERE deleted_at IS NOT NULL)
	`); err != nil {
		return 0, fmt.Errorf("purge all search rows: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM books WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("purge all books: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count purged books: %w", err)
	}
	if n > 0 {
		if _, err := DeleteOrphanAuthors(tx); err != nil {
			return 0, fmt.Errorf("purge orphan authors: %w", err)
		}
	}
	return int(n), nil
}

// PurgeBook permanently deletes a trashed book and everything keyed to it:
// assets, authorship links, shelf membership and per-user reader state fall away
// through ON DELETE CASCADE; the FTS row (a virtual table, not covered by FK
// cascade) and any now-orphaned author rows are removed explicitly. It refuses a
// live book — only a trashed book can be purged — returning sql.ErrNoRows when
// no trashed book has this id. The caller captures the asset/cover file paths
// *before* calling this (the rows are gone afterward) and unlinks them after the
// transaction commits, preserving "DB first, then storage".
func PurgeBook(tx *Tx, bookID int64) error {
	n, err := PurgeBooks(tx, []int64{bookID})
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
