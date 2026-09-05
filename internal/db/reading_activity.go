package db

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const ReadingIdleLimit = 5 * time.Minute

var ErrReadingTimeZoneRequired = errors.New("set your time zone before recording reading activity")

// ReadingActivityCheckpoint reports times relative to the current segment's
// start. Counted time from earlier segments stays in the session total.
type ReadingActivityCheckpoint struct {
	Segment        int64
	ElapsedMS      int64
	LastActivityMS int64
	Finished       bool
}

type ReadingActivityResult struct {
	Active    bool  `json:"active"`     // This segment may continue recording time.
	CountedMS int64 `json:"counted_ms"` // Session total, including earlier segments.
}

type readingSession struct {
	ID               int64
	AssetID          string
	TimeZone         string
	Segment          int64
	SegmentStartedAt int64
	CountedMS        int64
	ObservedAt       int64
	LastActivityAt   int64
	ClosedAt         sql.NullInt64
}

func getWebReadingSession(tx *sql.Tx, userID int64, sourceID []byte) (*readingSession, error) {
	s := new(readingSession)
	err := tx.QueryRow(`
		SELECT id, asset_id, time_zone, segment, segment_started_at, counted_ms, observed_at, last_activity_at, closed_at
		FROM reading_sessions WHERE user_id = ? AND source = 'web' AND source_id = ?
	`, userID, sourceID).Scan(&s.ID, &s.AssetID, &s.TimeZone, &s.Segment, &s.SegmentStartedAt, &s.CountedMS,
		&s.ObservedAt, &s.LastActivityAt, &s.ClosedAt)
	return s, err
}

func decodeReadingSourceID(id string) ([]byte, error) {
	if len(id) != 32 {
		return nil, ErrInvalidReaderInput
	}
	raw, err := hex.DecodeString(id)
	if err != nil {
		return nil, ErrInvalidReaderInput
	}
	return raw, nil
}

// StartWebReadingSession starts or resumes the user's sole active web session.
// Repeating a segment's start is idempotent, including after closure or takeover.
func (db *DB) StartWebReadingSession(ctx context.Context, userID int64, assetID, sourceID string, segment int64, now time.Time) (ReadingActivityResult, error) {
	rawID, err := decodeReadingSourceID(sourceID)
	if err != nil || segment < 0 {
		return ReadingActivityResult{}, ErrInvalidReaderInput
	}
	var result ReadingActivityResult
	err = db.Transact(ctx, func(tx *sql.Tx) error {
		s, err := getWebReadingSession(tx, userID, rawID)
		if err == nil {
			if s.AssetID != assetID {
				return ErrInvalidReaderInput
			}
			stamp := now.UnixMilli()
			result.CountedMS = s.CountedMS
			if segment == s.Segment {
				result = s.result(stamp)
				return nil
			}
			if segment < s.Segment || s.ClosedAt.Valid || stamp >= s.LastActivityAt+ReadingIdleLimit.Milliseconds() {
				return nil
			}
			// Retain the session total and skip the hidden gap. The new segment
			// number prevents late checkpoints from using this new clock origin.
			stamp = max(stamp, s.ObservedAt)
			_, err := tx.Exec(`UPDATE reading_sessions SET segment = ?, segment_started_at = ?,
				observed_at = ?, last_activity_at = ? WHERE id = ?`, segment, stamp, stamp, stamp, s.ID)
			result.Active = err == nil
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		if segment != 0 {
			return ErrInvalidReaderInput
		}
		var zone sql.NullString
		err = tx.QueryRow(`SELECT time_zone FROM user_settings WHERE user_id = ?`, userID).Scan(&zone)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !zone.Valid {
			return ErrReadingTimeZoneRequired
		}
		if err != nil {
			return err
		}
		// Do not invent a final checkpoint for the old reader. Its late report
		// may still fill time up to this boundary, but can never overlap us.
		stamp := now.UnixMilli()
		if _, err := tx.Exec(`UPDATE reading_sessions
			SET closed_at = MAX(observed_at, MIN(?, last_activity_at + ?))
			WHERE user_id = ? AND source = 'web' AND closed_at IS NULL`,
			stamp, ReadingIdleLimit.Milliseconds(), userID); err != nil {
			return err
		}
		// A backwards server-clock adjustment must not create overlapping time.
		if err := tx.QueryRow(`SELECT MAX(?, COALESCE(MAX(observed_at), ?))
			FROM reading_sessions WHERE user_id = ? AND source = 'web'`,
			stamp, stamp, userID).Scan(&stamp); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO reading_sessions
			(user_id, asset_id, source, source_id, time_zone, started_at, segment_started_at, observed_at, last_activity_at)
			VALUES (?, ?, 'web', ?, ?, ?, ?, ?, ?)`, userID, assetID, rawID, zone.String, stamp, stamp, stamp, stamp)
		result.Active = err == nil
		return err
	})
	return result, err
}

func (s *readingSession) result(now int64) ReadingActivityResult {
	return ReadingActivityResult{
		Active:    !s.ClosedAt.Valid && now < s.LastActivityAt+ReadingIdleLimit.Milliseconds(),
		CountedMS: s.CountedMS,
	}
}

// CheckpointWebReadingSession records cumulative time within a visible segment.
// Retries and reordered checkpoints cannot count time twice. Old segments are
// ignored; if their final report was lost, unreported time remains uncounted.
func (db *DB) CheckpointWebReadingSession(ctx context.Context, userID int64, assetID, sourceID string, checkpoint ReadingActivityCheckpoint, now time.Time) (ReadingActivityResult, error) {
	rawID, err := decodeReadingSourceID(sourceID)
	if err != nil || checkpoint.Segment < 0 || checkpoint.ElapsedMS < 0 ||
		checkpoint.LastActivityMS < 0 || checkpoint.LastActivityMS > checkpoint.ElapsedMS {
		return ReadingActivityResult{}, ErrInvalidReaderInput
	}
	var result ReadingActivityResult
	err = db.Transact(ctx, func(tx *sql.Tx) error {
		s, err := getWebReadingSession(tx, userID, rawID)
		if errors.Is(err, sql.ErrNoRows) {
			// A page can finish after its account/session was removed or the
			// browser signed into another account. Do not create or expose state.
			return nil
		}
		if err != nil {
			return err
		}
		if s.AssetID != assetID {
			return ErrInvalidReaderInput
		}
		if checkpoint.Segment != s.Segment {
			result.CountedMS = s.CountedMS
			return nil
		}
		stamp := now.UnixMilli()
		elapsed := min(checkpoint.ElapsedMS, max(0, stamp-s.SegmentStartedAt))
		end := s.SegmentStartedAt + elapsed
		idleAt := s.LastActivityAt + ReadingIdleLimit.Milliseconds()
		actionAt := s.SegmentStartedAt + min(elapsed, checkpoint.LastActivityMS)
		// Use the action's time, not its arrival time, to check the idle limit.
		// An action after the deadline cannot turn the preceding gap into reading.
		if actionAt < idleAt {
			s.LastActivityAt = max(s.LastActivityAt, actionAt)
			idleAt = s.LastActivityAt + ReadingIdleLimit.Milliseconds()
		}
		if end >= idleAt {
			if !s.ClosedAt.Valid || idleAt < s.ClosedAt.Int64 {
				s.ClosedAt = sql.NullInt64{Int64: idleAt, Valid: true}
			}
		}
		end = min(end, s.LastActivityAt+ReadingIdleLimit.Milliseconds())
		if s.ClosedAt.Valid {
			end = min(end, s.ClosedAt.Int64)
		}
		if end > s.ObservedAt {
			if err := addReadingTimeByDay(tx, s, end); err != nil {
				return err
			}
			s.CountedMS += end - s.ObservedAt
			s.ObservedAt = end
		}
		if checkpoint.Finished && s.SegmentStartedAt+elapsed >= s.ObservedAt {
			// An older final request must not truncate a newer checkpoint.
			s.ClosedAt = sql.NullInt64{Int64: s.ObservedAt, Valid: true}
		}
		_, err = tx.Exec(`UPDATE reading_sessions
			SET observed_at = ?, last_activity_at = ?, closed_at = ?, counted_ms = ? WHERE id = ?`,
			s.ObservedAt, s.LastActivityAt, s.ClosedAt, s.CountedMS, s.ID)
		result = s.result(stamp)
		return err
	})
	return result, err
}

func addReadingTimeByDay(tx *sql.Tx, s *readingSession, end int64) error {
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil {
		return fmt.Errorf("load reading session time zone: %w", err)
	}
	for cursor := s.ObservedAt; cursor < end; {
		local := time.UnixMilli(cursor).In(location)
		year, month, day := local.Date()
		// Advance under the current UTC offset, stopping at any intervening
		// offset change. A local midnight can be skipped or repeated; asking
		// time.Date to resolve that ambiguity can credit the wrong calendar day.
		_, offset := local.Zone()
		next := time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(offset) * time.Second)
		_, zoneEnd := local.ZoneBounds()
		if !zoneEnd.IsZero() && zoneEnd.Before(next) {
			next = zoneEnd
		}
		partEnd := min(end, next.UnixMilli())
		if _, err := tx.Exec(`INSERT INTO reading_session_days (session_id, day, started_at, ended_at, active_ms)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(session_id, day) DO UPDATE SET
				ended_at = excluded.ended_at, active_ms = reading_session_days.active_ms + excluded.active_ms`,
			s.ID, local.Format("2006-01-02"), cursor, partEnd, partEnd-cursor); err != nil {
			return err
		}
		cursor = partEnd
	}
	return nil
}
