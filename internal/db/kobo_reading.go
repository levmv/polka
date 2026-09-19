package db

import (
	"context"
	"database/sql/driver"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"time"
)

// KoboPosition retains the device's location and its progress within the chapter.
// Unknown location types can still round-trip through native sync.
type KoboPosition struct {
	Source                 string   `json:"source,omitzero"`
	Type                   string   `json:"type,omitzero"`
	Fragment               string   `json:"fragment,omitzero"`
	ChapterProgressPercent *float64 `json:"chapter_progress_percent,omitzero"`
}

func (p KoboPosition) IsZero() bool {
	return p.Source == "" && p.Type == "" && p.Fragment == "" && p.ChapterProgressPercent == nil
}

func (p KoboPosition) Equal(other KoboPosition) bool {
	// Chapter progress is part of the native bookmark, even at the same span.
	progressEqual := p.ChapterProgressPercent == nil && other.ChapterProgressPercent == nil ||
		p.ChapterProgressPercent != nil && other.ChapterProgressPercent != nil && *p.ChapterProgressPercent == *other.ChapterProgressPercent
	return p.Source == other.Source && p.Type == other.Type && p.Fragment == other.Fragment && progressEqual
}

func (p *KoboPosition) Scan(value any) error {
	var raw []byte
	switch value := value.(type) {
	case string:
		raw = []byte(value)
	case []byte:
		raw = value
	default:
		return fmt.Errorf("read Kobo position: unexpected SQL value %T", value)
	}
	return json.Unmarshal(raw, p)
}

func (p KoboPosition) Value() (driver.Value, error) {
	raw, err := json.Marshal(p)
	return string(raw), err
}

type KoboReadingUpdate struct {
	Position          *KoboPosition
	Progress          *float64
	PositionUpdatedAt int64 // Unix seconds from CurrentBookmark.LastModified.
	Status            string
	StatusUpdatedAt   int64 // Unix seconds from StatusInfo.LastModified.
	DeviceID          string
	DeviceName        string
}

type KoboReadingResult struct {
	PositionChanged bool
	StatusChanged   bool
}

// SaveKoboReading accepts independently dated bookmark and status updates.
// Without an explicit status, a changed bookmark can advance reading status.
// Older uploads cannot replace a newer position, including a deliberate rewind
// or reset. Device clocks are expected to agree with the server clock.
func (db *DB) SaveKoboReading(ctx context.Context, userID, assetID int64, input KoboReadingUpdate) (KoboReadingResult, error) {
	var result KoboReadingResult
	if input.Position != nil {
		p := *input.Position
		if len(p.Source) > 4096 || len(p.Fragment) > 4096 || len(p.Type) > 256 ||
			p.ChapterProgressPercent != nil && !(*p.ChapterProgressPercent >= 0 && *p.ChapterProgressPercent <= 100) || input.PositionUpdatedAt <= 0 {
			return result, ErrInvalidReaderInput
		}
	}
	if input.Progress != nil && !(*input.Progress >= 0 && *input.Progress <= 1) ||
		len(input.DeviceID) > 256 || len(input.DeviceName) > 256 ||
		input.Status != "" && (!ValidReadingStatus(input.Status) || input.StatusUpdatedAt <= 0) {
		return result, ErrInvalidReaderInput
	}
	err := db.Transact(ctx, func(tx *Tx) error {
		current, err := GetReaderState(tx, userID, assetID)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		progress := current.Progress
		if input.Progress != nil {
			progress = *input.Progress
		}
		if p := input.Position; p != nil {
			// Ignore pagination differences only when a full address identifies
			// the same place. A chapter percentage alone cannot do that.
			hasLocation := p.Source != "" && p.Type != "" && p.Fragment != ""
			equal := current.DeviceID != "" && current.KoboPosition.Equal(*p) &&
				(hasLocation || current.Progress == progress)
			modifiedAt := min(input.PositionUpdatedAt, now)
			if !equal && (current.Revision == 0 || modifiedAt >= current.UpdatedAt) {
				if _, err := writeReaderPosition(tx, &ReaderState{
					UserID: userID, AssetID: assetID,
					Progress: progress, KoboPosition: *p,
					DeviceID:   "urn:kobo:" + url.QueryEscape(input.DeviceID),
					DeviceName: input.DeviceName, UpdatedAt: modifiedAt,
				}); err != nil {
					return err
				}
				result.PositionChanged = true
			}
		}
		if input.Status == "" && !result.PositionChanged {
			return nil
		}
		status, err := GetReadingStatus(tx, userID, current.BookID)
		if err != nil {
			return err
		}
		target, modifiedAt := input.Status, input.StatusUpdatedAt
		if target == "" {
			// An inferred status belongs to the bookmark, not its arrival time.
			target = readingStatusAfterProgress(status.Status, progress)
			modifiedAt = input.PositionUpdatedAt
		}
		modifiedAt = min(modifiedAt, now)
		if modifiedAt < status.UpdatedAt {
			return nil
		}
		// Dropped has no Kobo equivalent. Its Reading echo leaves it intact.
		if status.Status == ReadingStatusDropped && target == ReadingStatusReading {
			return nil
		}
		change, err := setReadingStatusAt(tx, status, target, ReadingStatusSourceKobo, modifiedAt)
		result.StatusChanged = change.Changed
		return err
	})
	return result, err
}
