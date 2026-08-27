package writeback

import (
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/appsettings"
)

// Mode is the library-wide metadata write-back policy.
//
//   - off    — no write-back affordance anywhere (revs are still tracked, so a
//     later switch finds an honest backlog).
//   - manual — the default: nothing is written without an explicit action.
//   - auto   — dirty writable assets are reconciled by the server background
//     worker.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeManual Mode = "manual"
	ModeAuto   Mode = "auto"
)

const modeSettingKey = "metadata_writeback"

var ErrInvalidMode = errors.New("unsupported metadata write-back mode")

// OpenMode reads the configured write-back mode. An absent or unrecognized row
// reads as ModeManual, so the feature is discoverable without a migration and a
// stray value never silently enables background writes.
func OpenMode(q appsettings.Queryer) (Mode, error) {
	raw, ok, err := appsettings.Get(q, modeSettingKey)
	if err != nil {
		return "", fmt.Errorf("load metadata write-back mode: %w", err)
	}
	if !ok {
		return ModeManual, nil
	}
	switch Mode(strings.TrimSpace(raw)) {
	case ModeOff:
		return ModeOff, nil
	case ModeAuto:
		return ModeAuto, nil
	}
	return ModeManual, nil
}

// SaveMode stores the write-back mode.
func SaveMode(exec appsettings.Execer, mode Mode) error {
	switch mode {
	case ModeOff, ModeManual, ModeAuto:
	default:
		return fmt.Errorf("%w %q", ErrInvalidMode, mode)
	}
	if err := appsettings.Set(exec, modeSettingKey, string(mode)); err != nil {
		return fmt.Errorf("save metadata write-back mode: %w", err)
	}
	return nil
}
