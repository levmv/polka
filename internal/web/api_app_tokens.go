package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/db"
)

type AppTokenDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at,omitzero"`
}

type appTokenCreateRequest struct {
	Name string `json:"name"`
}

type appTokenCreateDTO struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

func appTokenDTO(t db.AppToken) AppTokenDTO {
	dto := AppTokenDTO{
		ID:        t.ID,
		Name:      t.Name,
		CreatedAt: t.CreatedAt,
	}
	if t.LastUsedAt.Valid {
		dto.LastUsedAt = &t.LastUsedAt.Int64
	}
	return dto
}

func appTokenDTOs(tokens []db.AppToken) []AppTokenDTO {
	out := make([]AppTokenDTO, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, appTokenDTO(token))
	}
	return out
}

func (s *Server) handleAPIAppTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.db.ListAppTokens(UserID(r.Context()))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, appTokenDTOs(tokens))
}

func (s *Server) handleAPIAppTokenCreate(w http.ResponseWriter, r *http.Request) {
	var req appTokenCreateRequest
	if !readJSON(w, r, &req) {
		return
	}

	name := strings.TrimSpace(req.Name)
	token, err := s.db.CreateAppToken(UserID(r.Context()), name)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrTokenNameExists):
			http.Error(w, "Token name already in use", http.StatusConflict)
		case errors.Is(err, db.ErrInvalidAppTokenInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			serverError(w, err)
		}
		return
	}

	writeJSON(w, http.StatusCreated, appTokenCreateDTO{Name: name, Token: token})
}

func (s *Server) handleAPIAppTokenDelete(w http.ResponseWriter, r *http.Request) {
	tokenID := r.PathValue("id")
	if tokenID == "" {
		http.Error(w, "Missing token ID", http.StatusBadRequest)
		return
	}
	if err := s.db.RevokeAppTokenByID(UserID(r.Context()), tokenID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Token not found", http.StatusNotFound)
			return
		}
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
