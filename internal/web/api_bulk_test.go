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

func insertWork(t *testing.T, database *db.DB, id, title string) {
	t.Helper()
	if _, err := database.Exec(
		"INSERT INTO works (id, title, sort_title) VALUES (?, ?, ?)", id, title, title,
	); err != nil {
		t.Fatalf("insert work %s: %v", id, err)
	}
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

func workTags(t *testing.T, database *db.DB, id string) string {
	t.Helper()
	var tags sql.NullString
	if err := database.QueryRow("SELECT tags FROM works WHERE id = ?", id).Scan(&tags); err != nil {
		t.Fatalf("query tags %s: %v", id, err)
	}
	return tags.String
}

func setWorkAuthors(t *testing.T, database *db.DB, id, authors string) {
	t.Helper()
	if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		return replaceWorkAuthors(tx, id, authors)
	}); err != nil {
		t.Fatalf("set authors %s: %v", id, err)
	}
}

func workAuthors(t *testing.T, database *db.DB, id string) string {
	t.Helper()
	byWork, err := db.AuthorsByWorkIDs(database, []string{id})
	if err != nil {
		t.Fatalf("authors %s: %v", id, err)
	}
	return formatAuthorRows(byWork[id])
}

func workOverrides(t *testing.T, database *db.DB, id string) map[string]bool {
	t.Helper()
	var raw sql.NullString
	if err := database.QueryRow("SELECT manual_overrides FROM works WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatalf("query overrides %s: %v", id, err)
	}
	m := make(map[string]bool)
	if raw.String != "" {
		json.Unmarshal([]byte(raw.String), &m)
	}
	return m
}

func workMetadataRev(t *testing.T, database *db.DB, id string) int {
	t.Helper()
	var rev int
	if err := database.QueryRow("SELECT metadata_rev FROM works WHERE id = ?", id).Scan(&rev); err != nil {
		t.Fatalf("query metadata_rev %s: %v", id, err)
	}
	return rev
}

func TestBulkEditTagsAdd(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()

	insertWork(t, database, "w1", "One")
	insertWork(t, database, "w2", "Two")
	database.Exec("UPDATE works SET tags = ? WHERE id = ?", "sci-fi", "w1")

	rr := callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []string{"w1", "w2"},
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
	if got := workTags(t, database, "w1"); got != "sci-fi, classic" {
		t.Errorf("w1 tags = %q, want %q", got, "sci-fi, classic")
	}
	if got := workTags(t, database, "w2"); got != "sci-fi, classic" {
		t.Errorf("w2 tags = %q, want %q", got, "sci-fi, classic")
	}
	if !workOverrides(t, database, "w1")["tags"] {
		t.Errorf("w1 missing tags override")
	}
	if got := workMetadataRev(t, database, "w1"); got != 1 {
		t.Errorf("w1 metadata_rev = %d, want 1", got)
	}
	if got := workMetadataRev(t, database, "w2"); got != 1 {
		t.Errorf("w2 metadata_rev = %d, want 1", got)
	}
}

func TestBulkEditTagsAddNoOpIsUnchanged(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()

	insertWork(t, database, "w1", "One")
	database.Exec("UPDATE works SET tags = ? WHERE id = ?", "sci-fi, classic", "w1")

	rr := callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []string{"w1"},
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
	if workOverrides(t, database, "w1")["tags"] {
		t.Errorf("no-op add should not set tags override")
	}
	if got := workMetadataRev(t, database, "w1"); got != 0 {
		t.Errorf("no-op metadata_rev = %d, want 0", got)
	}
}

func TestBulkEditTagsRemoveAndClear(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()
	insertWork(t, database, "w1", "One")
	insertWork(t, database, "w2", "Two")
	database.Exec("UPDATE works SET tags = ? WHERE id = ?", "sci-fi, classic, pulp", "w1")
	database.Exec("UPDATE works SET tags = ? WHERE id = ?", "sci-fi, classic", "w2")

	callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []string{"w1"},
		"operations": []map[string]any{
			{"type": "tags", "mode": "remove", "values": []string{"pulp"}},
		},
	})
	if got := workTags(t, database, "w1"); got != "sci-fi, classic" {
		t.Errorf("after remove w1 tags = %q, want %q", got, "sci-fi, classic")
	}

	callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []string{"w2"},
		"operations": []map[string]any{
			{"type": "tags", "mode": "clear"},
		},
	})
	if got := workTags(t, database, "w2"); got != "" {
		t.Errorf("after clear w2 tags = %q, want empty", got)
	}
}

func TestBulkEditSeriesAssignByOrder(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()
	insertWork(t, database, "w1", "One")
	insertWork(t, database, "w2", "Two")
	insertWork(t, database, "w3", "Three")

	rr := callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []string{"w1", "w2", "w3"},
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

	for i, id := range []string{"w1", "w2", "w3"} {
		var series sql.NullString
		var idx sql.NullFloat64
		database.QueryRow("SELECT series, series_index FROM works WHERE id = ?", id).Scan(&series, &idx)
		if series.String != "Dune" {
			t.Errorf("%s series = %q, want Dune", id, series.String)
		}
		if !idx.Valid || idx.Float64 != float64(i+1) {
			t.Errorf("%s index = %v, want %d", id, idx, i+1)
		}
		ov := workOverrides(t, database, id)
		if !ov["series"] || !ov["series_index"] {
			t.Errorf("%s overrides = %v, want series+series_index", id, ov)
		}
	}
}

func TestBulkEditAuthorsSet(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()
	insertWork(t, database, "w1", "One")
	insertWork(t, database, "w2", "Two")
	setWorkAuthors(t, database, "w1", "Ursula K. Le Guin")
	setWorkAuthors(t, database, "w2", "U. Le Guin")

	rr := callBulkEdit(t, database, dataDir, map[string]any{
		"ids": []string{"w1", "w2"},
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
	if got := workAuthors(t, database, "w2"); got != "Ursula K. Le Guin" {
		t.Errorf("w2 authors = %q, want %q", got, "Ursula K. Le Guin")
	}
	if !workOverrides(t, database, "w2")["authors"] {
		t.Errorf("w2 missing authors override")
	}
	// The no-op work must not churn its override.
	if workOverrides(t, database, "w1")["authors"] {
		t.Errorf("no-op author set should not set override on w1")
	}
}

func workTrashed(t *testing.T, database *db.DB, id string) bool {
	t.Helper()
	var deletedAt sql.NullInt64
	if err := database.QueryRow("SELECT deleted_at FROM works WHERE id = ?", id).Scan(&deletedAt); err != nil {
		t.Fatalf("query deleted_at %s: %v", id, err)
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

	trash := func(userID string, ids ...string) *httptest.ResponseRecorder {
		req := jsonRequest(t, s, userID, http.MethodPost, "/api/books/bulk/trash", map[string]any{"ids": ids})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// A reader cannot trash — catalog mutation is member/admin only.
	if w := trash(reader.ID, "w_1"); w.Code != http.StatusForbidden {
		t.Fatalf("reader bulk trash = %d, want 403", w.Code)
	}
	if workTrashed(t, database, "w_1") {
		t.Fatalf("reader request must not have trashed w_1")
	}

	// Member trashes both live works; an unknown id is skipped, not an error.
	w := trash(member.ID, "w_1", "w_2", "ghost")
	if w.Code != http.StatusOK {
		t.Fatalf("member bulk trash = %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp bulkTrashResponse
	decodeJSON(t, w, &resp)
	if resp.Trashed != 2 || len(resp.IDs) != 2 {
		t.Fatalf("response = %+v, want 2 trashed (w_1, w_2)", resp)
	}
	if !workTrashed(t, database, "w_1") || !workTrashed(t, database, "w_2") {
		t.Fatalf("both works should be trashed")
	}

	// Re-trashing an already-trashed selection is a no-op, not an error.
	w = trash(member.ID, "w_1")
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

func shelfWorkIDs(t *testing.T, database *db.DB, shelfID string) []string {
	t.Helper()
	rows, err := database.Query("SELECT work_id FROM shelf_books WHERE shelf_id = ? ORDER BY position", shelfID)
	if err != nil {
		t.Fatalf("query shelf books: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
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
	shelf, err := database.CreateShelf(member.ID, db.ShelfPersonal, "To Read", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	call := func(userID, op string, ids ...string) *httptest.ResponseRecorder {
		req := jsonRequest(t, s, userID, http.MethodPost,
			"/api/shelves/"+shelf.ID+"/books/bulk", map[string]any{"ids": ids, "op": op})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// Add both, then re-add (already present → no-op).
	w := call(member.ID, "add", "w_1", "w_2")
	if w.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", w.Code, w.Body)
	}
	var resp bulkShelfResponse
	decodeJSON(t, w, &resp)
	if resp.Changed != 2 {
		t.Fatalf("add changed = %d, want 2", resp.Changed)
	}
	decodeJSON(t, call(member.ID, "add", "w_1", "w_2"), &resp)
	if resp.Changed != 0 {
		t.Fatalf("re-add changed = %d, want 0", resp.Changed)
	}

	// Remove one; the other stays.
	decodeJSON(t, call(member.ID, "remove", "w_1"), &resp)
	if resp.Changed != 1 {
		t.Fatalf("remove changed = %d, want 1", resp.Changed)
	}
	if ids := shelfWorkIDs(t, database, shelf.ID); len(ids) != 1 || ids[0] != "w_2" {
		t.Fatalf("shelf books = %v, want [w_2]", ids)
	}

	// An unknown op is rejected.
	if w := call(member.ID, "toggle", "w_1"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad op = %d, want 400", w.Code)
	}
}

func TestBulkEditRejectsBadRequests(t *testing.T) {
	database := newBulkTestServer(t)
	dataDir := t.TempDir()
	insertWork(t, database, "w1", "One")

	cases := []map[string]any{
		{"ids": []string{}, "operations": []map[string]any{{"type": "tags", "mode": "clear"}}},
		{"ids": []string{"w1"}, "operations": []map[string]any{}},
		{"ids": []string{"w1"}, "operations": []map[string]any{{"type": "tags", "mode": "add", "values": []string{}}}},
		{"ids": []string{"w1"}, "operations": []map[string]any{{"type": "series", "mode": "set", "name": "  "}}},
		{"ids": []string{"w1"}, "operations": []map[string]any{{"type": "authors", "mode": "set", "authors": "  "}}},
		{"ids": []string{"w1"}, "operations": []map[string]any{{"type": "bogus"}}},
	}
	for i, body := range cases {
		rr := callBulkEdit(t, database, dataDir, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("case %d: status %d, want 400 (%s)", i, rr.Code, rr.Body.String())
		}
	}
}
