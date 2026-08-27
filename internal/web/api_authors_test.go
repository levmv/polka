package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIAuthorListPagination(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	s := &Server{db: database, dataDir: dir}

	w := httptest.NewRecorder()
	s.handleAPIAuthorList(w, httptest.NewRequest(http.MethodGet, "/api/authors/list?limit=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("first page status = %d: %s", w.Code, w.Body.String())
	}
	var first AuthorAdminPage
	if err := json.UnmarshalRead(w.Body, &first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Name != "Frank Herbert" || first.NextCursor == "" {
		t.Fatalf("first page = %+v; want Herbert plus cursor", first)
	}

	w = httptest.NewRecorder()
	s.handleAPIAuthorList(w, httptest.NewRequest(
		http.MethodGet,
		"/api/authors/list?limit=1&cursor="+url.QueryEscape(first.NextCursor),
		nil,
	))
	if w.Code != http.StatusOK {
		t.Fatalf("second page status = %d: %s", w.Code, w.Body.String())
	}
	var second AuthorAdminPage
	if err := json.UnmarshalRead(w.Body, &second); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Name != "J.R.R. Tolkien" || second.NextCursor != "" {
		t.Fatalf("second page = %+v; want Tolkien without cursor", second)
	}

	w = httptest.NewRecorder()
	s.handleAPIAuthorList(w, httptest.NewRequest(http.MethodGet, "/api/authors/list?cursor=not-a-cursor", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d; want 400", w.Code)
	}
}

// TestAPIAuthorInfo covers the lookup that powers the book-edit convergence
// prompt: an exact-name hit returns the work count, an unknown name 404s, and a
// missing name parameter is a 400.
func TestAPIAuthorInfo(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "Alice", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/authors/info"+query, nil)
		addSessionCookie(t, s, req, u.ID)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// Known author (seeded on one work).
	w := get("?name=J.R.R.+Tolkien")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var got AuthorAdmin
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "J.R.R. Tolkien" || got.BookCount != 1 || got.SortName != "Tolkien, J.R.R." {
		t.Fatalf("got %+v, want Tolkien / count 1", got)
	}

	// Unknown author → 404 (the prompt simply doesn't fire).
	if w := get("?name=Nobody"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown author status = %d, want 404", w.Code)
	}

	// Missing name → 400.
	if w := get(""); w.Code != http.StatusBadRequest {
		t.Fatalf("missing name status = %d, want 400", w.Code)
	}
}

func TestAPIAuthorMutationMissingAuthor(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	for _, tc := range []struct {
		path string
		body any
	}{
		{path: "/api/authors/rename", body: map[string]string{"old": "Missing", "new": "New"}},
		{path: "/api/authors/sort-name", body: map[string]string{"name": "Missing", "sort_name": "Missing"}},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, tc.path, tc.body))
		if w.Code != http.StatusNotFound {
			t.Errorf("POST %s status = %d, want %d; body: %s", tc.path, w.Code, http.StatusNotFound, w.Body.String())
		}
	}
}

func TestMemberIgnoresStaleShelfScope(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	member := mustUser(t, database, "member", db.RoleMember)
	scopeShelf, err := database.CreateShelf(member.ID, db.ShelfShared, "Kids", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create scope shelf: %v", err)
	}
	if err := database.AddBookToShelf(scopeShelf.ID, "", "w_1"); err != nil {
		t.Fatalf("seed scope shelf: %v", err)
	}
	if _, err := database.Exec(`UPDATE users SET content_scope = 'shelves' WHERE id = ?`, member.ID); err != nil {
		t.Fatalf("force stale member scope: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO user_scope_shelves (user_id, shelf_id) VALUES (?, ?)`, member.ID, scopeShelf.ID); err != nil {
		t.Fatalf("force stale member scope shelf: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := jsonRequest(t, s, member.ID, http.MethodGet, "/api/authors/info?name=Frank+Herbert", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("member author info status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}
