package relayout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestWorkRequiresLayoutBeforeMove(t *testing.T) {
	database, root := setupRelayoutTest(t)
	seedRelayoutWork(t, database, "w_1", "a_1", "New Title", "Jane Doe", bookmeta.AuthorSort("Jane Doe"), ".epub",
		"books/old/place/a_1.epub")

	if _, err := Work(database, root, "w_1"); !errors.Is(err, storage.ErrLayoutMissing) {
		t.Fatalf("Work error = %v; want ErrLayoutMissing", err)
	}
	if _, err := os.Stat(root.Abs("books")); !os.IsNotExist(err) {
		t.Fatalf("Work recreated books dir despite missing layout; stat err=%v", err)
	}
}

func TestWorkUsesDurableOriginalFilenameInTemplate(t *testing.T) {
	database, root := setupRelayoutTest(t)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	oldPath := "legacy/current-location.epub"
	seedRelayoutWork(t, database, "w_1", "a_1", "Old Title", "Jane Doe", bookmeta.AuthorSort("Jane Doe"), ".epub", oldPath)
	if _, err := database.Exec("UPDATE assets SET original_filename = 'Source Name.epub' WHERE id = 'a_1'"); err != nil {
		t.Fatalf("set original filename: %v", err)
	}
	if _, err := storage.SaveBookPathTemplate(database, "{title} - {original_filename}"); err != nil {
		t.Fatalf("save template: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(root.Abs(oldPath)), 0o755); err != nil {
		t.Fatalf("mkdir old path: %v", err)
	}
	if err := os.WriteFile(root.Abs(oldPath), []byte("book bytes"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET title = 'New Title', sort_title = 'New Title' WHERE id = 'w_1'"); err != nil {
		t.Fatalf("update title: %v", err)
	}

	moved, err := Work(database, root, "w_1")
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved = %d; want 1", moved)
	}

	var storagePath, filename, originalFilename string
	if err := database.QueryRow("SELECT storage_path, filename, original_filename FROM assets WHERE id = 'a_1'").Scan(&storagePath, &filename, &originalFilename); err != nil {
		t.Fatalf("query asset path: %v", err)
	}
	const wantPath = "New Title - Source Name.epub"
	if storagePath != wantPath || filename != filepath.Base(wantPath) || originalFilename != "Source Name.epub" {
		t.Fatalf("asset path fields = %q / %q / %q; want %q / %q / Source Name.epub", storagePath, filename, originalFilename, wantPath, filepath.Base(wantPath))
	}
	if got, err := os.ReadFile(root.Abs(wantPath)); err != nil {
		t.Fatalf("read new file: %v", err)
	} else if string(got) != "book bytes" {
		t.Fatalf("new file = %q; want original bytes", got)
	}
	if _, err := os.Stat(root.Abs(oldPath)); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed: %v", err)
	}

	moved, err = Work(database, root, "w_1")
	if err != nil {
		t.Fatalf("second Work: %v", err)
	}
	if moved != 0 {
		t.Fatalf("second moved = %d; want 0", moved)
	}
	var secondStoragePath, secondFilename, secondOriginalFilename string
	if err := database.QueryRow("SELECT storage_path, filename, original_filename FROM assets WHERE id = 'a_1'").Scan(&secondStoragePath, &secondFilename, &secondOriginalFilename); err != nil {
		t.Fatalf("query asset path after second Work: %v", err)
	}
	if secondStoragePath != storagePath || secondFilename != filename || secondOriginalFilename != originalFilename {
		t.Fatalf("second Work changed asset path fields: before=%q/%q/%q after=%q/%q/%q", storagePath, filename, originalFilename, secondStoragePath, secondFilename, secondOriginalFilename)
	}
	if got, err := os.ReadFile(root.Abs(wantPath)); err != nil {
		t.Fatalf("read new file after second Work: %v", err)
	} else if string(got) != "book bytes" {
		t.Fatalf("new file after second Work = %q; want original bytes", got)
	}
	if _, err := os.Stat(root.Abs(oldPath)); !os.IsNotExist(err) {
		t.Fatalf("old file exists after second Work or stat failed: %v", err)
	}
}

func TestRenameAuthorWarnsWhenRelayoutStorageUnavailable(t *testing.T) {
	database, root := setupRelayoutTest(t)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	authorSort := bookmeta.AuthorSort("Jane Doe")
	oldPath := relayoutTestPath(t, "Moved Title", "Jane Doe", authorSort, "a_1", ".epub")
	seedRelayoutWork(t, database, "w_1", "a_1", "Moved Title", "Jane Doe", authorSort, ".epub", oldPath)
	if err := os.MkdirAll(filepath.Dir(root.Abs(oldPath)), 0o755); err != nil {
		t.Fatalf("mkdir old path: %v", err)
	}
	if err := os.WriteFile(root.Abs(oldPath), []byte("book bytes"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	// Simulate a dropped mount: the whole books root disappears.
	if err := os.RemoveAll(root.Path); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	res, err := RenameAuthor(context.Background(), database, root, "Jane Doe", "Jane Roe")
	if err != nil {
		t.Fatalf("RenameAuthor: %v", err)
	}
	if res.Affected != 1 || res.Moved != 0 || len(res.Warnings) != 1 {
		t.Fatalf("result = %+v; want affected 1, moved 0, one warning", res)
	}
	if !errors.Is(res.Warnings[0], storage.ErrLayoutMissing) {
		t.Fatalf("warning = %v; want ErrLayoutMissing", res.Warnings[0])
	}
	var name string
	if err := database.QueryRow("SELECT name FROM authors WHERE id = 'author_1'").Scan(&name); err != nil {
		t.Fatalf("query author: %v", err)
	}
	if name != "Jane Roe" {
		t.Fatalf("author name = %q; want Jane Roe", name)
	}
	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets WHERE id = 'a_1'").Scan(&storagePath); err != nil {
		t.Fatalf("query storage path: %v", err)
	}
	if storagePath != oldPath {
		t.Fatalf("storage_path = %q; want unchanged %q", storagePath, oldPath)
	}
	if _, err := os.Stat(root.Path); !os.IsNotExist(err) {
		t.Fatalf("RenameAuthor recreated the books root despite missing layout; stat err=%v", err)
	}
}

func relayoutTestPath(t *testing.T, title, author, authorSort, assetID, ext string) string {
	t.Helper()
	rel, err := storage.BookPath(storage.DefaultBookPathTemplate, storage.BookPathData{
		Title:      title,
		Author:     author,
		AuthorSort: authorSort,
		AssetID:    assetID,
		Ext:        ext,
	})
	if err != nil {
		t.Fatalf("StoragePath: %v", err)
	}
	return rel
}

func setupRelayoutTest(t *testing.T) (*db.DB, storage.Root) {
	t.Helper()
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database, storage.NewRoot(filepath.Join(dataDir, "storage"))
}

func seedRelayoutWork(t *testing.T, database *db.DB, workID, assetID, title, authorName, authorSort, ext, storagePath string) {
	t.Helper()
	if _, err := database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, ?, ?)", workID, title, title); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	if _, err := database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('author_1', ?, ?)", authorName, authorSort); err != nil {
		t.Fatalf("insert author: %v", err)
	}
	if _, err := database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'author_1', 0)", workID); err != nil {
		t.Fatalf("insert work_author: %v", err)
	}
	if _, err := database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES (?, ?, ?, ?, ?)",
		assetID, workID, storagePath, filepath.Base(storagePath), ext); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
}
