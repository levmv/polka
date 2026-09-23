package cli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/storage"
)

func TestEnsureDefaultsRollsBackFailedInitialization(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(databasePath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var initialCount int
	if err := database.Read(t.Context()).QueryRow("SELECT count(*) FROM app_settings").Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Write(t.Context()).Exec(`
		CREATE TRIGGER reject_ingest_path BEFORE INSERT ON app_settings
		WHEN NEW.key = 'ingest.path'
		BEGIN SELECT RAISE(ABORT, 'settings write failed'); END
	`); err != nil {
		t.Fatal(err)
	}
	if err := ensureLibraryDefaults(t.Context(), database, dataDir); err == nil {
		t.Fatal("initialization succeeded despite failed settings write")
	}
	var count int
	if err := database.Read(t.Context()).QueryRow("SELECT count(*) FROM app_settings").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != initialCount {
		t.Fatalf("failed initialization left %d settings, want original %d", count, initialCount)
	}
	if _, err := database.Write(t.Context()).Exec("DROP TRIGGER reject_ingest_path"); err != nil {
		t.Fatal(err)
	}
	if err := ensureLibraryDefaults(t.Context(), database, dataDir); err != nil {
		t.Fatalf("retry initialization: %v", err)
	}
	if err := storage.RequireLayout(storage.NewRoot(filepath.Join(dataDir, "books"))); err != nil {
		t.Fatal(err)
	}

	// Subsequent startup must retain settings chosen by the owner.
	want := ingest.Config{Path: filepath.Join(dataDir, "incoming"), Enabled: false, DeleteSources: true}
	if _, err := ingest.SaveConfig(database.Write(t.Context()), dataDir, want); err != nil {
		t.Fatal(err)
	}
	if err := ensureLibraryDefaults(t.Context(), database, dataDir); err != nil {
		t.Fatal(err)
	}
	if got, err := ingest.OpenConfig(database.Read(t.Context()), dataDir); err != nil || got != want {
		t.Fatalf("config after startup = %+v, %v; want %+v", got, err, want)
	}
}

func TestEnsureLibraryCreatesDefaultLayout(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "library")

	database, err := ensureLibrary(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("ensureLibrary: %v", err)
	}
	defer database.Close()

	if _, err := os.Stat(databasePath(dataDir)); err != nil {
		t.Fatalf("database file missing: %v", err)
	}
	root, err := storage.OpenRoot(database.Read(t.Context()), dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	if root.Path != filepath.Join(dataDir, "books") {
		t.Fatalf("root.Path = %q; want data/books", root.Path)
	}
	if info, err := os.Stat(root.Path); err != nil || !info.IsDir() {
		t.Fatalf("books root stat = %v/%v; want directory", info, err)
	}

	ingestPath, err := ingest.OpenPath(database.Read(t.Context()), dataDir)
	if err != nil {
		t.Fatalf("OpenPath: %v", err)
	}
	if ingestPath != filepath.Join(dataDir, "ingest") {
		t.Fatalf("ingest path = %q; want data/ingest", ingestPath)
	}
	if info, err := os.Stat(ingestPath); err != nil || !info.IsDir() {
		t.Fatalf("ingest stat = %v/%v; want directory", info, err)
	}

	if template, err := storage.OpenBookPathTemplate(database.Read(t.Context())); err != nil {
		t.Fatalf("OpenBookPathTemplate: %v", err)
	} else if template != storage.DefaultBookPathTemplate {
		t.Fatalf("template = %q; want default", template)
	}
}

func TestOpenDatabaseMissingLibraryDoesNotCreateDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "missing")

	_, err := openDatabase(dataDir)
	if !errors.Is(err, errLibraryNotFound) {
		t.Fatalf("openDatabase = %v; want errLibraryNotFound", err)
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("openDatabase touched data dir; stat = %v", statErr)
	}
}

func TestEnsureLibraryUsesOwnerOnlyDefaultsWithoutChmoddingExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}

	t.Run("new data directory", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "library")
		database, err := ensureLibrary(t.Context(), dataDir)
		if err != nil {
			t.Fatalf("ensureLibrary: %v", err)
		}
		defer database.Close()

		assertPermissions(t, dataDir, 0o700)
		assertPermissions(t, databasePath(dataDir), 0o600)
	})

	t.Run("existing data directory", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "library")
		if err := os.Mkdir(dataDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		database, err := ensureLibrary(t.Context(), dataDir)
		if err != nil {
			t.Fatalf("ensureLibrary: %v", err)
		}
		defer database.Close()

		assertPermissions(t, dataDir, 0o755)
		assertPermissions(t, databasePath(dataDir), 0o600)
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

func TestEnsureLibraryDefaultsDoesNotCreateConfiguredMissingRoot(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()

	configuredRoot, err := storage.SaveRoot(database.Write(t.Context()), dataDir, "configured-books")
	if err != nil {
		t.Fatalf("SaveRoot: %v", err)
	}
	if _, err := os.Stat(configuredRoot.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition stat configured books root = %v; want not exist", err)
	}

	if err := ensureLibraryDefaults(t.Context(), database, dataDir); err != nil {
		t.Fatalf("ensureLibraryDefaults: %v", err)
	}
	root, err := storage.OpenRoot(database.Read(t.Context()), dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	if root.Path != configuredRoot.Path {
		t.Fatalf("root.Path = %q; want %q", root.Path, configuredRoot.Path)
	}
	if _, err := os.Stat(configuredRoot.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured missing root was created; stat err = %v", err)
	}
}
