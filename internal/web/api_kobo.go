package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/levmv/polka/internal/db"
)

type KoboConnectionDTO struct {
	ID         int64  `json:"id"`
	ShelfID    int64  `json:"shelf_id,omitzero"`
	ShelfName  string `json:"shelf_name"`
	SetupURL   string `json:"setup_url"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	LastUsedAt *int64 `json:"last_used_at,omitzero"`
}

type koboConnectionShelfRequest struct {
	ShelfID int64 `json:"shelf_id"`
}

func koboConnectionDTO(r *http.Request, connection *db.KoboConnection) KoboConnectionDTO {
	dto := KoboConnectionDTO{
		ID:        connection.ID,
		ShelfID:   connection.ShelfID.Int64,
		ShelfName: connection.ShelfName,
		SetupURL:  absoluteURL(r, "/kobo/"+url.PathEscape(connection.Token), nil),
		CreatedAt: connection.CreatedAt,
		UpdatedAt: connection.UpdatedAt,
	}
	if connection.LastUsedAt.Valid {
		dto.LastUsedAt = &connection.LastUsedAt.Int64
	}
	return dto
}

func (s *Server) handleAPIKoboConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	connection, err := db.KoboConnectionForUser(s.db.Read(r.Context()), UserID(r.Context()))
	if errors.Is(err, db.ErrKoboConnectionNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, koboConnectionDTO(r, connection))
}

func (s *Server) handleAPIKoboConnectionCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	var req koboConnectionShelfRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.ShelfID <= 0 {
		http.Error(w, "Shelf is required", http.StatusBadRequest)
		return
	}
	connection, err := s.db.CreateKoboConnection(r.Context(), UserID(r.Context()), req.ShelfID)
	if writeKoboConnectionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, koboConnectionDTO(r, connection))
}

func (s *Server) handleAPIKoboConnectionShelf(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	var req koboConnectionShelfRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.ShelfID <= 0 {
		http.Error(w, "Shelf is required", http.StatusBadRequest)
		return
	}
	connection, err := s.db.SetKoboConnectionShelf(r.Context(), UserID(r.Context()), req.ShelfID)
	if writeKoboConnectionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, koboConnectionDTO(r, connection))
}

func writeKoboConnectionError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, db.ErrShelfNotFound):
		http.Error(w, "Shelf not found", http.StatusBadRequest)
	case errors.Is(err, db.ErrKoboConnectionNotFound):
		http.Error(w, "Kobo connection not found", http.StatusNotFound)
	case errors.Is(err, db.ErrKoboConnectionExists):
		http.Error(w, "Kobo is already connected; change its shelf or revoke the connection first", http.StatusConflict)
	default:
		serverError(w, r, err)
	}
	return true
}

func (s *Server) handleAPIKoboConnectionDelete(w http.ResponseWriter, r *http.Request) {
	if writeKoboConnectionError(w, r, s.db.DeleteKoboConnection(r.Context(), UserID(r.Context()))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
