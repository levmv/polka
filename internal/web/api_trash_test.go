package web

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/workslot"
)

// TestTrashLifecycleHTTP exercises the full soft-delete → trash → restore →
// admin-purge flow through the router, including the member/admin role split on
// purge and on-disk file removal.
func TestTrashLifecycleHTTP(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	member := mustUser(t, database, "member", db.RoleMember)
	admin := mustUser(t, database, "admin", db.RoleAdmin)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	do := func(userID, method, target string) *httptest.ResponseRecorder {
		req := jsonRequest(t, s, userID, method, target, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	if w := do(member.ID, http.MethodDelete, "/api/books/w_2"); w.Code != http.StatusNoContent {
		t.Fatalf("member delete = %d, want 204; body: %s", w.Code, w.Body)
	}

	if got := listBookIDs(t, do(member.ID, http.MethodGet, "/api/books")); len(got) != 1 || got[0] != "w_1" {
		t.Fatalf("browse after delete = %v, want [w_1]", got)
	}
	if w := do(member.ID, http.MethodGet, "/api/books/w_2"); w.Code != http.StatusNotFound {
		t.Fatalf("detail of trashed = %d, want 404", w.Code)
	}

	var trash []TrashedBookDTO
	decodeJSON(t, do(member.ID, http.MethodGet, "/api/trash"), &trash)
	if len(trash) != 1 || trash[0].ID != "w_2" || trash[0].DeletedBy != "member" || trash[0].DeletedAt == 0 {
		t.Fatalf("trash listing = %+v, want one w_2 trashed by member", trash)
	}

	if w := do(member.ID, http.MethodPost, "/api/books/w_2/restore"); w.Code != http.StatusNoContent {
		t.Fatalf("restore = %d, want 204", w.Code)
	}
	if got := listBookIDs(t, do(member.ID, http.MethodGet, "/api/books")); len(got) != 2 {
		t.Fatalf("browse after restore = %v, want 2 works", got)
	}

	if err := db.SoftDeleteWork(database, "w_1", member.ID); err != nil {
		t.Fatalf("soft delete w_1: %v", err)
	}
	if w := do(member.ID, http.MethodDelete, "/api/books/w_1/purge"); w.Code != http.StatusForbidden {
		t.Fatalf("member purge = %d, want 403", w.Code)
	}

	assetPath := filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")
	if _, err := os.Stat(assetPath); err != nil {
		t.Fatalf("asset file gone after forbidden purge: %v", err)
	}

	if w := do(admin.ID, http.MethodDelete, "/api/books/w_1/purge"); w.Code != http.StatusNoContent {
		t.Fatalf("admin purge = %d, want 204; body: %s", w.Code, w.Body)
	}
	if _, err := os.Stat(assetPath); !os.IsNotExist(err) {
		t.Fatalf("asset file still present after purge: stat err = %v", err)
	}
	var after []TrashedBookDTO
	decodeJSON(t, do(admin.ID, http.MethodGet, "/api/trash"), &after)
	if len(after) != 0 {
		t.Fatalf("trash after purge = %+v, want empty", after)
	}
}

// TestEmptyTrashHTTP covers the bulk admin purge: a member is forbidden, an
// admin purges the whole trashed set in one call, and the on-disk files go too.
func TestEmptyTrashHTTP(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	member := mustUser(t, database, "member", db.RoleMember)
	admin := mustUser(t, database, "admin", db.RoleAdmin)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	do := func(userID, method, target string) *httptest.ResponseRecorder {
		req := jsonRequest(t, s, userID, method, target, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	for _, id := range []string{"w_1", "w_2"} {
		if err := db.SoftDeleteWork(database, id, member.ID); err != nil {
			t.Fatalf("soft delete %s: %v", id, err)
		}
	}
	assetPath := filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")

	if w := do(member.ID, http.MethodDelete, "/api/trash"); w.Code != http.StatusForbidden {
		t.Fatalf("member empty-trash = %d, want 403", w.Code)
	}
	if _, err := os.Stat(assetPath); err != nil {
		t.Fatalf("asset gone after forbidden empty-trash: %v", err)
	}

	w := do(admin.ID, http.MethodDelete, "/api/trash")
	var got struct {
		Purged int `json:"purged"`
	}
	decodeJSON(t, w, &got)
	if got.Purged != 2 {
		t.Fatalf("purged = %d, want 2", got.Purged)
	}
	if _, err := os.Stat(assetPath); !os.IsNotExist(err) {
		t.Fatalf("asset still present after empty-trash: stat err = %v", err)
	}

	var after []TrashedBookDTO
	decodeJSON(t, do(admin.ID, http.MethodGet, "/api/trash"), &after)
	if len(after) != 0 {
		t.Fatalf("trash after empty = %+v, want empty", after)
	}
	if got := listBookIDs(t, do(admin.ID, http.MethodGet, "/api/books")); len(got) != 0 {
		t.Fatalf("catalog after empty-trash = %v, want empty", got)
	}

	var none struct {
		Purged int `json:"purged"`
	}
	decodeJSON(t, do(admin.ID, http.MethodDelete, "/api/trash"), &none)
	if none.Purged != 0 {
		t.Fatalf("second empty-trash purged = %d, want 0", none.Purged)
	}
}

func TestPurgeUnavailableRootPreservesCatalog(t *testing.T) {
	tests := []struct {
		name   string
		target string
		ids    []string
	}{
		{name: "single", target: "/api/books/w_1/purge", ids: []string{"w_1"}},
		{name: "empty trash", target: "/api/trash", ids: []string{"w_1", "w_2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, dir := setupTestDB(t)
			defer database.Close()
			admin := mustUser(t, database, "admin", db.RoleAdmin)
			for _, id := range tt.ids {
				if err := db.SoftDeleteWork(database, id, admin.ID); err != nil {
					t.Fatalf("soft delete %s: %v", id, err)
				}
			}

			s := &Server{
				db:          database,
				dataDir:     dir,
				storageRoot: storage.NewRoot(filepath.Join(dir, "missing-books")),
				sessions:    newSessionStore(database),
			}
			handler := testRoutes(t, s)
			req := jsonRequest(t, s, admin.ID, http.MethodDelete, tt.target, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
			}

			for _, id := range tt.ids {
				var deleted bool
				if err := database.QueryRow("SELECT deleted_at IS NOT NULL FROM works WHERE id = ?", id).Scan(&deleted); err != nil {
					t.Fatalf("work %s disappeared after rejected purge: %v", id, err)
				}
				if !deleted {
					t.Fatalf("work %s was restored during rejected purge", id)
				}
			}
			assetPath := filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")
			if _, err := os.Stat(assetPath); err != nil {
				t.Fatalf("asset changed after rejected purge: %v", err)
			}
		})
	}
}

func TestPurgeWaitsForStorageSlot(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	admin := mustUser(t, database, "purge-slot-admin", db.RoleAdmin)
	if err := db.SoftDeleteWork(database, "w_1", admin.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	queue := workslot.New()
	s := &Server{db: database, dataDir: dir, storageQueue: queue}
	releaseOtherMutation, err := queue.Acquire(context.Background())
	if err != nil {
		t.Fatalf("hold storage slot: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.purgeTrashedWorks(context.Background(), []string{"w_1"})
		done <- err
	}()

	select {
	case err := <-done:
		releaseOtherMutation()
		t.Fatalf("purge bypassed occupied storage slot: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	var exists bool
	if err := database.QueryRow("SELECT EXISTS(SELECT 1 FROM works WHERE id = 'w_1')").Scan(&exists); err != nil {
		releaseOtherMutation()
		t.Fatalf("query work: %v", err)
	}
	if !exists {
		releaseOtherMutation()
		t.Fatal("purge changed DB while storage slot was occupied")
	}

	releaseOtherMutation()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("purge after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("purge did not resume after storage slot release")
	}
	if err := database.QueryRow("SELECT EXISTS(SELECT 1 FROM works WHERE id = 'w_1')").Scan(&exists); err != nil {
		t.Fatalf("query purged work: %v", err)
	}
	if exists {
		t.Fatal("work still exists after purge")
	}
}

func TestPurgeTreatsMissingAssetAsOrdinaryDrift(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	admin := mustUser(t, database, "missing-asset-admin", db.RoleAdmin)
	if err := db.SoftDeleteWork(database, "w_1", admin.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	assetPath := filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")
	if err := os.Remove(assetPath); err != nil {
		t.Fatalf("remove asset fixture: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	req := jsonRequest(t, s, admin.ID, http.MethodDelete, "/api/books/w_1/purge", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
	var exists bool
	if err := database.QueryRow("SELECT EXISTS(SELECT 1 FROM works WHERE id = 'w_1')").Scan(&exists); err != nil {
		t.Fatalf("query work: %v", err)
	}
	if exists {
		t.Fatal("work survived purge because its individual asset was missing")
	}
}

func listBookIDs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var books []BookSummaryDTO
	decodeJSON(t, w, &books)
	ids := make([]string, len(books))
	for i, b := range books {
		ids[i] = b.ID
	}
	return ids
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	if err := json.UnmarshalRead(w.Body, v); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}
