package web

import (
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/db"
)

func (s *Server) handleAPITags(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	tags, err := db.ListTags(s.db, scope, q, 20)
	if err != nil {
		serverError(w, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, tags)
}

type searchQueryValidateRequest struct {
	Query string `json:"query"`
}

type searchQueryValidationDTO struct {
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleAPISearchValidate(w http.ResponseWriter, r *http.Request) {
	var req searchQueryValidateRequest
	if !readJSON(w, r, &req) {
		return
	}

	validation := db.ValidateSearchQuery(req.Query)
	writeJSON(w, http.StatusOK, searchQueryValidationDTO{
		Valid: validation.Valid,
		Error: validation.Error,
	})
}
