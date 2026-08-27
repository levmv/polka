package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAppPageContentSecurityPolicy(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user, err := database.CreateUser("admin", "password", db.RoleAdmin)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const hostileUsername = `</script><script>alert(1)</script>`
	if _, err := database.Exec("UPDATE users SET username = ? WHERE id = ?", hostileUsername, user.ID); err != nil {
		t.Fatalf("set hostile username: %v", err)
	}
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
