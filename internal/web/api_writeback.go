package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/writeback"
)

// bookWritebackResultDTO reports the outcome of the manual single-book write,
// plus the refreshed book so the caller can update the action's state in one
// round trip.
type bookWritebackResultDTO struct {
	Written   int           `json:"written"`
	Unchanged int           `json:"unchanged"`
	Failed    int           `json:"failed"`
	Errors    []string      `json:"errors,omitempty"`
	Book      BookDetailDTO `json:"book"`
}

type bulkWritebackRequest struct {
	IDs []string `json:"ids"`
}

type bulkWritebackResultDTO struct {
	Selected int `json:"selected"`
	Queued   int `json:"queued"`
}

type writebackRetryResultDTO struct {
	Queued  int             `json:"queued"`
	Storage AdminStorageDTO `json:"storage"`
}

// handleAPIBookWriteback runs the "Write metadata to file" action for one work:
// it embeds the current metadata snapshot into every writable asset. Admin-only
// (route role), synchronous, and mode-agnostic like the CLI — the mode governs
// UI affordances, not an explicit operator action.
func (s *Server) handleAPIBookWriteback(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	// Confirm the work exists and is visible before touching files.
	if _, err := db.GetBook(s.db, scope, workID); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Book not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}

	root := s.managedRoot()
	catalogHasBooks, err := db.HasAnyAsset(s.db.DB)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := storage.RequireWritableRoot(root, catalogHasBooks); err != nil {
		serverError(w, err)
		return
	}

	summary, err := writeback.Run(r.Context(), s.db, root, writeback.Options{
		WorkIDs:   []string{workID},
		Scope:     scope,
		CoverRoot: storage.NewRoot(s.dataDir),
		WorkQueue: s.storageQueue,
	})
	if err != nil {
		serverError(w, err)
		return
	}

	refreshed, err := s.bookDetailDTO(scope, UserID(r.Context()), workID, true)
	if err != nil {
		serverError(w, err)
		return
	}

	result := bookWritebackResultDTO{
		Written:   summary.Written,
		Unchanged: summary.Unchanged,
		Failed:    summary.Failed,
		Book:      refreshed,
	}
	for _, res := range summary.Results {
		if res.Status == writeback.StatusFailed && res.Error != "" {
			result.Errors = append(result.Errors, res.Error)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAPIBulkWriteback(w http.ResponseWriter, r *http.Request) {
	var req bulkWritebackRequest
	if !readJSON(w, r, &req) {
		return
	}
	ids := dedupStrings(req.IDs)
	if len(ids) == 0 {
		http.Error(w, "no book ids provided", http.StatusBadRequest)
		return
	}
	if len(ids) > bulkEditMaxIDs {
		http.Error(w, fmt.Sprintf("too many books selected (max %d)", bulkEditMaxIDs), http.StatusBadRequest)
		return
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	rows, err := db.BooksForBulkEdit(s.db, scope, ids)
	if err != nil {
		serverError(w, err)
		return
	}
	visible := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		visible[row.ID] = struct{}{}
	}
	workIDs := make([]string, 0, len(rows))
	for _, id := range ids {
		if _, ok := visible[id]; ok {
			workIDs = append(workIDs, id)
		}
	}
	assetRows, err := db.ListMetadataWritebackAssetsByWorkIDs(s.db, scope, workIDs, 0)
	if err != nil {
		serverError(w, err)
		return
	}
	statusCode := http.StatusOK
	if len(assetRows) > 0 {
		root := s.managedRoot()
		if err := requireWritebackRoot(s.db, root); err != nil {
			serverError(w, err)
			return
		}
		if !s.startWritebackRun(root, writeback.Options{
			WorkIDs:   workIDs,
			Scope:     scope,
			CoverRoot: s.dataRoot(),
			WorkQueue: s.storageQueue,
		}) {
			http.Error(w, "Server is stopping", http.StatusServiceUnavailable)
			return
		}
		statusCode = http.StatusAccepted
	}
	writeJSON(w, statusCode, bulkWritebackResultDTO{
		Selected: len(workIDs),
		Queued:   len(assetRows),
	})
}

func (s *Server) handleAPIAdminWritebackRetry(w http.ResponseWriter, r *http.Request) {
	counts, err := db.CountDirtyMetadataWritebackAssets(s.db.DB, db.FullVisibilityScope())
	if err != nil {
		serverError(w, err)
		return
	}
	status, err := s.adminStorageStatus()
	if err != nil {
		serverError(w, err)
		return
	}
	if counts.Failed > 0 {
		root := s.managedRoot()
		if err := requireWritebackRoot(s.db, root); err != nil {
			serverError(w, err)
			return
		}
		if !s.startWritebackRun(root, writeback.Options{
			FailedOnly: true,
			CoverRoot:  s.dataRoot(),
			WorkQueue:  s.storageQueue,
		}) {
			http.Error(w, "Server is stopping", http.StatusServiceUnavailable)
			return
		}
	}
	statusCode := http.StatusOK
	if counts.Failed > 0 {
		statusCode = http.StatusAccepted
	}
	writeJSON(w, statusCode, writebackRetryResultDTO{Queued: counts.Failed, Storage: status})
}

func (s *Server) startWritebackRun(root storage.Root, opts writeback.Options) bool {
	if s.background == nil || !s.background.Go(func(ctx context.Context) {
		summary, err := writeback.Run(ctx, s.db, root, opts)
		if err != nil {
			log.Printf("metadata write-back run failed: %v", err)
			return
		}
		if summary.Failed > 0 {
			log.Printf("metadata write-back run completed with failures: written=%d unchanged=%d failed=%d", summary.Written, summary.Unchanged, summary.Failed)
		}
	}) {
		log.Printf("metadata write-back run not started: server is stopping")
		return false
	}
	return true
}

func requireWritebackRoot(database *db.DB, root storage.Root) error {
	catalogHasBooks, err := db.HasAnyAsset(database.DB)
	if err != nil {
		return err
	}
	return storage.RequireWritableRoot(root, catalogHasBooks)
}
