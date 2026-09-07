package db

import "fmt"

type SeriesCount struct {
	Name      string
	BookCount int
}

// ListSeriesCountsPage returns one stable keyset page ordered by series
// name. q is an optional substring filter used by the edit autocomplete; the
// Series page passes it empty. afterName is the opaque cursor's decoded key.
// limit is the positive page size.
func ListSeriesCountsPage(queryer Queryer, scope VisibilityScope, q, afterName string, limit int) ([]SeriesCount, error) {
	where := "b.deleted_at IS NULL AND b.series IS NOT NULL AND TRIM(b.series) <> ''"
	var args []any
	if q != "" {
		where += ` AND b.series LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if afterName != "" {
		where += ` AND (
			b.series COLLATE NOCASE > ? COLLATE NOCASE OR
			(b.series COLLATE NOCASE = ? COLLATE NOCASE AND b.series COLLATE BINARY > ? COLLATE BINARY)
		)`
		args = append(args, afterName, afterName, afterName)
	}
	where, args = scope.AppendBookWhere(where, "b.id", args...)
	args = append(args, limit)
	rows, err := queryer.Query(`
		SELECT b.series, COUNT(*) AS book_count
		FROM books b
		WHERE `+where+`
		GROUP BY b.series
		ORDER BY b.series COLLATE NOCASE ASC, b.series COLLATE BINARY ASC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list series with counts query: %w", err)
	}
	defer rows.Close()

	var series []SeriesCount
	for rows.Next() {
		var s SeriesCount
		if err := rows.Scan(&s.Name, &s.BookCount); err != nil {
			return nil, fmt.Errorf("list series with counts scan: %w", err)
		}
		series = append(series, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if series == nil {
		series = []SeriesCount{}
	}
	return series, nil
}

// SeriesCard is one tile on the Series page: the series, how many books it
// holds, how many of them the viewer finished, and the book that stands for the
// whole series with its cover and author.
type SeriesCard struct {
	Name          string
	Author        string
	BookCount     int
	FinishedCount int
	CoverBookID   int64
	CoverVersion  int
}

// ListSeriesCardsPage returns one keyset page of Series-page tiles. Names and
// counts come from the same keyset walk as ListSeriesCountsPage; a second
// query then enriches only that page with its cover book and finished count, so
// the per-request work stays proportional to the page instead of the library.
// limit is the positive page size.
func ListSeriesCardsPage(queryer Queryer, scope VisibilityScope, userID int64, q, afterName string, limit int) ([]SeriesCard, error) {
	counts, err := ListSeriesCountsPage(queryer, scope, q, afterName, limit)
	if err != nil {
		return nil, err
	}
	cards := make([]SeriesCard, 0, len(counts))
	if len(counts) == 0 {
		return cards, nil
	}

	names := make([]string, 0, len(counts))
	for _, c := range counts {
		names = append(names, c.Name)
	}
	details, err := seriesCardDetails(queryer, scope, userID, names)
	if err != nil {
		return nil, err
	}
	for _, c := range counts {
		card := details[c.Name]
		card.Name = c.Name
		card.BookCount = c.BookCount
		cards = append(cards, card)
	}
	return cards, nil
}

// seriesCardDetails picks the representative volume and counts the viewer's
// finished books for each named series. The representative is the first volume
// in series order that actually has a cover, so a missing cover on book one does
// not leave the whole series blank; its primary author names the series.
func seriesCardDetails(queryer Queryer, scope VisibilityScope, userID int64, names []string) (map[string]SeriesCard, error) {
	withSQL, fromSQL, args := scope.joinVisibleBooks("books b")
	args = append(args, userID)
	placeholders, nameArgs := idPlaceholders(names)
	args = append(args, nameArgs...)

	if withSQL != "" {
		withSQL += ",\n"
	}
	withSQL += `
		series_ranked AS (
			SELECT
				b.series AS name,
				b.id AS book_id,
				b.cover_version AS cover_version,
				COALESCE(` + subPrimaryAuthorName + `, '') AS author,
				ROW_NUMBER() OVER (
					PARTITION BY b.series
					ORDER BY CASE WHEN b.cover_version > 0 THEN 0 ELSE 1 END ASC, ` + seriesOrderBy + `
				) AS rank_in_series,
				SUM(CASE WHEN reading.status = 'finished' THEN 1 ELSE 0 END) OVER (
					PARTITION BY b.series
				) AS finished_count
			FROM ` + fromSQL + `
			LEFT JOIN user_book_reading_state reading
				ON reading.book_id = b.id AND reading.user_id = ?
			WHERE b.deleted_at IS NULL AND b.series IN (` + placeholders + `)
		)`

	rows, err := queryer.Query(`
		WITH `+withSQL+`
		SELECT name, book_id, cover_version, author, finished_count
		FROM series_ranked
		WHERE rank_in_series = 1
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("series card details query: %w", err)
	}
	defer rows.Close()

	details := make(map[string]SeriesCard, len(names))
	for rows.Next() {
		var card SeriesCard
		if err := rows.Scan(&card.Name, &card.CoverBookID, &card.CoverVersion, &card.Author, &card.FinishedCount); err != nil {
			return nil, fmt.Errorf("series card details scan: %w", err)
		}
		details[card.Name] = card
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return details, nil
}
