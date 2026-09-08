package web

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"strconv"

	"github.com/levmv/polka/internal/db"
)

type ReaderStateDTO struct {
	AssetID            int64            `json:"asset_id"`
	BookID             int64            `json:"book_id"`
	Progress           float64          `json:"progress"`
	Locator            db.ReaderLocator `json:"locator"`
	LastReadAt         int64            `json:"last_read_at,omitzero"`
	UpdatedAt          int64            `json:"updated_at,omitzero"`
	ReadingStatus      ReadingStatusDTO `json:"reading_status"`
	StatusChanged      bool             `json:"status_changed,omitzero"`
	StatusTransitionID int64            `json:"status_transition_id,omitzero"`
}

type ContinueReadingDTO struct {
	BookSummaryDTO
	AssetID    int64   `json:"asset_id"`
	Progress   float64 `json:"progress"`
	LastReadAt int64   `json:"last_read_at"`
}

type AnnotationDTO struct {
	ID            int64  `json:"id"`
	AssetID       int64  `json:"asset_id"`
	Kind          string `json:"kind"`
	CFI           string `json:"cfi"`
	Quote         string `json:"quote"`
	ContextBefore string `json:"context_before"`
	ContextAfter  string `json:"context_after"`
	Note          string `json:"note,omitempty"`
	Color         string `json:"color"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

type readerStateRequest struct {
	Progress *float64       `json:"progress"`
	Locator  jsontext.Value `json:"locator"`
}

type annotationRequest struct {
	Kind          string `json:"kind"`
	CFI           string `json:"cfi"`
	Quote         string `json:"quote"`
	ContextBefore string `json:"context_before"`
	ContextAfter  string `json:"context_after"`
	Note          string `json:"note"`
	Color         string `json:"color"`
}

type annotationUpdateRequest struct {
	Note  *string `json:"note,omitzero"`
	Color *string `json:"color,omitzero"`
}

func readerStateDTO(state *db.ReaderState, change db.ReadingStatusChange) ReaderStateDTO {
	return ReaderStateDTO{
		AssetID:            state.AssetID,
		BookID:             state.BookID,
		Progress:           state.Progress,
		Locator:            state.Locator,
		LastReadAt:         state.LastReadAt,
		UpdatedAt:          state.UpdatedAt,
		ReadingStatus:      readingStatusDTO(change.State),
		StatusChanged:      change.Changed,
		StatusTransitionID: change.EventID,
	}
}

func annotationDTO(ann db.Annotation) AnnotationDTO {
	return AnnotationDTO{
		ID:            ann.ID,
		AssetID:       ann.AssetID,
		Kind:          ann.Kind,
		CFI:           ann.CFI,
		Quote:         ann.Quote,
		ContextBefore: ann.ContextBefore,
		ContextAfter:  ann.ContextAfter,
		Note:          ann.Note,
		Color:         ann.Color,
		CreatedAt:     ann.CreatedAt,
		UpdatedAt:     ann.UpdatedAt,
	}
}

func annotationDTOs(rows []db.Annotation) []AnnotationDTO {
	out := make([]AnnotationDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, annotationDTO(row))
	}
	return out
}

func (s *Server) handleAPIContinueReading(w http.ResponseWriter, r *http.Request) {
	settings, err := db.GetUserSettings(s.db.Read(r.Context()), UserID(r.Context()))
	if writeUserSettingsError(w, r, err) {
		return
	}
	if !settings.ShowContinueReading {
		writeJSON(w, http.StatusOK, []ContinueReadingDTO{})
		return
	}

	limit := 8
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = min(parsed, 20)
		}
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	rows, err := db.ListContinueReading(s.db.Read(r.Context()), scope, UserID(r.Context()), limit)
	if writeReaderStateError(w, r, err) {
		return
	}

	books, err := s.continueReadingDTOs(r.Context(), rows)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, books)
}

func (s *Server) continueReadingDTOs(ctx context.Context, rows []db.ContinueReadingRow) ([]ContinueReadingDTO, error) {
	summaryRows := make([]db.BookSummaryRow, len(rows))
	for i, row := range rows {
		summaryRows[i] = row.BookSummaryRow
	}

	summaries, err := s.bookSummaryDTOs(ctx, summaryRows)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]BookSummaryDTO, len(summaries))
	for _, summary := range summaries {
		byID[summary.ID] = summary
	}

	out := make([]ContinueReadingDTO, 0, len(rows))
	for _, row := range rows {
		summary, ok := byID[row.ID]
		if !ok {
			continue
		}
		out = append(out, ContinueReadingDTO{
			BookSummaryDTO: summary,
			AssetID:        row.AssetID,
			Progress:       row.Progress,
			LastReadAt:     row.LastReadAt,
		})
	}
	return out, nil
}

func (s *Server) handleAPIReaderState(w http.ResponseWriter, r *http.Request) {
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	state, err := db.GetReaderState(s.db.Read(r.Context()), UserID(r.Context()), assetID)
	if writeReaderStateError(w, r, err) {
		return
	}
	status, err := db.GetReadingStatus(s.db.Read(r.Context()), UserID(r.Context()), state.BookID)
	if writeReaderStateError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, readerStateDTO(state, db.ReadingStatusChange{State: status}))
}

func (s *Server) handleAPIReaderStateSave(w http.ResponseWriter, r *http.Request) {
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	var req readerStateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Progress == nil {
		http.Error(w, "Missing progress", http.StatusBadRequest)
		return
	}
	if len(req.Locator) == 0 {
		http.Error(w, "Missing locator", http.StatusBadRequest)
		return
	}

	state, change, err := s.db.SaveReaderStateAndAdvanceStatus(
		r.Context(),
		UserID(r.Context()),
		assetID,
		*req.Progress,
		db.ReaderLocator(req.Locator),
		db.ReadingStatusSourceWebReader,
	)
	if writeReaderStateError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, readerStateDTO(state, change))
}

func (s *Server) handleAPIReaderStateReset(w http.ResponseWriter, r *http.Request) {
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	if writeReaderStateError(w, r, s.db.ResetReaderState(r.Context(), UserID(r.Context()), assetID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIReaderStateTouch(w http.ResponseWriter, r *http.Request) {
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	state, change, err := s.db.TouchReaderStateAndAdvanceStatus(
		r.Context(),
		UserID(r.Context()),
		assetID,
		db.ReadingStatusSourceWebReader,
	)
	if writeReaderStateError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, readerStateDTO(state, change))
}

func (s *Server) handleAPIBookAnnotations(w http.ResponseWriter, r *http.Request) {
	bookID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireBookAccess(w, r, bookID); !ok {
		return
	}
	rows, err := db.ListBookAnnotations(s.db.Read(r.Context()), UserID(r.Context()), bookID)
	if writeReaderStateError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, annotationDTOs(rows))
}

func (s *Server) handleAPIAnnotations(w http.ResponseWriter, r *http.Request) {
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	rows, err := db.ListAnnotations(s.db.Read(r.Context()), UserID(r.Context()), assetID)
	if writeReaderStateError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, annotationDTOs(rows))
}

func (s *Server) handleAPIAnnotationCreate(w http.ResponseWriter, r *http.Request) {
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	var req annotationRequest
	if !readJSON(w, r, &req) {
		return
	}
	ann, err := s.db.CreateAnnotation(r.Context(), UserID(r.Context()), assetID, db.AnnotationCreate{
		Kind:          req.Kind,
		CFI:           req.CFI,
		Quote:         req.Quote,
		ContextBefore: req.ContextBefore,
		ContextAfter:  req.ContextAfter,
		Note:          req.Note,
		Color:         req.Color,
	})
	if writeReaderStateError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, annotationDTO(ann))
}

func (s *Server) handleAPIAnnotationUpdate(w http.ResponseWriter, r *http.Request) {
	annotationID, validID := pathID(w, r, "annotationID")
	if !validID {
		return
	}
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	var req annotationUpdateRequest
	if !readJSON(w, r, &req) {
		return
	}
	ann, err := s.db.UpdateAnnotation(r.Context(), UserID(r.Context()), assetID, annotationID, db.AnnotationUpdate{
		Note:  req.Note,
		Color: req.Color,
	})
	if writeReaderStateError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, annotationDTO(ann))
}

func (s *Server) handleAPIAnnotationDelete(w http.ResponseWriter, r *http.Request) {
	annotationID, validID := pathID(w, r, "annotationID")
	if !validID {
		return
	}
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	err := s.db.DeleteAnnotation(r.Context(), UserID(r.Context()), assetID, annotationID)
	if writeReaderStateError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeReaderStateError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, db.ErrAssetNotFound):
		http.Error(w, "Asset not found", http.StatusNotFound)
	case errors.Is(err, db.ErrInvalidReaderInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, db.ErrAnnotationNotFound):
		http.Error(w, "Annotation not found", http.StatusNotFound)
	case errors.Is(err, db.ErrInvalidAnnotation):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		serverError(w, r, err)
	}
	return true
}
