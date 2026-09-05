package relayout

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

// Changed is the bookkeeping a book-metadata mutation owes after its own SQL
// writes. BumpMetadataRev marks user-visible metadata changes, Relayout marks
// canonical-path input changes, and Reindex covers searchable refreshes that do
// not themselves bump write-back dirtiness.
type Changed struct {
	BumpMetadataRev []string
	Relayout        []string
	Reindex         []string
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
func MutateBooks(ctx context.Context, database *db.DB, root storage.Root, apply func(tx *sql.Tx) (Changed, error)) (MutationResult, error) {
	var changed Changed

	err := database.Transact(ctx, func(tx *sql.Tx) error {
		next, err := apply(tx)
		if err != nil {
			return err
		}
		next.BumpMetadataRev = dedupBookIDs(next.BumpMetadataRev)
		next.Relayout = dedupBookIDs(next.Relayout)
		next.Reindex = dedupBookIDs(next.Reindex)

		if err := db.BumpMetadataRev(tx, next.BumpMetadataRev); err != nil {
			return err
		}
		for _, bookID := range dedupBookIDs(slices.Concat(next.BumpMetadataRev, next.Reindex)) {
			if err := db.UpdateSearchIndex(tx, bookID); err != nil {
				return fmt.Errorf("update search index %s: %w", bookID, err)
			}
		}
		changed = next
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}

	return relayoutBooks(context.WithoutCancel(ctx), database, root, changed.Relayout), nil
}

func relayoutBooks(ctx context.Context, database *db.DB, root storage.Root, bookIDs []string) MutationResult {
	var result MutationResult
	for _, bookID := range bookIDs {
		n, err := Book(database, root, bookID)
		result.Moved += n
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("relayout %s: %w", bookID, err))
		}
		if n > 0 {
			if err := refreshSearchAfterRelayout(ctx, database, bookID); err != nil {
				result.Warnings = append(result.Warnings, err)
			}
		}
	}
	return result
}

func refreshSearchAfterRelayout(ctx context.Context, database *db.DB, bookID string) error {
	return database.Transact(ctx, func(tx *sql.Tx) error {
		if err := db.UpdateSearchIndex(tx, bookID); err != nil {
			return fmt.Errorf("update search index after relayout %s: %w", bookID, err)
		}
		return nil
	})
}

func dedupBookIDs(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
