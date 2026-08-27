package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/writeback"
)

func runLibraryWriteback(parent context.Context, dataDir string, args []string) (retErr error) {
	fs := commandFlagSet("library writeback", "polka library writeback [--all|<work-id>...] [--dry-run] [--limit N] [--force]")
	all := fs.Bool("all", false, "write every supported managed file, including clean files")
	dryRun := fs.Bool("dry-run", false, "show what would be written without changing files")
	force := fs.Bool("force", false, "override a fresh writer lease from another polka process")
	limit := fs.Int("limit", 0, "maximum number of assets to process")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	workIDs := fs.Args()

	database, err := openDatabase(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()

	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		return err
	}
	ctx := parent
	if !*dryRun {
		hasAnyAsset, err := db.HasAnyAsset(database.DB)
		if err != nil {
			return err
		}
		if err := storage.RequireWritableRoot(root, hasAnyAsset); err != nil {
			return err
		}
		noteStorageFilesystem(root)

		lease, err := acquireCLIWriterLease(ctx, database, "library-writeback", *force)
		if err != nil {
			return err
		}
		defer func() { retErr = lease.finish(retErr) }()
		ctx = lease.Context()
	}

	summary, err := writeback.Run(ctx, database, root, writeback.Options{
		All:       *all,
		DryRun:    *dryRun,
		Limit:     *limit,
		WorkIDs:   workIDs,
		CoverRoot: storage.NewRoot(dataDir),
	})
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("Would write metadata for %s.\n", formatCount(summary.WouldWrite, "asset", "assets"))
		return nil
	}
	for _, result := range summary.Results {
		if result.Status == writeback.StatusFailed {
			fmt.Fprintf(os.Stderr, "Failed %s (%s): %s\n", result.AssetID, result.StoragePath, result.Error)
		}
	}
	fmt.Printf("Metadata write-back: %d written, %d unchanged, %d skipped, %d failed.\n",
		summary.Written, summary.Unchanged, summary.Skipped, summary.Failed)
	if summary.Failed > 0 {
		return ErrIssuesFound
	}
	return nil
}
