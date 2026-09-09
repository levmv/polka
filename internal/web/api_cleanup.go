package web

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"slices"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/relayout"
	"github.com/levmv/polka/internal/storage"
)

type cleanupDuplicateDismissRequest struct {
	BookIDs []int64 `json:"book_ids"`
}

type cleanupDuplicateMergeRequest struct {
	SurvivorID int64   `json:"survivor_id"`
	BookIDs    []int64 `json:"book_ids"`
}

type cleanupDuplicateMergeResponse struct {
	Survivor         BookSummaryDTO `json:"survivor"`
	TrashedIDs       []int64        `json:"trashed_ids"`
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
	MissingAuthor      CleanupCategory            `json:"missing_author"`
	NoTags             CleanupCategory            `json:"no_tags"`
	NoDescription      CleanupCategory            `json:"no_description"`
	PossibleDuplicates PossibleDuplicatesCategory `json:"possible_duplicates"`
}

func (s *Server) handleAPICleanup(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return
	}

	counts, err := db.GetCleanupCounts(s.db.Read(r.Context()), scope)
	if err != nil {
		serverError(w, r, err)
		return
	}

	limit := 24
	dupCount, dupGroups, err := db.GetPossibleDuplicates(s.db.Read(r.Context()), scope, limit)
	if err != nil {
		serverError(w, r, err)
		return
	}

	var cleanup Cleanup
	cleanup.MissingCover.Count = counts.MissingCover
	cleanup.MissingAuthor.Count = counts.MissingAuthor
	cleanup.NoTags.Count = counts.NoTags
	cleanup.NoDescription.Count = counts.NoDescription

	cleanup.PossibleDuplicates.Count = dupCount
	var rows []db.BookSummaryRow
	for _, group := range dupGroups {
		rows = append(rows, group.Books...)
	}
	// Enrich groups together, bounding SQL parameter lists for large groups.
	books := make([]BookSummaryDTO, 0, len(rows))
	for chunk := range slices.Chunk(rows, 500) {
		batch, err := s.bookSummaryDTOs(r.Context(), chunk)
		if err != nil {
			serverError(w, r, err)
			return
		}
		books = append(books, batch...)
	}

	// bookSummaryDTOs preserves row order, so group boundaries stay unchanged.
	apiDupGroups := make([]DuplicateGroupAPI, 0, len(dupGroups))
	for _, group := range dupGroups {
		apiDupGroups = append(apiDupGroups, DuplicateGroupAPI{
			Reason: group.Reason,
			Key:    group.Key,
			Books:  books[:len(group.Books)],
		})
		books = books[len(group.Books):]
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
	ids := db.DedupBookIDs(req.BookIDs)
	if len(ids) < 2 {
		http.Error(w, "at least two book ids are required", http.StatusBadRequest)
		return
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	err = s.db.Transact(r.Context(), func(tx *db.Tx) error {
		return db.DismissDuplicateGroup(tx, scope, ids, u.ID)
	})
	if writeDuplicateMutationError(w, r, err) {
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
	ids := db.DedupBookIDs(req.BookIDs)
	if req.SurvivorID <= 0 || len(ids) < 2 {
		http.Error(w, "survivor_id and at least two book ids are required", http.StatusBadRequest)
		return
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	coverSourceID, err := db.DuplicateMergeCoverSource(s.db.Read(r.Context()), scope, req.SurvivorID, ids)
	if writeDuplicateMutationError(w, r, err) {
		return
	}

	coverFromID := int64(0)
	var coverBytes []byte
	if coverSourceID != 0 {
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
	if coverFromID != 0 {
		coverTempRel, err = storage.WriteAdjacentTemp(dataRoot, coverRel, covers.TempLabel(req.SurvivorID), coverBytes)
		if err != nil {
			serverError(w, r, err)
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
		serverError(w, r, err)
		return
	}
	defer releaseStorageSlot()

	mutation, err := relayout.MutateBooks(r.Context(), s.db, s.managedRoot(), func(tx *db.Tx) (relayout.Changed, error) {
		var err error
		result, err = db.MergeDuplicateBooks(tx, scope, db.DuplicateMergeRequest{
			SurvivorID:  req.SurvivorID,
			BookIDs:     ids,
			DeletedBy:   u.ID,
			CoverFromID: coverFromID,
		})
		if err != nil {
			return relayout.Changed{}, err
		}
		return relayout.Changed{
			BumpMetadataRev: []int64{result.SurvivorID},
			Relayout:        []int64{result.SurvivorID},
			Reindex:         append([]int64{result.SurvivorID}, result.TrashedIDs...),
		}, nil
	})
	if writeDuplicateMutationError(w, r, err) {
		return
	}
	cleanupCoverTemp = false

	if coverTempRel != "" {
		if err := storage.ReplaceWithStaged(dataRoot, coverTempRel, coverRel); err != nil {
			serverError(w, r, err)
			return
		}
		coverTempRel = ""
	}

	relayoutWarnings := len(mutation.Warnings)
	for _, warning := range mutation.Warnings {
		log.Printf("relayout after duplicate merge of %d: %v", req.SurvivorID, warning)
	}

	if result.FilledCover {
		covers.RemoveDerived(dataRoot, req.SurvivorID)
	}

	rows, err := db.BookSummaryRowsByIDs(s.db.Read(r.Context()), scope, []int64{req.SurvivorID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	books, err := s.bookSummaryDTOs(r.Context(), orderSummaryRows([]int64{req.SurvivorID}, rows))
	if err != nil {
		serverError(w, r, err)
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

func (s *Server) readDuplicateCover(bookID int64) ([]byte, bool) {
	coverPath, err := s.dataRoot().Resolve(covers.OriginalPath(bookID))
	if err != nil {
		log.Printf("duplicate merge cover source %d: %v", bookID, err)
		return nil, false
	}
	b, err := os.ReadFile(coverPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("duplicate merge cover source %d: %v", bookID, err)
		}
		return nil, false
	}
	return b, true
}

func writeDuplicateMutationError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, db.ErrInvalidDuplicateGroup):
		http.Error(w, "Duplicate group changed", http.StatusConflict)
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "Book not found", http.StatusNotFound)
	default:
		serverError(w, r, err)
	}
	return true
}
