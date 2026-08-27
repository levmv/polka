package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func runStorageRoot(ctx context.Context, dataDir string, args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printStorageRootUsage()
		if len(args) == 0 {
			return reportedErrorf("missing storage root command")
		}
		return nil
	}
	if len(args) > 1 && isHelpArg(args[1]) {
		printStorageRootUsage()
		return nil
	}

	switch args[0] {
	case "set":
		return runStorageRootSet(ctx, dataDir, args[1:])
	default:
		printStorageRootUsage()
		return reportedErrorf("unknown storage root command: %s", args[0])
	}
}

func printStorageRootUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka storage root set [--force] <path>

The books folder holds managed book files only. Relative paths are resolved
under the data dir. For an existing catalog, the target must already contain
every cataloged file at the same relative path.
`)
}

func runStorageRootSet(parent context.Context, dataDir string, args []string) (retErr error) {
	fs := commandFlagSet("storage root set", "polka storage root set [--force] <path>")
	force := fs.Bool("force", false, "override a fresh writer lease from another polka process")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		fs.Usage()
		return reportedErrorf("usage: polka storage root set [--force] <path>")
	}
	configured := fs.Arg(0)

	newRoot, err := storage.ResolveRoot(dataDir, configured)
	if err != nil {
		return err
	}

	database, err := ensureLibraryWithoutBooksRoot(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()

	lease, err := acquireCLIWriterLease(parent, database, "storage-root-set", *force)
	if err != nil {
		return err
	}
	defer func() { retErr = lease.finish(retErr) }()
	ctx := lease.Context()

	assets, err := storageRootAssets(ctx, database)
	if err != nil {
		return err
	}

	if err := verifyStorageRootReady(ctx, newRoot, assets); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}

	saved, err := storage.SaveRoot(database.DB, dataDir, configured)
	if err != nil {
		return err
	}
	fmt.Printf("Books folder: %s\n", saved.Path)
	fmt.Printf("Verified files: %d\n", len(assets))
	return nil
}

type storageRootAsset struct {
	ID   string
	Path string
}

func storageRootAssets(ctx context.Context, database *db.DB) ([]storageRootAsset, error) {
	rows, err := database.QueryContext(ctx, `SELECT id, storage_path FROM assets ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query asset paths: %w", err)
	}
	defer rows.Close()

	var assets []storageRootAsset
	for rows.Next() {
		var a storageRootAsset
		if err := rows.Scan(&a.ID, &a.Path); err != nil {
			return nil, fmt.Errorf("scan asset path: %w", err)
		}
		assets = append(assets, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows asset paths: %w", err)
	}
	return assets, nil
}

func verifyStorageRootReady(ctx context.Context, root storage.Root, assets []storageRootAsset) error {
	if len(assets) == 0 {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		return storage.EnsureLayout(root)
	}
	if err := storage.RequireLayout(root); err != nil {
		return fmt.Errorf("target books folder is not ready at %s; copy or move the existing book tree there first: %w", root.Path, err)
	}
	missing, err := storageRootMissingFiles(ctx, root, assets)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		printStorageRootIssues("Missing files in target books folder:", missing)
		return ErrIssuesFound
	}
	return nil
}

func storageRootMissingFiles(ctx context.Context, root storage.Root, assets []storageRootAsset) ([]string, error) {
	var missing []string
	for _, asset := range assets {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		abs, err := root.Resolve(asset.Path)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s: invalid path %s: %v", asset.ID, asset.Path, err))
			continue
		}
		info, err := os.Stat(abs)
		switch {
		case err == nil && info.IsDir():
			missing = append(missing, fmt.Sprintf("%s: %s is a directory", asset.ID, asset.Path))
		case err == nil:
		case errors.Is(err, os.ErrNotExist):
			missing = append(missing, fmt.Sprintf("%s: %s", asset.ID, asset.Path))
		default:
			missing = append(missing, fmt.Sprintf("%s: %s: %v", asset.ID, asset.Path, err))
		}
	}
	return missing, nil
}

func printStorageRootIssues(title string, items []string) {
	fmt.Println(title)
	for i, item := range items {
		if i >= 20 {
			fmt.Printf("  ... %d more\n", len(items)-i)
			break
		}
		fmt.Printf("  - %s\n", item)
	}
}
