package db

import (
	"database/sql"
	"errors"
	"fmt"
)

var ErrInvalidTheme = errors.New("theme must be system, light, dark, or sepia")

const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
	ThemeSepia  = "sepia"
)

type UserSettings struct {
	UserID              string
	Theme               string
	HideContinueReading bool
	UpdatedAt           int64
}

func (db *DB) GetUserSettings(userID string) (*UserSettings, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}

	settings := &UserSettings{UserID: userID, Theme: ThemeSystem}
	var hideContinueReading int
	err := db.QueryRow(`
		SELECT theme, hide_continue_reading, updated_at
		FROM user_settings
		WHERE user_id = ?
	`, userID).Scan(&settings.Theme, &hideContinueReading, &settings.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user settings: %w", err)
	}
	settings.HideContinueReading = hideContinueReading != 0
	return settings, nil
}

func (db *DB) SaveUserSettings(userID string, settings UserSettings) (*UserSettings, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	if !validTheme(settings.Theme) {
		return nil, ErrInvalidTheme
	}

	if _, err := db.Exec(`
		INSERT INTO user_settings (user_id, theme, hide_continue_reading, updated_at)
		VALUES (?, ?, ?, unixepoch())
		ON CONFLICT(user_id) DO UPDATE SET
			theme = excluded.theme,
			hide_continue_reading = excluded.hide_continue_reading,
			updated_at = unixepoch()
	`, userID, settings.Theme, settings.HideContinueReading); err != nil {
		return nil, fmt.Errorf("save user settings: %w", err)
	}
	return db.GetUserSettings(userID)
}

func validTheme(theme string) bool {
	return theme == ThemeSystem || theme == ThemeLight || theme == ThemeDark || theme == ThemeSepia
}
