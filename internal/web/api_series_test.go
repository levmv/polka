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

	if _, err := database.Exec(`
		UPDATE works SET series = 'Middle-earth', series_index = 1 WHERE id = 'w_1';
		UPDATE works SET series = 'Dune', series_index = 1, cover_version = 0 WHERE id = 'w_2';
		INSERT INTO works (id, title, sort_title, series, series_index, cover_version) VALUES ('w_3', 'Dune Messiah', 'Dune Messiah', 'Dune', 2, 5);
		INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w_3', 'a_2', 0);
		INSERT INTO assets (id, work_id, storage_path, filename, extension, is_primary) VALUES ('asset_3', 'w_3', 'Herbert/Dune_Messiah/asset_3.epub', 'asset_3.epub', '.epub', 1);
	`); err != nil {
		t.Fatalf("seed series: %v", err)
	}

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
	// Dune's first volume has no cover, so the tile falls through to w_3.
	wantDune := SeriesDTO{Name: "Dune", Author: "Frank Herbert", BookCount: 2, CoverWorkID: "w_3", CoverVersion: 5}
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
