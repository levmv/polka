package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/relayout"
)

type TagSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	BookCount   int    `json:"book_count"`
	HasChildren bool   `json:"has_children"`
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
	sort := db.TagSort(r.URL.Query().Get("sort"))
	switch sort {
	case "", db.TagSortName:
		sort = db.TagSortName
	case db.TagSortBooks:
	default:
		http.Error(w, "Invalid sort", http.StatusBadRequest)
		return
	}
	var parentID int64
	if raw := r.URL.Query().Get("parent"); raw != "" {
		parentID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || parentID < 0 {
			http.Error(w, "Invalid parent", http.StatusBadRequest)
			return
		}
	}
	if q != "" {
		parentID = 0
	}
	filter := string(sort) + ":" + strconv.FormatInt(parentID, 10) + ":" + q
	limit, err := collectionPageSize(r)
	if err != nil {
		http.Error(w, "Invalid limit", http.StatusBadRequest)
		return
	}
	cursor, err := decodeCollectionCursor(r.URL.Query().Get("cursor"), string(kind), filter)
	if err != nil {
		http.Error(w, "Invalid cursor", http.StatusBadRequest)
		return
	}
	var afterCount int
	if sort == db.TagSortBooks && r.URL.Query().Get("cursor") != "" {
		afterCount, err = strconv.Atoi(cursor.Tie)
		if err != nil || afterCount <= 0 || cursor.Primary == "" {
			http.Error(w, "Invalid cursor", http.StatusBadRequest)
			return
		}
	}
	rows, err := db.ListTagCountsPage(s.db.Read(r.Context()), scope, db.TagListOptions{
		Kind: kind, Query: q, ParentID: parentID, Sort: sort,
		AfterName: cursor.Primary, AfterCount: afterCount, Limit: limit + 1,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	var nextCursor string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := collectionCursor{Kind: string(kind), Primary: last.Key, Filter: filter}
		if sort == db.TagSortBooks {
			next.Tie = strconv.Itoa(last.BookCount)
		}
		nextCursor = encodeCollectionCursor(next)
	}
	items := make([]TagSummary, 0, len(rows))
	for _, row := range rows {
		parts := bookmeta.TagParts(row.Name)
		items = append(items, TagSummary{
			ID: row.ID, Name: row.Name, Label: parts[len(parts)-1],
			BookCount: row.BookCount, HasChildren: row.HasChildren,
		})
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
	if errors.Is(err, db.ErrTagCycle) || errors.Is(err, db.ErrTagPath) {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
