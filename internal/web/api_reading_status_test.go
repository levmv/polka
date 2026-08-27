package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIReadingStatusManualLifecycleAndBookDetail(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	alice := mustUser(t, database, "alice", db.RoleReader)
	bob := mustUser(t, database, "bob", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/books/w_1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", w.Code, w.Body.String())
	}
	var book BookDetailDTO
	if err := json.UnmarshalRead(w.Body, &book); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if book.ReadingStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("default detail reading status = %+v", book.ReadingStatus)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/books/w_1/reading-status", readingStatusRequest{Status: db.ReadingStatusDropped}))
	if w.Code != http.StatusOK {
		t.Fatalf("set dropped status = %d: %s", w.Code, w.Body.String())
	}
	var status ReadingStatusDTO
	if err := json.UnmarshalRead(w.Body, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Status != db.ReadingStatusDropped || status.UpdatedAt == 0 {
		t.Fatalf("saved status = %+v", status)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/books/w_1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("bob detail status = %d: %s", w.Code, w.Body.String())
	}
	book = BookDetailDTO{}
	if err := json.UnmarshalRead(w.Body, &book); err != nil {
		t.Fatalf("decode bob detail: %v", err)
	}
	if book.ReadingStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("alice status leaked to bob: %+v", book.ReadingStatus)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/books/w_1/reading-status", readingStatusRequest{Status: "paused"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d; want 400: %s", w.Code, w.Body.String())
	}
}
