package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// SoftDeleteWork marks a live work as trashed: it drops out of every normal
// projection (browse / search / cleanup / shelves — all filter
// deleted_at IS NULL) while its row and files stay on disk until an admin purge.
// deletedBy records who trashed it, for a legible "Deleted by X — Restore?"
// trash view. Returns sql.ErrNoRows when no *live* work has this id (unknown id
// or already trashed), so the handler can answer 404 / no-op uniformly.
func SoftDeleteWork(execer Execer, workID, deletedBy string) error {
	res, err := execer.Exec(`
		UPDATE works SET deleted_at = unixepoch(), deleted_by = ?
		WHERE id = ? AND deleted_at IS NULL
	`, deletedBy, workID)
	if err != nil {
		return fmt.Errorf("soft delete work: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RestoreWork clears the trash flags, returning the work to the live catalog.
// Returns sql.ErrNoRows when no *trashed* work has this id.
func RestoreWork(execer Execer, workID string) error {
	res, err := execer.Exec(`
		UPDATE works SET deleted_at = NULL, deleted_by = NULL
		WHERE id = ? AND deleted_at IS NOT NULL
	`, workID)
	if err != nil {
		return fmt.Errorf("restore work: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// TrashedWorkRow is a trashed work for the trash listing: the same summary
// fields the library grid renders, plus when it was trashed and the display name
// of whoever trashed it (empty if that user has since been removed).
type TrashedWorkRow struct {
	BookSummaryRow
	DeletedAt     int64
	DeletedByName string
}

// ListTrashedWorks returns the soft-deleted works, most-recently-trashed first.
func ListTrashedWorks(queryer Queryer, scope VisibilityScope) ([]TrashedWorkRow, error) {
	where, args := scope.AppendWorkWhere("w.deleted_at IS NOT NULL", "w.id")
	rows, err := queryer.Query(fmt.Sprintf(`
		SELECT %s, w.deleted_at, COALESCE(u.username, '')
		FROM works w
		LEFT JOIN users u ON u.id = w.deleted_by
		WHERE %s
		ORDER BY w.deleted_at DESC
	`, bookSummaryColumns, where), args...)
	if err != nil {
		return nil, fmt.Errorf("list trashed works query: %w", err)
	}
	defer rows.Close()

	var works []TrashedWorkRow
	for rows.Next() {
		var t TrashedWorkRow
		if err := rows.Scan(&t.ID, &t.Title, &t.Series, &t.SeriesIndex,
			&t.Tags, &t.CoverVersion, &t.Date,
			&t.DeletedAt, &t.DeletedByName); err != nil {
			return nil, fmt.Errorf("list trashed works scan: %w", err)
		}
		works = append(works, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list trashed works rows: %w", err)
	}
	return works, nil
}

// ListTrashedWorkIDs returns just the ids of trashed works. With no workIDs it
// selects the whole trash; with ids it filters to the trashed subset of that
// explicit selection. Purge uses this inside its deletion transaction so a
// concurrent restore cannot change the selected set between inspection and
// commit.
func ListTrashedWorkIDs(queryer Queryer, workIDs ...string) ([]string, error) {
	query := `SELECT id FROM works WHERE deleted_at IS NOT NULL`
	args := make([]any, len(workIDs))
	if len(workIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(workIDs)), ",")
		query += ` AND id IN (` + placeholders + `)`
		for i, id := range workIDs {
			args[i] = id
		}
	}
	query += ` ORDER BY id`
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list trashed work ids query: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list trashed work ids scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PurgeWorks permanently deletes the trashed subset of workIDs in one batch.
// Search rows are explicit because FTS is not covered by foreign-key cascades;
// orphan authors are swept once after the whole set, not once per work. The
// caller captures file paths before this call and unlinks only after commit.
func PurgeWorks(tx *sql.Tx, workIDs []string) (int, error) {
	if len(workIDs) == 0 {
		return 0, nil
	}
	trashedIDs, err := ListTrashedWorkIDs(tx, workIDs...)
	if err != nil {
		return 0, err
	}
	if len(trashedIDs) == 0 {
		return 0, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(trashedIDs)), ",")
	args := make([]any, len(trashedIDs))
	for i, id := range trashedIDs {
		args[i] = id
	}
	if _, err := tx.Exec(`DELETE FROM works WHERE deleted_at IS NOT NULL AND id IN (`+placeholders+`)`, args...); err != nil {
		return 0, fmt.Errorf("purge works: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM search WHERE work_id IN (`+placeholders+`)`, args...); err != nil {
		return 0, fmt.Errorf("purge search rows: %w", err)
	}
	if _, err := DeleteOrphanAuthors(tx); err != nil {
		return 0, fmt.Errorf("purge orphan authors: %w", err)
	}
	return len(trashedIDs), nil
}

// PurgeAllTrashedWorks permanently deletes every trashed work without
// expanding their ids into SQL host parameters. Search is deleted first because
// its FTS5 rows are not covered by foreign-key cascades and the work subquery is
// no longer available after the authoritative rows are removed.
func PurgeAllTrashedWorks(tx *sql.Tx) (int, error) {
	if _, err := tx.Exec(`
		DELETE FROM search
		WHERE work_id IN (SELECT id FROM works WHERE deleted_at IS NOT NULL)
	`); err != nil {
		return 0, fmt.Errorf("purge all search rows: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM works WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("purge all works: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count purged works: %w", err)
	}
	if n > 0 {
		if _, err := DeleteOrphanAuthors(tx); err != nil {
			return 0, fmt.Errorf("purge orphan authors: %w", err)
		}
	}
	return int(n), nil
}

// PurgeWork permanently deletes a trashed work and everything keyed to it:
// assets, authorship links, shelf membership and per-user reader state fall away
// through ON DELETE CASCADE; the FTS row (a virtual table, not covered by FK
// cascade) and any now-orphaned author rows are removed explicitly. It refuses a
// live work — only a trashed work can be purged — returning sql.ErrNoRows when
// no trashed work has this id. The caller captures the asset/cover file paths
// *before* calling this (the rows are gone afterward) and unlinks them after the
// transaction commits, preserving "DB first, then storage".
func PurgeWork(tx *sql.Tx, workID string) error {
	n, err := PurgeWorks(tx, []string{workID})
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
