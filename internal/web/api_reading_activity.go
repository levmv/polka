package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/levmv/polka/internal/db"
)

type readingActivityRequest struct {
	Segment        int64  `json:"segment"`
	SessionID      string `json:"session_id"`
	ElapsedMS      int64  `json:"elapsed_ms"`
	LastActivityMS int64  `json:"last_activity_ms"`
	Finished       bool   `json:"finished"`
}

func (s *Server) handleAPIReadingActivity(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	var req readingActivityRequest
	if !readJSON(w, r, &req) {
		return
	}
	var result db.ReadingActivityResult
	var err error
	if r.Method == http.MethodPost {
		result, err = s.db.StartWebReadingSession(r.Context(), UserID(r.Context()), assetID, req.SessionID, req.Segment, time.Now())
	} else {
		result, err = s.db.CheckpointWebReadingSession(r.Context(), UserID(r.Context()), assetID, req.SessionID,
			db.ReadingActivityCheckpoint{
				Segment:        req.Segment,
				ElapsedMS:      req.ElapsedMS,
				LastActivityMS: req.LastActivityMS,
				Finished:       req.Finished,
			}, time.Now())
	}
	if errors.Is(err, db.ErrReadingTimeZoneRequired) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if writeReaderStateError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}
