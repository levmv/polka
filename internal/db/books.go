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
	ID           int64
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
	// display callers batch-load the ordered book_authors rows.
	bookSummaryColumns = `b.id, b.title, b.series, b.series_index, b.tags, b.cover_version,
		b.published_date`

	// colAuthors flattens author names for FTS and delivery. UI-facing book rows
	// load ordered authors from book_authors.
	colAuthors = `COALESCE((SELECT group_concat(name, ', ') FROM (
		SELECT a.name FROM book_authors ba
		JOIN authors a ON ba.author_id = a.id
		WHERE ba.book_id = b.id
		ORDER BY ba.author_order ASC, ba.rowid ASC)), '') AS authors`

	// subPrimaryAuthorName is a scalar subquery for the name of a book's primary
	// (lowest author_order) author, for duplicate detection.
	subPrimaryAuthorName = `(SELECT a.name FROM book_authors ba
		JOIN authors a ON ba.author_id = a.id
		WHERE ba.book_id = b.id
		ORDER BY ba.author_order ASC, ba.rowid ASC LIMIT 1)`

	noCoverCondition       = `b.cover_version <= 0`
	noTagsCondition        = `b.tags IS NULL OR b.tags = ''`
	noDescriptionCondition = `b.description IS NULL OR b.description = ''`
	noAuthorCondition      = `NOT EXISTS (SELECT 1 FROM book_authors ba WHERE ba.book_id = b.id)`
	noSeriesCondition      = `b.series IS NULL OR TRIM(b.series) = ''`
)

// scanBookSummary scans one row produced by bookSummaryColumns.
func scanBookSummary(rows *sql.Rows) (BookSummaryRow, error) {
	var b BookSummaryRow
	err := rows.Scan(&b.ID, &b.Title, &b.Series, &b.SeriesIndex,
		&b.Tags, &b.CoverVersion, &b.Date)
	return b, err
}

func bookOrderBy(sort BookSort, hasRank bool) string {
	orderBy := "b.added_at DESC"
	switch sort {
	case SortTitle:
		orderBy = "b.sort_title COLLATE NOCASE ASC, b.title COLLATE NOCASE ASC"
	case SortAuthor:
		orderBy = "b.primary_author_sort ASC, b.sort_title COLLATE NOCASE ASC, b.title COLLATE NOCASE ASC"
	case SortYear:
		orderBy = "b.published_date DESC NULLS LAST, b.added_at DESC"
	case SortSeries:
		orderBy = seriesMissingLast + ", b.series COLLATE NOCASE ASC, " + seriesVolumeOrderBy
	case SortRelevance:
		if hasRank {
			orderBy = "rank"
		}
	}
	return orderBy
}

func stableBookOrderBy(sort BookSort, hasRank bool) string {
	return bookOrderBy(sort, hasRank) + ", b.id ASC"
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
		valueExpr = "b.sort_title"
	case SortAuthor:
		valueExpr = "b.primary_author_sort"
	default:
		return nil, 0, fmt.Errorf("book jumps require title or author sort")
	}

	withSQL, fromSQL, args := scope.joinVisibleBooks("books b")
	query := fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE b.deleted_at IS NULL
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
	case SortTitle, SortAuthor, SortYear, SortSeries:
		return bookOrderBy(sort, false)
	default:
		return "sb.position ASC, sb.added_at DESC, b.added_at DESC"
	}
}

func stableManualShelfOrderBy(sort BookSort) string {
	return manualShelfOrderBy(sort) + ", b.id ASC"
}

const (
	seriesOrderGroup = "CASE WHEN b.series_index IS NOT NULL AND b.series_index > 0 THEN 0 ELSE 1 END"
	seriesOrderIndex = "CASE WHEN b.series_index IS NOT NULL AND b.series_index > 0 THEN b.series_index ELSE 0 END"
	// Volume order inside one series: numbered volumes first in index order,
	// then the unnumbered ones by title.
	seriesVolumeOrderBy = seriesOrderGroup + " ASC, " + seriesOrderIndex + " ASC, b.title COLLATE NOCASE ASC"
	seriesOrderBy       = seriesVolumeOrderBy + ", b.id ASC"
	// Series-less books sort after every named series rather than clumping at
	// the front on an empty string.
	seriesMissingLast = "CASE WHEN b.series IS NULL OR TRIM(b.series) = '' THEN 1 ELSE 0 END ASC"
)

type BookSequenceItem struct {
	ID    int64
	Title string
}

type BookSequenceWindow struct {
	Items        []BookSequenceItem
	CurrentIndex int
	Total        int
}

func BookSequenceInList(queryer Queryer, scope VisibilityScope, userID int64, bookID int64, q string, sort BookSort, before, after int) (BookSequenceWindow, error) {
	plan := newBookSearchPlan(scope, userID, q)
	return queryBookSequenceWindow(
		queryer,
		bookID,
		plan.withSQL,
		plan.fromSQL,
		plan.whereSQL,
		stableBookOrderBy(sort, plan.hasRank),
		before,
		after,
		plan.args...,
	)
}

func BookSequenceInManualShelf(queryer Queryer, scope VisibilityScope, bookID int64, shelfID int64, sort BookSort, before, after int) (BookSequenceWindow, error) {
	withSQL, fromSQL, args := scope.joinVisibleBooks("shelf_books sb JOIN books b ON b.id = sb.book_id")
	args = append(args, shelfID)
	return queryBookSequenceWindow(
		queryer,
		bookID,
		withSQL,
		fromSQL,
		"sb.shelf_id = ? AND b.deleted_at IS NULL",
		stableManualShelfOrderBy(sort),
		before,
		after,
		args...,
	)
}

func queryBookSequenceWindow(queryer Queryer, bookID int64, withSQL string, fromSQL string, whereSQL string, orderBy string, before, after int, args ...any) (BookSequenceWindow, error) {
	withPrefix := "WITH "
	if strings.TrimSpace(withSQL) != "" {
		withPrefix += withSQL + ","
	}
	query := fmt.Sprintf(`
		%s
		ordered AS (
			SELECT
				b.id,
				b.title,
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

	args = append(args, bookID, before, after)
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
		if item.ID == bookID {
			window.CurrentIndex = len(window.Items)
		}
		window.Items = append(window.Items, item)
	}
	if err := rows.Err(); err != nil {
		return BookSequenceWindow{}, fmt.Errorf("book sequence rows: %w", err)
	}
	return window, nil
}

func ListBooks(queryer Queryer, scope VisibilityScope, userID int64, q string, sort BookSort, limit, offset int) ([]BookSummaryRow, error) {
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

func ListBooksInManualShelf(queryer Queryer, scope VisibilityScope, shelfID int64, sort BookSort, limit, offset int) ([]BookSummaryRow, error) {
	withSQL, fromSQL, args := scope.joinVisibleBooks("shelf_books sb JOIN books b ON b.id = sb.book_id")
	args = append(args, shelfID, limit, offset)
	queryStr := fmt.Sprintf(`
		%s
		SELECT %s
		FROM %s
		WHERE sb.shelf_id = ? AND b.deleted_at IS NULL
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

func GetBook(queryer Queryer, scope VisibilityScope, bookID int64) (BookDetailRow, error) {
	where, args := scope.AppendBookWhere("b.id = ? AND b.deleted_at IS NULL", "b.id", bookID)
	row := queryer.QueryRow(fmt.Sprintf(`
		SELECT b.id, b.title, b.series, b.series_index, b.tags, b.cover_version,
		       b.sort_title, b.description, b.language, b.publisher, b.published_date, b.identifiers,
		       b.added_at, b.updated_at
		FROM books b
		WHERE %s
	`, where), args...)

	var b BookDetailRow
	err := row.Scan(&b.ID, &b.Title, &b.Series, &b.SeriesIndex, &b.Tags, &b.CoverVersion, &b.SortTitle, &b.Description, &b.Language, &b.Publisher, &b.Date, &b.Identifiers, &b.AddedAt, &b.UpdatedAt)
	if err != nil {
		return b, err
	}
	return b, nil
}

// PlaceholderCoverText returns the title and primary-author name for a book,
// the inputs for a generated fallback cover. found is false when the book does
// not exist (so the cover handler can keep returning 404 for unknown IDs).
func PlaceholderCoverText(queryer Queryer, bookID int64) (title, author string, found bool, err error) {
	var a sql.NullString
	err = queryer.QueryRow(
		`SELECT b.title, `+subPrimaryAuthorName+` FROM books b WHERE b.id = ?`, bookID,
	).Scan(&title, &a)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return title, a.String, true, nil
}

// DedupBookIDs returns positive IDs in their original order, without duplicates.
func DedupBookIDs(in []int64) []int64 {
	seen := make(map[int64]struct{}, len(in))
	out := make([]int64, 0, len(in))
	for _, id := range in {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
