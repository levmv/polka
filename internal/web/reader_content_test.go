package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestPinnedReaderNeverLabelsReplacementAsOpenedContent(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "pinned-reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	asset, err := s.assetFile(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.managedRoot().Resolve(asset.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte("original reader bytes")
	replacement := []byte("replaced reader bytes")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(original)
	mustExec(t, database, "UPDATE assets SET format='epub',can_read=1,current_sha256=? WHERE id=1", hash[:])
	request := func(digest []byte) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, "GET", "/read/assets/1?source="+hex.EncodeToString(digest), nil))
		return w
	}
	first := request(hash[:])
	if first.Code != 200 || first.Body.String() != string(original) {
		t.Fatalf("read: %d %s", first.Code, first.Body.String())
	}
	// Atomic replacement can retain the same size and timestamp. Inode/file
	// identity, not just mtime and size, must invalidate the verification cache.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	staged := path + ".new"
	if err := os.WriteFile(staged, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staged, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, path); err != nil {
		t.Fatal(err)
	}
	if stale := request(hash[:]); stale.Code != 409 {
		t.Fatalf("replacement mislabeled: %d %s", stale.Code, stale.Body.String())
	}
	next := sha256.Sum256(replacement)
	mustExec(t, database, "UPDATE assets SET current_sha256=? WHERE id=1", next[:])
	if stale := request(hash[:]); stale.Code != 409 {
		t.Fatalf("old shell received new book: %d", stale.Code)
	}
	updated := request(next[:])
	if updated.Code != 200 || updated.Body.String() != string(replacement) {
		t.Fatalf("new read: %d %s", updated.Code, updated.Body.String())
	}
}
