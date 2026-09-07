package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/levmv/polka/internal/db"
)

// viewerIsAdmin reports whether the request's authenticated user is an admin.
// Route middleware stores the full user in context, so this needs no query;
// token/basic-auth paths (no context user) read as non-admin.
func (s *Server) viewerIsAdmin(r *http.Request) bool {
	u := contextUser(r.Context())
	return u != nil && u.Role == db.RoleAdmin
}

func (s *Server) visibilityScope(r *http.Request) (db.VisibilityScope, error) {
	if u := contextUser(r.Context()); u != nil {
		if u.Role == db.RoleReader && u.ContentScope == db.ContentScopeShelves {
			return db.VisibilityScope{UserID: u.ID, ContentScope: db.ContentScopeShelves}, nil
		}
		return db.FullVisibilityScope(), nil
	}
	// Route middleware stores the full user, while KOReader/basic-auth paths may
	// carry only a user id; load the scope from SQLite for those requests.
	return db.VisibilityScopeForUser(s.db.Read(r.Context()), UserID(r.Context()))
}

func requireAccess[T any](s *Server,
	w http.ResponseWriter,
	r *http.Request,
	resourceID T,
	canAccess func(db.Queryer, db.VisibilityScope, T) (bool, error),
) (db.VisibilityScope, bool) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		} else {
			serverError(w, r, err)
		}
		return db.VisibilityScope{}, false
	}
	ok, err := canAccess(s.db.Read(r.Context()), scope, resourceID)
	if err != nil {
		serverError(w, r, err)
		return db.VisibilityScope{}, false
	}
	if !ok {
		http.NotFound(w, r)
		return db.VisibilityScope{}, false
	}
	return scope, true
}

func (s *Server) requireBookAccess(w http.ResponseWriter, r *http.Request, bookID int64) (db.VisibilityScope, bool) {
	return requireAccess(s, w, r, bookID, db.CanAccessBook)
}

func (s *Server) requireTrashedBookAccess(w http.ResponseWriter, r *http.Request, bookID int64) (db.VisibilityScope, bool) {
	return requireAccess(s, w, r, bookID, db.CanAccessTrashedBook)
}

func (s *Server) requireAssetAccess(w http.ResponseWriter, r *http.Request, assetID string) (db.VisibilityScope, bool) {
	return requireAccess(s, w, r, assetID, db.CanAccessAsset)
}

func pathBookID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}
