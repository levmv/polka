package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestStorageTemplatePreviewDetectsCollisions(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	initDefaultTestLibrary(t, dataDir)

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "one.epub"), []byte("one epub bytes"), 0o644); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "two.epub"), []byte("two epub bytes"), 0o644); err != nil {
		t.Fatalf("write two: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcDir}); err != nil {
		t.Fatalf("import folder: %v", err)
	}

	if err := runStorageTemplatePreview(dataDir, "books/{author_bucket}/{author_sort}/{title} [{asset_id}]{dot_ext}"); err != nil {
		t.Fatalf("default-like preview: %v", err)
	}
	if err := runStorageTemplatePreview(dataDir, "books/collide{dot_ext}"); !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("collision preview error = %v; want ErrIssuesFound", err)
	}
}

func TestStorageTemplateApplyPersistsRelayoutsAndAffectsNewImports(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	initDefaultTestLibrary(t, dataDir)

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "one.epub"), []byte("one epub bytes"), 0o644); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcDir}); err != nil {
		t.Fatalf("import folder: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	var oldPath string
	if err := database.QueryRow("SELECT storage_path FROM assets ORDER BY id LIMIT 1").Scan(&oldPath); err != nil {
		t.Fatalf("query old path: %v", err)
	}
	database.Close()

	template := "books/flat/{title} [{asset_id}]{dot_ext}"
	if err := runStorageTemplateApply(context.Background(), dataDir, []string{template}); err != nil {
		t.Fatalf("apply template: %v", err)
	}

	database, err = db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("reopen after apply: %v", err)
	}
	defer database.Close()
	gotTemplate, err := storage.OpenBookPathTemplate(database.DB)
	if err != nil {
		t.Fatalf("open template: %v", err)
	}
	if gotTemplate != template {
		t.Fatalf("template = %q; want %q", gotTemplate, template)
	}
	var newPath string
	if err := database.QueryRow("SELECT storage_path FROM assets ORDER BY id LIMIT 1").Scan(&newPath); err != nil {
		t.Fatalf("query new path: %v", err)
	}
	if !strings.HasPrefix(newPath, "books/flat/") || newPath == oldPath {
		t.Fatalf("new path = %q, old path = %q", newPath, oldPath)
	}
	if _, err := os.Stat(root.Abs(newPath)); err != nil {
		t.Fatalf("new file missing: %v", err)
	}
	if _, err := os.Stat(root.Abs(oldPath)); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(srcDir, "two.epub"), []byte("two epub bytes"), 0o644); err != nil {
		t.Fatalf("write two: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{filepath.Join(srcDir, "two.epub")}); err != nil {
		t.Fatalf("import after template apply: %v", err)
	}
	rows, err := database.Query("SELECT storage_path FROM assets")
	if err != nil {
		t.Fatalf("query paths: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan path: %v", err)
		}
		if !strings.HasPrefix(p, "books/flat/") {
			t.Fatalf("path after apply/import = %q; want books/flat prefix", p)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
}

func TestStorageTemplateImportRejectsPathCollision(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	initDefaultTestLibrary(t, dataDir)

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	first := filepath.Join(srcDir, "one.epub")
	second := filepath.Join(srcDir, "two.epub")
	if err := os.WriteFile(first, []byte("one epub bytes"), 0o644); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{first}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if err := runStorageTemplateApply(context.Background(), dataDir, []string{"books/collide.epub"}); err != nil {
		t.Fatalf("apply colliding-for-future template: %v", err)
	}
	if err := os.WriteFile(second, []byte("two epub bytes"), 0o644); err != nil {
		t.Fatalf("write two: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{second}); err == nil || !strings.Contains(err.Error(), "storage path collision") {
		t.Fatalf("second import error = %v; want storage path collision", err)
	}
}
