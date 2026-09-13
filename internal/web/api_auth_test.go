package web

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/workslot"
)

func TestAPIMe(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "alice", db.RoleMember)

	s := newTestServer(database, dir)
	sid, err := s.sessions.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	handler := testRoutes(t, s)
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest("GET", "/api/me", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauthenticated.Code)
	}

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var got MeDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got != meDTO(*u) {
		t.Fatalf("me = %+v, want %+v", got, meDTO(*u))
	}
}

func TestLogoutRequiresSameOriginPost(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "alice", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	sid, err := s.sessions.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	getReq := httptest.NewRequest("GET", "/logout", nil)
	getReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /logout status = %d, want %d", getRec.Code, http.StatusMethodNotAllowed)
	}
	assertSessionLive(t, s.sessions, sid, true)

	crossReq := httptest.NewRequest("POST", "/logout", nil)
	crossReq.Header.Set("Origin", "https://other.example")
	crossReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	crossRec := httptest.NewRecorder()
	handler.ServeHTTP(crossRec, crossReq)
	if crossRec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin logout status = %d, want 403", crossRec.Code)
	}
	assertSessionLive(t, s.sessions, sid, true)

	postReq := httptest.NewRequest("POST", "/logout", nil)
	postReq.Header.Set("Origin", "http://example.com")
	postReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusFound {
		t.Fatalf("POST /logout status = %d, want %d", postRec.Code, http.StatusFound)
	}
	if loc := postRec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("POST /logout Location = %q, want /login", loc)
	}
	assertSessionLive(t, s.sessions, sid, false)
}

func testRoutes(t *testing.T, s *Server) http.Handler {
	t.Helper()
	if s.storageQueue == nil {
		s.storageQueue = workslot.New()
	}
	if s.background == nil {
		s.background = newTaskGroup(context.Background())
		t.Cleanup(s.background.Stop)
	}
	if s.sessions == nil {
		s.sessions = newSessionStore(s.db)
	}
	mux, err := s.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	return opdsProgressionMiddleware(s.authMiddleware(mux))
}

func addSessionCookie(t *testing.T, s *Server, req *http.Request, userID int64) {
	t.Helper()
	sid, err := s.sessions.issue(t.Context(), userID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
}
