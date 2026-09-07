package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	var created AppTokenDTO
	if err := json.UnmarshalRead(w.Body, &created); err != nil {
		t.Fatalf("decode created token: %v", err)
	}
	if created.Name != "KOReader" || created.Token == "" || created.ID <= 0 || created.CreatedAt == 0 {
		t.Fatalf("created = %+v; want saved token with trimmed name", created)
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
	if len(tokens) != 1 || tokens[0] != created {
		t.Fatalf("tokens = %+v; want one KOReader token", tokens)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("credential cache control = %q", got)
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
	req := httptest.NewRequest(http.MethodGet, "/api/app-tokens", nil)
	req.SetBasicAuth("polka", created.Token)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("device credential can retrieve credentials: %d", w.Code)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodDelete, "/api/app-tokens/"+strconv.FormatInt(tokens[0].ID, 10), nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob delete status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodDelete, "/api/app-tokens/"+strconv.FormatInt(tokens[0].ID, 10), nil))
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
	mustExec(t, database, `
		CREATE TRIGGER fail_broken_token
		BEFORE INSERT ON app_tokens WHEN NEW.name = 'Broken'
		BEGIN SELECT RAISE(ABORT, 'forced token insert failure'); END
	`)

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/app-tokens", appTokenCreateRequest{Name: "Broken"}))
	if w.Code != http.StatusInternalServerError || strings.TrimSpace(w.Body.String()) != "Internal server error" {
		t.Fatalf("unexpected persistence error = %d %q; want generic 500", w.Code, w.Body.String())
	}
}
