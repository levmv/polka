package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/storage"
)

var ErrLibraryNotFound = errors.New("library not found")

func DatabasePath(dataDir string) string {
	return filepath.Join(dataDir, "library.db")
}

// EnsureLibrary opens or creates the catalog and initializes a fresh library's
// managed-books and ingest layouts.
func EnsureLibrary(ctx context.Context, dataDir string) (*db.DB, error) {
	return ensureLibrary(ctx, dataDir, true)
}

// EnsureLibraryWithoutBooksRoot initializes the database and ingest layout
// without choosing or creating a managed books root.
func EnsureLibraryWithoutBooksRoot(ctx context.Context, dataDir string) (*db.DB, error) {
	return ensureLibrary(ctx, dataDir, false)
}

func ensureLibrary(ctx context.Context, dataDir string, ensureBooksRoot bool) (*db.DB, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dataDir, err)
	}
	databasePath := DatabasePath(dataDir)
	// Create new databases with owner-only access even when dataDir already has
	// broader permissions. SQLite defaults to 0644 before umask.
	f, err := os.OpenFile(databasePath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create sqlite database %s: %w", databasePath, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close sqlite database %s: %w", databasePath, err)
	}

	database, err := db.InitPath(databasePath)
	if err != nil {
		return nil, err
	}
	if err := ensureDefaults(ctx, database, dataDir, ensureBooksRoot); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func OpenExisting(dataDir string) (*db.DB, error) {
	if err := requireDatabaseFile(dataDir); err != nil {
		return nil, err
	}
	return db.InitPath(DatabasePath(dataDir))
}

func OpenExistingReadOnly(dataDir string) (*db.DB, error) {
	if err := requireDatabaseFile(dataDir); err != nil {
		return nil, err
	}
	return db.InitPathReadOnly(DatabasePath(dataDir))
}

func requireDatabaseFile(dataDir string) error {
	path := DatabasePath(dataDir)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w at %s; run `polka serve` or `polka import` first", ErrLibraryNotFound, path)
		}
		return fmt.Errorf("stat library database %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("library database path is a directory: %s", path)
	}
	return nil
}

func ensureDefaults(ctx context.Context, database *db.DB, dataDir string, ensureBooksRoot bool) error {
	var newRoot storage.Root
	err := database.Transact(ctx, func(tx *db.Tx) error {
		configured, err := storage.RootConfigured(tx)
		if err != nil {
			return err
		}
		if configured {
			return nil
		}

		// The books-root row marks initialization complete, so commit it with
		// every default. A failed write must leave bootstrap safe to retry.
		if _, err := storage.SaveBookPathTemplate(tx, ""); err != nil {
			return err
		}
		if _, err := ingest.SaveConfig(tx, dataDir, ingest.Config{Enabled: true}); err != nil {
			return err
		}
		if ensureBooksRoot {
			newRoot, err = storage.SaveRoot(tx, dataDir, "")
		}
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
