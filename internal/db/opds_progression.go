package db

import (
	"context"
	"time"
)

const opdsPositionTimeTolerance = 5 * time.Minute

// SaveOPDSProgression checks an optional ETag under the writer transaction.
// Without one, client time is only a hint for rejecting stale backward moves.
// The transport parses modified; it is never persisted or returned as server time.
func (db *DB) SaveOPDSProgression(ctx context.Context, userID, assetID int64, input ReaderPositionWrite, modified time.Time, expected *int64) (*ReaderState, error) {
	input, err := validateReaderPosition(input)
	if err != nil {
		return nil, err
	}
	var saved *ReaderState
	err = db.Transact(ctx, func(tx *Tx) error {
		current, err := GetReaderState(tx, userID, assetID)
		if err != nil {
			return err
		}
		saved = current
		changed, err := checkOPDSPositionWrite(current, input, modified, expected)
		if err != nil {
			return err
		}
		if !changed {
			return touchReaderState(tx, current)
		}
		saved, err = writeReaderPosition(tx, userID, assetID, input)
		if err != nil {
			return err
		}
		_, err = advanceReadingStatus(tx, userID, saved.BookID, input.Progress, ReadingStatusSourceOPDS)
		return err
	})
	return saved, err
}

func checkOPDSPositionWrite(current *ReaderState, input ReaderPositionWrite, modified time.Time, expected *int64) (bool, error) {
	if sameReaderPosition(current, input) {
		return false, nil
	}
	if expected != nil {
		if *expected != current.Revision {
			return false, ErrReadingConflict
		}
		return true, nil
	}
	// Clock skew alone must not discard progress. Only a smaller position with
	// a date well before the last server update looks like a delayed old write.
	if input.Progress < current.Progress && modified.Before(time.Unix(current.UpdatedAt, 0).Add(-opdsPositionTimeTolerance)) {
		return false, ErrReadingConflict
	}
	return true, nil
}
