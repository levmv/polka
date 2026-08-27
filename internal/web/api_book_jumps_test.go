package web

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIBookJumpsThresholdAndValidation(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "jump-member", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/api/books/jumps?sort=title", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("small jumps status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var small BookJumpsDTO
	if err := json.UnmarshalRead(w.Body, &small); err != nil {
		t.Fatalf("decode small jumps: %v", err)
	}
	if small.Total != 2 || len(small.Items) != 0 {
		t.Fatalf("small jumps = %+v; want total 2 and hidden items", small)
	}

	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 2; i < minBookJumpTotal; i++ {
		title := fmt.Sprintf("Book %03d", i)
		if _, err := tx.Exec(
			`INSERT INTO works (id, title, sort_title, primary_author_sort) VALUES (?, ?, ?, 'Author')`,
			fmt.Sprintf("jump_%03d", i),
			title,
			title,
		); err != nil {
			tx.Rollback()
			t.Fatalf("insert work %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/api/books/jumps?sort=title", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("large jumps status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var large BookJumpsDTO
	if err := json.UnmarshalRead(w.Body, &large); err != nil {
		t.Fatalf("decode large jumps: %v", err)
	}
	if large.Total != minBookJumpTotal || len(large.Items) == 0 {
		t.Fatalf("large jumps = %+v; want visible items at threshold", large)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/api/books/jumps?sort=added", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid sort status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/books/jumps?sort=title", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated jumps status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
