package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/relayout"
)

// AuthorAdmin is a manage-authors list row: an author plus its work count.
type AuthorAdmin struct {
	Name      string `json:"name"`
	SortName  string `json:"sort_name"`
	BookCount int    `json:"book_count"`
}

type AuthorAdminPage struct {
	Items      []AuthorAdmin `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// The manage-authors endpoints live under /api/authors/ (list, rename,
// sort-name); bare /api/authors stays the lightweight autocomplete endpoint.

func (s *Server) handleAPIAuthors(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := db.ListAuthorNames(s.db, scope, q, 20)
	if err != nil {
		serverError(w, err)
		return
	}

	authors := make([]Author, 0, len(rows))
	for _, a := range rows {
		authors = append(authors, Author{Name: a.Name, SortName: a.SortName})
	}

	writeJSON(w, http.StatusOK, authors)
}

func (s *Server) handleAPIAuthorList(w http.ResponseWriter, r *http.Request) {
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
	cursor, err := decodeCollectionCursor(r.URL.Query().Get("cursor"), "authors", "")
	if err != nil || (r.URL.Query().Get("cursor") != "" && cursor.Tie == "") {
		http.Error(w, "Invalid cursor", http.StatusBadRequest)
		return
	}
	rows, err := db.ListAuthorCountsPage(s.db, scope, cursor.Primary, cursor.Tie, pageSize+1)
	if err != nil {
		serverError(w, err)
		return
	}
	hasNext := len(rows) > pageSize
	if hasNext {
		rows = rows[:pageSize]
	}
	authors := make([]AuthorAdmin, 0, len(rows))
	for _, a := range rows {
		authors = append(authors, AuthorAdmin{Name: a.Name, SortName: a.SortName, BookCount: a.BookCount})
	}
	page := AuthorAdminPage{Items: authors}
	if hasNext {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCollectionCursor(collectionCursor{
			Kind:    "authors",
			Primary: last.SortName,
			Tie:     last.ID,
		})
	}
	writeJSON(w, http.StatusOK, page)
}

// handleAPIAuthorInfo serves GET /api/authors/info?name=... — the sort_name and
// work count for one author by exact name. The book-edit convergence prompt uses
// the count to ask whether a per-book author rename should also apply to the
// other books still crediting the old name. 404 when no such author exists.
func (s *Server) handleAPIAuthorInfo(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	info, ok, err := db.GetAuthorInfo(s.db, scope, name)
	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, AuthorAdmin{Name: info.Name, SortName: info.SortName, BookCount: info.BookCount})
}

// handleAPIAuthorRename renames an author in place, or merges into an existing
// author when `new` already names one. Mirrors `polka library authors rename`:
// it relayouts every affected work's files (primary author is part of the path).
func (s *Server) handleAPIAuthorRename(w http.ResponseWriter, r *http.Request) {
	if !s.requireFullCatalogScope(w, r) {
		return
	}

	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Old = strings.TrimSpace(req.Old)
	req.New = strings.TrimSpace(req.New)
	if req.Old == "" || req.New == "" {
		http.Error(w, "Both old and new author names are required", http.StatusBadRequest)
		return
	}

	releaseStorageSlot, err := s.acquireStorageWorkSlot(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	defer releaseStorageSlot()

	res, err := relayout.RenameAuthor(r.Context(), s.db, s.managedRoot(), req.Old, req.New)
	if writeAuthorOpError(w, err) {
		return
	}
	writeAuthorOpResult(w, res)
}

// handleAPIAuthorSortName overrides an author's sort_name (canonical-path sort
// key) and relayouts its works. The display name is unchanged.
func (s *Server) handleAPIAuthorSortName(w http.ResponseWriter, r *http.Request) {
	if !s.requireFullCatalogScope(w, r) {
		return
	}

	var req struct {
		Name     string `json:"name"`
		SortName string `json:"sort_name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.SortName = strings.TrimSpace(req.SortName)
	if req.Name == "" || req.SortName == "" {
		http.Error(w, "Both name and sort_name are required", http.StatusBadRequest)
		return
	}

	releaseStorageSlot, err := s.acquireStorageWorkSlot(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	defer releaseStorageSlot()

	res, err := relayout.SetAuthorSortName(r.Context(), s.db, s.managedRoot(), req.Name, req.SortName)
	if writeAuthorOpError(w, err) {
		return
	}
	writeAuthorOpResult(w, res)
}

func writeAuthorOpError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, db.ErrAuthorNotFound) {
		http.Error(w, "Author not found", http.StatusNotFound)
	} else {
		serverError(w, err)
	}
	return true
}

func writeAuthorOpResult(w http.ResponseWriter, res relayout.AuthorMutationResult) {
	writeJSON(w, http.StatusOK, map[string]int{
		"affected": res.Affected,
		"moved":    res.Moved,
		"warnings": len(res.Warnings),
	})
}

func (s *Server) requireFullCatalogScope(w http.ResponseWriter, r *http.Request) bool {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return false
	}
	if !scope.IsFull() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}
