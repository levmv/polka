package relayout

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

// Changed is the bookkeeping a book-metadata mutation owes after its own SQL
// writes. BumpMetadataRev marks user-visible metadata changes, Relayout marks
// canonical-path input changes, and Reindex covers searchable refreshes that do
// not themselves bump write-back dirtiness.
type Changed struct {
	BumpMetadataRev []int64
	Relayout        []int64
	Reindex         []int64
}

// MutationResult summarizes the post-commit storage maintenance. Warnings are
// non-fatal relayout or post-relayout reindex errors: the metadata commit
// already happened, and relayout.Book keeps DB/disk consistent for each failed
// move.
type MutationResult struct {
	Moved    int
	Warnings []error
}

// MutateBooks runs a book-metadata mutation with the shared catalog
// bookkeeping, in the order the storage/write-back invariants require:
//
//	tx:    caller writes, metadata_rev bump, search-index refresh
//	commit
//	after: relayout path-sensitive books, refresh filename search terms,
//	       with warning semantics
//
// A returned error means the transaction did not commit. Relayout failures are
// returned as warnings because the metadata change is durable and repair can
// recover any remaining storage drift.
func MutateBooks(ctx context.Context, database *db.DB, root storage.Root, apply func(tx *db.Tx) (Changed, error)) (MutationResult, error) {
	var relayoutIDs []int64

	err := database.Transact(ctx, func(tx *db.Tx) error {
		next, err := apply(tx)
		if err != nil {
			return err
		}
		if err := db.BumpMetadataRev(tx, next.BumpMetadataRev); err != nil {
			return err
		}
		for _, bookID := range db.DedupBookIDs(slices.Concat(next.BumpMetadataRev, next.Reindex)) {
			if err := db.UpdateSearchIndex(tx, bookID); err != nil {
				return fmt.Errorf("update search index %d: %w", bookID, err)
			}
		}
		relayoutIDs = db.DedupBookIDs(next.Relayout)
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}

	maintenanceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	return relayoutBooks(maintenanceCtx, database, root, relayoutIDs), nil
}

func relayoutBooks(ctx context.Context, database *db.DB, root storage.Root, bookIDs []int64) MutationResult {
	var result MutationResult
	for _, bookID := range bookIDs {
		if err := ctx.Err(); err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("remaining file relayout interrupted: %w", err))
			break
		}
		n, err := Book(ctx, database, root, bookID)
		result.Moved += n
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("relayout %d: %w", bookID, err))
		}
		if n > 0 {
			if err := refreshSearchAfterRelayout(ctx, database, bookID); err != nil {
				result.Warnings = append(result.Warnings, err)
			}
		}
	}
	return result
}

func refreshSearchAfterRelayout(ctx context.Context, database *db.DB, bookID int64) error {
	return database.Transact(ctx, func(tx *db.Tx) error {
		if err := db.UpdateSearchIndex(tx, bookID); err != nil {
			return fmt.Errorf("update search index after relayout %d: %w", bookID, err)
		}
		return nil
	})
}
