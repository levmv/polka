package web

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/workslot"
)

func setupPageCountServer(t *testing.T) (*Server, string) {
	t.Helper()
	database, dataDir := setupTestDB(t)
	t.Cleanup(func() { database.Close() })
	raw := []byte(strings.Repeat("A paragraph of text. ", 1000))
	hash := sha256.Sum256(raw)
	name := filepath.Join(dataDir, "Tolkien/The_Hobbit/a_1.epub")
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	mustExec(t, database, `UPDATE assets SET format='txt', is_primary=1, current_size=?, current_sha256=? WHERE id=1`, len(raw), hash[:])
	return &Server{db: database, dataDir: dataDir, storageQueue: workslot.New()}, name
}

func TestBookPageCountIsExplicitCachedAndScoped(t *testing.T) {
	s, name := setupPageCountServer(t)
	database := s.db
	user := &db.User{ID: 1, Role: db.RoleReader, ContentScope: db.ContentScopeAll}
	request := func(post bool, viewer *db.User) *httptest.ResponseRecorder {
		t.Helper()
		method := http.MethodGet
		if post {
			method = http.MethodPost
		}
		r := httptest.NewRequest(method, "/api/books/1", nil)
		r.SetPathValue("id", "1")
		r = r.WithContext(withUser(r.Context(), viewer))
		w := httptest.NewRecorder()
		if post {
			s.handleAPIBookPageCount(w, r)
		} else {
			s.handleAPIBookDetail(w, r)
		}
		return w
	}
	w := request(false, user)
	if w.Code != 200 {
		t.Fatalf("detail: %d %s", w.Code, w.Body.String())
	}
	var missing bool
	if err := database.Read(t.Context()).QueryRow("SELECT page_count IS NULL FROM assets WHERE id=1").Scan(&missing); err != nil || !missing {
		t.Fatalf("GET performed a calculation: %v", err)
	}
	w = request(true, user)
	if w.Code != 200 {
		t.Fatalf("count: %d %s", w.Code, w.Body.String())
	}
	var first pageCountResultDTO
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.PageCount <= 0 || first.AssetID != 1 {
		t.Fatalf("result: %+v", first)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	w = request(true, user)
	var second pageCountResultDTO
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || second.PageCount != first.PageCount {
		t.Fatal("cached count reread the file")
	}
	restricted := &db.User{ID: 1, Role: db.RoleReader, ContentScope: db.ContentScopeShelves}
	if w := request(true, restricted); w.Code != 404 {
		t.Fatalf("restricted count: %d %s", w.Code, w.Body.String())
	}
	mustExec(t, database, "UPDATE books SET deleted_at=unixepoch() WHERE id=1")
	if w := request(true, user); w.Code != 404 {
		t.Fatalf("trashed count: %d", w.Code)
	}
}

func TestBookPageCountSurvivesClientDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := setupPageCountServer(t)
		s.requestBaseContext = t.Context()
		// Holding the writer lets us disconnect after counting, before saving.
		tx, err := s.db.BeginWrite(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		user := &db.User{ID: 1, Role: db.RoleReader, ContentScope: db.ContentScopeAll}
		r := httptest.NewRequest(http.MethodPost, "/api/books/1/page-count", nil)
		r.SetPathValue("id", "1")
		r = r.WithContext(withUser(ctx, user))
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.handleAPIBookPageCount(httptest.NewRecorder(), r)
		}()
		synctest.Wait()
		cancel()
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		<-done
		var pages int
		if err := s.db.Read(t.Context()).QueryRow("SELECT COALESCE(page_count, 0) FROM assets WHERE id=1").Scan(&pages); err != nil {
			t.Fatal(err)
		}
		if pages <= 0 {
			t.Fatal("client disconnect discarded the count")
		}
	})
}
