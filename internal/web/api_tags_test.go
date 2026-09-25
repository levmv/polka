package web

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPITagRenameMergeDelete(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	member := mustUser(t, database, "member", db.RoleMember)
	reader := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(t, database, dir)
	handler := testRoutes(t, s)
	request := func(method, path string, body any) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, member.ID, method, path, body))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	assertTags := func(id int64, want string, rev int) {
		t.Helper()
		if got := bookTags(t, database, id); got != want {
			t.Fatalf("book %d tags = %q; want %q", id, got, want)
		}
		if got := bookMetadataRev(t, database, id); got != rev {
			t.Fatalf("book %d metadata_rev = %d; want %d", id, got, rev)
		}
	}
	tagPath := func(name string) string {
		t.Helper()
		var id int64
		if err := database.Read(t.Context()).QueryRow("SELECT id FROM tags WHERE kind = 'tag' AND name = ?", name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("/api/tags/%d", id)
	}
	assertBooks := func(scope db.VisibilityScope, query string, want ...int64) {
		t.Helper()
		books, err := db.ListBooks(database.Read(t.Context()), scope, reader.ID, query, db.SortTitle, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, book := range books {
			got = append(got, book.ID)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("query %q books = %v; want %v", query, got, want)
		}
	}

	request(http.MethodPatch, "/api/books/1", map[string]string{"tags": "Sci-Fi, Favourites, Speculative"})
	request(http.MethodPatch, "/api/books/2", map[string]string{"tags": "sci-fi, SCI-FI"})
	assertTags(2, "Sci-Fi", 1)
	request(http.MethodPatch, "/api/books/2", map[string]string{"tags": "SCI-FI"})
	assertTags(2, "Sci-Fi", 1) // A book edit cannot rename a shared tag.

	mustExec(t, database, "INSERT INTO books (id, title, sort_title, deleted_at) VALUES (3, 'Trash', 'Trash', 1)")
	mustSetTags(t, database, 3, "sci-fi, Trash-only")
	shelf, err := database.CreateShelf(t.Context(), member.ID, db.ShelfShared, "Science fiction", db.ShelfQuery, `tag:"sci-fi"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), reader.ID, db.UserAccess{
		Role: db.RoleReader, ContentScope: db.ContentScopeShelves, ShelfIDs: []int64{shelf.ID},
	}); err != nil {
		t.Fatal(err)
	}
	scope := db.VisibilityScope{UserID: reader.ID, ContentScope: db.ContentScopeShelves}
	assertBooks(scope, "", 1, 2)

	request(http.MethodPatch, tagPath("Sci-Fi"), map[string]string{"name": "Science Fiction"})
	assertTags(1, "Science Fiction, Favourites, Speculative", 2)
	assertTags(2, "Science Fiction", 2)
	assertTags(3, "Science Fiction, Trash-only", 1)
	assertBooks(db.FullVisibilityScope(), `tag:"sci-fi"`)
	assertBooks(db.FullVisibilityScope(), `tag:"Science Fiction"`, 1, 2)
	assertBooks(scope, "", 1, 2) // Both saved query and access projection follow the rename.
	saved, err := db.GetShelf(database.Read(t.Context()), shelf.ID, member.ID)
	if err != nil || saved.Query != `tag:"Science Fiction"` {
		t.Fatalf("renamed shelf = %+v, err = %v", saved, err)
	}

	request(http.MethodPatch, tagPath("Science Fiction"), map[string]string{"name": "speculative"})
	assertTags(1, "Speculative, Favourites", 3) // Keep the first position when both tags were present.
	assertTags(2, "Speculative", 3)
	assertTags(3, "Speculative, Trash-only", 2)
	assertBooks(scope, "", 1, 2)
	snapshot, err := db.LoadMetadataWritebackSnapshot(database.Read(t.Context()), 1)
	if err != nil || !slices.Equal(snapshot.Metadata.Tags, []string{"Speculative", "Favourites"}) || snapshot.MetadataRev != 3 {
		t.Fatalf("write-back snapshot = %+v, err = %v", snapshot, err)
	}

	request(http.MethodDelete, tagPath("Speculative"), nil)
	assertTags(1, "Favourites", 4)
	assertTags(2, "", 4)
	assertTags(3, "Trash-only", 3)
	assertBooks(db.FullVisibilityScope(), "no:tags", 2)
	assertBooks(db.FullVisibilityScope(), `tag:"Speculative"`)
	assertBooks(scope, "")
}

func TestAPITagListVisibleCountsAndPagination(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	owner := mustUser(t, database, "owner", db.RoleMember)
	reader := mustUser(t, database, "reader", db.RoleReader)
	mustSetTags(t, database, 1, "Alpha, Shared")
	mustSetTags(t, database, 2, "shared, Hidden")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, deleted_at) VALUES (3, 'Trash', 'Trash', 1)")
	mustSetTags(t, database, 3, "Shared, Trash-only")
	shelf, err := database.CreateShelf(t.Context(), owner.ID, db.ShelfShared, "Visible", db.ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AddBookToShelf(t.Context(), shelf.ID, owner.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), reader.ID, db.UserAccess{
		Role: db.RoleReader, ContentScope: db.ContentScopeShelves, ShelfIDs: []int64{shelf.ID},
	}); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, database, dir)
	handler := testRoutes(t, s)
	type page struct {
		Items      []TagSummary `json:"items"`
		NextCursor string       `json:"next_cursor"`
	}
	get := func(userID int64, query string) page {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, userID, http.MethodGet, "/api/tags/list?"+query, nil))
		var got page
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		if err := json.UnmarshalRead(w.Body, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := get(reader.ID, "limit=1")
	if len(first.Items) != 1 || first.Items[0].Name != "Alpha" || first.Items[0].BookCount != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second := get(reader.ID, "limit=1&cursor="+url.QueryEscape(first.NextCursor))
	if len(second.Items) != 1 || second.Items[0].Name != "Shared" || second.Items[0].BookCount != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
	full := get(owner.ID, "q=SHAR")
	if len(full.Items) != 1 || full.Items[0].Name != "Shared" || full.Items[0].BookCount != 2 {
		t.Fatalf("full-scope count = %+v", full)
	}
}
