package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/bootstrap"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/storage"
)

func TestImportCreatesLibraryOnFirstRun(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "app-data")
	srcPath := filepath.Join(base, "book.epub")
	writeEPUB(t, srcPath, "First Import", "Ada Writer", "Writer, Ada")

	if err := runImportFile(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImportFile: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()

	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	if root.Path != filepath.Join(dataDir, "books") {
		t.Fatalf("root.Path = %q; want data/books", root.Path)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ingest")); err != nil {
		t.Fatalf("default ingest dir missing: %v", err)
	}

	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets LIMIT 1").Scan(&storagePath); err != nil {
		t.Fatalf("query storage_path: %v", err)
	}
	if _, err := os.Stat(root.Abs(storagePath)); err != nil {
		t.Fatalf("imported file missing under default root: %v", err)
	}
}

func TestStorageRootSetCreatesLibraryOnFirstRun(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "app-data")
	libraryDir := filepath.Join(base, "managed-books")

	if err := runStorageRootSet(context.Background(), dataDir, []string{libraryDir}); err != nil {
		t.Fatalf("runStorageRootSet: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()

	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	if root.Path != libraryDir {
		t.Fatalf("root.Path = %q; want %q", root.Path, libraryDir)
	}
	if info, err := os.Stat(libraryDir); err != nil || !info.IsDir() {
		t.Fatalf("managed books root stat = %v/%v; want directory", info, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "books")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default books dir exists after storage root set first-run; err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ingest")); err != nil {
		t.Fatalf("default ingest dir missing: %v", err)
	}
}

func TestStorageRootSetInvalidPathDoesNotCreateLibrary(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "app-data")

	err := runStorageRootSet(context.Background(), dataDir, []string{dataDir})
	if err == nil {
		t.Fatal("runStorageRootSet accepted data dir as books root")
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid storage root set touched data dir; stat = %v", statErr)
	}
}

func TestImportUsesConfiguredLibraryRoot(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "app-data")
	libraryDir := filepath.Join(base, "managed-books")

	if err := runStorageRootSet(context.Background(), dataDir, []string{libraryDir}); err != nil {
		t.Fatalf("runStorageRootSet: %v", err)
	}

	srcPath := filepath.Join(base, "book.epub")
	writeEPUB(t, srcPath, "Separate Storage", "Ada Writer", "Writer, Ada")
	if err := runImportFile(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImportFile: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()

	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets LIMIT 1").Scan(&storagePath); err != nil {
		t.Fatalf("query storage_path: %v", err)
	}

	if _, err := os.Stat(filepath.Join(libraryDir, storagePath)); err != nil {
		t.Fatalf("imported file missing under library root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, storagePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("imported file exists under dataDir; err=%v", err)
	}
}

// TestImportRefusesEmptyRootWithExistingCatalog covers the CLI import write
// guard: once the catalog holds a book, importing into a books folder that
// exists but looks empty (the classic dropped-mount signal) must refuse rather
// than shadow-write a fresh layout.
func TestImportRefusesEmptyRootWithExistingCatalog(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "app-data")
	booksDir := filepath.Join(base, "managed-books")
	if err := runStorageRootSet(context.Background(), dataDir, []string{booksDir}); err != nil {
		t.Fatalf("runStorageRootSet: %v", err)
	}

	first := filepath.Join(base, "first.epub")
	writeEPUB(t, first, "First Book", "Ada Writer", "Writer, Ada")
	if err := runImportFile(context.Background(), dataDir, []string{first}); err != nil {
		t.Fatalf("runImportFile: %v", err)
	}

	// Simulate a dropped mount: the folder is present but empty again.
	if err := os.RemoveAll(booksDir); err != nil {
		t.Fatalf("remove books dir: %v", err)
	}
	if err := os.MkdirAll(booksDir, 0o755); err != nil {
		t.Fatalf("recreate empty books dir: %v", err)
	}

	second := filepath.Join(base, "second.epub")
	writeEPUB(t, second, "Second Book", "Ada Writer", "Writer, Ada")
	if err := runImportFile(context.Background(), dataDir, []string{second}); !errors.Is(err, storage.ErrRootEmpty) {
		t.Fatalf("import into empty root = %v; want ErrRootEmpty", err)
	}
}

func TestUserAddCreatesLibraryOnFirstRun(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "app-data")
	withPasswordStdin(t, "devpass\n", func() {
		if err := runUser(dataDir, []string{"add", "--admin", "admin"}); err != nil {
			t.Fatalf("runUser add: %v", err)
		}
	})

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()
	users, err := database.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].Username != "admin" || users[0].Role != db.RoleAdmin {
		t.Fatalf("users = %+v; want one admin", users)
	}
}

func TestUserAddInvalidArgsDoesNotCreateLibrary(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "app-data")

	err := runUser(dataDir, []string{"add"})
	if err == nil {
		t.Fatal("runUser add without username returned nil error")
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid user add touched data dir; stat = %v", statErr)
	}
}

func TestIngestProcessesConfiguredDropFolder(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "app-data")
	libraryDir := filepath.Join(base, "managed-books")
	ingestDir := filepath.Join(base, "drop")

	database, err := ensureLibraryWithoutBooksRoot(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryWithoutBooksRoot: %v", err)
	}
	if _, err := storage.SaveRoot(database.DB, dataDir, libraryDir); err != nil {
		t.Fatalf("SaveRoot: %v", err)
	}
	if err := storage.EnsureLayout(storage.NewRoot(libraryDir)); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	if _, err := ingest.SavePath(database.DB, dataDir, ingestDir); err != nil {
		t.Fatalf("SavePath: %v", err)
	}
	database.Close()

	srcPath := filepath.Join(ingestDir, "dropped.epub")
	if err := os.MkdirAll(ingestDir, 0o755); err != nil {
		t.Fatalf("mkdir ingest: %v", err)
	}
	writeEPUB(t, srcPath, "Dropped Book", "Queue Author", "Author, Queue")
	if err := runIngest(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runIngest: %v", err)
	}

	database, err = db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()

	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets LIMIT 1").Scan(&storagePath); err != nil {
		t.Fatalf("query storage_path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(libraryDir, storagePath)); err != nil {
		t.Fatalf("managed imported file missing: %v", err)
	}
	if _, err := os.Stat(srcPath); err != nil {
		t.Fatalf("ingest source was not left in place: %v", err)
	}
}

func TestMaintenanceCommandsDoNotCreateLibrary(t *testing.T) {
	cases := []struct {
		name string
		run  func(string) error
	}{
		{"check", func(dataDir string) error { return runCheck(dataDir, nil) }},
		{"repair", func(dataDir string) error { return runRepair(context.Background(), dataDir, nil) }},
		{"writeback", func(dataDir string) error { return runLibraryWriteback(context.Background(), dataDir, nil) }},
		{"storage_template_apply", func(dataDir string) error {
			return runStorageTemplateApply(context.Background(), dataDir, []string{storage.DefaultBookPathTemplate})
		}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "missing")
			err := tt.run(dataDir)
			if !errors.Is(err, bootstrap.ErrLibraryNotFound) {
				t.Fatalf("%s error = %v; want ErrLibraryNotFound", tt.name, err)
			}
			if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%s touched data dir; stat = %v", tt.name, statErr)
			}
		})
	}
}

func withPasswordStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write password pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close password pipe writer: %v", err)
	}
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		r.Close()
	}()
	fn()
}
