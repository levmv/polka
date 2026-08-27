package web

import (
	"errors"
	"net/http"

	"github.com/levmv/polka/internal/db"
)

type UserSettingsDTO struct {
	Theme               string `json:"theme"`
	HideContinueReading bool   `json:"hide_continue_reading"`
	UpdatedAt           int64  `json:"updated_at,omitzero"`
}

type userSettingsRequest struct {
	Theme               *string `json:"theme"`
	HideContinueReading *bool   `json:"hide_continue_reading"`
}

func userSettingsDTO(settings *db.UserSettings) UserSettingsDTO {
	return UserSettingsDTO{
		Theme:               settings.Theme,
		HideContinueReading: settings.HideContinueReading,
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

	settings, err := s.db.GetUserSettings(UserID(r.Context()))
	if writeUserSettingsError(w, err) {
		return
	}
	if req.Theme != nil {
		settings.Theme = *req.Theme
	}
	if req.HideContinueReading != nil {
		settings.HideContinueReading = *req.HideContinueReading
	}

	settings, err = s.db.SaveUserSettings(UserID(r.Context()), *settings)
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
	case errors.Is(err, db.ErrInvalidTheme):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		serverError(w, err)
	}
	return true
}
