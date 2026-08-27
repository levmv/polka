package db

import "fmt"

type WorkCoverRow struct {
	ID              string
	CoverVersion    int
	ManualOverrides string
}

// AllWorkCovers returns the persisted cover state used by library check and
// repair to validate or rebuild the derived cover files.
func AllWorkCovers(queryer Queryer) ([]WorkCoverRow, error) {
	rows, err := queryer.Query(`
		SELECT id, cover_version, COALESCE(manual_overrides, '')
		FROM works
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("query work covers: %w", err)
	}
	defer rows.Close()

	var works []WorkCoverRow
	for rows.Next() {
		var w WorkCoverRow
		if err := rows.Scan(&w.ID, &w.CoverVersion, &w.ManualOverrides); err != nil {
			return nil, fmt.Errorf("scan work covers: %w", err)
		}
		works = append(works, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows work covers: %w", err)
	}
	return works, nil
}
