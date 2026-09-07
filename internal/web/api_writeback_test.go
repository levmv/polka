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
		bookID     int64
		seal       bool
		wantStatus int
	}{
		{name: "nothing to submit", bookID: 2, seal: true, wantStatus: http.StatusOK},
		{name: "accepted", bookID: 1, wantStatus: http.StatusAccepted},
		{name: "server stopping", bookID: 1, seal: true, wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, dataDir := setupTestDB(t)
			defer database.Close()
			mustExec(t, database, "UPDATE assets SET format = 'epub' WHERE id = 1")

			background := newTaskGroup(context.Background())
			defer background.Stop()
			if tt.seal {
				background.Seal()
			}
			s := &Server{db: database, dataDir: dataDir, background: background}

			body, err := json.Marshal(bulkWritebackRequest{IDs: []int64{tt.bookID}})
			if err != nil {
				t.Fatalf("encode request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/books/bulk/writeback", bytes.NewReader(body))
			req = req.WithContext(withUser(req.Context(), &db.User{
				ID:           1,
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

	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (3,'Book','Book')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) "+
		"VALUES (2,3,'B/Book [a2].epub','Book.epub','.epub','epub', randomblob(32), randomblob(32))")

	writebackDTO := func(bookID int64) BookWritebackDTO {
		t.Helper()
		wb, err := s.bookWritebackDTO(t.Context(), bookID, true)
		if err != nil {
			t.Fatalf("bookWritebackDTO(%d): %v", bookID, err)
		}
		if wb == nil {
			t.Fatalf("bookWritebackDTO(%d) admin = nil; want an object", bookID)
		}
		return *wb
	}

	// Born clean (both revs 0): available to an admin in manual mode, not dirty.
	if wb := writebackDTO(3); !wb.Available || wb.Dirty {
		t.Fatalf("clean admin = %+v; want available and not dirty", wb)
	}
	// A non-admin gets no write-back object at all (the field is omitted).
	if wb, err := s.bookWritebackDTO(t.Context(), 3, false); err != nil || wb != nil {
		t.Fatalf("member writeback = %+v, %v; want nil object", wb, err)
	}

	// A metadata edit bumps the rev, making the file dirty.
	if err := database.Transact(t.Context(), func(tx *db.Tx) error {
		return db.BumpMetadataRev(tx, []int64{3})
	}); err != nil {
		t.Fatalf("BumpMetadataRev: %v", err)
	}
	if wb := writebackDTO(3); !wb.Available || !wb.Dirty {
		t.Fatalf("dirty admin = %+v; want available and dirty", wb)
	}

	// Off mode hides the action even for an admin; the dirty fact is still true.
	if err := writeback.SaveMode(database.Write(t.Context()), writeback.ModeOff); err != nil {
		t.Fatalf("SaveMode off: %v", err)
	}
	if wb := writebackDTO(3); wb.Available || !wb.Dirty {
		t.Fatalf("off-mode admin = %+v; want unavailable but still dirty", wb)
	}

	// A PDF-only book has no writable asset, so the action never appears.
	if err := writeback.SaveMode(database.Write(t.Context()), writeback.ModeManual); err != nil {
		t.Fatalf("SaveMode manual: %v", err)
	}
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (4,'Paper','Paper')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) "+
		"VALUES (3,4,'P/Paper [a3].pdf','Paper.pdf','.pdf','pdf', randomblob(32), randomblob(32))")
	if wb := writebackDTO(4); wb.Available || wb.Dirty {
		t.Fatalf("pdf-only admin = %+v; want neither available nor dirty", wb)
	}
}

func mustExec(t *testing.T, database *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Write(t.Context()).Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
