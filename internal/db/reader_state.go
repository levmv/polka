package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrAssetNotFound      = errors.New("asset not found")
	ErrInvalidReaderInput = errors.New("invalid reader input")
	ErrReadingConflict    = errors.New("reading data changed on another reader")
)

type ReaderState struct {
	UserID           int64
	AssetID          int64
	BookID           int64
	Progress         float64
	Locator          Locator
	KOReaderPosition string
	KoboPosition     KoboPosition
	Revision         int64
	DeviceID         string
	DeviceName       string
	UpdatedAt        int64
	hasProgress      bool
}

type ContinueReadingRow struct {
	BookSummaryRow
	AssetID   int64
	Progress  float64
	UpdatedAt int64
}

type ReaderProgress struct {
	// Nil means no reading yet or an explicitly reset position; zero is the beginning.
	Progress      *float64
	ReadingStatus ReadingStatusState
}

func GetReaderProgress(queryer Queryer, userID, assetID int64) (*ReaderProgress, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	progress := &ReaderProgress{ReadingStatus: ReadingStatusState{UserID: userID}}
	status := &progress.ReadingStatus
	err := queryer.QueryRow(`
        SELECT p.progress,
               a.book_id, COALESCE(rs.status, 'unread'),
               COALESCE(rs.last_event_id, 0), COALESCE(rs.updated_at, 0)
        FROM assets a
        LEFT JOIN reading_positions p ON p.asset_id = a.id AND p.user_id = ?
        LEFT JOIN user_book_reading_state rs ON rs.book_id = a.book_id AND rs.user_id = ?
        WHERE a.id = ?`, userID, userID, assetID).
		Scan(&progress.Progress, &status.BookID, &status.Status, &status.LastEventID, &status.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get reader progress: %w", err)
	}
	return progress, nil
}

func GetReaderState(queryer Queryer, userID, assetID int64) (*ReaderState, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	state := &ReaderState{UserID: userID, AssetID: assetID}
	var progress sql.NullFloat64
	err := queryer.QueryRow(`
        SELECT COALESCE(s.koreader_position, ''), COALESCE(s.kobo_position, '{}'),
               a.book_id, s.progress, COALESCE(s.locator, '{}'),
               COALESCE(s.revision, 0), COALESCE(s.device_id, ''), COALESCE(s.device_name, ''),
               COALESCE(s.updated_at, 0)
        FROM assets a LEFT JOIN reading_positions s ON s.asset_id = a.id AND s.user_id = ?
        WHERE a.id = ?`, userID, assetID).Scan(&state.KOReaderPosition, &state.KoboPosition,
		&state.BookID, &progress, &state.Locator,
		&state.Revision, &state.DeviceID, &state.DeviceName, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get reader state: %w", err)
	}
	state.Progress, state.hasProgress = progress.Float64, progress.Valid
	return state, nil
}

// TouchReader records opening a book and advances unread books to reading.
func (db *DB) TouchReader(ctx context.Context, userID, assetID int64, source ReadingStatusSource) error {
	if userID <= 0 {
		return ErrUserIDRequired
	}
	return db.Transact(ctx, func(tx *Tx) error {
		var bookID int64
		err := tx.QueryRow(`SELECT book_id FROM assets WHERE id = ?`, assetID).Scan(&bookID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAssetNotFound
		}
		if err != nil {
			return fmt.Errorf("get reader book: %w", err)
		}
		// Opening adds an unpositioned book to Continue reading. Once a position
		// is saved, its timestamp represents reading, not reopening or a retry.
		_, err = tx.Exec(`INSERT INTO reading_positions (user_id, asset_id, updated_at)
            VALUES (?, ?, unixepoch()) ON CONFLICT(user_id, asset_id) DO UPDATE
            SET progress = COALESCE(reading_positions.progress, 0), updated_at = excluded.updated_at
            WHERE reading_positions.device_id = ''`, userID, assetID)
		if err != nil {
			return fmt.Errorf("touch reader state: %w", err)
		}
		// Opening is an unread -> reading signal, even at an old finished position.
		_, err = advanceReadingStatus(tx, userID, bookID, 0, source)
		return err
	})
}

// SaveReaderState saves a position and advances its book's reading status atomically.
func (db *DB) SaveReaderState(ctx context.Context, userID, assetID int64, input ReaderPositionWrite, source ReadingStatusSource) (*ReaderState, ReadingStatusChange, error) {
	input, err := validateReaderPosition(input)
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	var state *ReaderState
	var change ReadingStatusChange
	err = db.Transact(ctx, func(tx *Tx) error {
		var err error
		state, err = GetReaderState(tx, userID, assetID)
		if err != nil {
			return err
		}
		changed, err := checkReaderPositionWrite(state, input)
		if err != nil {
			return err
		}
		if !changed {
			// Replaying a save must not undo a subsequent manual status change.
			change.State, err = GetReadingStatus(tx, userID, state.BookID)
			return err
		}
		state, err = writeReaderPosition(tx, &ReaderState{
			UserID: userID, AssetID: assetID, BookID: state.BookID,
			Progress: input.Progress, Locator: input.Locator,
			DeviceID: input.DeviceID, DeviceName: input.DeviceName, UpdatedAt: time.Now().Unix(),
		})
		if err != nil {
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

// Call only after validating and checking the observation against the state
// read in this transaction. Each transport applies its own ordering policy.
// All coordinates are replaced; supply only those belonging to this observation.
func writeReaderPosition(tx *Tx, state *ReaderState) (*ReaderState, error) {
	err := tx.QueryRow(`
        INSERT INTO reading_positions (user_id, asset_id, progress, locator,
            koreader_position, kobo_position, revision, device_id, device_name, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
        ON CONFLICT(user_id, asset_id) DO UPDATE SET
            progress = excluded.progress, locator = excluded.locator,
            koreader_position = excluded.koreader_position, kobo_position = excluded.kobo_position,
            revision = reading_positions.revision + 1,
            device_id = excluded.device_id, device_name = excluded.device_name,
            updated_at = excluded.updated_at
        RETURNING revision`,
		state.UserID, state.AssetID, state.Progress, state.Locator, state.KOReaderPosition, state.KoboPosition,
		state.DeviceID, state.DeviceName, state.UpdatedAt).Scan(&state.Revision)
	if err != nil {
		return nil, fmt.Errorf("save reader state: %w", err)
	}
	state.hasProgress = true
	return state, nil
}

// CachePositionConversion fills coordinates only while the reading revision
// remains current, without changing the observation's source or timestamp.
func (db *DB) CachePositionConversion(ctx context.Context, state *ReaderState) (bool, error) {
	result, err := db.Write(ctx).Exec(`UPDATE reading_positions
        SET locator = CASE WHEN locator = '{}' THEN ? ELSE locator END,
            koreader_position = CASE WHEN koreader_position = '' THEN ? ELSE koreader_position END,
            kobo_position = CASE WHEN kobo_position = '{}' THEN ? ELSE kobo_position END
        WHERE user_id = ? AND asset_id = ? AND revision = ?`,
		state.Locator, state.KOReaderPosition, state.KoboPosition, state.UserID, state.AssetID, state.Revision)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count != 0, err
}

// ResetReaderState clears the position and advances its revision.
// Repeating a reset of an already cleared position is a no-op.
func (db *DB) ResetReaderState(ctx context.Context, userID, assetID int64) error {
	return db.Transact(ctx, func(tx *Tx) error {
		current, err := GetReaderState(tx, userID, assetID)
		if err != nil {
			return err
		}
		if current.positionIsReset() {
			return nil
		}
		_, err = tx.Exec(`INSERT INTO reading_positions (user_id, asset_id, revision, progress, updated_at)
            VALUES (?, ?, 1, NULL, unixepoch()) ON CONFLICT(user_id, asset_id) DO UPDATE SET
            progress = NULL, locator = '{}', koreader_position = '', kobo_position = '{}',
            revision = reading_positions.revision + 1,
            device_id = '', device_name = '',
            updated_at = excluded.updated_at`, userID, assetID)
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
			FROM reading_positions s
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
