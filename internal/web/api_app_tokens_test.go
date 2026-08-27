package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIAppTokensLifecycle(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/app-tokens", appTokenCreateRequest{Name: " KOReader "}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var created appTokenCreateDTO
	if err := json.UnmarshalRead(w.Body, &created); err != nil {
		t.Fatalf("decode created token: %v", err)
	}
	if created.Name != "KOReader" || created.Token == "" {
		t.Fatalf("created = %+v; want trimmed name and one-time token", created)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/app-tokens", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var tokens []AppTokenDTO
	if err := json.UnmarshalRead(w.Body, &tokens); err != nil {
		t.Fatalf("decode tokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "KOReader" || tokens[0].ID == "" {
		t.Fatalf("tokens = %+v; want one KOReader token", tokens)
	}
	if stringsContains(w.Body.String(), created.Token) {
		t.Fatalf("raw token leaked in list response: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/app-tokens", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("bob list status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var bobTokens []AppTokenDTO
	if err := json.UnmarshalRead(w.Body, &bobTokens); err != nil {
		t.Fatalf("decode bob tokens: %v", err)
	}
	if len(bobTokens) != 0 {
		t.Fatalf("tokens leaked across users: %+v", bobTokens)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodDelete, "/api/app-tokens/"+tokens[0].ID, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob delete status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodDelete, "/api/app-tokens/"+tokens[0].ID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/app-tokens", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list after delete status = %d, want %d", w.Code, http.StatusOK)
	}
	tokens = nil
	if err := json.UnmarshalRead(w.Body, &tokens); err != nil {
		t.Fatalf("decode tokens after delete: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("tokens after delete = %+v; want none", tokens)
	}
}

func TestAPIAppTokensErrors(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "alice", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/app-tokens", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth list status = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/app-tokens", appTokenCreateRequest{Name: "   "}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("blank create status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/app-tokens", appTokenCreateRequest{Name: "Reader"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, want %d", w.Code, http.StatusCreated)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/app-tokens", appTokenCreateRequest{Name: "Reader"}))
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, want %d", w.Code, http.StatusConflict)
	}

	if _, err := database.Exec(`
		CREATE TRIGGER fail_broken_token
		BEFORE INSERT ON app_tokens WHEN NEW.name = 'Broken'
		BEGIN SELECT RAISE(ABORT, 'forced token insert failure'); END
	`); err != nil {
		t.Fatalf("create failing token trigger: %v", err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/app-tokens", appTokenCreateRequest{Name: "Broken"}))
	if w.Code != http.StatusInternalServerError || strings.TrimSpace(w.Body.String()) != "Internal server error" {
		t.Fatalf("unexpected persistence error = %d %q; want generic 500", w.Code, w.Body.String())
	}
}

func stringsContains(s, substr string) bool {
	return substr != "" && strings.Contains(s, substr)
}
