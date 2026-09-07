package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrReadingStatusBookMissing     = errors.New("book not found")
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
	UserID      int64
	BookID      int64
	Status      string
	LastEventID int64
	UpdatedAt   int64
}

type ReadingStatusChange struct {
	State   ReadingStatusState
	Changed bool
	EventID int64
}

func ValidReadingStatus(status string) bool {
	switch status {
	case ReadingStatusUnread, ReadingStatusReading, ReadingStatusFinished, ReadingStatusDropped:
		return true
	default:
		return false
	}
}

func GetReadingStatus(queryer Queryer, userID int64, bookID int64) (ReadingStatusState, error) {
	if userID <= 0 {
		return ReadingStatusState{}, ErrUserIDRequired
	}
	var state ReadingStatusState
	err := queryer.QueryRow(`
		SELECT ?, b.id, COALESCE(rs.status, 'unread'), COALESCE(rs.last_event_id, 0), COALESCE(rs.updated_at, 0)
		FROM books b
		JOIN users u ON u.id = ?
		LEFT JOIN user_book_reading_state rs ON rs.user_id = u.id AND rs.book_id = b.id
		WHERE b.id = ?
	`, userID, userID, bookID).Scan(&state.UserID, &state.BookID, &state.Status, &state.LastEventID, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReadingStatusState{}, ErrReadingStatusBookMissing
	}
	if err != nil {
		return ReadingStatusState{}, fmt.Errorf("get reading status: %w", err)
	}
	return state, nil
}

func (db *DB) SetReadingStatus(ctx context.Context, userID int64, bookID int64, status string, source ReadingStatusSource) (ReadingStatusChange, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if !ValidReadingStatus(status) {
		return ReadingStatusChange{}, ErrInvalidReadingStatus
	}
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *Tx) error {
		current, err := GetReadingStatus(tx, userID, bookID)
		if err != nil {
			return err
		}
		change, err = setReadingStatus(tx, current, status, source)
		return err
	})
	return change, err
}

func setReadingStatus(tx *Tx, current ReadingStatusState, status string, source ReadingStatusSource) (ReadingStatusChange, error) {
	if current.Status == status {
		return ReadingStatusChange{State: current}, nil
	}

	previousEventID := sql.NullInt64{Int64: current.LastEventID, Valid: current.LastEventID != 0}
	var eventID int64
	err := tx.QueryRow(`
		INSERT INTO user_book_reading_events
			(user_id, book_id, previous_event_id, from_status, to_status, source)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id
	`, current.UserID, current.BookID, previousEventID, current.Status, status, source).Scan(&eventID)
	if err != nil {
		return ReadingStatusChange{}, fmt.Errorf("record reading status change: %w", err)
	}
	next := ReadingStatusState{UserID: current.UserID, BookID: current.BookID}
	err = tx.QueryRow(`
		INSERT INTO user_book_reading_state (user_id, book_id, status, last_event_id, updated_at)
		VALUES (?, ?, ?, ?, unixepoch())
		ON CONFLICT(user_id, book_id) DO UPDATE SET
			status = excluded.status,
			last_event_id = excluded.last_event_id,
			updated_at = unixepoch()
		RETURNING status, last_event_id, updated_at
	`, current.UserID, current.BookID, status, eventID).Scan(&next.Status, &next.LastEventID, &next.UpdatedAt)
	if err != nil {
		return ReadingStatusChange{}, fmt.Errorf("set reading status: %w", err)
	}
	return ReadingStatusChange{State: next, Changed: true, EventID: eventID}, nil
}

func (db *DB) AdvanceReadingStatusForDocumentHash(ctx context.Context, userID int64, documentHash string, progress float64) (ReadingStatusChange, error) {
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *Tx) error {
		var err error
		change, err = advanceReadingStatusForDocumentHash(tx, userID, documentHash, progress)
		return err
	})
	return change, err
}

func advanceReadingStatusForDocumentHash(tx *Tx, userID int64, documentHash string, progress float64) (ReadingStatusChange, error) {
	target, err := ResolveKOReaderHash(tx, documentHash)
	if err != nil {
		return ReadingStatusChange{}, fmt.Errorf("resolve koreader reading status: %w", err)
	}
	if target.BookID == 0 || target.Ambiguous {
		return ReadingStatusChange{}, nil
	}
	return advanceReadingStatus(tx, userID, target.BookID, progress, ReadingStatusSourceKOSync)
}

func advanceReadingStatus(tx *Tx, userID int64, bookID int64, progress float64, source ReadingStatusSource) (ReadingStatusChange, error) {
	current, err := GetReadingStatus(tx, userID, bookID)
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
	return setReadingStatus(tx, current, target, source)
}

func (db *DB) UndoAutomaticReadingStatus(ctx context.Context, userID int64, bookID int64, eventID int64) (ReadingStatusChange, error) {
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *Tx) error {
		current, err := GetReadingStatus(tx, userID, bookID)
		if err != nil {
			return err
		}
		if current.LastEventID == 0 || current.LastEventID != eventID {
			return ErrReadingStatusUndoUnavailable
		}

		var fromStatus, toStatus string
		var source ReadingStatusSource
		var previousEvent sql.NullInt64
		var revertedAt sql.NullInt64
		err = tx.QueryRow(`
			SELECT from_status, to_status, source, previous_event_id, reverted_at
			FROM user_book_reading_events
			WHERE id = ? AND user_id = ? AND book_id = ?
		`, eventID, userID, bookID).Scan(&fromStatus, &toStatus, &source, &previousEvent, &revertedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReadingStatusUndoUnavailable
		}
		if err != nil {
			return fmt.Errorf("get reading status event: %w", err)
		}
		if revertedAt.Valid || toStatus != ReadingStatusFinished || source == ReadingStatusSourceManual {
			return ErrReadingStatusUndoUnavailable
		}

		if _, err := tx.Exec("UPDATE user_book_reading_events SET reverted_at = unixepoch() WHERE id = ?", eventID); err != nil {
			return fmt.Errorf("revert reading status event: %w", err)
		}
		next := ReadingStatusState{UserID: userID, BookID: bookID}
		err = tx.QueryRow(`
			UPDATE user_book_reading_state
			SET status = ?, last_event_id = ?, updated_at = unixepoch()
			WHERE user_id = ? AND book_id = ?
			RETURNING status, COALESCE(last_event_id, 0), updated_at
		`, fromStatus, previousEvent, userID, bookID).Scan(&next.Status, &next.LastEventID, &next.UpdatedAt)
		if err != nil {
			return fmt.Errorf("restore reading status: %w", err)
		}
		change = ReadingStatusChange{State: next, Changed: true, EventID: eventID}
		return nil
	})
	return change, err
}
