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
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	LastUsedAt *int64 `json:"last_used_at,omitzero"`
}

type koboConnectionCreateRequest struct {
	ShelfID string `json:"shelf_id"`
}

type koboConnectionCreateDTO struct {
	KoboConnectionDTO
	SetupURL string `json:"setup_url"`
}

func koboConnectionDTO(connection *db.KoboConnection) KoboConnectionDTO {
	dto := KoboConnectionDTO{
		ID:        connection.ID,
		ShelfID:   connection.ShelfID,
		ShelfName: connection.ShelfName,
		CreatedAt: connection.CreatedAt,
		UpdatedAt: connection.UpdatedAt,
	}
	if connection.LastUsedAt.Valid {
		dto.LastUsedAt = &connection.LastUsedAt.Int64
	}
	return dto
}

func (s *Server) handleAPIKoboConnection(w http.ResponseWriter, r *http.Request) {
	connection, err := s.db.KoboConnectionForUser(UserID(r.Context()))
	if errors.Is(err, db.ErrKoboConnectionNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, koboConnectionDTO(connection))
}

func (s *Server) handleAPIKoboConnectionCreate(w http.ResponseWriter, r *http.Request) {
	var req koboConnectionCreateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.ShelfID == "" {
		http.Error(w, "Shelf is required", http.StatusBadRequest)
		return
	}
	connection, token, err := s.db.ReplaceKoboConnection(r.Context(), UserID(r.Context()), req.ShelfID)
	if errors.Is(err, db.ErrShelfNotFound) {
		http.Error(w, "Shelf not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	setupPath := "/kobo/" + url.PathEscape(token)
	writeJSON(w, http.StatusCreated, koboConnectionCreateDTO{
		KoboConnectionDTO: koboConnectionDTO(connection),
		SetupURL:          absoluteURL(r, setupPath, nil),
	})
}

func (s *Server) handleAPIKoboConnectionDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteKoboConnection(UserID(r.Context())); err != nil {
		if errors.Is(err, db.ErrKoboConnectionNotFound) {
			http.Error(w, "Kobo connection not found", http.StatusNotFound)
			return
		}
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
