package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestReaderRoutePolicy(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	reader := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	for _, tc := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/books", http.StatusOK},
		{http.MethodPost, "/api/import", http.StatusForbidden},
		{http.MethodPost, "/api/admin/storage/import/preview", http.StatusForbidden},
		{http.MethodPost, "/api/admin/storage/import", http.StatusForbidden},
		{http.MethodGet, "/api/cleanup", http.StatusForbidden},
		{http.MethodPost, "/api/authors/rename", http.StatusForbidden},
	} {
		req := jsonRequest(t, s, reader.ID, tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s %s status = %d, want %d; body: %s", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}
