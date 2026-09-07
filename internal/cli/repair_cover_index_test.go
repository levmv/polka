package cli

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/storage"
)

func TestRecoverableCoverIndex(t *testing.T) {
	root := storage.NewRoot(t.TempDir())
	database, err := db.InitPath(root.Abs("library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	sourceHash := sha256.Sum256([]byte("source bytes"))
	if _, err := database.Write(t.Context()).Exec(`
		INSERT INTO books(id, title, sort_title) VALUES (1, 'One', 'One');
		INSERT INTO assets(id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (1, 1, 'one.epub', 'one.epub', '.epub', ?, ?);
	`, sourceHash[:], sourceHash[:]); err != nil {
		t.Fatal(err)
	}
	staging := root.StagingDir()
	coverDir := root.Abs("covers")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		t.Fatalf("mkdir covers: %v", err)
	}

	if _, err := storage.Stage(root, covers.ImportTempLabel(sourceHash[:]), bytes.NewBufferString("staged cover")); err != nil {
		t.Fatal(err)
	}
	stagedEntries, err := os.ReadDir(staging)
	if err != nil || len(stagedEntries) != 1 {
		t.Fatalf("staged cover files = %v, %v; want one complete temp", stagedEntries, err)
	}
	staged := filepath.Join(staging, stagedEntries[0].Name())
	// Block the final rename with an empty directory, leaving a complete temp
	// after the commit callback succeeds.
	coverRel := covers.OriginalPath(2)
	err = storage.Place(root, coverRel, covers.TempLabel(2), bytes.NewBufferString("placed cover"), func() error {
		return os.Mkdir(root.Abs(coverRel), 0o755)
	})
	if err == nil {
		t.Fatal("cover placement unexpectedly succeeded")
	}
	if err := os.Remove(root.Abs(coverRel)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(coverDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("stranded cover files = %v, %v; want one complete temp", entries, err)
	}
	placed := filepath.Join(coverDir, entries[0].Name())
	adjacentRel, err := storage.WriteAdjacentTemp(root, covers.OriginalPath(3), covers.TempLabel(3), []byte("adjacent cover"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.WriteAdjacentTemp(root, covers.OriginalPath(1), covers.TempLabel(1), []byte("shadowed cover")); err != nil {
		t.Fatal(err)
	}
	// A numeric basename alone must not turn an unrelated temp into a cover.
	unrelated := filepath.Join(coverDir, ".tmp-0123456789abcdef-100")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := newRecoverableCoverIndex(t.Context(), database.Read(t.Context()), root)
	for _, tc := range []struct {
		bookID int64
		want   string
	}{
		{bookID: 1, want: staged},
		{bookID: 2, want: placed},
		{bookID: 3, want: root.Abs(adjacentRel)},
		{bookID: 100, want: ""},
	} {
		if got, err := idx.find(tc.bookID); err != nil {
			t.Fatal(err)
		} else if got != tc.want {
			t.Errorf("find(%d) = %q; want %q", tc.bookID, got, tc.want)
		}
	}
}

func TestRepairImportCoverDoesNotFollowRolledBackBookID(t *testing.T) {
	root := storage.NewRoot(t.TempDir())
	database, err := db.InitPath(root.Abs("library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	// Model a process stopping before its import transaction commits, leaving
	// a complete staged cover but no persisted source asset.
	tx, err := database.BeginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT INTO books(title, sort_title) VALUES ('Aborted', 'Aborted')")
	if err != nil {
		t.Fatal(err)
	}
	abortedID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	abortedHash := sha256.Sum256([]byte("aborted source bytes"))
	if _, err := tx.Exec(`INSERT INTO assets(id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (1, ?, 'aborted.epub', 'aborted.epub', '.epub', ?, ?)`, abortedID, abortedHash[:], abortedHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Stage(root, covers.ImportTempLabel(abortedHash[:]), bytes.NewBufferString("aborted cover")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// A different import reuses the uncommitted ID. Fail its final cover
	// placement so repair must distinguish the two staged covers.
	source := root.Abs("source.epub")
	writeEPUB(t, source, "Committed", "Ada Writer", "Writer, Ada")
	plan, err := importer.Resolve(t.Context(), importer.Source{Path: source}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan.CoverBytes = []byte("committed cover")
	if err := os.WriteFile(root.Abs("covers"), []byte("placement blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Persist(t.Context(), database, root, plan, importer.Options{}); err == nil {
		t.Fatal("cover placement unexpectedly succeeded")
	}
	books, err := db.AllBookCovers(database.Read(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ID != abortedID || books[0].CoverVersion != 1 {
		t.Fatalf("committed books = %+v; want reused ID %d with a cover", books, abortedID)
	}
	if err := os.Remove(root.Abs("covers")); err != nil {
		t.Fatal(err)
	}
	repaired, err := repairCovers(t.Context(), database, root, root, books, nil)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Restored != 1 || repaired.StagedRemoved != 1 {
		t.Fatalf("repair = %+v; want committed cover restored and aborted temp removed", repaired)
	}
	got, err := os.ReadFile(root.Abs(covers.OriginalPath(abortedID)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plan.CoverBytes) {
		t.Fatalf("restored cover = %q; want %q", got, plan.CoverBytes)
	}
}
