package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
)

func TestSetupConcurrentRequestsCreateOneAdmin(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, username := range []string{"alice", "bob"} {
		body := &setupRequestBody{
			Reader:  strings.NewReader("username=" + username + "&password=secret&confirm=secret"),
			entered: entered, release: release,
		}
		req := httptest.NewRequest(http.MethodPost, "/setup", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		go func() {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			responses <- w
		}()
	}
	// Both requests have passed the initial empty-library check before either
	// can finish reading its form and create an account.
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("setup request did not reach its form")
		}
	}
	unblock()
	sessions := 0
	for range 2 {
		select {
		case w := <-responses:
			if w.Code != http.StatusFound {
				t.Errorf("setup status = %d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Set-Cookie") != "" {
				sessions++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("setup did not finish")
		}
	}
	if n, err := db.CountUsers(database.Read(t.Context())); err != nil || n != 1 || sessions != 1 {
		t.Fatalf("users = %d, sessions = %d, err = %v; want one initial admin and session", n, sessions, err)
	}
}

type setupRequestBody struct {
	*strings.Reader
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *setupRequestBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		b.entered <- struct{}{}
		<-b.release
	})
	return b.Reader.Read(p)
}

func TestSetupCrossOriginProtection(t *testing.T) {
	for _, tc := range []struct {
		name, origin, fetchSite string
		allowed                 bool
	}{
		{name: "same origin", origin: "https://polka.example", fetchSite: "same-origin", allowed: true},
		{name: "cross site", origin: "https://other.example", fetchSite: "cross-site"},
		{name: "foreign origin fallback", origin: "https://other.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, dir := setupTestDB(t)
			defer database.Close()
			s := newTestServer(database, dir)
			req := httptest.NewRequest(http.MethodPost, "https://polka.example/setup", strings.NewReader("username=admin&password=secret&confirm=secret"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			w := httptest.NewRecorder()
			testRoutes(t, s).ServeHTTP(w, req)
			wantStatus, wantUsers := http.StatusForbidden, 0
			if tc.allowed {
				wantStatus, wantUsers = http.StatusFound, 1
			}
			if w.Code != wantStatus {
				t.Errorf("setup status = %d, want %d: %s", w.Code, wantStatus, w.Body.String())
			}
			if n, err := db.CountUsers(database.Read(req.Context())); err != nil || n != wantUsers {
				t.Fatalf("users = %d, err = %v; want %d", n, err, wantUsers)
			}
		})
	}
}

func TestAppPageContentSecurityPolicy(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "admin", db.RoleAdmin)
	const hostileUsername = `</script><script>alert(1)</script>`
	mustExec(t, database, "UPDATE users SET username = ? WHERE id = ?", hostileUsername, user.ID)

	s := &Server{db: database, dataDir: dir}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUserID(req.Context(), user.ID))
	w := httptest.NewRecorder()

	s.handleApp(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("app page status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"content_scope":"all"`) {
		t.Fatalf("app bootstrap omits current-user content scope: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), hostileUsername) || !strings.Contains(w.Body.String(), `\u003c/script\u003e`) {
		t.Fatalf("app bootstrap is not safe for an HTML script context: %s", w.Body.String())
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"https://covers.openlibrary.org",
		"https://books.google.com",
		"https://books.googleusercontent.com",
		"https://archive.org",
		"https://*.archive.org",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("app Content-Security-Policy = %q; missing %q", csp, directive)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("app Content-Security-Policy permits inline scripts: %q", csp)
	}
}
