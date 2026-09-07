package db

import "fmt"

type BookCoverRow struct {
	ID              int64
	CoverVersion    int
	ManualOverrides string
}

// AllBookCovers returns the persisted cover state used by library check and
// repair to validate or rebuild the derived cover files.
func AllBookCovers(queryer Queryer) ([]BookCoverRow, error) {
	rows, err := queryer.Query(`
		SELECT id, cover_version, COALESCE(manual_overrides, '')
		FROM books
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("query book covers: %w", err)
	}
	defer rows.Close()

	var books []BookCoverRow
	for rows.Next() {
		var w BookCoverRow
		if err := rows.Scan(&w.ID, &w.CoverVersion, &w.ManualOverrides); err != nil {
			return nil, fmt.Errorf("scan book covers: %w", err)
		}
		books = append(books, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows book covers: %w", err)
	}
	return books, nil
}
