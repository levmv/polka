package web

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/storage"
)

func TestAPIAdminStorageImportFolderPreview(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	ensureTestStorageLayout(t, dataDir)

	admin := mustUser(t, database, "Admin", db.RoleAdmin)

	root := storage.NewRoot(dataDir)
	duplicateBytes := testEPUB(t, "Existing Book", "Ada Writer", "Writer, Ada")
	seedPath := filepath.Join(t.TempDir(), "seed.epub")
	if err := os.WriteFile(seedPath, duplicateBytes, 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if _, err := importer.ImportFile(context.Background(), database, root, seedPath, nil, importer.Options{CoverRoot: root}); err != nil {
		t.Fatalf("seed import: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET deleted_at = unixepoch()"); err != nil {
		t.Fatalf("trash seed work: %v", err)
	}

	sourceDir := t.TempDir()
	writeFile(t, filepath.Join(sourceDir, "duplicate.epub"), duplicateBytes)
	writeFile(t, filepath.Join(sourceDir, "new.epub"), testEPUB(t, "New Book", "New Writer", "Writer, New"))
	writeFile(t, filepath.Join(sourceDir, "notes.xyz"), []byte("not a book"))
	calibreDir := filepath.Join(sourceDir, "Calibre Book")
	if err := os.MkdirAll(calibreDir, 0o755); err != nil {
		t.Fatalf("mkdir calibre dir: %v", err)
	}
	writeFile(t, filepath.Join(calibreDir, "metadata.opf"), []byte(`<package></package>`))
	writeFile(t, filepath.Join(calibreDir, "calibre.epub"), testEPUB(t, "Calibre Book", "Cal Writer", "Writer, Cal"))

	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/admin/storage/import/preview", folderImportRequest{Path: sourceDir}))
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got FolderImportPreviewDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if got.Files != 3 || got.CalibreBooks != 1 || got.WouldImport != 2 || got.Duplicates != 1 || got.Trashed != 1 || got.Skipped != 1 || got.Failed != 0 {
		t.Fatalf("preview = %+v; want files=3 calibre=1 would=2 duplicates=1 trashed=1 skipped=1 failed=0", got)
	}
}

func TestAPIAdminStorageImportFolderRejectsDataDirOverlap(t *testing.T) {
	database, dataDir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	sourceDir := filepath.Join(dataDir, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}

	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/admin/storage/import/preview", folderImportRequest{Path: sourceDir}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("preview status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestAPIAdminStorageImportFolderPreviewFollowsRootSymlink(t *testing.T) {
	database, dataDir := setupTestDB(t)
	defer database.Close()

	sourceDir := t.TempDir()
	writeFile(t, filepath.Join(sourceDir, "book.epub"), testEPUB(t, "Linked Import", "Ada Writer", "Writer, Ada"))
	link := filepath.Join(t.TempDir(), "books-link")
	if err := os.Symlink(sourceDir, link); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/admin/storage/import/preview", folderImportRequest{Path: link}))
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got FolderImportPreviewDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if got.Files != 1 || got.WouldImport != 1 || got.Failed != 0 {
		t.Fatalf("preview = %+v; want one importable file", got)
	}
}

func TestAPIAdminStorageImportFolderRun(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	ensureTestStorageLayout(t, dataDir)

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	sourceDir := t.TempDir()
	firstPath := filepath.Join(sourceDir, "first.epub")
	secondPath := filepath.Join(sourceDir, "second.epub")
	writeFile(t, firstPath, testEPUB(t, "First Import", "One Writer", "Writer, One"))
	writeFile(t, secondPath, testEPUB(t, "Second Import", "Two Writer", "Writer, Two"))
	writeFile(t, filepath.Join(sourceDir, "notes.xyz"), []byte("not a book"))

	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/admin/storage/import", folderImportRequest{Path: sourceDir}))
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got FolderImportResultDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode import: %v", err)
	}
	if got.Files != 2 || got.Imported != 2 || got.Duplicates != 0 || got.Skipped != 1 || got.Failed != 0 {
		t.Fatalf("import result = %+v; want files=2 imported=2 skipped=1 failed=0", got)
	}
	if got.Storage.Books.BookCount != 2 {
		t.Fatalf("storage book count = %d, want 2", got.Storage.Books.BookCount)
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("source first was removed: %v", err)
	}
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("source second was removed: %v", err)
	}
	var assets int
	if err := database.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 2 {
		t.Fatalf("assets = %d, want 2", assets)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
