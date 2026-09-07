package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAPISeriesRoutes(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	mustExec(t, database, `
		UPDATE books SET series = 'Middle-earth', series_index = 1 WHERE id = 1;
		UPDATE books SET series = 'Dune', series_index = 1, cover_version = 0 WHERE id = 2;
		INSERT INTO books (id, title, sort_title, series, series_index, cover_version) VALUES (3, 'Dune Messiah', 'Dune Messiah', 'Dune', 2, 5);
		INSERT INTO book_authors (book_id, author_id, author_order)
		VALUES (3, (SELECT id FROM authors WHERE name = 'Frank Herbert'), 0);
		INSERT INTO assets (id, book_id, storage_path, filename, extension, is_primary, original_sha256, current_sha256) VALUES (3, 3, 'Herbert/Dune_Messiah/asset_3.epub', 'asset_3.epub', '.epub', 1, randomblob(32), randomblob(32));
	`)

	s := &Server{db: database, dataDir: dir}

	w := httptest.NewRecorder()
	s.handleAPISeries(w, httptest.NewRequest(http.MethodGet, "/api/series?limit=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("series status = %d, body: %s", w.Code, w.Body.String())
	}
	var firstPage SeriesPageDTO
	if err := json.UnmarshalRead(w.Body, &firstPage); err != nil {
		t.Fatalf("decode series: %v", err)
	}
	// Dune's first volume has no cover, so the tile falls through to book 3.
	wantDune := SeriesDTO{Name: "Dune", Author: "Frank Herbert", BookCount: 2, CoverBookID: 3, CoverVersion: 5}
	if len(firstPage.Items) != 1 || firstPage.Items[0] != wantDune || firstPage.NextCursor == "" {
		t.Fatalf("first series page = %+v, want %+v plus cursor", firstPage, wantDune)
	}

	w = httptest.NewRecorder()
	s.handleAPISeries(w, httptest.NewRequest(http.MethodGet, "/api/series?limit=1&cursor="+url.QueryEscape(firstPage.NextCursor), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("second series page status = %d, body: %s", w.Code, w.Body.String())
	}
	var secondPage SeriesPageDTO
	if err := json.UnmarshalRead(w.Body, &secondPage); err != nil {
		t.Fatalf("decode second series page: %v", err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].Name != "Middle-earth" || secondPage.Items[0].BookCount != 1 || secondPage.NextCursor != "" {
		t.Fatalf("second series page = %+v, want Middle-earth(1) without cursor", secondPage)
	}
}
