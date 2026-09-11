package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/levmv/polka/internal/db"
)

type UserDTO struct {
	ID            int64   `json:"id"`
	Username      string  `json:"username"`
	Role          string  `json:"role"`
	ContentScope  string  `json:"content_scope"`
	ScopeShelfIDs []int64 `json:"scope_shelf_ids,omitempty"`
	// SharedShelfNames are the shared shelves this user owns; deleting the user
	// removes them for the whole household, so the delete UI warns with them.
	SharedShelfNames []string `json:"shared_shelf_names,omitempty"`
	CreatedAt        int64    `json:"created_at,omitzero"`
	UpdatedAt        int64    `json:"updated_at,omitzero"`
}

type MeDTO struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	ContentScope string `json:"content_scope"`
}

func meDTO(u db.User) MeDTO {
	return MeDTO{ID: u.ID, Username: u.Username, Role: u.Role, ContentScope: u.ContentScope}
}

type userCreateRequest struct {
	Username      string  `json:"username"`
	Password      string  `json:"password"`
	Role          string  `json:"role"`
	ContentScope  string  `json:"content_scope"`
	ScopeShelfIDs []int64 `json:"scope_shelf_ids"`
}

type userPasswordRequest struct {
	Password string `json:"password"`
}

type userAccessRequest struct {
	Role          string  `json:"role"`
	ContentScope  string  `json:"content_scope"`
	ScopeShelfIDs []int64 `json:"scope_shelf_ids"`
}

func (s *Server) userDTO(ctx context.Context, u db.User) (UserDTO, error) {
	scopeShelfIDs, err := db.UserScopeShelfIDs(s.db.Read(ctx), u.ID)
	if err != nil {
		return UserDTO{}, err
	}
	sharedShelfNames, err := db.SharedShelfNamesOwnedBy(s.db.Read(ctx), u.ID)
	if err != nil {
		return UserDTO{}, err
	}
	return UserDTO{
		ID:               u.ID,
		Username:         u.Username,
		Role:             u.Role,
		ContentScope:     u.ContentScope,
		ScopeShelfIDs:    scopeShelfIDs,
		SharedShelfNames: sharedShelfNames,
		CreatedAt:        u.CreatedAt,
		UpdatedAt:        u.UpdatedAt,
	}, nil
}

func (s *Server) userDTOs(ctx context.Context, users []db.User) ([]UserDTO, error) {
	out := make([]UserDTO, 0, len(users))
	for _, u := range users {
		dto, err := s.userDTO(ctx, u)
		if err != nil {
			return nil, err
		}
		out = append(out, dto)
	}
	return out, nil
}

func (s *Server) requireRole(w http.ResponseWriter, r *http.Request, minRole string) (*db.User, bool) {
	u := contextUser(r.Context())
	if u == nil {
		userID := UserID(r.Context())
		if userID <= 0 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return nil, false
		}
		var err error
		u, err = db.GetUserByID(s.db.Read(r.Context()), userID)
		if err != nil {
			serverError(w, r, err)
			return nil, false
		}
		if u == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return nil, false
		}
	}
	if !db.RoleAtLeast(u.Role, minRole) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil, false
	}
	return u, true
}

func (s *Server) handleAPIMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, meDTO(*contextUser(r.Context())))
}

func (s *Server) handleAPIUsers(w http.ResponseWriter, r *http.Request) {
	users, err := db.ListUsers(s.db.Read(r.Context()))
	if err != nil {
		serverError(w, r, err)
		return
	}

	out, err := s.userDTOs(r.Context(), users)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIUserCreate(w http.ResponseWriter, r *http.Request) {
	var req userCreateRequest
	if !readJSON(w, r, &req) {
		return
	}
	contentScope := req.ContentScope
	if contentScope == "" && len(req.ScopeShelfIDs) > 0 {
		contentScope = db.ContentScopeShelves
	}
	u, err := s.db.CreateUserWithAccess(r.Context(), req.Username,
		req.Password,
		db.UserAccess{
			Role:          req.Role,
			ContentScope:  contentScope,
			ShelfIDs:      req.ScopeShelfIDs,
			ShelfViewerID: UserID(r.Context()),
		},
	)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrUserExists):
			http.Error(w, "Username already taken", http.StatusConflict)
		case errors.Is(err, db.ErrInvalidUserInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, db.ErrShelfNotFound):
			http.Error(w, "Scope shelf not found", http.StatusBadRequest)
		case errors.Is(err, db.ErrScopeShelfNotVisible):
			http.Error(w, "Scope shelf is not visible", http.StatusBadRequest)
		case errors.Is(err, db.ErrScopeShelfNotEligible):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			serverError(w, r, err)
		}
		return
	}

	dto, err := s.userDTO(r.Context(), *u)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) handleAPIUserUpdate(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	target, err := db.GetUserByID(s.db.Read(r.Context()), userID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if target == nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	var req userAccessRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Role == "" {
		req.Role = target.Role
	}
	if req.ContentScope == "" {
		req.ContentScope = target.ContentScope
	}

	updated, err := s.db.UpdateUserAccess(r.Context(), userID, db.UserAccess{
		Role:          req.Role,
		ContentScope:  req.ContentScope,
		ShelfIDs:      req.ScopeShelfIDs,
		ShelfViewerID: UserID(r.Context()),
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "User not found", http.StatusNotFound)
		case errors.Is(err, db.ErrLastAdmin):
			http.Error(w, "Cannot demote the last admin", http.StatusConflict)
		case errors.Is(err, db.ErrInvalidUserInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, db.ErrShelfNotFound):
			http.Error(w, "Scope shelf not found", http.StatusBadRequest)
		case errors.Is(err, db.ErrScopeShelfNotVisible):
			http.Error(w, "Scope shelf is not visible", http.StatusBadRequest)
		case errors.Is(err, db.ErrScopeShelfNotEligible):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			serverError(w, r, err)
		}
		return
	}
	dto, err := s.userDTO(r.Context(), *updated)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleAPIUserDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.db.DeleteUser(r.Context(), userID); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "User not found", http.StatusNotFound)
		case errors.Is(err, db.ErrLastAdmin):
			http.Error(w, "Cannot remove the last admin", http.StatusConflict)
		default:
			serverError(w, r, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIUserPassword(w http.ResponseWriter, r *http.Request) {
	current := contextUser(r.Context())

	userID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if current.Role != db.RoleAdmin && current.ID != userID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req userPasswordRequest
	if !readJSON(w, r, &req) {
		return
	}

	var keepSessionHash []byte
	if current.ID == userID {
		if c, err := r.Cookie(sessionCookieName); err == nil {
			keepSessionHash = sessionTokenHash(c.Value)
		}
	}
	if err := s.db.SetUserPassword(r.Context(), userID, req.Password, keepSessionHash); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "User not found", http.StatusNotFound)
		case errors.Is(err, db.ErrInvalidUserInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			serverError(w, r, err)
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
