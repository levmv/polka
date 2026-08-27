package web

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/writeback"
)

func TestBulkWritebackAdmissionStatus(t *testing.T) {
	tests := []struct {
		name       string
		workID     string
		seal       bool
		wantStatus int
	}{
		{name: "nothing to submit", workID: "w_2", seal: true, wantStatus: http.StatusOK},
		{name: "accepted", workID: "w_1", wantStatus: http.StatusAccepted},
		{name: "server stopping", workID: "w_1", seal: true, wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, dataDir := setupTestDB(t)
			defer database.Close()
			mustExec(t, database, "UPDATE assets SET format = 'epub' WHERE id = 'asset_1'")

			background := newTaskGroup(context.Background())
			defer background.Stop()
			if tt.seal {
				background.Seal()
			}
			s := &Server{db: database, dataDir: dataDir, background: background}

			body, err := json.Marshal(bulkWritebackRequest{IDs: []string{tt.workID}})
			if err != nil {
				t.Fatalf("encode request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/books/bulk/writeback", bytes.NewReader(body))
			req = req.WithContext(withUser(req.Context(), &db.User{
				ID:           "admin",
				Role:         db.RoleAdmin,
				ContentScope: db.ContentScopeAll,
			}))
			rr := httptest.NewRecorder()
			s.handleAPIBulkWriteback(rr, req)
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d; want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

// TestBookWritebackDTOGating covers the affordance logic without touching files:
// the action is admin-only, manual-mode-only, needs a writable asset, and is
// "dirty" only once the file is behind the catalog.
func TestBookWritebackDTOGating(t *testing.T) {
	database, dataDir := setupTestDB(t)
	defer database.Close()
	s := &Server{db: database, dataDir: dataDir}

	mustExec(t, database, "INSERT INTO works (id, title, sort_title) VALUES ('w1','Book','Book')")
	mustExec(t, database, "INSERT INTO assets (id, work_id, storage_path, filename, extension, format) "+
		"VALUES ('a1','w1','B/Book [a1].epub','Book.epub','.epub','epub')")

	writebackDTO := func(workID string) BookWritebackDTO {
		t.Helper()
		wb, err := s.bookWritebackDTO(workID, true)
		if err != nil {
			t.Fatalf("bookWritebackDTO(%s): %v", workID, err)
		}
		if wb == nil {
			t.Fatalf("bookWritebackDTO(%s) admin = nil; want an object", workID)
		}
		return *wb
	}

	// Born clean (both revs 0): available to an admin in manual mode, not dirty.
	if wb := writebackDTO("w1"); !wb.Available || wb.Dirty {
		t.Fatalf("clean admin = %+v; want available and not dirty", wb)
	}
	// A non-admin gets no write-back object at all (the field is omitted).
	if wb, err := s.bookWritebackDTO("w1", false); err != nil || wb != nil {
		t.Fatalf("member writeback = %+v, %v; want nil object", wb, err)
	}

	// A metadata edit bumps the rev, making the file dirty.
	if err := db.BumpMetadataRev(database.DB, []string{"w1"}); err != nil {
		t.Fatalf("BumpMetadataRev: %v", err)
	}
	if wb := writebackDTO("w1"); !wb.Available || !wb.Dirty {
		t.Fatalf("dirty admin = %+v; want available and dirty", wb)
	}

	// Off mode hides the action even for an admin; the dirty fact is still true.
	if err := writeback.SaveMode(database.DB, writeback.ModeOff); err != nil {
		t.Fatalf("SaveMode off: %v", err)
	}
	if wb := writebackDTO("w1"); wb.Available || !wb.Dirty {
		t.Fatalf("off-mode admin = %+v; want unavailable but still dirty", wb)
	}

	// A PDF-only work has no writable asset, so the action never appears.
	if err := writeback.SaveMode(database.DB, writeback.ModeManual); err != nil {
		t.Fatalf("SaveMode manual: %v", err)
	}
	mustExec(t, database, "INSERT INTO works (id, title, sort_title) VALUES ('w2','Paper','Paper')")
	mustExec(t, database, "INSERT INTO assets (id, work_id, storage_path, filename, extension, format) "+
		"VALUES ('a2','w2','P/Paper [a2].pdf','Paper.pdf','.pdf','pdf')")
	if wb := writebackDTO("w2"); wb.Available || wb.Dirty {
		t.Fatalf("pdf-only admin = %+v; want neither available nor dirty", wb)
	}
}

func mustExec(t *testing.T, database *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
