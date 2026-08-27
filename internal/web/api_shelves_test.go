package web

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIShelvesManualAndQuery(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "Alice", "admin")

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	createBody := bytes.NewBufferString(`{"name":"Favorites","kind":"manual"}`)
	req := httptest.NewRequest("POST", "/api/shelves", createBody)
	addSessionCookie(t, s, req, u.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create manual status = %d, body: %s", w.Code, w.Body.String())
	}
	var manual ShelfDTO
	if err := json.UnmarshalRead(w.Body, &manual); err != nil {
		t.Fatalf("decode manual shelf: %v", err)
	}
	if manual.Kind != "manual" || manual.Name != "Favorites" {
		t.Fatalf("manual shelf = %+v", manual)
	}

	req = httptest.NewRequest("PUT", "/api/shelves/"+manual.ID+"/books/w_1", nil)
	addSessionCookie(t, s, req, u.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("add book status = %d, body: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/books?shelf="+manual.ID, nil)
	addSessionCookie(t, s, req, u.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("manual shelf books status = %d, body: %s", w.Code, w.Body.String())
	}
	var books []BookSummaryDTO
	if err := json.UnmarshalRead(w.Body, &books); err != nil {
		t.Fatalf("decode manual shelf books: %v", err)
	}
	if len(books) != 1 || books[0].ID != "w_1" {
		t.Fatalf("manual shelf books = %+v, want w_1 only", books)
	}

	req = httptest.NewRequest("GET", "/api/books/w_1/shelves", nil)
	addSessionCookie(t, s, req, u.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("book shelves status = %d, body: %s", w.Code, w.Body.String())
	}
	var memberships []BookShelfDTO
	if err := json.UnmarshalRead(w.Body, &memberships); err != nil {
		t.Fatalf("decode memberships: %v", err)
	}
	if len(memberships) != 1 || memberships[0].ID != manual.ID || !memberships[0].InShelf {
		t.Fatalf("memberships = %+v, want manual shelf checked", memberships)
	}

	createBody = bytes.NewBufferString(`{"name":"Hobbit search","kind":"query","query":"Hobbit"}`)
	req = httptest.NewRequest("POST", "/api/shelves", createBody)
	addSessionCookie(t, s, req, u.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create query status = %d, body: %s", w.Code, w.Body.String())
	}
	var query ShelfDTO
	if err := json.UnmarshalRead(w.Body, &query); err != nil {
		t.Fatalf("decode query shelf: %v", err)
	}

	req = httptest.NewRequest("GET", "/api/books?shelf="+query.ID, nil)
	addSessionCookie(t, s, req, u.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("query shelf books status = %d, body: %s", w.Code, w.Body.String())
	}
	books = nil
	if err := json.UnmarshalRead(w.Body, &books); err != nil {
		t.Fatalf("decode query shelf books: %v", err)
	}
	if len(books) != 1 || books[0].ID != "w_1" {
		t.Fatalf("query shelf books = %+v, want Hobbit/w_1 only", books)
	}
}

func TestAPIShelfUpdateQueryAndVisibility(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "admin", db.RoleAdmin)
	reader := mustUser(t, database, "reader", db.RoleReader)
	otherAdmin := mustUser(t, database, "other-admin", db.RoleAdmin)
	queryShelf, err := database.CreateShelf(admin.ID, db.ShelfShared, "Hobbit search", db.ShelfQuery, "Hobbit")
	if err != nil {
		t.Fatalf("create query shelf: %v", err)
	}
	deleteShelf, err := database.CreateShelf(admin.ID, db.ShelfShared, "Delete me", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create delete shelf: %v", err)
	}
	privateShelf, err := database.CreateShelf(reader.ID, db.ShelfPersonal, "Mine", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create private shelf: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, otherAdmin.ID, http.MethodPatch, "/api/shelves/"+queryShelf.ID, map[string]any{
		"shared": false,
	}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-owner visibility change status = %d, want %d; body: %s", w.Code, http.StatusForbidden, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, otherAdmin.ID, http.MethodDelete, "/api/shelves/"+deleteShelf.ID, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-owner delete status = %d, want %d; body: %s", w.Code, http.StatusForbidden, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodDelete, "/api/shelves/"+deleteShelf.ID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("owner delete status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPatch, "/api/shelves/"+queryShelf.ID, map[string]any{
		"name":   "Dune search",
		"query":  "Dune",
		"shared": false,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("update query shelf status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var updated ShelfDTO
	if err := json.UnmarshalRead(w.Body, &updated); err != nil {
		t.Fatalf("decode updated shelf: %v", err)
	}
	if updated.Name != "Dune search" || updated.Query != "Dune" || updated.OwnerID != admin.ID || updated.Visibility != string(db.ShelfPersonal) {
		t.Fatalf("updated shelf = %+v", updated)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodGet, "/api/books?shelf="+queryShelf.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("updated query books status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var books []BookSummaryDTO
	if err := json.UnmarshalRead(w.Body, &books); err != nil {
		t.Fatalf("decode updated query books: %v", err)
	}
	if len(books) != 1 || books[0].ID != "w_2" {
		t.Fatalf("updated query books = %+v, want Dune/w_2 only", books)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, reader.ID, http.MethodPatch, "/api/shelves/"+privateShelf.ID, map[string]any{
		"name":   "Shared mine",
		"shared": true,
	}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("reader share shelf status = %d, want %d; body: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestReaderCannotMutateSharedShelfOrAddOutOfScopeBook(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	reader := mustUser(t, database, "reader", db.RoleReader)
	scopeShelf, err := database.CreateShelf(reader.ID, db.ShelfShared, "Kids", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create scope shelf: %v", err)
	}
	if err := database.AddBookToShelf(scopeShelf.ID, "", "w_1"); err != nil {
		t.Fatalf("seed scope shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(reader.ID, db.UserAccess{
		Role:         db.RoleReader,
		ContentScope: db.ContentScopeShelves,
		ShelfIDs:     []string{scopeShelf.ID},
	}); err != nil {
		t.Fatalf("scope reader: %v", err)
	}
	privateShelf, err := database.CreateShelf(reader.ID, db.ShelfPersonal, "Mine", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create private shelf: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	for _, tc := range []struct {
		method string
		path   string
		body   any
		want   int
	}{
		{http.MethodPatch, "/api/shelves/" + scopeShelf.ID, map[string]string{"name": "Renamed"}, http.StatusForbidden},
		{http.MethodDelete, "/api/shelves/" + scopeShelf.ID, nil, http.StatusForbidden},
		{http.MethodPut, "/api/shelves/" + scopeShelf.ID + "/books/w_1", nil, http.StatusForbidden},
		{http.MethodPut, "/api/shelves/" + privateShelf.ID + "/books/w_1", nil, http.StatusNoContent},
		{http.MethodPut, "/api/shelves/" + privateShelf.ID + "/books/w_2", nil, http.StatusNotFound},
	} {
		req := jsonRequest(t, s, reader.ID, tc.method, tc.path, tc.body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s %s status = %d, want %d; body: %s", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
	}

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM shelf_books WHERE shelf_id = ? AND work_id = 'w_2'`, privateShelf.ID).Scan(&count); err != nil {
		t.Fatalf("count private shelf w_2: %v", err)
	}
	if count != 0 {
		t.Fatalf("out-of-scope book was added to private shelf")
	}
	var name string
	if err := database.QueryRow(`SELECT name FROM shelves WHERE id = ?`, scopeShelf.ID).Scan(&name); err != nil {
		t.Fatalf("read shared shelf name: %v", err)
	}
	if name != "Kids" {
		t.Fatalf("shared shelf was renamed to %q", name)
	}
}

func TestAPIShelvesSharedCreationAndScopedVisibility(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "admin", db.RoleAdmin)
	reader := mustUser(t, database, "reader", db.RoleReader)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/shelves", map[string]any{
		"name":   "Kids",
		"kind":   "manual",
		"shared": true,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create shared status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var kids ShelfDTO
	if err := json.UnmarshalRead(w.Body, &kids); err != nil {
		t.Fatalf("decode kids shelf: %v", err)
	}
	if kids.OwnerID != admin.ID || kids.Visibility != string(db.ShelfShared) {
		t.Fatalf("shared shelf = %+v, want owner %q and shared visibility", kids, admin.ID)
	}
	if err := database.AddBookToShelf(kids.ID, "", "w_1"); err != nil {
		t.Fatalf("seed kids shelf: %v", err)
	}

	other, err := database.CreateShelf(admin.ID, db.ShelfShared, "Adults", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create other shared shelf: %v", err)
	}
	private, err := database.CreateShelf(reader.ID, db.ShelfPersonal, "Mine", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create private shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(reader.ID, db.UserAccess{
		Role:         db.RoleReader,
		ContentScope: db.ContentScopeShelves,
		ShelfIDs:     []string{kids.ID},
	}); err != nil {
		t.Fatalf("scope reader: %v", err)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, reader.ID, http.MethodPost, "/api/shelves", map[string]any{
		"name":   "Shared by reader",
		"kind":   "manual",
		"shared": true,
	}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("reader shared create status = %d, want %d", w.Code, http.StatusForbidden)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, reader.ID, http.MethodGet, "/api/shelves", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list scoped shelves status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var shelves []ShelfDTO
	if err := json.UnmarshalRead(w.Body, &shelves); err != nil {
		t.Fatalf("decode scoped shelves: %v", err)
	}
	got := shelfDTOIDs(shelves)
	if len(got) != 2 || got[0] != kids.ID || got[1] != private.ID {
		t.Fatalf("scoped shelves = %+v, want kids and private", got)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, reader.ID, http.MethodGet, "/api/books?shelf="+other.ID, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unassigned shelf books status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func shelfDTOIDs(shelves []ShelfDTO) []string {
	ids := make([]string, 0, len(shelves))
	for _, shelf := range shelves {
		ids = append(ids, shelf.ID)
	}
	return ids
}
