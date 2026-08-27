package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIKoboConnectionLifecycleAndIsolation(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	alice := mustUser(t, database, "alice-native-kobo", db.RoleMember)
	bob := mustUser(t, database, "bob-native-kobo", db.RoleMember)
	shelf, err := database.CreateShelf(alice.ID, db.ShelfPersonal, "Travel", db.ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/kobo-connection", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "null" {
		t.Fatalf("empty connection = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req := jsonRequest(t, s, alice.ID, http.MethodPost, "/api/kobo-connection", koboConnectionCreateRequest{ShelfID: shelf.ID})
	req.Host = "books.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created koboConnectionCreateDTO
	if err := json.UnmarshalRead(w.Body, &created); err != nil {
		t.Fatal(err)
	}
	setupURL, err := url.Parse(created.SetupURL)
	if err != nil || setupURL.Scheme != "https" || setupURL.Host != "books.example" || !strings.HasPrefix(setupURL.Path, "/kobo/") {
		t.Fatalf("setup URL = %q, err=%v", created.SetupURL, err)
	}
	secret := strings.TrimPrefix(setupURL.Path, "/kobo/")
	if secret == "" {
		t.Fatal("setup URL has no token")
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/kobo-connection", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), `"shelf_name":"Travel"`) {
		t.Fatalf("listed connection = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/kobo-connection", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "null" {
		t.Fatalf("bob connection = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodPost, "/api/kobo-connection", koboConnectionCreateRequest{ShelfID: shelf.ID}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invisible shelf create = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodDelete, "/api/kobo-connection", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, setupURL.Path+"/v1/library/sync", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted setup URL status = %d", w.Code)
	}
}
