package web

import (
	"errors"
	"net/http"

	"github.com/levmv/polka/internal/db"
)

type UserSettingsDTO struct {
	Theme               string  `json:"theme"`
	ShowContinueReading bool    `json:"show_continue_reading"`
	TimeZone            string  `json:"time_zone"`
	ReaderFlow          string  `json:"reader_flow"`
	ReaderStyle         string  `json:"reader_style"`
	ReaderFontSize      int     `json:"reader_font_size"`
	ReaderColumnWidth   int     `json:"reader_column_width"`
	ReaderLineHeight    float64 `json:"reader_line_height"`
	UpdatedAt           int64   `json:"updated_at,omitzero"`
}

type userSettingsRequest struct {
	Theme               *string  `json:"theme"`
	ShowContinueReading *bool    `json:"show_continue_reading"`
	TimeZone            *string  `json:"time_zone"`
	ReaderFlow          *string  `json:"reader_flow"`
	ReaderStyle         *string  `json:"reader_style"`
	ReaderFontSize      *int     `json:"reader_font_size"`
	ReaderColumnWidth   *int     `json:"reader_column_width"`
	ReaderLineHeight    *float64 `json:"reader_line_height"`
	InitializeTimeZone  bool     `json:"initialize_time_zone"`
}

func userSettingsDTO(settings *db.UserSettings) UserSettingsDTO {
	return UserSettingsDTO{
		Theme:               settings.Theme,
		ShowContinueReading: settings.ShowContinueReading,
		TimeZone:            settings.TimeZone,
		ReaderFlow:          settings.ReaderFlow,
		ReaderStyle:         settings.ReaderStyle,
		ReaderFontSize:      settings.ReaderFontSize,
		ReaderColumnWidth:   settings.ReaderColumnWidth,
		ReaderLineHeight:    settings.ReaderLineHeight,
		UpdatedAt:           settings.UpdatedAt,
	}
}

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.db.GetUserSettings(UserID(r.Context()))
	if writeUserSettingsError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, userSettingsDTO(settings))
}

func (s *Server) handleAPISettingsSave(w http.ResponseWriter, r *http.Request) {
	var req userSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}

	settings, err := s.db.SaveUserSettings(UserID(r.Context()), db.UserSettingsPatch{
		Theme:               req.Theme,
		ShowContinueReading: req.ShowContinueReading,
		TimeZone:            req.TimeZone,
		ReaderFlow:          req.ReaderFlow,
		ReaderStyle:         req.ReaderStyle,
		ReaderFontSize:      req.ReaderFontSize,
		ReaderColumnWidth:   req.ReaderColumnWidth,
		ReaderLineHeight:    req.ReaderLineHeight,
		InitializeTimeZone:  req.InitializeTimeZone,
	})
	if writeUserSettingsError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, userSettingsDTO(settings))
}

func writeUserSettingsError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, db.ErrInvalidUserSettings):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		serverError(w, err)
	}
	return true
}
