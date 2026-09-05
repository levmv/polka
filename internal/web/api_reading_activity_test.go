package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIReadingActivityBoundaries(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	alice := mustUser(t, database, "alice", db.RoleReader)
	bob := mustUser(t, database, "bob", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	const endpoint = "/api/reader/assets/asset_1/activity"
	request := readingActivityRequest{SessionID: "0123456789abcdef0123456789abcdef"}
	call := func(user int64, method, path string, payload any, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user, method, path, payload))
		if w.Code != want {
			t.Fatalf("%s %s: status %d want %d; %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	call(alice.ID, http.MethodPost, endpoint, request, http.StatusConflict)
	call(alice.ID, http.MethodPut, "/api/settings", userSettingsRequest{TimeZone: new("UTC"), InitializeTimeZone: true}, http.StatusOK)
	call(alice.ID, http.MethodPost, endpoint, request, http.StatusOK)
	call(alice.ID, http.MethodPost, "/api/reader/assets/missing/activity", request, http.StatusNotFound)
	w := call(bob.ID, http.MethodPut, endpoint, request, http.StatusOK)
	var result db.ReadingActivityResult
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Active || result.CountedMS != 0 {
		t.Fatalf("another user's session leaked: %+v", result)
	}
	request.ElapsedMS = -1
	call(alice.ID, http.MethodPut, endpoint, request, http.StatusBadRequest)
	request.ElapsedMS = 0
	request.Finished = true
	w = call(alice.ID, http.MethodPut, endpoint, request, http.StatusOK)
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Active || result.CountedMS != 0 {
		t.Fatalf("zero-length close = %+v", result)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, endpoint, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated activity status = %d", w.Code)
	}
	// Scoped accounts cannot record activity against hidden assets even when
	// they know another reader's session identity.
	if _, err := database.Exec(`UPDATE users SET content_scope = 'shelves' WHERE id = ?`, alice.ID); err != nil {
		t.Fatal(err)
	}
	call(alice.ID, http.MethodPut, endpoint, request, http.StatusNotFound)
}
