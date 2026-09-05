package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrAssetNotFound      = errors.New("asset not found")
	ErrInvalidReaderInput = errors.New("invalid reader input")
)

type ReaderState struct {
	UserID     int64
	AssetID    string
	BookID     string
	Progress   float64
	Locator    ReaderLocator
	LastReadAt int64
	UpdatedAt  int64
}

type ContinueReadingRow struct {
	BookSummaryRow
	AssetID    string
	Progress   float64
	LastReadAt int64
}

func (db *DB) GetReaderState(userID int64, assetID string) (*ReaderState, error) {
	return getReaderState(db, userID, assetID)
}

func getReaderState(queryer Queryer, userID int64, assetID string) (*ReaderState, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	state := &ReaderState{UserID: userID, AssetID: assetID, Locator: EmptyReaderLocator()}
	var locator string
	err := queryer.QueryRow(`
		SELECT a.book_id,
		       COALESCE(s.progress, 0),
		       COALESCE(s.locator, '{}'),
		       COALESCE(s.last_read_at, 0),
		       COALESCE(s.updated_at, 0)
		FROM assets a
		LEFT JOIN user_asset_state s ON s.asset_id = a.id AND s.user_id = ?
		WHERE a.id = ?
	`, userID, assetID).Scan(&state.BookID, &state.Progress, &locator, &state.LastReadAt, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get reader state: %w", err)
	}
	normalized, err := ReaderLocatorFromString(locator)
	if err != nil {
		return nil, fmt.Errorf("get reader state locator: %w", err)
	}
	state.Locator = normalized
	return state, nil
}

func (db *DB) TouchReaderStateAndAdvanceStatus(
	ctx context.Context,
	userID int64, assetID string,
	source ReadingStatusSource,
) (*ReaderState, ReadingStatusChange, error) {
	var state *ReaderState
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = touchReaderState(tx, userID, assetID)
		if err != nil {
			return err
		}
		// Opening is an unread -> reading signal. Deliberately use zero rather
		// than the stored percentage: reopening an old last-page position after
		// "Read again" must not immediately finish the book.
		change, err = advanceReadingStatus(tx, userID, state.BookID, 0, source)
		return err
	})
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	return state, change, nil
}

func touchReaderState(tx *sql.Tx, userID int64, assetID string) (*ReaderState, error) {
	if _, err := getReaderState(tx, userID, assetID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		INSERT INTO user_asset_state (user_id, asset_id, last_read_at, updated_at)
		VALUES (?, ?, unixepoch(), unixepoch())
		ON CONFLICT(user_id, asset_id) DO UPDATE SET
			last_read_at = unixepoch(),
			updated_at = unixepoch()
	`, userID, assetID); err != nil {
		return nil, fmt.Errorf("touch reader state: %w", err)
	}
	return getReaderState(tx, userID, assetID)
}

func (db *DB) SaveReaderStateAndAdvanceStatus(
	ctx context.Context,
	userID int64, assetID string,
	progress float64,
	locator ReaderLocator,
	source ReadingStatusSource,
) (*ReaderState, ReadingStatusChange, error) {
	normalized, err := validateReaderPosition(progress, locator)
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	var state *ReaderState
	var change ReadingStatusChange
	err = db.Transact(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = saveReaderState(tx, userID, assetID, progress, normalized)
		if err != nil {
			return err
		}
		change, err = advanceReadingStatus(tx, userID, state.BookID, progress, source)
		return err
	})
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	return state, change, nil
}

func validateReaderPosition(progress float64, locator ReaderLocator) (ReaderLocator, error) {
	if progress < 0 || progress > 1 {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader progress must be between 0 and 1")
	}
	normalized, err := NewReaderLocator(locator)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func saveReaderState(tx *sql.Tx, userID int64, assetID string, progress float64, locator ReaderLocator) (*ReaderState, error) {
	if _, err := getReaderState(tx, userID, assetID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at)
		VALUES (?, ?, ?, ?, unixepoch(), unixepoch())
		ON CONFLICT(user_id, asset_id) DO UPDATE SET
			progress = excluded.progress,
			locator = excluded.locator,
			last_read_at = unixepoch(),
			updated_at = unixepoch()
	`, userID, assetID, progress, locator.String()); err != nil {
		return nil, fmt.Errorf("save reader state: %w", err)
	}
	return getReaderState(tx, userID, assetID)
}

func (db *DB) ResetReaderState(userID int64, assetID string) error {
	if _, err := db.GetReaderState(userID, assetID); err != nil {
		return err
	}
	if _, err := db.Exec(`
		DELETE FROM user_asset_state
		WHERE user_id = ? AND asset_id = ?
	`, userID, assetID); err != nil {
		return fmt.Errorf("reset reader state: %w", err)
	}
	return nil
}

func ListContinueReading(queryer Queryer, scope VisibilityScope, userID int64, limit int) ([]ContinueReadingRow, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	if limit <= 0 {
		limit = 8
	}

	where, args := scope.AppendBookWhere(`s.user_id = ?
				AND s.last_read_at > 0
				AND s.progress < 0.995
				AND rs.status = 'reading'
				AND b.deleted_at IS NULL`, "b.id", userID)
	args = append(args, limit)

	queryStr := fmt.Sprintf(`
		WITH latest AS (
			SELECT
				a.book_id,
				s.asset_id,
				s.progress,
				s.last_read_at,
				ROW_NUMBER() OVER (
					PARTITION BY a.book_id
					ORDER BY s.last_read_at DESC, s.updated_at DESC, s.asset_id ASC
				) AS rn
			FROM user_asset_state s
			JOIN assets a ON a.id = s.asset_id
			JOIN books b ON b.id = a.book_id
			JOIN user_book_reading_state rs
				ON rs.user_id = s.user_id AND rs.book_id = a.book_id
			WHERE `+where+`
		)
		SELECT %s,
			latest.asset_id,
			latest.progress,
			latest.last_read_at
		FROM latest
		JOIN books b ON b.id = latest.book_id
		WHERE latest.rn = 1
		ORDER BY latest.last_read_at DESC
		LIMIT ?
	`, bookSummaryColumns)

	rows, err := queryer.Query(queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("list continue reading query: %w", err)
	}
	defer rows.Close()

	var out []ContinueReadingRow
	for rows.Next() {
		var r ContinueReadingRow
		if err := rows.Scan(
			&r.ID,
			&r.Title,
			&r.Series,
			&r.SeriesIndex,
			&r.Tags,
			&r.CoverVersion,
			&r.Date,
			&r.AssetID,
			&r.Progress,
			&r.LastReadAt,
		); err != nil {
			return nil, fmt.Errorf("list continue reading scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list continue reading rows: %w", err)
	}
	return out, nil
}
