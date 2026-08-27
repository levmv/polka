package web

import (
	"encoding/json/jsontext"
	"errors"
	"net/http"

	"github.com/levmv/polka/internal/db"
)

type koReaderAuthDTO struct {
	Username string `json:"username"`
}

type koReaderProgressRequest struct {
	Document string `json:"document"`
	// Metadata is optional in the KOSync protocol. Polka owns book metadata in
	// SQLite, but it must still accept this client-supplied advisory object.
	Metadata   jsontext.Value `json:"metadata"`
	Progress   string         `json:"progress"`
	Percentage float64        `json:"percentage"`
	Device     string         `json:"device"`
	DeviceID   string         `json:"device_id"`
}

type koReaderProgressDTO struct {
	Document   string  `json:"document,omitempty"`
	Progress   string  `json:"progress,omitempty"`
	Percentage float64 `json:"percentage,omitzero"`
	Device     string  `json:"device,omitempty"`
	DeviceID   string  `json:"device_id,omitempty"`
	Timestamp  int64   `json:"timestamp,omitzero"`
}

func (s *Server) handleKOReaderAuth(w http.ResponseWriter, r *http.Request) {
	user, err := s.db.GetUserByID(UserID(r.Context()))
	if err != nil {
		serverError(w, err)
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
	if !s.allowKOReaderDocument(w, r, req.Document) {
		return
	}

	progress, _, err := s.db.SaveKOReaderProgressAndAdvanceStatus(r.Context(), UserID(r.Context()), db.KOReaderProgress{
		DocumentHash: req.Document,
		Progress:     req.Progress,
		Percentage:   req.Percentage,
		Device:       req.Device,
		DeviceID:     req.DeviceID,
	})
	if err != nil {
		if errors.Is(err, db.ErrKOReaderInvalidInput) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, koReaderProgressDTO{
		Document:  progress.DocumentHash,
		Timestamp: progress.UpdatedAt,
	})
}

func (s *Server) handleKOReaderProgress(w http.ResponseWriter, r *http.Request) {
	document := r.PathValue("document")
	if !s.allowKOReaderDocument(w, r, document) {
		return
	}
	progress, err := s.db.GetKOReaderProgress(UserID(r.Context()), document)
	if errors.Is(err, db.ErrKOReaderProgressNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if err != nil {
		if errors.Is(err, db.ErrKOReaderInvalidInput) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, koReaderProgressDTO{
		Document:   progress.DocumentHash,
		Progress:   progress.Progress,
		Percentage: progress.Percentage,
		Device:     progress.Device,
		DeviceID:   progress.DeviceID,
		Timestamp:  progress.UpdatedAt,
	})
}

func (s *Server) allowKOReaderDocument(w http.ResponseWriter, r *http.Request, documentHash string) bool {
	target, err := db.ResolveKOReaderHash(s.db, documentHash)
	if err != nil {
		serverError(w, err)
		return false
	}
	if target.WorkID == "" || target.Ambiguous {
		return true
	}
	_, accessOK := s.requireAssetAccess(w, r, target.AssetID)
	return accessOK
}
