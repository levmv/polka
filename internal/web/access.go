package web

import (
	"database/sql"
	"errors"
	"net/http"

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
	return s.db.VisibilityScopeForUser(UserID(r.Context()))
}

func (s *Server) requireAccess(
	w http.ResponseWriter,
	r *http.Request,
	resourceID string,
	canAccess func(db.Queryer, db.VisibilityScope, string) (bool, error),
) (db.VisibilityScope, bool) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		} else {
			serverError(w, err)
		}
		return db.VisibilityScope{}, false
	}
	ok, err := canAccess(s.db, scope, resourceID)
	if err != nil {
		serverError(w, err)
		return db.VisibilityScope{}, false
	}
	if !ok {
		http.NotFound(w, r)
		return db.VisibilityScope{}, false
	}
	return scope, true
}

func (s *Server) requireWorkAccess(w http.ResponseWriter, r *http.Request, workID string) (db.VisibilityScope, bool) {
	return s.requireAccess(w, r, workID, db.CanAccessWork)
}

func (s *Server) requireTrashedWorkAccess(w http.ResponseWriter, r *http.Request, workID string) (db.VisibilityScope, bool) {
	return s.requireAccess(w, r, workID, db.CanAccessTrashedWork)
}

func (s *Server) requireAssetAccess(w http.ResponseWriter, r *http.Request, assetID string) (db.VisibilityScope, bool) {
	return s.requireAccess(w, r, assetID, db.CanAccessAsset)
}
