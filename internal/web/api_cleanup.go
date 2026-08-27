package web

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/relayout"
	"github.com/levmv/polka/internal/storage"
)

type cleanupDuplicateDismissRequest struct {
	WorkIDs []string `json:"work_ids"`
}

type cleanupDuplicateMergeRequest struct {
	SurvivorID string   `json:"survivor_id"`
	WorkIDs    []string `json:"work_ids"`
}

type cleanupDuplicateMergeResponse struct {
	Survivor         BookSummaryDTO `json:"survivor"`
	TrashedIDs       []string       `json:"trashed_ids"`
	RelayoutWarnings int            `json:"relayout_warnings"`
}

type CleanupCategory struct {
	Count int `json:"count"`
}

type DuplicateGroupAPI struct {
	Reason string           `json:"reason"`
	Key    string           `json:"key"`
	Books  []BookSummaryDTO `json:"books"`
}

type PossibleDuplicatesCategory struct {
	Count  int                 `json:"count"`
	Groups []DuplicateGroupAPI `json:"groups"`
}

type Cleanup struct {
	MissingCover       CleanupCategory            `json:"missing_cover"`
	UnknownAuthor      CleanupCategory            `json:"unknown_author"`
	NoTags             CleanupCategory            `json:"no_tags"`
	NoDescription      CleanupCategory            `json:"no_description"`
	PossibleDuplicates PossibleDuplicatesCategory `json:"possible_duplicates"`
}

func (s *Server) handleAPICleanup(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}

	counts, err := db.GetCleanupCounts(s.db, scope)
	if err != nil {
		serverError(w, err)
		return
	}

	limit := 24
	dupCount, dupGroups, err := db.GetPossibleDuplicates(s.db, scope, limit)
	if err != nil {
		serverError(w, err)
		return
	}

	var cleanup Cleanup
	cleanup.MissingCover.Count = counts.MissingCover
	cleanup.UnknownAuthor.Count = counts.UnknownAuthor
	cleanup.NoTags.Count = counts.NoTags
	cleanup.NoDescription.Count = counts.NoDescription

	cleanup.PossibleDuplicates.Count = dupCount
	var apiDupGroups []DuplicateGroupAPI
	for _, g := range dupGroups {
		books, err := s.bookSummaryDTOs(g.Books)
		if err != nil {
			serverError(w, err)
			return
		}
		apiDupGroups = append(apiDupGroups, DuplicateGroupAPI{
			Reason: g.Reason,
			Key:    g.Key,
			Books:  books,
		})
	}
	// Always initialize so it serializes as an array even if empty
	if apiDupGroups == nil {
		apiDupGroups = []DuplicateGroupAPI{}
	}
	cleanup.PossibleDuplicates.Groups = apiDupGroups

	writeJSON(w, http.StatusOK, cleanup)
}

func (s *Server) handleAPICleanupDuplicateDismiss(w http.ResponseWriter, r *http.Request) {
	u := contextUser(r.Context())

	var req cleanupDuplicateDismissRequest
	if !readJSON(w, r, &req) {
		return
	}
	ids := dedupStrings(req.WorkIDs)
	if len(ids) < 2 {
		http.Error(w, "at least two book ids are required", http.StatusBadRequest)
		return
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	err = s.db.Transact(r.Context(), func(tx *sql.Tx) error {
		return db.DismissDuplicateGroup(tx, scope, ids, u.ID)
	})
	if writeDuplicateMutationError(w, err) {
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPICleanupDuplicateMerge(w http.ResponseWriter, r *http.Request) {
	u := contextUser(r.Context())

	var req cleanupDuplicateMergeRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.SurvivorID = strings.TrimSpace(req.SurvivorID)
	ids := dedupStrings(req.WorkIDs)
	if req.SurvivorID == "" || len(ids) < 2 {
		http.Error(w, "survivor_id and at least two book ids are required", http.StatusBadRequest)
		return
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	coverSourceID, err := db.DuplicateMergeCoverSource(s.db, scope, req.SurvivorID, ids)
	if writeDuplicateMutationError(w, err) {
		return
	}

	coverFromID := ""
	var coverBytes []byte
	if coverSourceID != "" {
		if b, ok := s.readDuplicateCover(coverSourceID); ok {
			coverFromID = coverSourceID
			coverBytes = b
		}
	}

	var result db.DuplicateMergeResult
	dataRoot := s.dataRoot()
	coverRel := covers.OriginalPath(req.SurvivorID)
	coverTempRel := ""
	cleanupCoverTemp := false
	if coverFromID != "" {
		coverTempRel, err = storage.WriteAdjacentTemp(dataRoot, coverRel, req.SurvivorID+"-cover", coverBytes)
		if err != nil {
			serverError(w, err)
			return
		}
		cleanupCoverTemp = true
		defer func() {
			if !cleanupCoverTemp || coverTempRel == "" {
				return
			}
			tempPath, err := dataRoot.Resolve(coverTempRel)
			if err == nil {
				_ = os.Remove(tempPath)
			}
		}()
	}

	releaseStorageSlot, err := s.acquireStorageWorkSlot(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	defer releaseStorageSlot()

	mutation, err := relayout.MutateWorks(r.Context(), s.db, s.managedRoot(), func(tx *sql.Tx) (relayout.Changed, error) {
		var err error
		result, err = db.MergeDuplicateWorks(tx, scope, db.DuplicateMergeRequest{
			SurvivorID:  req.SurvivorID,
			WorkIDs:     ids,
			DeletedBy:   u.ID,
			CoverFromID: coverFromID,
		})
		if err != nil {
			return relayout.Changed{}, err
		}
		return relayout.Changed{
			BumpMetadataRev: []string{result.SurvivorID},
			Relayout:        []string{result.SurvivorID},
			Reindex:         append([]string{result.SurvivorID}, result.TrashedIDs...),
		}, nil
	})
	if writeDuplicateMutationError(w, err) {
		return
	}
	cleanupCoverTemp = false

	if coverTempRel != "" {
		if err := storage.ReplaceWithStaged(dataRoot, coverTempRel, coverRel); err != nil {
			serverError(w, err)
			return
		}
		coverTempRel = ""
	}

	relayoutWarnings := len(mutation.Warnings)
	for _, warning := range mutation.Warnings {
		log.Printf("relayout after duplicate merge of %s: %v", req.SurvivorID, warning)
	}

	if result.FilledCover {
		covers.RemoveDerived(dataRoot, req.SurvivorID)
	}

	rows, err := db.BookSummaryRowsByIDs(s.db, scope, []string{req.SurvivorID})
	if err != nil {
		serverError(w, err)
		return
	}
	books, err := s.bookSummaryDTOs(orderSummaryRows([]string{req.SurvivorID}, rows))
	if err != nil {
		serverError(w, err)
		return
	}
	if len(books) == 0 {
		http.Error(w, "Book not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, cleanupDuplicateMergeResponse{
		Survivor:         books[0],
		TrashedIDs:       result.TrashedIDs,
		RelayoutWarnings: relayoutWarnings,
	})
}

func (s *Server) readDuplicateCover(workID string) ([]byte, bool) {
	coverPath, err := s.dataRoot().Resolve(covers.OriginalPath(workID))
	if err != nil {
		log.Printf("duplicate merge cover source %s: %v", workID, err)
		return nil, false
	}
	b, err := os.ReadFile(coverPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("duplicate merge cover source %s: %v", workID, err)
		}
		return nil, false
	}
	return b, true
}

func writeDuplicateMutationError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, db.ErrInvalidDuplicateGroup):
		http.Error(w, "Duplicate group changed", http.StatusConflict)
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "Book not found", http.StatusNotFound)
	default:
		serverError(w, err)
	}
	return true
}
