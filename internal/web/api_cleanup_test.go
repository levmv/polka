package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
)

func TestCleanupDuplicateMergeUsesMutationSequencerAndStagesCover(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()
	ensureTestStorageLayout(t, dataDir)

	member := mustUser(t, database, "curator", db.RoleMember)

	const (
		survivorID = 173
		loserID    = 147
		survivorA  = 1
		loserA     = 2
		title      = "Duplicate Book"
		author     = "Jane Doe"
	)
	authorSort := bookmeta.AuthorSort(author)
	survivorPath := defaultStoragePath(t, title, author, authorSort, survivorA, ".epub")
	loserPath := defaultStoragePath(t, title, author, authorSort, loserA, ".epub")
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, cover_version)
		VALUES (?, ?, ?, 0), (?, ?, ?, 1)
	`, survivorID, title, title, loserID, title, title)
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, ?, ?)", author, authorSort)
	mustExec(t, database, `
		INSERT INTO book_authors (book_id, author_id, author_order)
		VALUES (?, 1, 0), (?, 1, 0)
	`, survivorID, loserID)
	mustExec(t, database, `
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, writeback_rev, original_sha256, current_sha256)
		VALUES
			(?, ?, ?, ?, '.epub', 'epub', 1, 0, randomblob(32), randomblob(32)),
			(?, ?, ?, ?, '.epub', 'epub', 1, 0, randomblob(32), randomblob(32))
	`, survivorA, survivorID, survivorPath, filepath.Base(survivorPath), loserA, loserID, loserPath, filepath.Base(loserPath))

	for rel, body := range map[string][]byte{
		survivorPath: []byte("survivor book"),
		loserPath:    []byte("loser book"),
	} {
		if err := os.MkdirAll(filepath.Join(dataDir, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, rel), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	coverBytes := []byte("loser cover bytes")
	loserCover := filepath.Join(dataDir, covers.OriginalPath(loserID))
	if err := os.MkdirAll(filepath.Dir(loserCover), 0o755); err != nil {
		t.Fatalf("mkdir cover: %v", err)
	}
	if err := os.WriteFile(loserCover, coverBytes, 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}

	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	handler := testRoutes(t, s)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, member.ID, http.MethodPost, "/api/cleanup/duplicates/merge", map[string]any{
		"survivor_id": survivorID,
		"book_ids":    []int64{survivorID, loserID},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("merge status = %d; body: %s", w.Code, w.Body.String())
	}

	var resp cleanupDuplicateMergeResponse
	decodeJSON(t, w, &resp)
	if resp.Survivor.ID != survivorID || len(resp.TrashedIDs) != 1 || resp.TrashedIDs[0] != loserID || resp.RelayoutWarnings != 0 {
		t.Fatalf("response = %+v", resp)
	}

	var coverVersion, metadataRev int
	if err := database.Read(t.Context()).QueryRow("SELECT cover_version, metadata_rev FROM books WHERE id = ?", survivorID).Scan(&coverVersion, &metadataRev); err != nil {
		t.Fatalf("query survivor: %v", err)
	}
	if coverVersion != 1 || metadataRev != 1 {
		t.Fatalf("survivor cover_version/metadata_rev = %d/%d; want 1/1", coverVersion, metadataRev)
	}
	var deletedAt sql.NullInt64
	if err := database.Read(t.Context()).QueryRow("SELECT deleted_at FROM books WHERE id = ?", loserID).Scan(&deletedAt); err != nil {
		t.Fatalf("query loser deleted_at: %v", err)
	}
	if !deletedAt.Valid {
		t.Fatalf("loser was not moved to trash")
	}

	gotCover, err := os.ReadFile(filepath.Join(dataDir, covers.OriginalPath(survivorID)))
	if err != nil {
		t.Fatalf("read survivor cover: %v", err)
	}
	if string(gotCover) != string(coverBytes) {
		t.Fatalf("survivor cover = %q; want copied loser cover", gotCover)
	}

	var assetCount, primaryCount int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*), SUM(is_primary) FROM assets WHERE book_id = ?", survivorID).Scan(&assetCount, &primaryCount); err != nil {
		t.Fatalf("query survivor assets: %v", err)
	}
	if assetCount != 2 || primaryCount != 1 {
		t.Fatalf("survivor assets=%d primaries=%d; want 2/1", assetCount, primaryCount)
	}
}
