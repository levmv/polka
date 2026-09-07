package relayout

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestMutateBooksBumpsMetadataRevAndReindexes(t *testing.T) {
	database, root := setupRelayoutTest(t)

	if _, err := database.Write(t.Context()).Exec("INSERT INTO books (id, title, sort_title) VALUES (1, 'Old Title', 'Old Title')"); err != nil {
		t.Fatalf("insert book: %v", err)
	}
	if _, err := database.Write(t.Context()).Exec(`
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, writeback_rev, original_sha256, current_sha256)
		VALUES (1, 1, 'old/a_1.epub', 'a_1.epub', '.epub', 'epub', 0, randomblob(32), randomblob(32));
		INSERT INTO search (rowid, title) VALUES (1, 'Old Title');
	`); err != nil {
		t.Fatalf("insert asset: %v", err)
	}

	res, err := MutateBooks(context.Background(), database, root, func(tx *db.Tx) (Changed, error) {
		if _, err := tx.Exec("UPDATE books SET title = 'New Title', sort_title = 'New Title' WHERE id = 1"); err != nil {
			return Changed{}, err
		}
		return Changed{BumpMetadataRev: []int64{1, 1}}, nil
	})
	if err != nil {
		t.Fatalf("MutateBooks: %v", err)
	}
	if res.Moved != 0 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v; want no relayout work", res)
	}

	var metadataRev int
	if err := database.Read(t.Context()).QueryRow("SELECT metadata_rev FROM books WHERE id = 1").Scan(&metadataRev); err != nil {
		t.Fatalf("query metadata_rev: %v", err)
	}
	if metadataRev != 1 {
		t.Fatalf("metadata_rev = %d; want 1", metadataRev)
	}

	var oldMatches, newMatches int
	if err := database.Read(t.Context()).QueryRow(`SELECT count(*) FROM search WHERE search MATCH 'title:"Old Title"' AND rowid = 1`).Scan(&oldMatches); err != nil {
		t.Fatalf("query old search title: %v", err)
	}
	if err := database.Read(t.Context()).QueryRow(`SELECT count(*) FROM search WHERE search MATCH 'title:"New Title"' AND rowid = 1`).Scan(&newMatches); err != nil {
		t.Fatalf("query new search title: %v", err)
	}
	if oldMatches != 0 || newMatches != 1 {
		t.Fatalf("title matches after mutation = old %d, new %d; want 0, 1", oldMatches, newMatches)
	}

	counts, err := db.CountDirtyMetadataWritebackAssets(database.Read(t.Context()), db.FullVisibilityScope())
	if err != nil {
		t.Fatalf("CountDirtyMetadataWritebackAssets: %v", err)
	}
	if counts.Dirty != 1 {
		t.Fatalf("dirty writeback assets = %d; want 1", counts.Dirty)
	}
}

func TestMutateBooksRefreshesSearchFilenameAfterRelayout(t *testing.T) {
	database, root := setupRelayoutTest(t)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	authorSort := bookmeta.AuthorSort("Jane Doe")
	oldPath := relayoutTestPath(t, "Old Title", "Jane Doe", authorSort, 1, ".epub")
	seedRelayoutBook(t, database, 1, 1, "Old Title", "Jane Doe", authorSort, ".epub", oldPath)
	if err := os.MkdirAll(filepath.Dir(root.Abs(oldPath)), 0o755); err != nil {
		t.Fatalf("mkdir old path: %v", err)
	}
	if err := os.WriteFile(root.Abs(oldPath), []byte("book bytes"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}

	res, err := MutateBooks(context.Background(), database, root, func(tx *db.Tx) (Changed, error) {
		if _, err := tx.Exec("UPDATE books SET title = 'New Title', sort_title = 'New Title' WHERE id = 1"); err != nil {
			return Changed{}, err
		}
		return Changed{BumpMetadataRev: []int64{1}, Relayout: []int64{1}}, nil
	})
	if err != nil {
		t.Fatalf("MutateBooks: %v", err)
	}
	if res.Moved != 1 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v; want one clean move", res)
	}

	var oldMatches, newMatches int
	if err := database.Read(t.Context()).QueryRow(`SELECT count(*) FROM search WHERE search MATCH 'filename:old' AND rowid = 1`).Scan(&oldMatches); err != nil {
		t.Fatalf("query old search filename: %v", err)
	}
	if err := database.Read(t.Context()).QueryRow(`SELECT count(*) FROM search WHERE search MATCH 'filename:new' AND rowid = 1`).Scan(&newMatches); err != nil {
		t.Fatalf("query new search filename: %v", err)
	}
	if oldMatches != 0 || newMatches != 1 {
		t.Fatalf("filename matches after relayout = old %d, new %d; want 0, 1", oldMatches, newMatches)
	}
}
