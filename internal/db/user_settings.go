package db

import (
	"context" // Keep named zones available in the standalone binary.
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
	_ "time/tzdata"
)

var (
	ErrInvalidUserSettings = errors.New("invalid user settings")
	ErrInvalidTheme        = errorWithDetail(ErrInvalidUserSettings, "theme must be system, light, dark, or sepia")
	ErrInvalidTimeZone     = errorWithDetail(ErrInvalidUserSettings, "choose a valid named time zone, such as Europe/Berlin or UTC")
)

const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
	ThemeSepia  = "sepia"
)

const (
	ReaderFlowPaginated = "paginated"
	ReaderFlowScrolled  = "scrolled"

	ReaderStyleOriginal = "original"
	ReaderStylePaper    = "paper"
	ReaderStyleCustom   = "custom"

	DefaultReaderFontSize    = 0
	DefaultReaderColumnWidth = 760
	DefaultReaderLineHeight  = 1.72
)

type UserSettings struct {
	UserID              int64
	Theme               string
	ShowContinueReading bool
	TimeZone            string // Empty means unset; explicit UTC is a saved choice.
	ReaderFlow          string
	ReaderStyle         string
	ReaderFontSize      int
	ReaderColumnWidth   int
	ReaderLineHeight    float64
	UpdatedAt           int64
}

type UserSettingsPatch struct {
	Theme               *string
	ShowContinueReading *bool
	TimeZone            *string
	ReaderFlow          *string
	ReaderStyle         *string
	ReaderFontSize      *int
	ReaderColumnWidth   *int
	ReaderLineHeight    *float64
	InitializeTimeZone  bool // Set TimeZone only if no zone is saved yet.
}

func GetUserSettings(queryer Queryer, userID int64) (*UserSettings, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}

	settings := &UserSettings{
		UserID: userID, Theme: ThemeSystem, ShowContinueReading: true,
		ReaderFlow: ReaderFlowPaginated, ReaderStyle: ReaderStylePaper,
		ReaderFontSize:    DefaultReaderFontSize,
		ReaderColumnWidth: DefaultReaderColumnWidth,
		ReaderLineHeight:  DefaultReaderLineHeight,
	}
	var showContinueReading int
	err := queryer.QueryRow(`
		SELECT theme, show_continue_reading, COALESCE(time_zone, ''),
			reader_flow, reader_style, reader_font_size, reader_column_width, reader_line_height, updated_at
		FROM user_settings
		WHERE user_id = ?
	`, userID).Scan(&settings.Theme, &showContinueReading, &settings.TimeZone,
		&settings.ReaderFlow, &settings.ReaderStyle, &settings.ReaderFontSize, &settings.ReaderColumnWidth, &settings.ReaderLineHeight, &settings.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user settings: %w", err)
	}
	settings.ShowContinueReading = showContinueReading != 0
	return settings, nil
}

func (db *DB) SaveUserSettings(ctx context.Context, userID int64, patch UserSettingsPatch) (*UserSettings, error) {
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

	if patch.ReaderFlow != nil && *patch.ReaderFlow != ReaderFlowPaginated && *patch.ReaderFlow != ReaderFlowScrolled {
		return nil, errorWithDetail(ErrInvalidUserSettings, "reader flow must be paginated or scrolled")
	}
	if patch.ReaderStyle != nil && *patch.ReaderStyle != ReaderStyleOriginal && *patch.ReaderStyle != ReaderStylePaper && *patch.ReaderStyle != ReaderStyleCustom {
		return nil, errorWithDetail(ErrInvalidUserSettings, "reader style must be original, paper, or custom")
	}
	if patch.ReaderFontSize != nil && (*patch.ReaderFontSize < -4 || *patch.ReaderFontSize > 6) {
		return nil, errorWithDetail(ErrInvalidUserSettings, "reader font size must be between -4 and 6 (0 is the default size)")
	}
	if patch.ReaderColumnWidth != nil && (*patch.ReaderColumnWidth < 560 || *patch.ReaderColumnWidth > 920) {
		return nil, errorWithDetail(ErrInvalidUserSettings, "reader column width must be between 560 and 920 CSS pixels")
	}
	if patch.ReaderLineHeight != nil && (math.IsNaN(*patch.ReaderLineHeight) || *patch.ReaderLineHeight < 1.2 || *patch.ReaderLineHeight > 2.2) {
		return nil, errorWithDetail(ErrInvalidUserSettings, "reader line height must be a multiplier between 1.2 and 2.2")
	}

	// Apply only submitted fields, so saves from different tabs cannot replace
	// unrelated settings. Automatic zone detection only fills an unset zone.
	if _, err := db.Write(ctx).Exec(`
		INSERT INTO user_settings (user_id, theme, show_continue_reading, time_zone,
			reader_flow, reader_style, reader_font_size, reader_column_width, reader_line_height, updated_at)
		VALUES (?, COALESCE(?, 'system'), COALESCE(?, 1), ?,
			COALESCE(?, 'paginated'), COALESCE(?, 'paper'), COALESCE(?, 0),
			COALESCE(?, 760), COALESCE(?, 1.72), unixepoch())
		ON CONFLICT(user_id) DO UPDATE SET
			theme = COALESCE(?, user_settings.theme),
			show_continue_reading = COALESCE(?, user_settings.show_continue_reading),
			time_zone = CASE WHEN ? THEN COALESCE(user_settings.time_zone, excluded.time_zone)
				ELSE COALESCE(excluded.time_zone, user_settings.time_zone) END,
			reader_flow = COALESCE(?, user_settings.reader_flow),
			reader_style = COALESCE(?, user_settings.reader_style),
			reader_font_size = COALESCE(?, user_settings.reader_font_size),
			reader_column_width = COALESCE(?, user_settings.reader_column_width),
			reader_line_height = COALESCE(?, user_settings.reader_line_height),
			updated_at = unixepoch()
	`, userID, patch.Theme, patch.ShowContinueReading, patch.TimeZone,
		patch.ReaderFlow, patch.ReaderStyle, patch.ReaderFontSize, patch.ReaderColumnWidth, patch.ReaderLineHeight,
		patch.Theme, patch.ShowContinueReading, patch.InitializeTimeZone,
		patch.ReaderFlow, patch.ReaderStyle, patch.ReaderFontSize, patch.ReaderColumnWidth, patch.ReaderLineHeight); err != nil {
		return nil, fmt.Errorf("save user settings: %w", err)
	}
	return GetUserSettings(db.Read(ctx), userID)
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
