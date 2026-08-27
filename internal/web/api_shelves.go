package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/levmv/polka/internal/db"
)

type ShelfDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Query      string `json:"query,omitempty"`
	OwnerID    string `json:"owner_id"`
	Visibility string `json:"visibility"`
	Position   int    `json:"position"`
}

type BookShelfDTO struct {
	ShelfDTO
	InShelf bool `json:"in_shelf"`
}

func shelfDTO(s db.Shelf) ShelfDTO {
	dto := ShelfDTO{
		ID:         s.ID,
		Name:       s.Name,
		Kind:       string(s.Kind),
		Query:      s.Query,
		OwnerID:    s.OwnerID,
		Visibility: string(s.Visibility),
		Position:   s.Position,
	}
	return dto
}

func (s *Server) handleAPIShelves(w http.ResponseWriter, r *http.Request) {
	shelves, err := s.db.ListShelvesForUser(UserID(r.Context()))
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]ShelfDTO, 0, len(shelves))
	for _, shelf := range shelves {
		out = append(out, shelfDTO(shelf))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIShelfCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Query  string `json:"query"`
		Shared bool   `json:"shared"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	ownerID := UserID(r.Context())
	visibility := db.ShelfPersonal
	if req.Shared {
		if _, ok := s.requireRole(w, r, db.RoleMember); !ok {
			return
		}
		visibility = db.ShelfShared
	}

	shelf, err := s.db.CreateShelf(ownerID, visibility, req.Name, db.ShelfKind(req.Kind), req.Query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusCreated, shelfDTO(*shelf))
}

func (s *Server) handleAPIShelfUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   *string `json:"name"`
		Query  *string `json:"query"`
		Shared *bool   `json:"shared"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	u := contextUser(r.Context())
	shelf, ok := s.requireMutableShelf(w, r, r.PathValue("id"))
	if !ok {
		return
	}

	name := shelf.Name
	if req.Name != nil {
		name = *req.Name
	}
	query := shelf.Query
	if req.Query != nil {
		if shelf.Kind != db.ShelfQuery {
			http.Error(w, "Manual shelves do not have a query", http.StatusBadRequest)
			return
		}
		query = *req.Query
	}
	visibility := shelf.Visibility
	if req.Shared != nil {
		nextVisibility := db.ShelfPersonal
		if *req.Shared {
			nextVisibility = db.ShelfShared
		}
		// Sharing/unsharing is the owner's call alone (member+): other
		// members may curate a shared shelf's contents but cannot take
		// ownership, unshare, or delete someone else's shelf.
		if nextVisibility != shelf.Visibility && (!db.RoleAtLeast(u.Role, db.RoleMember) || shelf.OwnerID != u.ID) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		visibility = nextVisibility
	}

	updated, err := s.db.UpdateShelf(r.PathValue("id"), u.ID, name, query, visibility)
	if writeShelfError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, shelfDTO(*updated))
}

func (s *Server) handleAPIShelfDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireDeletableShelf(w, r, r.PathValue("id")); !ok {
		return
	}
	if writeShelfError(w, s.db.DeleteShelf(r.PathValue("id"), UserID(r.Context()))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIShelfAddBook(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("workID")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}
	if _, ok := s.requireMutableShelf(w, r, r.PathValue("id")); !ok {
		return
	}
	err := s.db.AddBookToShelf(r.PathValue("id"), UserID(r.Context()), workID)
	if writeShelfError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIShelfRemoveBook(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("workID")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}
	if _, ok := s.requireMutableShelf(w, r, r.PathValue("id")); !ok {
		return
	}
	err := s.db.RemoveBookFromShelf(r.PathValue("id"), UserID(r.Context()), workID)
	if writeShelfError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// bulkShelfRequest is the body of POST /api/shelves/{id}/books/bulk.
type bulkShelfRequest struct {
	IDs []string `json:"ids"`
	Op  string   `json:"op"` // "add" | "remove"
}

type bulkShelfResponse struct {
	Changed int `json:"changed"`
}

// handleAPIShelfBulkBooks adds or removes a selection of books to/from one manual
// shelf. Like the single membership routes it is per-user (reader+), but only
// touches the works the caller can actually see; already-present adds and
// absent removes are silently skipped, so `changed` is the real delta.
func (s *Server) handleAPIShelfBulkBooks(w http.ResponseWriter, r *http.Request) {
	shelfID := r.PathValue("id")
	if _, ok := s.requireMutableShelf(w, r, shelfID); !ok {
		return
	}
	var req bulkShelfRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Op != "add" && req.Op != "remove" {
		http.Error(w, "op must be add or remove", http.StatusBadRequest)
		return
	}
	ids := dedupStrings(req.IDs)
	if len(ids) == 0 {
		http.Error(w, "no book ids provided", http.StatusBadRequest)
		return
	}
	if len(ids) > bulkEditMaxIDs {
		http.Error(w, fmt.Sprintf("too many books selected (max %d)", bulkEditMaxIDs), http.StatusBadRequest)
		return
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	rows, err := db.BooksForBulkEdit(s.db, scope, ids)
	if err != nil {
		serverError(w, err)
		return
	}
	visible := make([]string, 0, len(rows))
	for _, row := range rows {
		visible = append(visible, row.ID)
	}

	viewerID := UserID(r.Context())
	var changed int
	if req.Op == "add" {
		changed, err = s.db.AddBooksToShelf(r.Context(), shelfID, viewerID, visible)
	} else {
		changed, err = s.db.RemoveBooksFromShelf(shelfID, viewerID, visible)
	}
	if writeShelfError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, bulkShelfResponse{Changed: changed})
}

func (s *Server) handleAPIBookShelves(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	u := contextUser(r.Context())
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}

	rows, err := s.db.ListBookShelfMemberships(UserID(r.Context()), workID)
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]BookShelfDTO, 0, len(rows))
	for _, row := range rows {
		if !db.RoleAtLeast(u.Role, db.RoleMember) && (row.Visibility != db.ShelfPersonal || row.OwnerID != u.ID) {
			continue
		}
		out = append(out, BookShelfDTO{ShelfDTO: shelfDTO(row.Shelf), InShelf: row.InShelf})
	}
	writeJSON(w, http.StatusOK, out)
}

// requireMutableShelf: members curate any shared shelf; readers only their
// own personal shelves (which never grant scope — see db.VisibilityScope).
// A shelf assigned as someone's scope is an access boundary, so a trusted
// member editing it is changing access policy, not just organization — the
// accepted household-trust compromise; revisit only if it proves too loose.
func (s *Server) requireMutableShelf(w http.ResponseWriter, r *http.Request, shelfID string) (*db.Shelf, bool) {
	u := contextUser(r.Context())
	shelf, err := s.db.GetShelf(shelfID, u.ID)
	if writeShelfError(w, err) {
		return nil, false
	}
	if db.RoleAtLeast(u.Role, db.RoleMember) || (shelf.Visibility == db.ShelfPersonal && shelf.OwnerID == u.ID) {
		return shelf, true
	}
	http.Error(w, "Forbidden", http.StatusForbidden)
	return nil, false
}

func (s *Server) requireDeletableShelf(w http.ResponseWriter, r *http.Request, shelfID string) (*db.Shelf, bool) {
	u := contextUser(r.Context())
	shelf, err := s.db.GetShelf(shelfID, u.ID)
	if writeShelfError(w, err) {
		return nil, false
	}
	if shelf.OwnerID != u.ID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil, false
	}
	if shelf.Visibility == db.ShelfShared && !db.RoleAtLeast(u.Role, db.RoleMember) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil, false
	}
	return shelf, true
}

func writeShelfError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, db.ErrShelfNotFound):
		http.Error(w, "Shelf not found", http.StatusNotFound)
	case errors.Is(err, db.ErrQueryShelf), errors.Is(err, db.ErrEmptyShelfName), errors.Is(err, db.ErrShelfOwnerRequired):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		serverError(w, err)
	}
	return true
}
