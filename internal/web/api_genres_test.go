package web

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestGenresStayIndependentThroughEditsAndSearch(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	member := mustUser(t, database, "member", db.RoleMember)
	s := newTestServer(t, database, dir)
	handler := testRoutes(t, s)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, member.ID, method, path, body))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	assertBook := func(id int64, genres, tags string) {
		t.Helper()
		var got BookSummaryDTO
		w := request("GET", fmt.Sprintf("/api/books/%d", id), nil)
		if err := json.UnmarshalRead(w.Body, &got); err != nil {
			t.Fatal(err)
		}
		value := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		if value(got.Genres) != genres || value(got.Tags) != tags {
			t.Fatalf("book %d = genres %q tags %q; want %q / %q", id, value(got.Genres), value(got.Tags), genres, tags)
		}
	}
	assertSearch := func(query string, want ...int64) {
		t.Helper()
		rows, err := db.ListBooks(database.Read(t.Context()), db.FullVisibilityScope(), member.ID, query, db.SortTitle, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, want) {
			t.Fatalf("query %q = %v; want %v", query, ids, want)
		}
	}
	request("PATCH", "/api/books/1", map[string]string{"genres": "Science Fiction, History", "tags": "History"})
	request("PATCH", "/api/books/2", map[string]string{"genres": "history", "tags": "Science Fiction, Favourite"})
	assertBook(1, "Science Fiction, History", "History")
	assertBook(2, "History", "Science Fiction, Favourite")
	snapshot, err := db.LoadMetadataWritebackSnapshot(database.Read(t.Context()), 1)
	if err != nil || !slices.Equal(snapshot.Metadata.Genres, []string{"Science Fiction", "History"}) || !slices.Equal(snapshot.Metadata.Tags, []string{"History"}) {
		t.Fatalf("writeback snapshot = %+v, %v", snapshot, err)
	}
	assertSearch(`genre:"Science Fiction"`, 1)
	assertSearch(`tag:"Science Fiction"`, 2)
	assertSearch(`genre:Sci`, 1)
	assertSearch(`"Science Fiction"`, 1, 2)
	assertSearch(`genre:"History" tag:"History"`, 1)
	var id int64
	if err := database.Read(t.Context()).QueryRow("SELECT id FROM tags WHERE kind = 'genre' AND name_key = 'science fiction'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	// A name used only by a tag must not absorb a renamed genre.
	request("PATCH", fmt.Sprintf("/api/tags/%d", id), map[string]string{"name": "Favourite"})
	assertBook(1, "Favourite, History", "History")
	assertBook(2, "History", "Science Fiction, Favourite")
	assertSearch(`tag:"Science Fiction"`, 2)

	// A partial patch and a bulk operation each affect only their own kind.
	request("PATCH", "/api/books/1", map[string]any{"tags": nil})
	assertBook(1, "Favourite, History", "")
	assertSearch("no:tags", 1)
	request("PATCH", "/api/books/bulk", map[string]any{"ids": []int{1, 2}, "operations": []map[string]string{{"type": "genres", "mode": "clear"}}})
	assertBook(1, "", "")
	assertBook(2, "", "Science Fiction, Favourite")
	assertSearch("no:genres", 1, 2)
	snap, err := db.LoadMetadataWritebackSnapshot(database.Read(t.Context()), 1)
	if err != nil || len(snap.Metadata.Genres) != 0 || len(snap.Metadata.Tags) != 0 {
		t.Fatalf("writeback snapshot = %+v, %v", snap, err)
	}
}
