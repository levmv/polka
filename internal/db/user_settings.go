package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
	_ "time/tzdata" // Keep named zones available in the standalone binary.
)

var ErrInvalidTheme = errors.New("theme must be system, light, dark, or sepia")
var ErrInvalidTimeZone = errors.New("choose a valid named time zone, such as Europe/Berlin or UTC")

const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
	ThemeSepia  = "sepia"
)

type UserSettings struct {
	UserID              int64
	Theme               string
	HideContinueReading bool
	TimeZone            string // Empty means unset; explicit UTC is a saved choice.
	UpdatedAt           int64
}

type UserSettingsPatch struct {
	Theme               *string
	HideContinueReading *bool
	TimeZone            *string
	InitializeTimeZone  bool // Set TimeZone only if no zone is saved yet.
}

func (db *DB) GetUserSettings(userID int64) (*UserSettings, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}

	settings := &UserSettings{UserID: userID, Theme: ThemeSystem}
	var hideContinueReading int
	err := db.QueryRow(`
		SELECT theme, hide_continue_reading, COALESCE(time_zone, ''), updated_at
		FROM user_settings
		WHERE user_id = ?
	`, userID).Scan(&settings.Theme, &hideContinueReading, &settings.TimeZone, &settings.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user settings: %w", err)
	}
	settings.HideContinueReading = hideContinueReading != 0
	return settings, nil
}

func (db *DB) SaveUserSettings(userID int64, patch UserSettingsPatch) (*UserSettings, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	if patch.Theme != nil && !validTheme(*patch.Theme) {
		return nil, ErrInvalidTheme
	}
	if patch.TimeZone != nil {
		if err := validateUserTimeZone(*patch.TimeZone); err != nil {
			return nil, err
		}
	}

	// Apply only submitted fields in SQL. A delayed theme save or browser
	// initialization must not replace a time zone chosen in another tab.
	if _, err := db.Exec(`
		INSERT INTO user_settings (user_id, theme, hide_continue_reading, time_zone, updated_at)
		VALUES (?, COALESCE(?, 'system'), COALESCE(?, 0), ?, unixepoch())
		ON CONFLICT(user_id) DO UPDATE SET
			theme = COALESCE(?, user_settings.theme),
			hide_continue_reading = COALESCE(?, user_settings.hide_continue_reading),
			time_zone = CASE WHEN ? THEN COALESCE(user_settings.time_zone, excluded.time_zone)
				ELSE COALESCE(excluded.time_zone, user_settings.time_zone) END,
			updated_at = unixepoch()
	`, userID, patch.Theme, patch.HideContinueReading, patch.TimeZone,
		patch.Theme, patch.HideContinueReading, patch.InitializeTimeZone); err != nil {
		return nil, fmt.Errorf("save user settings: %w", err)
	}
	return db.GetUserSettings(userID)
}

func validateUserTimeZone(name string) error {
	// Local would depend on the server's deployment rather than the account.
	if name == "" || name == "Local" {
		return ErrInvalidTimeZone
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ErrInvalidTimeZone
	}
	return nil
}

func validTheme(theme string) bool {
	return theme == ThemeSystem || theme == ThemeLight || theme == ThemeDark || theme == ThemeSepia
}
