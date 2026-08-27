package relayout

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

// Changed is the bookkeeping a work-metadata mutation owes after its own SQL
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
// already happened, and relayout.Work keeps DB/disk consistent for each failed
// move.
type MutationResult struct {
	Moved    int
	Warnings []error
}

// MutateWorks runs a work-metadata mutation with the shared catalog
// bookkeeping, in the order the storage/write-back invariants require:
//
//	tx:    caller writes, metadata_rev bump, search-index refresh
//	commit
//	after: relayout path-sensitive works, refresh filename search terms,
//	       with warning semantics
//
// A returned error means the transaction did not commit. Relayout failures are
// returned as warnings because the metadata change is durable and repair can
// recover any remaining storage drift.
func MutateWorks(ctx context.Context, database *db.DB, root storage.Root, apply func(tx *sql.Tx) (Changed, error)) (MutationResult, error) {
	var changed Changed

	err := database.Transact(ctx, func(tx *sql.Tx) error {
		next, err := apply(tx)
		if err != nil {
			return err
		}
		next.BumpMetadataRev = dedupWorkIDs(next.BumpMetadataRev)
		next.Relayout = dedupWorkIDs(next.Relayout)
		next.Reindex = dedupWorkIDs(next.Reindex)

		if err := db.BumpMetadataRev(tx, next.BumpMetadataRev); err != nil {
			return err
		}
		for _, workID := range dedupWorkIDs(slices.Concat(next.BumpMetadataRev, next.Reindex)) {
			if err := db.UpdateSearchIndex(tx, workID); err != nil {
				return fmt.Errorf("update search index %s: %w", workID, err)
			}
		}
		changed = next
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}

	return relayoutWorks(context.WithoutCancel(ctx), database, root, changed.Relayout), nil
}

func relayoutWorks(ctx context.Context, database *db.DB, root storage.Root, workIDs []string) MutationResult {
	var result MutationResult
	for _, workID := range workIDs {
		n, err := Work(database, root, workID)
		result.Moved += n
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Errorf("relayout %s: %w", workID, err))
		}
		if n > 0 {
			if err := refreshSearchAfterRelayout(ctx, database, workID); err != nil {
				result.Warnings = append(result.Warnings, err)
			}
		}
	}
	return result
}

func refreshSearchAfterRelayout(ctx context.Context, database *db.DB, workID string) error {
	return database.Transact(ctx, func(tx *sql.Tx) error {
		if err := db.UpdateSearchIndex(tx, workID); err != nil {
			return fmt.Errorf("update search index after relayout %s: %w", workID, err)
		}
		return nil
	})
}

func dedupWorkIDs(in []string) []string {
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
