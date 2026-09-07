package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func newBulkTestServer(t *testing.T) *db.DB {
	t.Helper()
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func insertBook(t *testing.T, database *db.DB, id int64, title string) {
	t.Helper()
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, ?, ?)", id, title, title)

}

func callBulkEdit(t *testing.T, database *db.DB, dataDir string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	srv := &Server{db: database, dataDir: dataDir}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/books/bulk", bytes.NewBuffer(raw))
	rr := httptest.NewRecorder()
	srv.handleAPIBulkEdit(rr, req)
	return rr
}

func bookTags(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	var tags sql.NullString
	if err := database.Read(t.Context()).QueryRow("SELECT tags FROM books WHERE id = ?", id).Scan(&tags); err != nil {
		t.Fatalf("query tags %d: %v", id, err)
	}
	return tags.String
}

func setBookAuthors(t *testing.T, database *db.DB, id int64, authors string) {
	t.Helper()
	if err := database.Transact(context.Background(), func(tx *db.Tx) error {
		return replaceBookAuthors(t.Context(), tx, id, authors)
	}); err != nil {
		t.Fatalf("set authors %d: %v", id, err)
	}
}

func bookAuthors(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	byBook, err := db.AuthorsByBookIDs(database.Read(t.Context()), []int64{id})
	if err != nil {
		t.Fatalf("authors %d: %v", id, err)
	}
	return formatAuthorRows(byBook[id])
}

func bookOverrides(t *testing.T, database *db.DB, id int64) map[string]bool {
	t.Helper()
	var raw sql.NullString
	if err := database.Read(t.Context()).QueryRow("SELECT manual_overrides FROM books WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatalf("query overrides %d: %v", id, err)
	}
	m := make(map[string]bool)
	if raw.String != "" {
		json.Unmarshal([]byte(raw.String), &m)
	}
	return m
}

func bookMetadataRev(t *testing.T, database *db.DB, id int64) int {
	t.Helper()
	var rev int
	if err := database.Read(t.Context()).QueryRow("SELECT metadata_rev FROM books WHERE id = ?", id).Scan(&rev); err != nil {
		t.Fatalf("query metadata_rev %d: %v", id, err)
	}
	return rev
}

func TestBulkEditTagsAdd(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()

	insertBook(t, database, 1, "One")
	insertBook(t, database, 2, "Two")
	mustExec(t, database, "UPDATE books SET tags = ? WHERE id = ?", "sci-fi", 1)

	rr := callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []int64{1, 2},
		"operations": []map[string]any{
			{"type": "tags", "mode": "add", "values": []string{"sci-fi", "classic"}},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}

	var resp bulkEditResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Selected != 2 || resp.Changed != 2 {
		t.Errorf("counts: selected=%d changed=%d, want 2/2", resp.Selected, resp.Changed)
	}
	// w1 already had "sci-fi" (only "classic" is new); w2 gets both.
	if got := bookTags(t, database, 1); got != "sci-fi, classic" {
		t.Errorf("1 tags = %q, want %q", got, "sci-fi, classic")
	}
	if got := bookTags(t, database, 2); got != "sci-fi, classic" {
		t.Errorf("2 tags = %q, want %q", got, "sci-fi, classic")
	}
	if !bookOverrides(t, database, 1)["tags"] {
		t.Errorf("1 missing tags override")
	}
	if got := bookMetadataRev(t, database, 1); got != 1 {
		t.Errorf("1 metadata_rev = %d, want 1", got)
	}
	if got := bookMetadataRev(t, database, 2); got != 1 {
		t.Errorf("2 metadata_rev = %d, want 1", got)
	}
}

func TestBulkEditTagsAddNoOpIsUnchanged(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()

	insertBook(t, database, 1, "One")
	mustExec(t, database, "UPDATE books SET tags = ? WHERE id = ?", "sci-fi, classic", 1)

	rr := callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []int64{1},
		"operations": []map[string]any{
			{"type": "tags", "mode": "add", "values": []string{"Sci-Fi"}},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp bulkEditResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Changed != 0 || resp.Unchanged != 1 {
		t.Errorf("counts: changed=%d unchanged=%d, want 0/1", resp.Changed, resp.Unchanged)
	}
	// Adding a tag that is already present (case-insensitively) must not churn.
	if bookOverrides(t, database, 1)["tags"] {
		t.Errorf("no-op add should not set tags override")
	}
	if got := bookMetadataRev(t, database, 1); got != 0 {
		t.Errorf("no-op metadata_rev = %d, want 0", got)
	}
}

func TestBulkEditTagsRemoveAndClear(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()
	insertBook(t, database, 1, "One")
	insertBook(t, database, 2, "Two")
	mustExec(t, database, "UPDATE books SET tags = ? WHERE id = ?", "sci-fi, classic, pulp", 1)
	mustExec(t, database, "UPDATE books SET tags = ? WHERE id = ?", "sci-fi, classic", 2)

	callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []int64{1},
		"operations": []map[string]any{
			{"type": "tags", "mode": "remove", "values": []string{"pulp"}},
		},
	})
	if got := bookTags(t, database, 1); got != "sci-fi, classic" {
		t.Errorf("after remove 1 tags = %q, want %q", got, "sci-fi, classic")
	}

	callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []int64{2},
		"operations": []map[string]any{
			{"type": "tags", "mode": "clear"},
		},
	})
	if got := bookTags(t, database, 2); got != "" {
		t.Errorf("after clear 2 tags = %q, want empty", got)
	}
}

func TestBulkEditSeriesAssignByOrder(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()
	insertBook(t, database, 1, "One")
	insertBook(t, database, 2, "Two")
	insertBook(t, database, 3, "Three")

	rr := callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []int64{1, 2, 3},
		"operations": []map[string]any{
			{
				"type": "series", "mode": "set", "name": "Dune",
				"index": map[string]any{"mode": "assign", "start": 1, "step": 1},
			},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}

	for i, id := range []int64{1, 2, 3} {
		var series sql.NullString
		var idx sql.NullFloat64
		database.Read(t.Context()).QueryRow("SELECT series, series_index FROM books WHERE id = ?", id).Scan(&series, &idx)
		if series.String != "Dune" {
			t.Errorf("%d series = %q, want Dune", id, series.String)
		}
		if !idx.Valid || idx.Float64 != float64(i+1) {
			t.Errorf("%d index = %v, want %d", id, idx, i+1)
		}
		ov := bookOverrides(t, database, id)
		if !ov["series"] || !ov["series_index"] {
			t.Errorf("%d overrides = %v, want series+series_index", id, ov)
		}
	}
}

func TestBulkEditAuthorsSet(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()
	insertBook(t, database, 1, "One")
	insertBook(t, database, 2, "Two")
	setBookAuthors(t, database, 1, "Ursula K. Le Guin")
	setBookAuthors(t, database, 2, "U. Le Guin")

	rr := callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []int64{1, 2},
		"operations": []map[string]any{
			{"type": "authors", "mode": "set", "authors": "Ursula K. Le Guin"},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp bulkEditResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	// w1 already had the target author (no-op); only w2 changes.
	if resp.Changed != 1 || resp.Unchanged != 1 {
		t.Errorf("counts: changed=%d unchanged=%d, want 1/1", resp.Changed, resp.Unchanged)
	}
	if got := bookAuthors(t, database, 2); got != "Ursula K. Le Guin" {
		t.Errorf("2 authors = %q, want %q", got, "Ursula K. Le Guin")
	}
	if !bookOverrides(t, database, 2)["authors"] {
		t.Errorf("2 missing authors override")
	}
	// The no-op book must not churn its override.
	if bookOverrides(t, database, 1)["authors"] {
		t.Errorf("no-op author set should not set override on 1")
	}
}

func bookTrashed(t *testing.T, database *db.DB, id int64) bool {
	t.Helper()
	var deletedAt sql.NullInt64
	if err := database.Read(t.Context()).QueryRow("SELECT deleted_at FROM books WHERE id = ?", id).Scan(&deletedAt); err != nil {
		t.Fatalf("query deleted_at %d: %v", id, err)
	}
	return deletedAt.Valid
}

func TestBulkTrashMovesSelectedToTrash(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	member := mustUser(t, database, "member", db.RoleMember)
	reader := mustUser(t, database, "reader", db.RoleReader)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	trash := func(userID int64, ids ...int64) *httptest.ResponseRecorder {
		req := jsonRequest(t, s, userID, http.MethodPost, "/api/books/bulk/trash", map[string]any{"ids": ids})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// A reader cannot trash — catalog mutation is member/admin only.
	if w := trash(reader.ID, 1); w.Code != http.StatusForbidden {
		t.Fatalf("reader bulk trash = %d, want 403", w.Code)
	}
	if bookTrashed(t, database, 1) {
		t.Fatalf("reader request must not have trashed 1")
	}

	// Member trashes both live books; an unknown id is skipped, not an error.
	w := trash(member.ID, 1, 2, 999)
	if w.Code != http.StatusOK {
		t.Fatalf("member bulk trash = %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp bulkTrashResponse
	decodeJSON(t, w, &resp)
	if resp.Trashed != 2 || len(resp.IDs) != 2 {
		t.Fatalf("response = %+v, want 2 trashed (1, 2)", resp)
	}
	if !bookTrashed(t, database, 1) || !bookTrashed(t, database, 2) {
		t.Fatalf("both books should be trashed")
	}

	// Re-trashing an already-trashed selection is a no-op, not an error.
	w = trash(member.ID, 1)
	if w.Code != http.StatusOK {
		t.Fatalf("re-trash = %d, want 200", w.Code)
	}
	decodeJSON(t, w, &resp)
	if resp.Trashed != 0 {
		t.Fatalf("re-trash trashed = %d, want 0", resp.Trashed)
	}

	// Empty selection is a bad request.
	if w := trash(member.ID); w.Code != http.StatusBadRequest {
		t.Fatalf("empty bulk trash = %d, want 400", w.Code)
	}
}

func shelfBookIDs(t *testing.T, database *db.DB, shelfID string) []int64 {
	t.Helper()
	rows, err := database.Read(t.Context()).Query("SELECT book_id FROM shelf_books WHERE shelf_id = ? ORDER BY position", shelfID)
	if err != nil {
		t.Fatalf("query shelf books: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan shelf book: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestBulkShelfAddAndRemove(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	member := mustUser(t, database, "member", db.RoleMember)
	shelf, err := database.CreateShelf(t.Context(), member.ID, db.ShelfPersonal, "To Read", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	call := func(userID int64, op string, ids ...int64) *httptest.ResponseRecorder {
		req := jsonRequest(t, s, userID, http.MethodPost,
			"/api/shelves/"+shelf.ID+"/books/bulk", map[string]any{"ids": ids, "op": op})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// Add both, then re-add (already present → no-op).
	w := call(member.ID, "add", 1, 2)
	if w.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", w.Code, w.Body)
	}
	var resp bulkShelfResponse
	decodeJSON(t, w, &resp)
	if resp.Changed != 2 {
		t.Fatalf("add changed = %d, want 2", resp.Changed)
	}
	decodeJSON(t, call(member.ID, "add", 1, 2), &resp)
	if resp.Changed != 0 {
		t.Fatalf("re-add changed = %d, want 0", resp.Changed)
	}

	// Remove one; the other stays.
	decodeJSON(t, call(member.ID, "remove", 1), &resp)
	if resp.Changed != 1 {
		t.Fatalf("remove changed = %d, want 1", resp.Changed)
	}
	if ids := shelfBookIDs(t, database, shelf.ID); len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("shelf books = %v, want [2]", ids)
	}

	// An unknown op is rejected.
	if w := call(member.ID, "toggle", 1); w.Code != http.StatusBadRequest {
		t.Fatalf("bad op = %d, want 400", w.Code)
	}
}

func TestBulkEditRejectsBadRequests(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()
	insertBook(t, database, 1, "One")

	cases := []map[string]any{
		{"ids": []string{}, "operations": []map[string]any{{"type": "tags", "mode": "clear"}}},
		{"ids": []int64{1}, "operations": []map[string]any{}},
		{"ids": []int64{1}, "operations": []map[string]any{{"type": "tags", "mode": "add", "values": []string{}}}},
		{"ids": []int64{1}, "operations": []map[string]any{{"type": "series", "mode": "set", "name": "  "}}},
		{"ids": []int64{1}, "operations": []map[string]any{{"type": "authors", "mode": "set", "authors": "  "}}},
		{"ids": []int64{1}, "operations": []map[string]any{{"type": "bogus"}}},
	}
	for i, body := range cases {
		rr := callBulkEdit(t, database, dataDir, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("case %d: status %d, want 400 (%s)", i, rr.Code, rr.Body.String())
		}
	}
}
