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
	ErrReadingConflict    = errors.New("reading data changed on another reader")
)

type ReaderState struct {
	UserID     int64
	AssetID    int64
	BookID     int64
	Progress   float64
	Locator    Locator
	Revision   int64
	DeviceID   string
	DeviceName string
	UpdatedAt  int64
}

type ContinueReadingRow struct {
	BookSummaryRow
	AssetID   int64
	Progress  float64
	UpdatedAt int64
}

func GetReaderState(queryer Queryer, userID, assetID int64) (*ReaderState, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	state := &ReaderState{UserID: userID, AssetID: assetID}
	err := queryer.QueryRow(`
        SELECT a.book_id, COALESCE(s.progress, 0), COALESCE(s.locator, '{}'),
               COALESCE(s.revision, 0), COALESCE(s.device_id, ''), COALESCE(s.device_name, ''),
               COALESCE(s.updated_at, 0)
        FROM assets a LEFT JOIN user_asset_state s ON s.asset_id = a.id AND s.user_id = ?
        WHERE a.id = ?`, userID, assetID).Scan(&state.BookID, &state.Progress, &state.Locator,
		&state.Revision, &state.DeviceID, &state.DeviceName, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get reader state: %w", err)
	}
	return state, err
}

func (db *DB) TouchReaderStateAndAdvanceStatus(ctx context.Context, userID, assetID int64, source ReadingStatusSource) (*ReaderState, ReadingStatusChange, error) {
	var state *ReaderState
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *Tx) error {
		var err error
		state, err = GetReaderState(tx, userID, assetID)
		if err != nil {
			return err
		}
		if err := touchReaderState(tx, state); err != nil {
			return err
		}
		// Opening is an unread -> reading signal, even at an old finished position.
		change, err = advanceReadingStatus(tx, userID, state.BookID, 0, source)
		return err
	})
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	return state, change, nil
}

// The caller loads state in this transaction. Opening and equivalent saves
// refresh recency without changing the position or its revision.
func touchReaderState(tx *Tx, state *ReaderState) error {
	if err := tx.QueryRow(`INSERT INTO user_asset_state (user_id, asset_id, updated_at)
        VALUES (?, ?, unixepoch()) ON CONFLICT(user_id, asset_id) DO UPDATE SET updated_at = unixepoch()
        RETURNING updated_at`, state.UserID, state.AssetID).Scan(&state.UpdatedAt); err != nil {
		return fmt.Errorf("touch reader state: %w", err)
	}
	return nil
}

func (db *DB) SaveReaderStateAndAdvanceStatus(ctx context.Context, userID, assetID int64, input ReaderPositionWrite, source ReadingStatusSource) (*ReaderState, ReadingStatusChange, error) {
	input, err := validateReaderPosition(input)
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	var state *ReaderState
	var change ReadingStatusChange
	err = db.Transact(ctx, func(tx *Tx) error {
		var changed bool
		var err error
		state, changed, err = saveReaderState(tx, userID, assetID, input)
		if err != nil {
			return err
		}
		if !changed {
			// Replaying a save must not undo a subsequent manual status change.
			change.State, err = GetReadingStatus(tx, userID, state.BookID)
			return err
		}
		change, err = advanceReadingStatus(tx, userID, state.BookID, input.Progress, source)
		return err
	})
	if err != nil {
		return state, ReadingStatusChange{}, err
	}
	return state, change, nil
}

func saveReaderState(tx *Tx, userID, assetID int64, input ReaderPositionWrite) (*ReaderState, bool, error) {
	current, err := GetReaderState(tx, userID, assetID)
	if err != nil {
		return nil, false, err
	}
	changed, err := checkReaderPositionWrite(current, input)
	if err != nil {
		return current, false, err
	}
	if !changed {
		return current, false, touchReaderState(tx, current)
	}
	saved, err := writeReaderPosition(tx, userID, assetID, input)
	return saved, true, err
}

// Call only after validating and checking the observation against the state
// read in this transaction. Web and OPDS use different ordering policies.
func writeReaderPosition(tx *Tx, userID, assetID int64, input ReaderPositionWrite) (*ReaderState, error) {
	_, err := tx.Exec(`
        INSERT INTO user_asset_state (user_id, asset_id, progress, locator,
            revision, device_id, device_name, updated_at)
        VALUES (?, ?, ?, ?, 1, ?, ?, unixepoch())
        ON CONFLICT(user_id, asset_id) DO UPDATE SET
            progress = excluded.progress, locator = excluded.locator,
            revision = user_asset_state.revision + 1,
            device_id = excluded.device_id, device_name = excluded.device_name,
            updated_at = unixepoch()`,
		userID, assetID, input.Progress, input.Locator,
		input.DeviceID, input.DeviceName)
	if err != nil {
		return nil, fmt.Errorf("save reader state: %w", err)
	}
	return GetReaderState(tx, userID, assetID)
}

func (db *DB) ResetReaderState(ctx context.Context, userID, assetID int64, revision int64) error {
	if revision < 0 {
		return ErrInvalidReaderInput
	}
	return db.Transact(ctx, func(tx *Tx) error {
		current, err := GetReaderState(tx, userID, assetID)
		if err != nil {
			return err
		}
		// A reset is a versioned write even for an empty position. Retrying
		// the same cleared state is a no-op.
		if current.positionIsReset() {
			return nil
		}
		if current.Revision != revision {
			return ErrReadingConflict
		}
		_, err = tx.Exec(`INSERT INTO user_asset_state (user_id, asset_id, revision, updated_at)
            VALUES (?, ?, 1, 0) ON CONFLICT(user_id, asset_id) DO UPDATE SET
            progress = 0, locator = '{}', revision = user_asset_state.revision + 1,
            device_id = '', device_name = '',
            updated_at = 0`, userID, assetID)
		return err
	})
}

func ListContinueReading(queryer Queryer, scope VisibilityScope, userID int64, limit int) ([]ContinueReadingRow, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	if limit <= 0 {
		limit = 8
	}

	where, args := scope.AppendBookWhere(`s.user_id = ?
				AND s.updated_at > 0
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
				s.updated_at,
				ROW_NUMBER() OVER (
					PARTITION BY a.book_id
					ORDER BY s.updated_at DESC, s.asset_id ASC
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
			latest.updated_at
		FROM latest
		JOIN books b ON b.id = latest.book_id
		WHERE latest.rn = 1
		ORDER BY latest.updated_at DESC
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
			&r.UpdatedAt,
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
