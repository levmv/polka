package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BookSummaryRow is the list/cleanup projection — the columns every books
// listing needs (scanBookSummary reads exactly these). BookDetailRow extends it
// with the detail-only fields the single-book view loads, so a list query can't
// hand back fields it never selected.
type BookSummaryRow struct {
	ID           string
	Title        string
	Series       sql.NullString
	SeriesIndex  sql.NullFloat64
	Tags         sql.NullString
	CoverVersion int
	Date         sql.NullString
}

type BookDetailRow struct {
	BookSummaryRow
	SortTitle   string
	Description sql.NullString
	Language    sql.NullString
	Publisher   sql.NullString
	Identifiers sql.NullString
	AddedAt     int64
	UpdatedAt   int64
}

type BookSort string

const (
	SortAdded     BookSort = "added"
	SortTitle     BookSort = "title"
	SortAuthor    BookSort = "author"
	SortYear      BookSort = "year"
	SortSeries    BookSort = "series"
	SortRelevance BookSort = "relevance"
)

const (
	// bookSummaryColumns is the SELECT column list consumed by scanBookSummary.
	// Authors deliberately are not flattened into this common projection:
	// display callers batch-load the ordered work_authors rows.
	bookSummaryColumns = `w.id, w.title, w.series, w.series_index, w.tags, w.cover_version,
		w.published_date`

	// colAuthors is retained for specialized flat projections such as FTS and
	// delivery. UI-facing book rows load ordered authors from work_authors.
	colAuthors = `COALESCE((SELECT group_concat(name, ', ') FROM (
		SELECT a.name FROM work_authors wa
		JOIN authors a ON wa.author_id = a.id
		WHERE wa.work_id = w.id
		ORDER BY wa.author_order ASC, wa.rowid ASC)), '') AS authors`

	// subPrimaryAuthorName is a scalar subquery for the name of a work's primary
	// (lowest author_order) author, for duplicate / unknown-author detection.
	subPrimaryAuthorName = `(SELECT a.name FROM work_authors wa
		JOIN authors a ON wa.author_id = a.id
		WHERE wa.work_id = w.id
		ORDER BY wa.author_order ASC, wa.rowid ASC LIMIT 1)`

	noCoverCondition       = `w.cover_version <= 0`
	noTagsCondition        = `w.tags IS NULL OR w.tags = ''`
	noDescriptionCondition = `w.description IS NULL OR w.description = ''`
	noAuthorCondition      = subPrimaryAuthorName + ` = 'Unknown Author'`
	noSeriesCondition      = `w.series IS NULL OR TRIM(w.series) = ''`
)

// scanBookSummary scans one row produced by bookSummaryColumns.
func scanBookSummary(rows *sql.Rows) (BookSummaryRow, error) {
	var b BookSummaryRow
	err := rows.Scan(&b.ID, &b.Title, &b.Series, &b.SeriesIndex,
		&b.Tags, &b.CoverVersion, &b.Date)
	return b, err
}

func bookOrderBy(sort BookSort, hasRank bool) string {
	orderBy := "w.added_at DESC"
	switch sort {
	case SortTitle:
		orderBy = "w.sort_title COLLATE NOCASE ASC, w.title COLLATE NOCASE ASC"
	case SortAuthor:
		orderBy = "w.primary_author_sort ASC, w.sort_title COLLATE NOCASE ASC, w.title COLLATE NOCASE ASC"
	case SortYear:
		orderBy = "w.published_date DESC NULLS LAST, w.added_at DESC"
	case SortSeries:
		orderBy = seriesMissingLast + ", w.series COLLATE NOCASE ASC, " + seriesVolumeOrderBy
	case SortRelevance:
		if hasRank {
			orderBy = "rank"
		}
	}
	return orderBy
}

func stableBookOrderBy(sort BookSort, hasRank bool) string {
	return bookOrderBy(sort, hasRank) + ", w.id ASC"
}

const maxBookJumpBuckets = 128

type BookJump struct {
	Label  string
	Offset int
}

// ListBookJumps streams the indexed sort key so each offset exactly matches the
// list order without retaining the catalog in memory. An adversarial catalog
// with too many distinct Unicode labels hides the affordance instead of
// producing an unbounded response.
func ListBookJumps(queryer Queryer, scope VisibilityScope, sort BookSort) ([]BookJump, int, error) {
	var valueExpr string
	switch sort {
	case SortTitle:
		valueExpr = "w.sort_title"
	case SortAuthor:
		valueExpr = "w.primary_author_sort"
	default:
		return nil, 0, fmt.Errorf("book jumps require title or author sort")
	}

	withSQL, fromSQL, args := scope.joinVisibleWorks("works w")
	query := fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE w.deleted_at IS NULL
		ORDER BY %s
	`, withClause(withSQL), valueExpr, fromSQL, stableBookOrderBy(sort, false))

	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list book jumps query: %w", err)
	}
	defer rows.Close()

	var (
		out       []BookJump
		total     int
		tooMany   bool
		seenLabel = make(map[string]bool)
	)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, 0, fmt.Errorf("scan book jump: %w", err)
		}
		offset := total
		total++
		if tooMany {
			continue
		}
		label := bookJumpLabel(value)
		if seenLabel[label] {
			continue
		}
		if len(out) == maxBookJumpBuckets {
			tooMany = true
			continue
		}
		seenLabel[label] = true
		out = append(out, BookJump{Label: label, Offset: offset})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list book jumps rows: %w", err)
	}
	if tooMany {
		return nil, total, nil
	}
	return out, total, nil
}

func bookJumpLabel(sortValue string) string {
	r, _ := utf8.DecodeRuneInString(sortValue)
	switch {
	case r == utf8.RuneError || r == 0:
		return "#"
	case unicode.IsLetter(r):
		return string(unicode.ToUpper(r))
	case unicode.IsDigit(r):
		return "0–9"
	default:
		return "#"
	}
}

func manualShelfOrderBy(sort BookSort) string {
	switch sort {
	case SortTitle:
		return "w.sort_title COLLATE NOCASE ASC, w.title COLLATE NOCASE ASC"
	case SortAuthor:
		return "w.primary_author_sort ASC, w.sort_title COLLATE NOCASE ASC, w.title COLLATE NOCASE ASC"
	case SortYear:
		return "w.published_date DESC NULLS LAST, w.added_at DESC"
	default:
		return "sb.position ASC, sb.added_at DESC, w.added_at DESC"
	}
}

func stableManualShelfOrderBy(sort BookSort) string {
	return manualShelfOrderBy(sort) + ", w.id ASC"
}

const (
	seriesOrderGroup = "CASE WHEN w.series_index IS NOT NULL AND w.series_index > 0 THEN 0 ELSE 1 END"
	seriesOrderIndex = "CASE WHEN w.series_index IS NOT NULL AND w.series_index > 0 THEN w.series_index ELSE 0 END"
	// Volume order inside one series: numbered volumes first in index order,
	// then the unnumbered ones by title.
	seriesVolumeOrderBy = seriesOrderGroup + " ASC, " + seriesOrderIndex + " ASC, w.title COLLATE NOCASE ASC"
	seriesOrderBy       = seriesVolumeOrderBy + ", w.id ASC"
	// Series-less works sort after every named series rather than clumping at
	// the front on an empty string.
	seriesMissingLast = "CASE WHEN w.series IS NULL OR TRIM(w.series) = '' THEN 1 ELSE 0 END ASC"
)

type BookSequenceItem struct {
	ID    string
	Title string
}

type BookSequenceWindow struct {
	Items        []BookSequenceItem
	CurrentIndex int
	Total        int
}

func BookSequenceInList(queryer Queryer, scope VisibilityScope, userID, workID, q string, sort BookSort, before, after int) (BookSequenceWindow, error) {
	plan := newBookSearchPlan(scope, userID, q)
	return queryBookSequenceWindow(
		queryer,
		workID,
		plan.withSQL,
		plan.fromSQL,
		plan.whereSQL,
		stableBookOrderBy(sort, plan.hasRank),
		before,
		after,
		plan.args...,
	)
}

func BookSequenceInManualShelf(queryer Queryer, scope VisibilityScope, workID, shelfID string, sort BookSort, before, after int) (BookSequenceWindow, error) {
	withSQL, fromSQL, args := scope.joinVisibleWorks("shelf_books sb JOIN works w ON w.id = sb.work_id")
	args = append(args, shelfID)
	return queryBookSequenceWindow(
		queryer,
		workID,
		withSQL,
		fromSQL,
		"sb.shelf_id = ? AND w.deleted_at IS NULL",
		stableManualShelfOrderBy(sort),
		before,
		after,
		args...,
	)
}

func queryBookSequenceWindow(queryer Queryer, workID, withSQL, fromSQL, whereSQL, orderBy string, before, after int, args ...any) (BookSequenceWindow, error) {
	withPrefix := "WITH "
	if strings.TrimSpace(withSQL) != "" {
		withPrefix += withSQL + ","
	}
	query := fmt.Sprintf(`
		%s
		ordered AS (
			SELECT
				w.id,
				w.title,
				ROW_NUMBER() OVER (ORDER BY %s) AS rn,
				COUNT(*) OVER () AS total
			FROM %s
			WHERE %s
		),
		current AS (
			SELECT rn
			FROM ordered
			WHERE id = ?
		)
		SELECT ordered.id, ordered.title, ordered.rn, ordered.total
		FROM ordered, current
		WHERE ordered.rn BETWEEN current.rn - ? AND current.rn + ?
		ORDER BY ordered.rn
	`, withPrefix, orderBy, fromSQL, whereSQL)

	args = append(args, workID, before, after)
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return BookSequenceWindow{}, fmt.Errorf("book sequence query: %w", err)
	}
	defer rows.Close()

	window := BookSequenceWindow{CurrentIndex: -1}
	for rows.Next() {
		var item BookSequenceItem
		var rn int
		if err := rows.Scan(&item.ID, &item.Title, &rn, &window.Total); err != nil {
			return BookSequenceWindow{}, fmt.Errorf("scan book sequence: %w", err)
		}
		if item.ID == workID {
			window.CurrentIndex = len(window.Items)
		}
		window.Items = append(window.Items, item)
	}
	if err := rows.Err(); err != nil {
		return BookSequenceWindow{}, fmt.Errorf("book sequence rows: %w", err)
	}
	return window, nil
}

func ListBooks(queryer Queryer, scope VisibilityScope, userID, q string, sort BookSort, limit, offset int) ([]BookSummaryRow, error) {
	plan := newBookSearchPlan(scope, userID, q)
	rows, err := queryer.Query(fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE %s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, withClause(plan.withSQL), bookSummaryColumns, plan.fromSQL,
		plan.whereSQL, stableBookOrderBy(sort, plan.hasRank)), plan.argsWith(limit, offset)...)
	if err != nil {
		return nil, fmt.Errorf("list books query: %w", err)
	}
	defer rows.Close()

	var books []BookSummaryRow
	for rows.Next() {
		b, err := scanBookSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("list books scan: %w", err)
		}
		books = append(books, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list books rows: %w", err)
	}
	return books, nil
}

func ListBooksInManualShelf(queryer Queryer, scope VisibilityScope, shelfID string, sort BookSort, limit, offset int) ([]BookSummaryRow, error) {
	withSQL, fromSQL, args := scope.joinVisibleWorks("shelf_books sb JOIN works w ON w.id = sb.work_id")
	args = append(args, shelfID, limit, offset)
	queryStr := fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE sb.shelf_id = ? AND w.deleted_at IS NULL
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, withClause(withSQL), bookSummaryColumns, fromSQL, stableManualShelfOrderBy(sort))

	rows, err := queryer.Query(queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("list shelf books query: %w", err)
	}
	defer rows.Close()

	var books []BookSummaryRow
	for rows.Next() {
		b, err := scanBookSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("list shelf books scan: %w", err)
		}
		books = append(books, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list shelf books rows: %w", err)
	}
	return books, nil
}

func GetBook(queryer Queryer, scope VisibilityScope, workID string) (BookDetailRow, error) {
	where, args := scope.AppendWorkWhere("w.id = ? AND w.deleted_at IS NULL", "w.id", workID)
	row := queryer.QueryRow(fmt.Sprintf(`
		SELECT w.id, w.title, w.series, w.series_index, w.tags, w.cover_version,
		       w.sort_title, w.description, w.language, w.publisher, w.published_date, w.identifiers,
		       w.added_at, w.updated_at
		FROM works w
		WHERE %s
	`, where), args...)

	var b BookDetailRow
	err := row.Scan(&b.ID, &b.Title, &b.Series, &b.SeriesIndex, &b.Tags, &b.CoverVersion, &b.SortTitle, &b.Description, &b.Language, &b.Publisher, &b.Date, &b.Identifiers, &b.AddedAt, &b.UpdatedAt)
	if err != nil {
		return b, err
	}
	return b, nil
}

// PlaceholderCoverText returns the title and primary-author name for a work,
// the inputs for a generated fallback cover. found is false when the work does
// not exist (so the cover handler can keep returning 404 for unknown IDs).
func (db *DB) PlaceholderCoverText(workID string) (title, author string, found bool, err error) {
	var a sql.NullString
	err = db.QueryRow(
		`SELECT w.title, `+subPrimaryAuthorName+` FROM works w WHERE w.id = ?`, workID,
	).Scan(&title, &a)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return title, a.String, true, nil
}
