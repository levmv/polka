package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// BulkEditRow carries the mutable metadata a bulk operation reads before
// computing its transform: the current tags/series values and the manual-override
// set to merge into.
type BulkEditRow struct {
	ID          int64
	Tags        sql.NullString
	Series      sql.NullString
	SeriesIndex sql.NullFloat64
	Overrides   sql.NullString
}

// idPlaceholders builds a "?,?,?" list and the matching args for an IN clause.
func idPlaceholders[T any](ids []T) (string, []any) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return placeholders, args
}

// BooksForBulkEdit loads the current bulk-editable state for the given live books
// that are visible in scope. Books that are missing, trashed, or out of scope are
// simply omitted, so the caller can treat the returned set as the authoritative
// list of books it may mutate.
func BooksForBulkEdit(queryer Queryer, scope VisibilityScope, ids []int64) ([]BulkEditRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := idPlaceholders(ids)
	where := "b.deleted_at IS NULL AND b.id IN (" + placeholders + ")"
	where, args = scope.AppendBookWhere(where, "b.id", args...)

	rows, err := queryer.Query(`
		SELECT b.id, b.tags, b.series, b.series_index, b.manual_overrides
		FROM books b
		WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("books for bulk edit query: %w", err)
	}
	defer rows.Close()

	var out []BulkEditRow
	for rows.Next() {
		var r BulkEditRow
		if err := rows.Scan(&r.ID, &r.Tags, &r.Series, &r.SeriesIndex, &r.Overrides); err != nil {
			return nil, fmt.Errorf("books for bulk edit scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("books for bulk edit rows: %w", err)
	}
	return out, nil
}

// BookSummaryRowsByIDs returns list-projection rows for the given books visible in
// scope, so a mutation handler can hand updated summaries back to the client.
func BookSummaryRowsByIDs(queryer Queryer, scope VisibilityScope, ids []int64) ([]BookSummaryRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := idPlaceholders(ids)
	where := "b.deleted_at IS NULL AND b.id IN (" + placeholders + ")"
	where, args = scope.AppendBookWhere(where, "b.id", args...)

	rows, err := queryer.Query(`
		SELECT `+bookSummaryColumns+`
		FROM books b
		WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("book summaries by ids query: %w", err)
	}
	defer rows.Close()

	var books []BookSummaryRow
	for rows.Next() {
		b, err := scanBookSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("book summaries by ids scan: %w", err)
		}
		books = append(books, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("book summaries by ids rows: %w", err)
	}
	return books, nil
}
