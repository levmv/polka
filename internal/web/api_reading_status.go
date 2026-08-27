package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/db"
)

type ReadingStatusDTO struct {
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updated_at,omitzero"`
}

type readingStatusRequest struct {
	Status string `json:"status"`
}

type readingStatusUndoRequest struct {
	EventID string `json:"event_id"`
}

func readingStatusDTO(state db.ReadingStatusState) ReadingStatusDTO {
	return ReadingStatusDTO{Status: state.Status, UpdatedAt: state.UpdatedAt}
}

func (s *Server) handleAPIReadingStatusSave(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}
	var req readingStatusRequest
	if !readJSON(w, r, &req) {
		return
	}
	change, err := s.db.SetReadingStatus(r.Context(), UserID(r.Context()), workID, req.Status, db.ReadingStatusSourceManual)
	if writeReadingStatusError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, readingStatusDTO(change.State))
}

func (s *Server) handleAPIReadingStatusUndo(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}
	var req readingStatusUndoRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.EventID) == "" {
		http.Error(w, "Missing event_id", http.StatusBadRequest)
		return
	}
	change, err := s.db.UndoAutomaticReadingStatus(r.Context(), UserID(r.Context()), workID, req.EventID)
	if writeReadingStatusError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, readingStatusDTO(change.State))
}

func writeReadingStatusError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, db.ErrInvalidReadingStatus):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, db.ErrReadingStatusWorkMissing):
		http.Error(w, "Book not found", http.StatusNotFound)
	case errors.Is(err, db.ErrReadingStatusUndoUnavailable):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		serverError(w, err)
	}
	return true
}
