package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/relayout"
)

type TagSummary struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	BookCount int    `json:"book_count"`
}

func (s *Server) handleAPITagList(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	kind, ok := tagKindParam(w, r)
	if !ok {
		return
	}
	q := bookmeta.TagKey(r.URL.Query().Get("q"))
	limit, err := collectionPageSize(r)
	if err != nil {
		http.Error(w, "Invalid limit", http.StatusBadRequest)
		return
	}
	cursor, err := decodeCollectionCursor(r.URL.Query().Get("cursor"), string(kind), q)
	if err != nil {
		http.Error(w, "Invalid cursor", http.StatusBadRequest)
		return
	}
	rows, err := db.ListTagCountsPage(s.db.Read(r.Context()), scope, kind, q, cursor.Primary, limit+1)
	if err != nil {
		serverError(w, r, err)
		return
	}
	var nextCursor string
	if len(rows) > limit {
		rows = rows[:limit]
		nextCursor = encodeCollectionCursor(collectionCursor{Kind: string(kind), Primary: rows[len(rows)-1].Key, Filter: q})
	}
	items := make([]TagSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, TagSummary{ID: row.ID, Name: row.Name, BookCount: row.BookCount})
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []TagSummary `json:"items"`
		NextCursor string       `json:"next_cursor,omitempty"`
	}{Items: items, NextCursor: nextCursor})
}

func (s *Server) handleAPITagRename(w http.ResponseWriter, r *http.Request) {
	if !s.requireFullCatalogScope(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || strings.Contains(req.Name, ",") {
		http.Error(w, "Enter one name without commas", http.StatusBadRequest)
		return
	}
	s.mutateTag(w, r, func(tx *db.Tx) ([]int64, error) { return db.RenameOrMergeTag(tx, id, req.Name) })
}

func (s *Server) handleAPITagDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireFullCatalogScope(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	s.mutateTag(w, r, func(tx *db.Tx) ([]int64, error) { return db.DeleteTag(tx, id) })
}

func (s *Server) mutateTag(w http.ResponseWriter, r *http.Request, apply func(*db.Tx) ([]int64, error)) {
	release, err := s.storageQueue.Acquire(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer release()
	var affected []int64
	_, err = relayout.MutateBooks(r.Context(), s.db, s.storageRoot, func(tx *db.Tx) (relayout.Changed, error) {
		var err error
		affected, err = apply(tx)
		if err != nil {
			return relayout.Changed{}, err
		}
		return relayout.Changed{BumpMetadataRev: affected}, nil
	})
	if errors.Is(err, db.ErrTagNotFound) {
		http.Error(w, "Entry not found", http.StatusNotFound)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Affected int `json:"affected"`
	}{len(affected)})
}

// Both suggestions and the dictionary page use the same explicit type.
func tagKindParam(w http.ResponseWriter, r *http.Request) (db.TagKind, bool) {
	switch kind := db.TagKind(r.URL.Query().Get("kind")); kind {
	case "", db.TagKindTag:
		return db.TagKindTag, true
	case db.TagKindGenre:
		return kind, true
	default:
		http.Error(w, "Invalid kind", http.StatusBadRequest)
		return "", false
	}
}
