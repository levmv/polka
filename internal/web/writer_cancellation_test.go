package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
)

func TestShelfRequestCancellationWhileWriterIsHeld(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "writer-cancel", db.RoleAdmin)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "POST", "/api/shelves", strings.NewReader(`{"name":"Canceled shelf","kind":"manual"}`))
	addSessionCookie(t, s, req, user.ID)
	tx, err := database.BeginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	done := make(chan struct{})
	w := httptest.NewRecorder()
	w.Code = 0 // Distinguish an untouched response from an explicitly sent 200.
	go func() {
		handler.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-done:
		t.Fatalf("request did not wait for writer: %d %s", w.Code, w.Body.String())
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not release the waiting handler")
	}
	if w.Code != 0 || w.Body.Len() != 0 {
		t.Fatalf("canceled request received a response: %d %s", w.Code, w.Body.String())
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.Read(t.Context()).QueryRow("SELECT count(*) FROM shelves WHERE name = 'Canceled shelf'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("canceled shelf persisted = %d, %v", count, err)
	}
}

func TestServerErrorForLiveRequest(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"writer timeout", db.ErrWriterTimeout, http.StatusServiceUnavailable},
		{"canceled sub-operation", context.Canceled, http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/books", nil)
			serverError(w, r, test.err)
			if w.Code != test.status {
				t.Fatalf("status = %d; want %d", w.Code, test.status)
			}
		})
	}
}
