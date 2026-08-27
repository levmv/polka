package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okHandler is a stand-in protected handler that records whether it ran.
func okHandler(ran *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthMiddleware(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	s := newTestServer(database, dir)

	check := func(name, path, cookie string, wantStatus int, wantNext bool) {
		t.Helper()
		var ran bool
		h := s.authMiddleware(okHandler(&ran))
		req := httptest.NewRequest("GET", path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Errorf("%s: status = %d, want %d", name, w.Code, wantStatus)
		}
		if ran != wantNext {
			t.Errorf("%s: next ran = %v, want %v", name, ran, wantNext)
		}
	}

	// No users yet: unauthenticated humans go to first-run setup, programmatic
	// clients get 401; the auth entry points and static stay open.
	check("no-user page→setup", "/", "", http.StatusFound, false)
	if loc := redirectLoc(t, s, "/"); loc != "/setup" {
		t.Errorf("no-user redirect = %q, want /setup", loc)
	}
	check("api→401", "/api/books", "", http.StatusUnauthorized, false)
	check("read asset→401", "/read/assets/asset_1", "", http.StatusUnauthorized, false)
	check("opds→401", "/opds", "", http.StatusUnauthorized, false)
	check("kosync→401", "/kosync/dead/users/auth", "", http.StatusUnauthorized, false)
	check("cover→401", "/covers/w_1", "", http.StatusUnauthorized, false)
	check("setup open", "/setup", "", http.StatusOK, true)
	check("static open", "/static/app.js", "", http.StatusOK, true)
	check("manifest open", "/static/manifest.webmanifest", "", http.StatusOK, true)

	// With a user present, unauthenticated humans go to login instead.
	u := mustUser(t, database, "alice", "admin")
	if loc := redirectLoc(t, s, "/"); loc != "/login" {
		t.Errorf("with-user redirect = %q, want /login", loc)
	}

	// A valid session passes through and the request carries the user id.
	sid, _ := s.sessions.issue(u.ID)
	var ran bool
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		if got := UserID(r.Context()); got != u.ID {
			t.Errorf("context user id = %q, want %q", got, u.ID)
		}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !ran {
		t.Error("valid session did not reach handler")
	}
}

func TestAuthMiddlewareBasicAuth(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "alice", "admin")
	s := newTestServer(database, dir)

	for _, path := range []string{"/opds", "/opds/books", "/download/asset_1", "/covers/w_1"} {
		t.Run(path, func(t *testing.T) {
			var ran bool
			h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ran = true
				if got := UserID(r.Context()); got != u.ID {
					t.Errorf("context user id = %q, want %q", got, u.ID)
				}
			}))
			req := httptest.NewRequest("GET", path, nil)
			req.SetBasicAuth("alice", "pw")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
			}
			if !ran {
				t.Fatal("handler did not run")
			}
		})
	}

	var ran bool
	h := s.authMiddleware(okHandler(&ran))
	req := httptest.NewRequest("GET", "/opds", nil)
	req.SetBasicAuth("alice", "wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad basic status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != `Basic realm="polka"` {
		t.Fatalf("WWW-Authenticate = %q, want Basic realm", got)
	}
	if ran {
		t.Fatal("handler ran for bad credentials")
	}
}

func TestPasswordAuthenticationWaitsForSlotAndHonorsContext(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	_ = mustUser(t, database, "alice", "admin")
	s := newTestServer(database, dir)

	slots := s.passwordAuthGate()
	for range cap(slots) {
		slots <- struct{}{}
	}
	defer func() {
		for len(slots) > 0 {
			<-slots
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.authenticatePassword(ctx, "alice", "pw"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("authenticate with full gate error = %v; want context deadline", err)
	}

	<-slots
	user, err := s.authenticatePassword(context.Background(), "alice", "pw")
	if err != nil || user == nil || user.Username != "alice" {
		t.Fatalf("authenticate after slot release = %#v, %v; want alice", user, err)
	}
}

func TestAuthMiddlewareAppToken(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "alice", "admin")
	token, err := database.CreateAppToken(u.ID, "kobo")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	s := newTestServer(database, dir)

	// The token used as the Basic-auth password authenticates on a delivery path,
	// even with an arbitrary username — the token is self-identifying.
	for _, username := range []string{"alice", "whatever"} {
		var ran bool
		h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran = true
			if got := UserID(r.Context()); got != u.ID {
				t.Errorf("context user id = %q, want %q", got, u.ID)
			}
		}))
		req := httptest.NewRequest("GET", "/opds", nil)
		req.SetBasicAuth(username, token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !ran {
			t.Fatalf("token auth (username %q): status=%d ran=%v, want 200/true", username, w.Code, ran)
		}
	}

	var kosyncRan bool
	hKO := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kosyncRan = true
		if got := UserID(r.Context()); got != u.ID {
			t.Errorf("kosync context user id = %q, want %q", got, u.ID)
		}
	}))
	reqKO := httptest.NewRequest("GET", "/kosync/"+token+"/users/auth", nil)
	wKO := httptest.NewRecorder()
	hKO.ServeHTTP(wKO, reqKO)
	if wKO.Code != http.StatusOK || !kosyncRan {
		t.Fatalf("kosync token auth: status=%d ran=%v, want 200/true", wKO.Code, kosyncRan)
	}

	var badKOSyncRan bool
	hBadKO := s.authMiddleware(okHandler(&badKOSyncRan))
	reqBadKO := httptest.NewRequest("GET", "/kosync/not-a-token/users/auth", nil)
	sid, err := s.sessions.issue(u.ID)
	if err != nil {
		t.Fatalf("issue browser session: %v", err)
	}
	// A browser cookie must not bypass the credential embedded in a KOReader URL.
	reqBadKO.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	wBadKO := httptest.NewRecorder()
	hBadKO.ServeHTTP(wBadKO, reqBadKO)
	if wBadKO.Code != http.StatusUnauthorized || badKOSyncRan {
		t.Fatalf("bad kosync token: status=%d ran=%v, want 401/false", wBadKO.Code, badKOSyncRan)
	}

	// The same token must NOT grant the catalog/admin API: /api is cookie-only, so
	// Basic auth (token or password) gets 401 there. This is the delivery-only scope.
	var apiRan bool
	hAPI := s.authMiddleware(okHandler(&apiRan))
	reqAPI := httptest.NewRequest("GET", "/api/books", nil)
	reqAPI.SetBasicAuth("alice", token)
	wAPI := httptest.NewRecorder()
	hAPI.ServeHTTP(wAPI, reqAPI)
	if wAPI.Code != http.StatusUnauthorized {
		t.Fatalf("token on /api: status = %d, want 401", wAPI.Code)
	}
	if apiRan {
		t.Fatal("token reached the /api handler — scope leak")
	}

	// A revoked token stops working.
	if err := database.RevokeAppToken(u.ID, "kobo"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	var ran bool
	h := s.authMiddleware(okHandler(&ran))
	req := httptest.NewRequest("GET", "/opds", nil)
	req.SetBasicAuth("alice", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || ran {
		t.Fatalf("revoked token: status=%d ran=%v, want 401/false", w.Code, ran)
	}

	var revokedKOSyncRan bool
	hRevokedKO := s.authMiddleware(okHandler(&revokedKOSyncRan))
	reqRevokedKO := httptest.NewRequest("GET", "/kosync/"+token+"/users/auth", nil)
	wRevokedKO := httptest.NewRecorder()
	hRevokedKO.ServeHTTP(wRevokedKO, reqRevokedKO)
	if wRevokedKO.Code != http.StatusUnauthorized || revokedKOSyncRan {
		t.Fatalf("revoked kosync token: status=%d ran=%v, want 401/false", wRevokedKO.Code, revokedKOSyncRan)
	}
}

func redirectLoc(t *testing.T, s *Server, path string) string {
	t.Helper()
	h := s.authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w.Header().Get("Location")
}
