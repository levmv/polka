package db

import (
	"database/sql"
	"fmt"
)

type OPDSPublicationRow struct {
	ID            int64
	Title         string
	Description   sql.NullString
	Tags          sql.NullString
	Publisher     sql.NullString
	PublishedDate sql.NullString
	Language      sql.NullString
	Identifiers   sql.NullString
	CoverVersion  int
	UpdatedAt     int64
}

const opdsPublicationColumns = `
	b.id, b.title,
	b.description, b.tags, b.publisher, b.published_date,
	b.language, b.identifiers, b.cover_version, b.updated_at`

// ListOPDSPublications is the narrow read model for OPDS acquisition feeds. It
// only returns live books that have at least one asset, because an OPDS
// publication entry without an acquisition link is not useful to external
// readers.
func ListOPDSPublications(queryer Queryer, scope VisibilityScope, limit, offset int) ([]OPDSPublicationRow, error) {
	withSQL, fromSQL, args := scope.joinVisibleBooks("books b")
	args = append(args, limit, offset)
	rows, err := queryer.Query(fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE b.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM assets a WHERE a.book_id = b.id)
		ORDER BY b.sort_title COLLATE NOCASE ASC, b.title COLLATE NOCASE ASC, b.id ASC
		LIMIT ? OFFSET ?
	`, withClause(withSQL), opdsPublicationColumns, fromSQL), args...)
	if err != nil {
		return nil, fmt.Errorf("list opds publications query: %w", err)
	}
	defer rows.Close()
	return scanOPDSPublications(rows)
}

// SearchOPDSPublications applies the same FTS terms and structural/per-user
// filters as browser search. Filter-only queries (including status:unread) are
// valid saved shelves; a genuinely empty query still returns no rows.
func SearchOPDSPublications(queryer Queryer, scope VisibilityScope, userID int64, q string, limit, offset int) ([]OPDSPublicationRow, error) {
	plan := newBookSearchPlan(scope, userID, q)
	if !plan.hasClauses {
		return nil, nil
	}
	orderBy := "b.sort_title COLLATE NOCASE ASC, b.title COLLATE NOCASE ASC, b.id ASC"
	if plan.hasRank {
		orderBy = "rank, b.id ASC"
	}
	rows, err := queryer.Query(fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE %s
		  AND EXISTS (SELECT 1 FROM assets a WHERE a.book_id = b.id)
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, withClause(plan.withSQL), opdsPublicationColumns, plan.fromSQL, plan.whereSQL,
		orderBy), plan.argsWith(limit, offset)...)
	if err != nil {
		return nil, fmt.Errorf("search opds publications query: %w", err)
	}
	defer rows.Close()
	return scanOPDSPublications(rows)
}

// ListRecentOPDSPublications is ListOPDSPublications ordered newest-first, for the
// "Recently added" navigation entry.
func ListRecentOPDSPublications(queryer Queryer, scope VisibilityScope, limit, offset int) ([]OPDSPublicationRow, error) {
	withSQL, fromSQL, args := scope.joinVisibleBooks("books b")
	args = append(args, limit, offset)
	rows, err := queryer.Query(fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE b.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM assets a WHERE a.book_id = b.id)
		-- Increasing IDs preserve insertion order within SQLite's one-second
		-- added_at bucket, including fast batch imports.
		ORDER BY b.added_at DESC, b.id DESC
		LIMIT ? OFFSET ?
	`, withClause(withSQL), opdsPublicationColumns, fromSQL), args...)
	if err != nil {
		return nil, fmt.Errorf("list recent opds publications query: %w", err)
	}
	defer rows.Close()
	return scanOPDSPublications(rows)
}

// ListManualShelfOPDSPublications returns the downloadable books on one manual
// shelf in its explicit shelf order. The caller owns shelf visibility; this
// query independently intersects the books with the user's content scope so a
// personal shelf can never widen a shelf-scoped reader's library.
func ListManualShelfOPDSPublications(queryer Queryer, scope VisibilityScope, shelfID string, limit, offset int) ([]OPDSPublicationRow, error) {
	withSQL, fromSQL, args := scope.joinVisibleBooks("shelf_books sb JOIN books b ON b.id = sb.book_id")
	args = append(args, shelfID, limit, offset)
	rows, err := queryer.Query(fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE sb.shelf_id = ?
		  AND b.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM assets a WHERE a.book_id = b.id)
		ORDER BY sb.position ASC, sb.added_at DESC, b.added_at DESC, b.id ASC
		LIMIT ? OFFSET ?
	`, withClause(withSQL), opdsPublicationColumns, fromSQL), args...)
	if err != nil {
		return nil, fmt.Errorf("list manual shelf opds publications query: %w", err)
	}
	defer rows.Close()
	return scanOPDSPublications(rows)
}

func CountManualShelfOPDSPublications(queryer Queryer, scope VisibilityScope, shelfID string) (int, error) {
	withSQL, fromSQL, args := scope.joinVisibleBooks("shelf_books sb JOIN books b ON b.id = sb.book_id")
	args = append(args, shelfID)
	var count int
	err := queryer.QueryRow(fmt.Sprintf(`
		%s
		SELECT COUNT(*)
		FROM %s
		WHERE sb.shelf_id = ?
		  AND b.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM assets a WHERE a.book_id = b.id)
	`, withClause(withSQL), fromSQL), args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count manual shelf opds publications query: %w", err)
	}
	return count, nil
}

func scanOPDSPublications(rows *sql.Rows) ([]OPDSPublicationRow, error) {
	var pubs []OPDSPublicationRow
	for rows.Next() {
		var p OPDSPublicationRow
		if err := rows.Scan(
			&p.ID, &p.Title, &p.Description, &p.Tags,
			&p.Publisher, &p.PublishedDate, &p.Language, &p.Identifiers,
			&p.CoverVersion, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan opds publication: %w", err)
		}
		pubs = append(pubs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("opds publications rows: %w", err)
	}
	return pubs, nil
}

func CountOPDSPublications(queryer Queryer, scope VisibilityScope) (int, error) {
	withSQL, fromSQL, args := scope.joinVisibleBooks("books b")
	var count int
	err := queryer.QueryRow(fmt.Sprintf(`
		%s
		SELECT COUNT(*)
		FROM %s
		WHERE b.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM assets a WHERE a.book_id = b.id)
	`, withClause(withSQL), fromSQL), args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count opds publications query: %w", err)
	}
	return count, nil
}

func CountSearchOPDSPublications(queryer Queryer, scope VisibilityScope, userID int64, q string) (int, error) {
	plan := newBookSearchPlan(scope, userID, q)
	if !plan.hasClauses {
		return 0, nil
	}
	var count int
	err := queryer.QueryRow(fmt.Sprintf(`
		%s
		SELECT COUNT(*)
		FROM %s
		WHERE %s
		  AND EXISTS (SELECT 1 FROM assets a WHERE a.book_id = b.id)
	`, withClause(plan.withSQL), plan.fromSQL, plan.whereSQL), plan.args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count search opds publications query: %w", err)
	}
	return count, nil
}
