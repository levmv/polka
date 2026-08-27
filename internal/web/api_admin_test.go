package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/writeback"
)

func TestAPIAdminStorageStatus(t *testing.T) {
	database, dataDir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	member := mustUser(t, database, "Member", db.RoleMember)

	storageDir := filepath.Join(dataDir, "managed")
	root, err := storage.SaveRoot(database.DB, dataDir, storageDir)
	if err != nil {
		t.Fatalf("SaveRoot: %v", err)
	}
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	template := "{author_bucket}/{series|Standalone}/{title} [{asset_id}]{dot_ext}"
	if _, err := storage.SaveBookPathTemplate(database.DB, template); err != nil {
		t.Fatalf("SaveBookPathTemplate: %v", err)
	}
	// The books root is the books tree; drop the catalog's book into it so the
	// root reads as a populated, reachable library. An empty root while the
	// catalog has books is the dropped-mount signal and reports unreachable.
	bookDir := filepath.Join(storageDir, "Tolkien", "The_Hobbit")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "a_1.epub"), []byte("epub content"), 0o644); err != nil {
		t.Fatalf("write book: %v", err)
	}
	ingestDir := filepath.Join(dataDir, "drop")
	if _, err := ingest.SavePath(database.DB, dataDir, ingestDir); err != nil {
		t.Fatalf("SavePath: %v", err)
	}
	if err := os.MkdirAll(ingestDir, 0o755); err != nil {
		t.Fatalf("mkdir ingest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ingestDir, "queued.epub"), []byte("queued"), 0o644); err != nil {
		t.Fatalf("write queued: %v", err)
	}

	s := &Server{db: database, dataDir: dataDir, storageRoot: root, sessions: newSessionStore(database)}
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodGet, "/api/admin/storage", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("admin storage status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got AdminStorageDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Books.Path != storageDir {
		t.Fatalf("books folder path = %q; want %q", got.Books.Path, storageDir)
	}
	if !got.Books.Reachable {
		t.Fatalf("books folder reachable = false; want true for an initialized root")
	}
	wantBooks, wantSize, err := db.LibraryStorageStats(database.DB)
	if err != nil {
		t.Fatalf("LibraryStorageStats: %v", err)
	}
	if got.Books.BookCount != wantBooks || got.Books.SizeBytes != wantSize {
		t.Fatalf("books folder stats = (%d books, %d bytes); want (%d, %d)",
			got.Books.BookCount, got.Books.SizeBytes, wantBooks, wantSize)
	}
	if got.Layout.Template != template {
		t.Fatalf("layout template = %q; want %q", got.Layout.Template, template)
	}
	if got.Ingest.Path != ingestDir || got.Ingest.Pending != 1 {
		t.Fatalf("ingest status = %+v; want path %q and pending=1", got.Ingest, ingestDir)
	}
	if !got.Ingest.Reachable {
		t.Fatalf("ingest reachable = false; want true for an existing folder")
	}
	if !got.Ingest.Enabled {
		t.Fatalf("ingest enabled = false, want true")
	}
	if got.Ingest.DeleteSources {
		t.Fatalf("delete sources = true, want default false")
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, member.ID, http.MethodGet, "/api/admin/storage", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member storage status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestAPIAdminStorageUpdateIncomingFolder(t *testing.T) {
	database, dataDir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	member := mustUser(t, database, "Member", db.RoleMember)

	storageDir := filepath.Join(dataDir, "managed")
	root, err := storage.SaveRoot(database.DB, dataDir, storageDir)
	if err != nil {
		t.Fatalf("SaveRoot: %v", err)
	}
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	oldIngest := filepath.Join(dataDir, "drop")
	if _, err := ingest.SavePath(database.DB, dataDir, oldIngest); err != nil {
		t.Fatalf("SavePath: %v", err)
	}

	s := &Server{db: database, dataDir: dataDir, storageRoot: root, sessions: newSessionStore(database)}
	t.Cleanup(s.stopIngester)
	handler := testRoutes(t, s)

	newIngest := filepath.Join(dataDir, "incoming")
	enabled := true
	deleteSources := true
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPatch, "/api/admin/storage", adminStorageUpdateRequest{
		Ingest: &ingestUpdateRequest{
			Enabled:       &enabled,
			DeleteSources: &deleteSources,
			Path:          &newIngest,
		},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("admin storage update = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var got AdminStorageDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Ingest.Enabled || !got.Ingest.DeleteSources || got.Ingest.Path != newIngest {
		t.Fatalf("ingest status = %+v; want enabled path %q", got.Ingest, newIngest)
	}
	if _, err := os.Stat(newIngest); err != nil {
		t.Fatalf("new ingest dir missing: %v", err)
	}
	if s.currentIngester() == nil {
		t.Fatalf("ingester was not started")
	}

	enabled = false
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPatch, "/api/admin/storage", adminStorageUpdateRequest{
		Ingest: &ingestUpdateRequest{Enabled: &enabled},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("disable ingest = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if s.currentIngester() != nil {
		t.Fatalf("ingester was not stopped")
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, member.ID, http.MethodPatch, "/api/admin/storage", adminStorageUpdateRequest{
		Ingest: &ingestUpdateRequest{Path: &newIngest},
	}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member storage update = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestAPIAdminStorageDoesNotSaveUnusableIncomingFolder(t *testing.T) {
	database, dataDir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	before, err := ingest.OpenConfig(database.DB, dataDir)
	if err != nil {
		t.Fatalf("open ingest config: %v", err)
	}

	blocker := filepath.Join(dataDir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatalf("write path blocker: %v", err)
	}
	requestedPath := filepath.Join(blocker, "incoming")
	deleteSources := true

	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	handler := testRoutes(t, s)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPatch, "/api/admin/storage", adminStorageUpdateRequest{
		Ingest: &ingestUpdateRequest{
			Path:          &requestedPath,
			DeleteSources: &deleteSources,
		},
	}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("unusable incoming folder status = %d, want %d; body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}

	after, err := ingest.OpenConfig(database.DB, dataDir)
	if err != nil {
		t.Fatalf("reopen ingest config: %v", err)
	}
	if after != before {
		t.Fatalf("ingest config after failed update = %+v, want unchanged %+v", after, before)
	}
}

func TestAPIAdminStorageUpdateWritebackAuto(t *testing.T) {
	database, dataDir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	root, err := storage.SaveRoot(database.DB, dataDir, filepath.Join(dataDir, "managed"))
	if err != nil {
		t.Fatalf("SaveRoot: %v", err)
	}

	s := &Server{db: database, dataDir: dataDir, storageRoot: root, sessions: newSessionStore(database)}
	handler := testRoutes(t, s)
	mode := "auto"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPatch, "/api/admin/storage", adminStorageUpdateRequest{
		Writeback: &writebackUpdateRequest{Mode: &mode},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("save writeback auto = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	got, err := writeback.OpenMode(database.DB)
	if err != nil {
		t.Fatalf("OpenMode: %v", err)
	}
	if got != writeback.ModeAuto {
		t.Fatalf("mode = %q; want auto", got)
	}

	invalidMode := "surprise"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPatch, "/api/admin/storage", adminStorageUpdateRequest{
		Writeback: &writebackUpdateRequest{Mode: &invalidMode},
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("save invalid writeback mode = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if got, err := writeback.OpenMode(database.DB); err != nil || got != writeback.ModeAuto {
		t.Fatalf("mode after rejected save = %q, %v; want auto", got, err)
	}
}
