package bootstrap

import (
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
func EnsureLibrary(dataDir string) (*db.DB, error) {
	return ensureLibrary(dataDir, true)
}

// EnsureLibraryWithoutBooksRoot initializes the database and ingest layout
// without choosing or creating a managed books root.
func EnsureLibraryWithoutBooksRoot(dataDir string) (*db.DB, error) {
	return ensureLibrary(dataDir, false)
}

func ensureLibrary(dataDir string, ensureBooksRoot bool) (*db.DB, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dataDir, err)
	}
	databasePath := DatabasePath(dataDir)
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
	if err := ensureDefaults(database, dataDir, ensureBooksRoot); err != nil {
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

func ensureDefaults(database *db.DB, dataDir string, ensureBooksRoot bool) error {
	rootConfigured, err := storage.RootConfigured(database.DB)
	if err != nil {
		return err
	}
	fresh := !rootConfigured

	if ensureBooksRoot && !rootConfigured {
		root, err := storage.SaveRoot(database.DB, dataDir, "")
		if err != nil {
			return err
		}
		if err := storage.EnsureLayout(root); err != nil {
			return err
		}
	}

	if fresh {
		if _, err := storage.SaveBookPathTemplate(database.DB, ""); err != nil {
			return err
		}
		if err := ingest.SaveEnabled(database.DB, true); err != nil {
			return err
		}
		if err := ingest.SaveDeleteSources(database.DB, false); err != nil {
			return err
		}
		if _, err := ingest.SavePath(database.DB, dataDir, ""); err != nil {
			return err
		}
	}

	cfg, err := ingest.OpenConfig(database.DB, dataDir)
	if err != nil {
		return err
	}
	if cfg.Enabled {
		if err := ingest.EnsureLayout(cfg.Path); err != nil {
			return err
		}
	}
	return nil
}
