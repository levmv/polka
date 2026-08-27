package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/workslot"
	"github.com/levmv/polka/internal/writeback"
)

func TestMixedStorageMutationBurstStaysConsistent(t *testing.T) {
	database, dataDir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "storage-burst-admin", db.RoleAdmin)
	if err := db.SoftDeleteWork(database, "w_2", admin.ID); err != nil {
		t.Fatalf("trash purge fixture: %v", err)
	}

	// Use a valid writable FB2 for the existing work so write-back can run in
	// either order relative to cover changes and repeated path-sensitive edits.
	oldPath := filepath.Join(dataDir, "Tolkien", "The_Hobbit", "a_1.epub")
	newPath := filepath.Join(dataDir, "Tolkien", "The_Hobbit", "a_1.fb2")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("rename writable fixture: %v", err)
	}
	fb2 := []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description><title-info>
    <author><first-name>J.R.R.</first-name><last-name>Tolkien</last-name></author>
    <book-title>The Hobbit</book-title><lang>en</lang>
  </title-info></description>
  <body><section><p>There and back again.</p></section></body>
</FictionBook>`)
	if err := os.WriteFile(newPath, fb2, 0o644); err != nil {
		t.Fatalf("write writable fixture: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE assets SET
			storage_path = 'Tolkien/The_Hobbit/a_1.fb2',
			filename = 'a_1.fb2', extension = '.fb2', format = 'fb2',
			original_sha256 = NULL, current_sha256 = NULL,
			original_size = NULL, current_size = NULL
		WHERE id = 'asset_1'
	`); err != nil {
		t.Fatalf("update writable fixture: %v", err)
	}

	queue := workslot.New()
	s := &Server{
		db:           database,
		dataDir:      dataDir,
		storageQueue: queue,
		sessions:     newSessionStore(database),
	}
	handler := testRoutes(t, s)
	sid, err := s.sessions.issue(admin.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	cover, err := validateCoverBytes(testPNG(t))
	if err != nil {
		t.Fatalf("validate cover: %v", err)
	}

	// Hold the queue so an injected cancellation is deterministic and cannot
	// publish half a cover mutation before the rest of the burst starts.
	releaseBurst, err := queue.Acquire(context.Background())
	if err != nil {
		t.Fatalf("hold storage queue: %v", err)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() {
		canceled <- s.storeCoverBytes(canceledCtx, "w_1", cover)
	}()
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		releaseBurst()
		t.Fatalf("canceled cover error = %v; want context.Canceled", err)
	}

	type mutation func() error
	mutations := make([]mutation, 0, 16)
	const burstSize = 5
	for i := range burstSize {
		importReq := uploadBookRequest(t, fmt.Sprintf("burst-%d.epub", i), testEPUB(
			t,
			fmt.Sprintf("Burst Import %d", i),
			fmt.Sprintf("Import Author %d", i),
			fmt.Sprintf("Author, Import %d", i),
		))
		importReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
		mutations = append(mutations, func() error {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, importReq)
			if w.Code != http.StatusCreated {
				return fmt.Errorf("import %d: status %d: %s", i, w.Code, w.Body.String())
			}
			return nil
		})

		editReq := jsonRequestWithSession(t, sid, http.MethodPatch, "/api/books/w_1", map[string]any{
			"title": fmt.Sprintf("Burst Title %d", i),
		})
		mutations = append(mutations, func() error {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, editReq)
			if w.Code != http.StatusOK {
				return fmt.Errorf("edit %d: status %d: %s", i, w.Code, w.Body.String())
			}
			return nil
		})

		mutations = append(mutations, func() error {
			if err := s.storeCoverBytes(context.Background(), "w_1", cover); err != nil {
				return fmt.Errorf("cover %d: %w", i, err)
			}
			return nil
		})
	}
	mutations = append(mutations,
		func() error {
			summary, err := writeback.Run(context.Background(), database, storage.NewRoot(dataDir), writeback.Options{
				WorkIDs:   []string{"w_1"},
				WorkQueue: queue,
				CoverRoot: storage.NewRoot(dataDir),
			})
			if err != nil {
				return fmt.Errorf("write-back: %w", err)
			}
			if summary.Failed != 0 {
				return fmt.Errorf("write-back failures: %+v", summary)
			}
			return nil
		},
		func() error {
			n, err := s.purgeTrashedWorks(context.Background(), []string{"w_2"})
			if err != nil {
				return fmt.Errorf("purge: %w", err)
			}
			if n != 1 {
				return fmt.Errorf("purge count = %d, want 1", n)
			}
			return nil
		},
	)

	start := make(chan struct{})
	errs := make(chan error, len(mutations))
	var wg sync.WaitGroup
	for _, run := range mutations {
		wg.Go(func() {
			<-start
			errs <- run()
		})
	}
	close(start)
	releaseBurst()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}

	// Converge a write-back that may have run before the last edit/cover, then
	// inspect the durable DB/filesystem state left by the entire burst.
	finalWriteback, err := writeback.Run(context.Background(), database, storage.NewRoot(dataDir), writeback.Options{
		WorkIDs:   []string{"w_1"},
		WorkQueue: queue,
		CoverRoot: storage.NewRoot(dataDir),
	})
	if err != nil || finalWriteback.Failed != 0 {
		t.Fatalf("final write-back = %+v, %v", finalWriteback, err)
	}

	var integrity string
	if err := database.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q", integrity)
	}
	fkRows, err := database.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if fkRows.Next() {
		fkRows.Close()
		t.Fatal("foreign_key_check reported a violation")
	}
	if err := fkRows.Err(); err != nil {
		fkRows.Close()
		t.Fatalf("foreign_key_check rows: %v", err)
	}
	if err := fkRows.Close(); err != nil {
		t.Fatalf("close foreign_key_check rows: %v", err)
	}

	var coverVersion, attempts, works, assets int
	if err := database.QueryRow("SELECT cover_version FROM works WHERE id = 'w_1'").Scan(&coverVersion); err != nil {
		t.Fatalf("query cover version: %v", err)
	}
	if coverVersion != burstSize {
		t.Fatalf("cover_version = %d, want %d", coverVersion, burstSize)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM metadata_writeback_attempts").Scan(&attempts); err != nil {
		t.Fatalf("count write-back attempts: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("unfinished write-back attempts = %d", attempts)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM works").Scan(&works); err != nil {
		t.Fatalf("count works: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if works != burstSize+1 || assets != burstSize+1 {
		t.Fatalf("catalog counts = %d works/%d assets, want %d/%d", works, assets, burstSize+1, burstSize+1)
	}

	rows, err := database.Query("SELECT storage_path FROM assets ORDER BY id")
	if err != nil {
		t.Fatalf("list asset paths: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			t.Fatalf("scan asset path: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("DB asset %q missing after burst: %v", rel, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("asset paths: %v", err)
	}
}
