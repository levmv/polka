package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/storage"
)

func TestEnsureLibraryCreatesDefaultLayout(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "library")

	database, err := EnsureLibrary(dataDir)
	if err != nil {
		t.Fatalf("EnsureLibrary: %v", err)
	}
	defer database.Close()

	if _, err := os.Stat(DatabasePath(dataDir)); err != nil {
		t.Fatalf("database file missing: %v", err)
	}
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	if root.Path != filepath.Join(dataDir, "books") {
		t.Fatalf("root.Path = %q; want data/books", root.Path)
	}
	if info, err := os.Stat(root.Path); err != nil || !info.IsDir() {
		t.Fatalf("books root stat = %v/%v; want directory", info, err)
	}

	ingestPath, err := ingest.OpenPath(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenPath: %v", err)
	}
	if ingestPath != filepath.Join(dataDir, "ingest") {
		t.Fatalf("ingest path = %q; want data/ingest", ingestPath)
	}
	if info, err := os.Stat(ingestPath); err != nil || !info.IsDir() {
		t.Fatalf("ingest stat = %v/%v; want directory", info, err)
	}

	if template, err := storage.OpenBookPathTemplate(database.DB); err != nil {
		t.Fatalf("OpenBookPathTemplate: %v", err)
	} else if template != storage.DefaultBookPathTemplate {
		t.Fatalf("template = %q; want default", template)
	}
}

func TestOpenExistingMissingLibraryDoesNotCreateDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "missing")

	_, err := OpenExisting(dataDir)
	if !errors.Is(err, ErrLibraryNotFound) {
		t.Fatalf("OpenExisting = %v; want ErrLibraryNotFound", err)
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("OpenExisting touched data dir; stat = %v", statErr)
	}
}

func TestEnsureLibraryUsesOwnerOnlyDefaultsWithoutChmoddingExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}

	t.Run("new data directory", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "library")
		database, err := EnsureLibraryWithoutBooksRoot(dataDir)
		if err != nil {
			t.Fatalf("EnsureLibrary: %v", err)
		}
		defer database.Close()

		assertPermissions(t, dataDir, 0o700)
		assertPermissions(t, DatabasePath(dataDir), 0o600)
	})

	t.Run("existing data directory", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "library")
		if err := os.Mkdir(dataDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		database, err := EnsureLibraryWithoutBooksRoot(dataDir)
		if err != nil {
			t.Fatalf("EnsureLibrary: %v", err)
		}
		defer database.Close()

		assertPermissions(t, dataDir, 0o755)
		assertPermissions(t, DatabasePath(dataDir), 0o600)
	})
}

func assertPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("permissions for %s = %04o, want %04o", path, got, want)
	}
}
