package relayout

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestMutateWorksBumpsMetadataRevAndReindexes(t *testing.T) {
	database, root := setupRelayoutTest(t)

	if _, err := database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w_1', 'Old Title', 'Old Title')"); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, writeback_rev)
		VALUES ('a_1', 'w_1', 'old/a_1.epub', 'a_1.epub', '.epub', 'epub', 0)
	`); err != nil {
		t.Fatalf("insert asset: %v", err)
	}

	res, err := MutateWorks(context.Background(), database, root, func(tx *sql.Tx) (Changed, error) {
		if _, err := tx.Exec("UPDATE works SET title = 'New Title', sort_title = 'New Title' WHERE id = 'w_1'"); err != nil {
			return Changed{}, err
		}
		return Changed{BumpMetadataRev: []string{"w_1", "w_1"}}, nil
	})
	if err != nil {
		t.Fatalf("MutateWorks: %v", err)
	}
	if res.Moved != 0 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v; want no relayout work", res)
	}

	var metadataRev int
	if err := database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w_1'").Scan(&metadataRev); err != nil {
		t.Fatalf("query metadata_rev: %v", err)
	}
	if metadataRev != 1 {
		t.Fatalf("metadata_rev = %d; want 1", metadataRev)
	}

	var searchTitle string
	if err := database.QueryRow("SELECT title FROM search WHERE work_id = 'w_1'").Scan(&searchTitle); err != nil {
		t.Fatalf("query search title: %v", err)
	}
	if searchTitle != "New Title" {
		t.Fatalf("search title = %q; want New Title", searchTitle)
	}

	counts, err := db.CountDirtyMetadataWritebackAssets(database, db.FullVisibilityScope())
	if err != nil {
		t.Fatalf("CountDirtyMetadataWritebackAssets: %v", err)
	}
	if counts.Dirty != 1 {
		t.Fatalf("dirty writeback assets = %d; want 1", counts.Dirty)
	}
}

func TestMutateWorksRefreshesSearchFilenameAfterRelayout(t *testing.T) {
	database, root := setupRelayoutTest(t)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	authorSort := bookmeta.AuthorSort("Jane Doe")
	oldPath := relayoutTestPath(t, "Old Title", "Jane Doe", authorSort, "a_1", ".epub")
	newPath := relayoutTestPath(t, "New Title", "Jane Doe", authorSort, "a_1", ".epub")
	seedRelayoutWork(t, database, "w_1", "a_1", "Old Title", "Jane Doe", authorSort, ".epub", oldPath)
	if err := os.MkdirAll(filepath.Dir(root.Abs(oldPath)), 0o755); err != nil {
		t.Fatalf("mkdir old path: %v", err)
	}
	if err := os.WriteFile(root.Abs(oldPath), []byte("book bytes"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}

	res, err := MutateWorks(context.Background(), database, root, func(tx *sql.Tx) (Changed, error) {
		if _, err := tx.Exec("UPDATE works SET title = 'New Title', sort_title = 'New Title' WHERE id = 'w_1'"); err != nil {
			return Changed{}, err
		}
		return Changed{BumpMetadataRev: []string{"w_1"}, Relayout: []string{"w_1"}}, nil
	})
	if err != nil {
		t.Fatalf("MutateWorks: %v", err)
	}
	if res.Moved != 1 || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v; want one clean move", res)
	}

	var filename string
	if err := database.QueryRow("SELECT filename FROM search WHERE work_id = 'w_1'").Scan(&filename); err != nil {
		t.Fatalf("query search filename: %v", err)
	}
	if filename != filepath.Base(newPath) {
		t.Fatalf("search filename = %q; want %q", filename, filepath.Base(newPath))
	}
}
