package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIUserSettingsLifecycle(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/settings", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("default settings status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var settings UserSettingsDTO
	if err := json.UnmarshalRead(w.Body, &settings); err != nil {
		t.Fatalf("decode default settings: %v", err)
	}
	if settings.Theme != db.ThemeSystem || settings.HideContinueReading || settings.UpdatedAt != 0 {
		t.Fatalf("default settings = %+v", settings)
	}

	hide := true
	theme := db.ThemeDark
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/settings", userSettingsRequest{
		Theme:               &theme,
		HideContinueReading: &hide,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("save settings status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	settings = UserSettingsDTO{}
	if err := json.UnmarshalRead(w.Body, &settings); err != nil {
		t.Fatalf("decode saved settings: %v", err)
	}
	if settings.Theme != db.ThemeDark || !settings.HideContinueReading || settings.UpdatedAt == 0 {
		t.Fatalf("saved settings = %+v", settings)
	}

	theme = db.ThemeSepia
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/settings", userSettingsRequest{
		Theme: &theme,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("partial settings status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	settings = UserSettingsDTO{}
	if err := json.UnmarshalRead(w.Body, &settings); err != nil {
		t.Fatalf("decode partial settings: %v", err)
	}
	if settings.Theme != db.ThemeSepia || !settings.HideContinueReading {
		t.Fatalf("partial settings = %+v, want sepia and preserved hide flag", settings)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/settings", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("bob settings status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	settings = UserSettingsDTO{}
	if err := json.UnmarshalRead(w.Body, &settings); err != nil {
		t.Fatalf("decode bob settings: %v", err)
	}
	if settings.Theme != db.ThemeSystem || settings.HideContinueReading {
		t.Fatalf("settings leaked across users: %+v", settings)
	}
}

func TestAPIUserSettingsErrors(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "reader", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	theme := "solarized"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/settings", userSettingsRequest{
		Theme: &theme,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid theme status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth settings status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
