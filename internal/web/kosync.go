package web

import (
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/db"
)

type koReaderAuthDTO struct {
	Username string `json:"username"`
}

type koReaderProgressRequest struct {
	Document string `json:"document"`
	// Accept optional KOReader metadata without changing the catalog.
	Metadata   jsontext.Value `json:"metadata"`
	Progress   string         `json:"progress"`
	Percentage float64        `json:"percentage"`
	Device     string         `json:"device"`
	DeviceID   string         `json:"device_id"`
}

type koReaderProgressDTO struct {
	Document   string  `json:"document,omitempty"`
	Progress   string  `json:"progress,omitempty"`
	Percentage float64 `json:"percentage"`
	Device     string  `json:"device,omitempty"`
	DeviceID   string  `json:"device_id,omitempty"`
	Timestamp  int64   `json:"timestamp,omitzero"`
}

func (s *Server) handleKOReaderAuth(w http.ResponseWriter, r *http.Request) {
	user, err := db.GetUserByID(s.db.Read(r.Context()), UserID(r.Context()))
	if err != nil {
		serverError(w, r, err)
		return
	}
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, koReaderAuthDTO{Username: user.Username})
}

func (s *Server) handleKOReaderProgressSave(w http.ResponseWriter, r *http.Request) {
	var req koReaderProgressRequest
	if !readJSON(w, r, &req) {
		return
	}
	if _, ok := s.requireKOReaderDocument(w, r, req.Document); !ok {
		return
	}

	progress, err := s.db.SaveKOReaderProgress(r.Context(), UserID(r.Context()), db.KOReaderProgress{
		DocumentHash: req.Document,
		Position:     req.Progress,
		Progress:     req.Percentage,
		DeviceName:   req.Device,
		DeviceID:     req.DeviceID,
	})
	if err != nil {
		if errors.Is(err, db.ErrKOReaderInvalidInput) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Document  string `json:"document"`
		Timestamp int64  `json:"timestamp"`
	}{strings.TrimSpace(req.Document), progress.UpdatedAt})
}

func (s *Server) handleKOReaderProgress(w http.ResponseWriter, r *http.Request) {
	document := strings.TrimSpace(r.PathValue("document"))
	if document == "" || len(document) > 256 {
		http.Error(w, db.ErrKOReaderInvalidInput.Error(), http.StatusBadRequest)
		return
	}
	target, ok := s.requireKOReaderDocument(w, r, document)
	if !ok {
		return
	}
	progress, err := s.koReaderState(r.Context(), UserID(r.Context()), target.AssetID, document)
	if err != nil {
		serverError(w, r, err)
		return
	}

	if progress == nil || progress.Position == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, koReaderProgressDTO{
		Document:   document,
		Progress:   progress.Position,
		Percentage: progress.Progress,
		Device:     progress.DeviceName,
		DeviceID:   progress.DeviceID,
		Timestamp:  progress.UpdatedAt,
	})
}

func (s *Server) requireKOReaderDocument(w http.ResponseWriter, r *http.Request, documentHash string) (db.KOReaderHashTarget, bool) {
	target, err := db.ResolveKOReaderHash(s.db.Read(r.Context()), documentHash)
	if err != nil {
		serverError(w, r, err)
		return target, false
	}
	if target.BookID == 0 {
		return target, true
	}
	_, accessOK := s.requireBookAccess(w, r, target.BookID)
	return target, accessOK
}
