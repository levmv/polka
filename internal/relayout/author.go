package relayout

import (
	"context"
	"database/sql"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

// AuthorMutationResult summarizes an author mutation. Warnings are non-fatal
// relayout errors: the mutation is already durable and the DB stays consistent
// with disk, but some file(s) could not be moved to their new canonical path
// (recoverable via `polka repair`). Callers present warnings as they see fit.
type AuthorMutationResult struct {
	Affected int
	MutationResult
}

// RenameAuthor renames an author in place, or merges into an existing author of
// newName, then rebuilds the search index and relayouts every affected work's
// files — the primary author is part of the canonical path, so a rename/merge
// moves files in bulk. It is the single home for this orchestration, shared by
// the CLI (`library authors rename|merge`) and the web manage-authors endpoint.
//
// A returned error is fatal (the rename did not happen). Per-work relayout
// failures are collected in AuthorMutationResult.Warnings instead, mirroring
// relayout.Work's contract: the metadata change is durable and the DB matches
// disk.
func RenameAuthor(ctx context.Context, database *db.DB, root storage.Root, oldName, newName string) (AuthorMutationResult, error) {
	return mutateAuthor(ctx, database, root, func(tx *sql.Tx) ([]string, error) {
		return db.RenameOrMergeAuthor(tx, oldName, newName, bookmeta.AuthorSort(newName))
	})
}

// SetAuthorSortName overrides an author's sort_name and relayouts the works it
// is primary on. sort_name selects the canonical-path bucket and author folder,
// so the override moves files just like a rename. Shares RenameAuthor's
// contract: a returned error is fatal; per-work relayout failures are warnings.
func SetAuthorSortName(ctx context.Context, database *db.DB, root storage.Root, name, sortName string) (AuthorMutationResult, error) {
	return mutateAuthor(ctx, database, root, func(tx *sql.Tx) ([]string, error) {
		return db.SetAuthorSortName(tx, name, sortName)
	})
}

// mutateAuthor gives both author operations the same affected-work contract:
// bump metadata revisions and refresh search in the transaction, then relayout
// canonical paths after commit with warning semantics.
func mutateAuthor(ctx context.Context, database *db.DB, root storage.Root, apply func(tx *sql.Tx) ([]string, error)) (AuthorMutationResult, error) {
	var affected []string

	mutation, err := MutateWorks(ctx, database, root, func(tx *sql.Tx) (Changed, error) {
		var err error
		affected, err = apply(tx)
		if err != nil {
			return Changed{}, err
		}
		return Changed{BumpMetadataRev: affected, Relayout: affected}, nil
	})
	if err != nil {
		return AuthorMutationResult{}, err
	}
	return AuthorMutationResult{
		Affected:       len(affected),
		MutationResult: mutation,
	}, nil
}
