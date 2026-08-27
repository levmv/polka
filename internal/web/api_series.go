package web

import (
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/db"
)

type SeriesDTO struct {
	Name string `json:"name"`
	// Author of the volume representing the series, empty when it has none.
	Author        string `json:"author"`
	BookCount     int    `json:"book_count"`
	FinishedCount int    `json:"finished_count"`
	// The work whose cover stands for the series on the Series page; the cover
	// route generates a placeholder when that work has no stored cover.
	CoverWorkID  string `json:"cover_work_id"`
	CoverVersion int    `json:"cover_version"`
}

type SeriesPageDTO struct {
	Items      []SeriesDTO `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

func (s *Server) handleAPISeries(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	pageSize, err := collectionPageSize(r)
	if err != nil {
		http.Error(w, "Invalid limit", http.StatusBadRequest)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	cursor, err := decodeCollectionCursor(r.URL.Query().Get("cursor"), "series", q)
	if err != nil || (r.URL.Query().Get("cursor") != "" && cursor.Primary == "") {
		http.Error(w, "Invalid cursor", http.StatusBadRequest)
		return
	}
	rows, err := db.ListSeriesCardsPage(s.db, scope, UserID(r.Context()), q, cursor.Primary, pageSize+1)
	if err != nil {
		serverError(w, err)
		return
	}

	hasNext := len(rows) > pageSize
	if hasNext {
		rows = rows[:pageSize]
	}
	series := make([]SeriesDTO, 0, len(rows))
	for _, row := range rows {
		series = append(series, SeriesDTO{
			Name:          row.Name,
			Author:        row.Author,
			BookCount:     row.BookCount,
			FinishedCount: row.FinishedCount,
			CoverWorkID:   row.CoverWorkID,
			CoverVersion:  row.CoverVersion,
		})
	}
	page := SeriesPageDTO{Items: series}
	if hasNext {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCollectionCursor(collectionCursor{
			Kind:    "series",
			Primary: last.Name,
			Filter:  q,
		})
	}
	writeJSON(w, http.StatusOK, page)
}
