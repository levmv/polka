package db

import (
	"net/url"
	"strings"
)

// ReaderPositionWrite is a reader's observed position. Revision identifies the
// saved position it was based on; DeviceID identifies the reader.
type ReaderPositionWrite struct {
	Revision   int64
	Progress   float64
	Locator    Locator
	DeviceID   string
	DeviceName string
}

func validateReaderPosition(input ReaderPositionWrite) (ReaderPositionWrite, error) {
	if input.Revision < 0 {
		return input, ErrInvalidReaderInput
	}
	if !(input.Progress >= 0 && input.Progress <= 1) {
		return input, ErrInvalidReaderInput
	}
	device, err := url.Parse(input.DeviceID)
	if err != nil || !device.IsAbs() || len(input.DeviceID) > 1024 || strings.TrimSpace(input.DeviceName) == "" || len(input.DeviceName) > 256 {
		return input, errorWithDetail(ErrInvalidReaderInput, "invalid reading device")
	}
	input.Locator, err = normalizeLocator(input.Locator)
	return input, err
}

// Device metadata, server receipt times and the expected revision are not part
// of the position. Equivalent saves only refresh recency, preserving the saved
// source and any subsequent manual reading-status change.
func sameReaderPosition(current *ReaderState, input ReaderPositionWrite) bool {
	// Opening and reset have no saved observation, even at zero percent.
	return current.DeviceID != "" && current.Progress == input.Progress && current.Locator.Equal(input.Locator)
}

// Check equivalent content before the expected revision; retries do not advance it.
func checkReaderPositionWrite(current *ReaderState, input ReaderPositionWrite) (bool, error) {
	if sameReaderPosition(current, input) {
		return false, nil
	}
	if current.Revision != input.Revision {
		return false, ErrReadingConflict
	}
	return true, nil
}

func (state *ReaderState) positionIsReset() bool {
	return state.Revision > 0 && state.Progress == 0 && state.Locator.IsZero() && state.UpdatedAt == 0
}
