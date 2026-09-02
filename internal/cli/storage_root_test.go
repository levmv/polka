package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestStorageRootSetCreatesTargetForEmptyCatalog(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	database.Close()

	target := filepath.Join(t.TempDir(), "managed-books")
	if err := runStorageRootSet(context.Background(), dataDir, []string{target}); err != nil {
		t.Fatalf("storage root set empty catalog: %v", err)
	}

	database, err = db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	if root.Path != target {
		t.Fatalf("root.Path = %q; want %q", root.Path, target)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("target stat = %v/%v; want directory", info, err)
	}
}

func TestStorageRootSetRequiresCopiedFilesForExistingCatalog(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	initDefaultTestLibrary(t, dataDir)

	src := filepath.Join(tempDir, "source.epub")
	if err := os.WriteFile(src, []byte("root set source"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{src}); err != nil {
		t.Fatalf("import source: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	oldRoot, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot old: %v", err)
	}
	var assetPath string
	if err := database.QueryRow(`SELECT storage_path FROM assets LIMIT 1`).Scan(&assetPath); err != nil {
		t.Fatalf("query asset path: %v", err)
	}
	database.Close()

	target := filepath.Join(tempDir, "new-books")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := runStorageRootSet(context.Background(), dataDir, []string{target}); !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("storage root set empty target = %v; want ErrIssuesFound", err)
	}

	database, err = db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("reopen after failed set: %v", err)
	}
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot after failed set: %v", err)
	}
	database.Close()
	if root.Path != oldRoot.Path {
		t.Fatalf("root changed after failed set: got %q, want %q", root.Path, oldRoot.Path)
	}

	copyAssetToRoot(t, oldRoot, storage.NewRoot(target), assetPath)
	if err := runStorageRootSet(context.Background(), dataDir, []string{target}); err != nil {
		t.Fatalf("storage root set copied target: %v", err)
	}

	database, err = db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("reopen after set: %v", err)
	}
	defer database.Close()
	root, err = storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot after set: %v", err)
	}
	if root.Path != target {
		t.Fatalf("root.Path = %q; want %q", root.Path, target)
	}
	if _, err := os.Stat(oldRoot.Abs(assetPath)); err != nil {
		t.Fatalf("old root file was moved or lost: %v", err)
	}
	if _, err := os.Stat(root.Abs(assetPath)); err != nil {
		t.Fatalf("target root file missing: %v", err)
	}
}

func copyAssetToRoot(t *testing.T, srcRoot, dstRoot storage.Root, relPath string) {
	t.Helper()
	data, err := os.ReadFile(srcRoot.Abs(relPath))
	if err != nil {
		t.Fatalf("read source asset: %v", err)
	}
	dst := dstRoot.Abs(relPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir target asset dir: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write target asset: %v", err)
	}
}
