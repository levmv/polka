package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

// TrashedBookDTO is a trashed book for the trash view: the normal library-card
// shape plus when it was trashed and who trashed it.
type TrashedBookDTO struct {
	BookSummaryDTO
	DeletedAt int64  `json:"deleted_at"`
	DeletedBy string `json:"deleted_by,omitempty"`
}

// handleAPIBookDelete soft-deletes (trashes) a book. It is a catalog mutation,
// so readers cannot do it; the physical files stay untouched until admin purge.
func (s *Server) handleAPIBookDelete(w http.ResponseWriter, r *http.Request) {
	u := contextUser(r.Context())
	bookID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireBookAccess(w, r, bookID); !ok {
		return
	}
	if err := db.SoftDeleteBook(s.db.Write(r.Context()), bookID, u.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Book not found", http.StatusNotFound)
			return
		}
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIBookRestore returns a trashed book to the live catalog.
func (s *Server) handleAPIBookRestore(w http.ResponseWriter, r *http.Request) {
	bookID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, ok := s.requireTrashedBookAccess(w, r, bookID); !ok {
		return
	}
	if err := db.RestoreBook(s.db.Write(r.Context()), bookID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Book not in trash", http.StatusNotFound)
			return
		}
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIBookPurge permanently deletes one trashed book. The shared batch
// operation below owns storage admission, serialization, DB ordering, and file
// cleanup; the handler only maps the domain outcome to HTTP.
func (s *Server) handleAPIBookPurge(w http.ResponseWriter, r *http.Request) {
	bookID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	if _, err := s.purgeTrashedBooks(r.Context(), []int64{bookID}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Book not in trash", http.StatusNotFound)
			return
		}
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPITrashEmpty is the all-trashed sibling of single purge.
func (s *Server) handleAPITrashEmpty(w http.ResponseWriter, r *http.Request) {
	n, err := s.purgeTrashedBooks(r.Context(), nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeTrashPurgedCount(w, n)
}

// purgeTrashedBooks owns one complete irreversible batch. A nil requested set
// means all currently trashed books; a non-nil set is explicit and must be
// entirely trashed or the operation returns sql.ErrNoRows. It holds the shared
// storage slot from inspection through best-effort cleanup, checks the books
// root before deleting authoritative rows, commits every DB deletion together,
// and sweeps orphan authors once via the matching DB purge primitive.
func (s *Server) purgeTrashedBooks(ctx context.Context, requested []int64) (int, error) {
	releaseStorageSlot, err := s.acquireStorageWorkSlot(ctx)
	if err != nil {
		return 0, err
	}
	defer releaseStorageSlot()

	explicit := requested != nil
	requested = db.DedupBookIDs(requested)
	if explicit && len(requested) == 0 {
		return 0, sql.ErrNoRows
	}

	var ids []int64
	var assets []db.AssetRow
	root := s.managedRoot()
	err = s.db.Transact(ctx, func(tx *db.Tx) error {
		ids, err = db.ListTrashedBookIDs(tx, requested...)
		if err != nil {
			return err
		}
		if explicit && len(ids) != len(requested) {
			return sql.ErrNoRows
		}
		if len(ids) == 0 {
			return nil
		}

		if explicit {
			assets, err = db.AssetsByBookIDs(tx, ids)
		} else {
			assets, err = db.AssetsForTrashedBooks(tx)
		}
		if err != nil {
			return err
		}
		catalogHasBooks, err := db.HasAnyAsset(tx)
		if err != nil {
			return err
		}
		if err := storage.RequireWritableRoot(root, catalogHasBooks); err != nil {
			return err
		}

		var purged int
		if explicit {
			purged, err = db.PurgeBooks(tx, ids)
		} else {
			purged, err = db.PurgeAllTrashedBooks(tx)
		}
		if err != nil {
			return err
		}
		if purged != len(ids) {
			return fmt.Errorf("purged %d of %d selected books", purged, len(ids))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	// SQLite is durable first. Missing individual files are an idempotent no-op;
	// other unlink failures leave only repairable orphans and do not falsify the
	// already-committed API result.
	for _, asset := range assets {
		if err := storage.Remove(root, asset.StoragePath); err != nil {
			log.Printf("purge: remove asset %d: %v", asset.ID, err)
		}
	}
	coverRoot := s.dataRoot()
	for _, id := range ids {
		covers.RemoveDerived(coverRoot, id)
		if err := storage.Remove(coverRoot, covers.OriginalPath(id)); err != nil {
			log.Printf("purge: remove cover %d: %v", id, err)
		}
	}
	return len(ids), nil
}

func writeTrashPurgedCount(w http.ResponseWriter, n int) {
	writeJSON(w, http.StatusOK, map[string]int{"purged": n})
}

// handleAPITrash lists the trashed books for the trash view.
func (s *Server) handleAPITrash(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	rows, err := db.ListTrashedBooks(s.db.Read(r.Context()), scope)
	if err != nil {
		serverError(w, r, err)
		return
	}

	summaryRows := make([]db.BookSummaryRow, len(rows))
	for i := range rows {
		summaryRows[i] = rows[i].BookSummaryRow
	}
	dtos, err := s.bookSummaryDTOs(r.Context(), summaryRows)
	if err != nil {
		serverError(w, r, err)
		return
	}

	out := make([]TrashedBookDTO, len(dtos))
	for i := range dtos {
		out[i] = TrashedBookDTO{
			BookSummaryDTO: dtos[i],
			DeletedAt:      rows[i].DeletedAt,
			DeletedBy:      rows[i].DeletedByName,
		}
	}

	writeJSON(w, http.StatusOK, out)
}
