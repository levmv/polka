package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/id"
)

var (
	ErrReadingStatusWorkMissing     = errors.New("work not found")
	ErrInvalidReadingStatus         = errors.New("invalid reading status")
	ErrReadingStatusUndoUnavailable = errors.New("reading status change cannot be undone")
)

const (
	ReadingStatusUnread   = "unread"
	ReadingStatusReading  = "reading"
	ReadingStatusFinished = "finished"
	ReadingStatusDropped  = "dropped"

	// Foliate reports a fraction rather than a semantic "last page" event. Its
	// own UI already treats this bounded tail as complete, so status follows the
	// same threshold instead of demanding a fragile exact 1.0.
	ReaderFinishedProgress = 0.995
)

type ReadingStatusSource string

const (
	ReadingStatusSourceManual    ReadingStatusSource = "manual"
	ReadingStatusSourceWebReader ReadingStatusSource = "web_reader"
	ReadingStatusSourceKOSync    ReadingStatusSource = "kosync"
)

type ReadingStatusState struct {
	UserID      string
	WorkID      string
	Status      string
	LastEventID string
	UpdatedAt   int64
}

type ReadingStatusChange struct {
	State   ReadingStatusState
	Changed bool
	EventID string
}

func ValidReadingStatus(status string) bool {
	switch status {
	case ReadingStatusUnread, ReadingStatusReading, ReadingStatusFinished, ReadingStatusDropped:
		return true
	default:
		return false
	}
}

func GetReadingStatus(queryer Queryer, userID, workID string) (ReadingStatusState, error) {
	userID = strings.TrimSpace(userID)
	workID = strings.TrimSpace(workID)
	if userID == "" {
		return ReadingStatusState{}, ErrUserIDRequired
	}
	var state ReadingStatusState
	var lastEvent sql.NullString
	err := queryer.QueryRow(`
		SELECT ?, w.id, COALESCE(rs.status, 'unread'), rs.last_event_id, COALESCE(rs.updated_at, 0)
		FROM works w
		JOIN users u ON u.id = ?
		LEFT JOIN user_work_reading_state rs ON rs.user_id = u.id AND rs.work_id = w.id
		WHERE w.id = ?
	`, userID, userID, workID).Scan(&state.UserID, &state.WorkID, &state.Status, &lastEvent, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReadingStatusState{}, ErrReadingStatusWorkMissing
	}
	if err != nil {
		return ReadingStatusState{}, fmt.Errorf("get reading status: %w", err)
	}
	if lastEvent.Valid {
		state.LastEventID = lastEvent.String
	}
	return state, nil
}

func (db *DB) SetReadingStatus(ctx context.Context, userID, workID, status string, source ReadingStatusSource) (ReadingStatusChange, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if !ValidReadingStatus(status) {
		return ReadingStatusChange{}, ErrInvalidReadingStatus
	}
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *sql.Tx) error {
		var err error
		change, err = setReadingStatus(tx, userID, workID, status, source)
		return err
	})
	return change, err
}

func setReadingStatus(tx *sql.Tx, userID, workID, status string, source ReadingStatusSource) (ReadingStatusChange, error) {
	current, err := GetReadingStatus(tx, userID, workID)
	if err != nil {
		return ReadingStatusChange{}, err
	}
	if current.Status == status {
		return ReadingStatusChange{State: current}, nil
	}

	eventID := id.New(id.ReadingStatusEvent)
	previousEventID := sql.NullString{String: current.LastEventID, Valid: current.LastEventID != ""}
	if _, err := tx.Exec(`
		INSERT INTO user_work_reading_events
			(id, user_id, work_id, previous_event_id, from_status, to_status, source)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, eventID, userID, workID, previousEventID, current.Status, status, source); err != nil {
		return ReadingStatusChange{}, fmt.Errorf("record reading status change: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO user_work_reading_state (user_id, work_id, status, last_event_id, updated_at)
		VALUES (?, ?, ?, ?, unixepoch())
		ON CONFLICT(user_id, work_id) DO UPDATE SET
			status = excluded.status,
			last_event_id = excluded.last_event_id,
			updated_at = unixepoch()
	`, userID, workID, status, eventID); err != nil {
		return ReadingStatusChange{}, fmt.Errorf("set reading status: %w", err)
	}
	next, err := GetReadingStatus(tx, userID, workID)
	if err != nil {
		return ReadingStatusChange{}, err
	}
	return ReadingStatusChange{State: next, Changed: true, EventID: eventID}, nil
}

func (db *DB) AdvanceReadingStatusForDocumentHash(ctx context.Context, userID, documentHash string, progress float64) (ReadingStatusChange, error) {
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *sql.Tx) error {
		var err error
		change, err = advanceReadingStatusForDocumentHash(tx, userID, documentHash, progress)
		return err
	})
	return change, err
}

func advanceReadingStatusForDocumentHash(tx *sql.Tx, userID, documentHash string, progress float64) (ReadingStatusChange, error) {
	target, err := ResolveKOReaderHash(tx, documentHash)
	if err != nil {
		return ReadingStatusChange{}, fmt.Errorf("resolve koreader reading status: %w", err)
	}
	if target.WorkID == "" || target.Ambiguous {
		return ReadingStatusChange{}, nil
	}
	return advanceReadingStatus(tx, userID, target.WorkID, progress, ReadingStatusSourceKOSync)
}

func advanceReadingStatus(tx *sql.Tx, userID, workID string, progress float64, source ReadingStatusSource) (ReadingStatusChange, error) {
	current, err := GetReadingStatus(tx, userID, workID)
	if err != nil {
		return ReadingStatusChange{}, err
	}
	target := current.Status
	switch current.Status {
	case ReadingStatusUnread:
		target = ReadingStatusReading
		if progress >= ReaderFinishedProgress {
			target = ReadingStatusFinished
		}
	case ReadingStatusReading:
		if progress >= ReaderFinishedProgress {
			target = ReadingStatusFinished
		}
	}
	if target == current.Status {
		return ReadingStatusChange{State: current}, nil
	}
	return setReadingStatus(tx, userID, workID, target, source)
}

func (db *DB) UndoAutomaticReadingStatus(ctx context.Context, userID, workID, eventID string) (ReadingStatusChange, error) {
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *sql.Tx) error {
		current, err := GetReadingStatus(tx, userID, workID)
		if err != nil {
			return err
		}
		if current.LastEventID == "" || current.LastEventID != strings.TrimSpace(eventID) {
			return ErrReadingStatusUndoUnavailable
		}

		var fromStatus, toStatus string
		var source ReadingStatusSource
		var previousEvent sql.NullString
		var revertedAt sql.NullInt64
		err = tx.QueryRow(`
			SELECT from_status, to_status, source, previous_event_id, reverted_at
			FROM user_work_reading_events
			WHERE id = ? AND user_id = ? AND work_id = ?
		`, eventID, userID, workID).Scan(&fromStatus, &toStatus, &source, &previousEvent, &revertedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReadingStatusUndoUnavailable
		}
		if err != nil {
			return fmt.Errorf("get reading status event: %w", err)
		}
		if revertedAt.Valid || toStatus != ReadingStatusFinished || source == ReadingStatusSourceManual {
			return ErrReadingStatusUndoUnavailable
		}

		if _, err := tx.Exec("UPDATE user_work_reading_events SET reverted_at = unixepoch() WHERE id = ?", eventID); err != nil {
			return fmt.Errorf("revert reading status event: %w", err)
		}
		if _, err := tx.Exec(`
			UPDATE user_work_reading_state
			SET status = ?, last_event_id = ?, updated_at = unixepoch()
			WHERE user_id = ? AND work_id = ?
		`, fromStatus, previousEvent, userID, workID); err != nil {
			return fmt.Errorf("restore reading status: %w", err)
		}
		next, err := GetReadingStatus(tx, userID, workID)
		if err != nil {
			return err
		}
		change = ReadingStatusChange{State: next, Changed: true, EventID: eventID}
		return nil
	})
	return change, err
}
