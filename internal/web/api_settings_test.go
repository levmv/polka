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
	defaults := UserSettingsDTO{
		Theme: db.ThemeSystem, ShowContinueReading: true, ReaderFlow: db.ReaderFlowPaginated,
		ReaderStyle: db.ReaderStylePaper, ReaderColumnWidth: 760, ReaderLineHeight: 1.72,
	}
	if settings != defaults {
		t.Fatalf("default settings = %+v", settings)
	}

	show := false
	theme := db.ThemeDark
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/settings", userSettingsRequest{
		Theme:               &theme,
		ShowContinueReading: &show,
		TimeZone:            new("UTC"),
		ReaderFlow:          new(db.ReaderFlowScrolled),
		ReaderStyle:         new(db.ReaderStyleCustom),
		ReaderFontSize:      new(2),
		ReaderColumnWidth:   new(820),
		ReaderLineHeight:    new(1.9),
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("save settings status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	settings = UserSettingsDTO{}
	if err := json.UnmarshalRead(w.Body, &settings); err != nil {
		t.Fatalf("decode saved settings: %v", err)
	}
	want := UserSettingsDTO{
		Theme: db.ThemeDark, ShowContinueReading: false, TimeZone: "UTC",
		ReaderFlow: db.ReaderFlowScrolled, ReaderStyle: db.ReaderStyleCustom, ReaderFontSize: 2,
		ReaderColumnWidth: 820, ReaderLineHeight: 1.9, UpdatedAt: settings.UpdatedAt,
	}
	if settings != want || settings.UpdatedAt == 0 {
		t.Fatalf("saved settings = %+v", settings)
	}

	theme = db.ThemeSepia
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/settings", userSettingsRequest{
		Theme: &theme, ReaderFontSize: new(0),
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("partial settings status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	settings = UserSettingsDTO{}
	if err := json.UnmarshalRead(w.Body, &settings); err != nil {
		t.Fatalf("decode partial settings: %v", err)
	}
	want.Theme, want.ReaderFontSize, want.UpdatedAt = db.ThemeSepia, 0, settings.UpdatedAt
	if settings != want {
		t.Fatalf("partial settings = %+v, want sepia and preserved visibility flag", settings)
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
	if settings != defaults {
		t.Fatalf("settings leaked across users: %+v", settings)
	}
}

func TestAPIUserSettingsErrors(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "reader", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	for _, patch := range []userSettingsRequest{
		{Theme: new("solarized")}, {TimeZone: new("Local")}, {ReaderFlow: new("sideways")},
		{ReaderStyle: new("neon")}, {ReaderFontSize: new(12)}, {ReaderColumnWidth: new(200)}, {ReaderLineHeight: new(3.0)},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/settings", patch))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid patch %+v status = %d; body: %s", patch, w.Code, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth settings status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
