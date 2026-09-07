package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/levmv/polka/internal/db"
)

type KoboConnectionDTO struct {
	ID         string `json:"id"`
	ShelfID    string `json:"shelf_id"`
	ShelfName  string `json:"shelf_name"`
	SetupURL   string `json:"setup_url"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	LastUsedAt *int64 `json:"last_used_at,omitzero"`
}

type koboConnectionCreateRequest struct {
	ShelfID string `json:"shelf_id"`
}

func koboConnectionDTO(r *http.Request, connection *db.KoboConnection) KoboConnectionDTO {
	dto := KoboConnectionDTO{
		ID:        connection.ID,
		ShelfID:   connection.ShelfID,
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
	var req koboConnectionCreateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.ShelfID == "" {
		http.Error(w, "Shelf is required", http.StatusBadRequest)
		return
	}
	connection, err := s.db.ReplaceKoboConnection(r.Context(), UserID(r.Context()), req.ShelfID)
	if errors.Is(err, db.ErrShelfNotFound) {
		http.Error(w, "Shelf not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, koboConnectionDTO(r, connection))
}

func (s *Server) handleAPIKoboConnectionDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteKoboConnection(r.Context(), UserID(r.Context())); err != nil {
		if errors.Is(err, db.ErrKoboConnectionNotFound) {
			http.Error(w, "Kobo connection not found", http.StatusNotFound)
			return
		}
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
