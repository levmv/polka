package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/fsprofile"
	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/storage"
)

var errLibraryNotFound = errors.New("library not found")

func databasePath(dataDir string) string {
	return filepath.Join(dataDir, "library.db")
}

// ensureLibrary opens or creates the catalog and initializes a fresh library's
// managed-books and ingest layouts.
func ensureLibrary(ctx context.Context, dataDir string) (*db.DB, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dataDir, err)
	}
	path := databasePath(dataDir)
	// Create new databases with owner-only access even when dataDir already has
	// broader permissions. SQLite defaults to 0644 before umask.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create sqlite database %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close sqlite database %s: %w", path, err)
	}

	database, err := db.InitPath(path)
	if err != nil {
		return nil, err
	}
	if err := ensureLibraryDefaults(ctx, database, dataDir); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func openDatabase(dataDir string) (*db.DB, error) {
	if err := requireDatabaseFile(dataDir); err != nil {
		return nil, err
	}
	return db.InitPath(databasePath(dataDir))
}

func openDatabaseReadOnly(dataDir string) (*db.DB, error) {
	if err := requireDatabaseFile(dataDir); err != nil {
		return nil, err
	}
	return db.InitPathReadOnly(databasePath(dataDir))
}

func requireDatabaseFile(dataDir string) error {
	path := databasePath(dataDir)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w at %s; run `polka serve` or `polka import` first", errLibraryNotFound, path)
		}
		return fmt.Errorf("stat library database %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("library database path is a directory: %s", path)
	}
	return nil
}

func ensureLibraryDefaults(ctx context.Context, database *db.DB, dataDir string) error {
	var newRoot storage.Root
	err := database.Transact(ctx, func(tx *db.Tx) error {
		configured, err := storage.RootConfigured(tx)
		if err != nil {
			return err
		}
		if configured {
			// A missing configured root can be a dropped drive or mount. Leave
			// it missing so storage write guards can report the problem.
			return nil
		}

		// The books-root row marks initialization complete, so commit it with
		// every default. A failed write must leave initialization safe to retry.
		if _, err := storage.SaveBookPathTemplate(tx, ""); err != nil {
			return err
		}
		if _, err := ingest.SaveConfig(tx, dataDir, ingest.Config{Enabled: true}); err != nil {
			return err
		}
		newRoot, err = storage.SaveRoot(tx, dataDir, "")
		return err
	})
	if err != nil {
		return err
	}
	if newRoot.Path != "" {
		if err := storage.EnsureLayout(newRoot); err != nil {
			return err
		}
	}

	cfg, err := ingest.OpenConfig(database.Read(ctx), dataDir)
	if err != nil {
		return err
	}
	if cfg.Enabled {
		return ingest.EnsureLayout(cfg.Path)
	}
	return nil
}

func noteStorageFilesystem(root storage.Root) {
	info := fsprofile.Detect(root.Path)
	if info.IsNetwork() {
		fmt.Fprintf(os.Stderr, "Note: the books folder is on network filesystem %q: %s\n", info.TypeOrUnknown(), root.Path)
	}
}

func requireStorageLayout(dataDir string, root storage.Root) (storage.Root, error) {
	if err := storage.RequireLayout(root); err == nil {
		return root, nil
	} else if errors.Is(err, storage.ErrLayoutMissing) {
		// A missing books folder is far more often a dropped mount than a truly
		// new library, so lead with checking the path. Brand-new libraries are
		// created by first-run entry points such as `serve`, `import`, or
		// `storage root set`.
		return storage.Root{}, fmt.Errorf("books folder not found at %s; check that the drive or mount is available. For a brand-new library, run `polka serve --data %s` or `polka import <path> --data %s` first: %w", root.Path, dataDir, dataDir, err)
	} else {
		return storage.Root{}, err
	}
}
