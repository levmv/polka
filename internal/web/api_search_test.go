package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPISearchValidate(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	reader := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := jsonRequest(t, s, reader.ID, http.MethodPost, "/api/search/validate", map[string]string{
		"query": "author:asimov tag:scifi",
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid query status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var valid searchQueryValidationDTO
	if err := json.UnmarshalRead(w.Body, &valid); err != nil {
		t.Fatalf("decode valid response: %v", err)
	}
	if !valid.Valid || valid.Error != "" {
		t.Fatalf("valid response = %+v", valid)
	}

	req = jsonRequest(t, s, reader.ID, http.MethodPost, "/api/search/validate", map[string]string{
		"query": "author:",
	})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("invalid query status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var invalid searchQueryValidationDTO
	if err := json.UnmarshalRead(w.Body, &invalid); err != nil {
		t.Fatalf("decode invalid response: %v", err)
	}
	if invalid.Valid || invalid.Error == "" {
		t.Fatalf("invalid response = %+v", invalid)
	}
}
