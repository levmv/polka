package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/bootstrap"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/writeback"
)

func TestMergeEditedWorkAutoWritebackUpdatesEveryFile(t *testing.T) {
	s, handler, userID := writebackIdentityServer(t)
	a, _ := addWritebackIdentityBook(t, s, "one")
	b, _ := addWritebackIdentityBook(t, s, "two")
	writebackIdentityJSON(t, s, handler, userID, http.MethodPatch, "/api/books/"+b.WorkID, map[string]any{"publisher": "Former publisher"})
	writebackIdentityJSON(t, s, handler, userID, http.MethodPost, "/api/books/"+b.WorkID+"/writeback", nil)
	writebackIdentityJSON(t, s, handler, userID, http.MethodPost, "/api/cleanup/duplicates/merge", map[string]any{
		"survivor_id": a.WorkID, "work_ids": []string{a.WorkID, b.WorkID},
	})

	state, err := db.GetWorkWritebackState(s.db, a.WorkID)
	if err != nil || state.Dirty != 2 {
		t.Fatalf("merged writeback state = %+v, %v; want both files dirty", state, err)
	}
	runIdentityAutoWriteback(t, s, 2)
	for _, assetID := range []string{a.AssetID, b.AssetID} {
		data := readWritebackIdentityAsset(t, s, assetID)
		meta, err := format.ExtractFB2MetadataFromXMLBytes(data)
		if err != nil || meta.Publisher != "" {
			t.Fatalf("asset %s metadata = %+v, %v; want survivor's empty publisher", assetID, meta, err)
		}
	}
}

func TestRestoreWritebackAcknowledgementMatchesRestoredBytes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		useCurrent bool
		wantDirty  int
	}{
		{name: "original bytes", wantDirty: 1},
		{name: "last written bytes", useCurrent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, handler, userID := writebackIdentityServer(t)
			a, original := addWritebackIdentityBook(t, s, "restore")
			writebackIdentityJSON(t, s, handler, userID, http.MethodPatch, "/api/books/"+a.WorkID, map[string]any{"title": "Edited title"})
			writebackIdentityJSON(t, s, handler, userID, http.MethodPost, "/api/books/"+a.WorkID+"/writeback", nil)
			current := readWritebackIdentityAsset(t, s, a.AssetID)
			row, err := db.GetMetadataWritebackAsset(s.db, a.AssetID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(s.managedRoot().Abs(row.StoragePath)); err != nil {
				t.Fatal(err)
			}
			source := original
			if tc.useCurrent {
				source = current
			}
			req := uploadBookRequest(t, "restored.fb2", source)
			addSessionCookie(t, s, req, userID)
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("upload: %d %s", resp.Code, resp.Body.String())
			}
			if data := readWritebackIdentityAsset(t, s, a.AssetID); !bytes.Equal(data, source) {
				t.Fatal("restored file differs from uploaded bytes")
			}
			state, err := db.GetWorkWritebackState(s.db, a.WorkID)
			if err != nil || state.Dirty != tc.wantDirty {
				t.Fatalf("restored writeback state = %+v, %v; want dirty=%d", state, err, tc.wantDirty)
			}
			runIdentityAutoWriteback(t, s, tc.wantDirty)
			meta, err := format.ExtractFB2MetadataFromXMLBytes(readWritebackIdentityAsset(t, s, a.AssetID))
			if err != nil || meta.Title != "Edited title" {
				t.Fatalf("restored metadata after auto = %+v, %v; want Edited title", meta, err)
			}
		})
	}
}

func writebackIdentityServer(t *testing.T) (*Server, http.Handler, int64) {
	t.Helper()
	dir := t.TempDir()
	database, err := bootstrap.EnsureLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	user := mustUser(t, database, "admin", db.RoleAdmin)
	s := &Server{db: database, dataDir: dir, storageRoot: storage.NewRoot(filepath.Join(dir, "books"))}
	return s, testRoutes(t, s), user.ID
}

func addWritebackIdentityBook(t *testing.T, s *Server, name string) (importer.Result, []byte) {
	t.Helper()
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?><FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0"><description><title-info><genre>prose</genre><author><first-name>Ada</first-name><last-name>Writer</last-name></author><book-title>Story</book-title><lang>en</lang></title-info></description><body><section><p>` + name + `</p></section></body></FictionBook>`)
	path := filepath.Join(t.TempDir(), name+".fb2")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := importer.ImportFile(context.Background(), s.db, s.managedRoot(), path, nil, importer.Options{CoverRoot: s.dataRoot()})
	if err != nil {
		t.Fatal(err)
	}
	return result, data
}

func writebackIdentityJSON(t *testing.T, s *Server, handler http.Handler, userID int64, method, path string, body any) {
	t.Helper()
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, jsonRequest(t, s, userID, method, path, body))
	if resp.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, resp.Code, resp.Body.String())
	}
}

func readWritebackIdentityAsset(t *testing.T, s *Server, assetID string) []byte {
	t.Helper()
	row, err := db.GetMetadataWritebackAsset(s.db, assetID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.managedRoot().Abs(row.StoragePath))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runIdentityAutoWriteback(t *testing.T, s *Server, wantPlanned int) {
	t.Helper()
	if err := writeback.SaveMode(s.db, writeback.ModeAuto); err != nil {
		t.Fatal(err)
	}
	service := writeback.NewService(s.db, s.managedRoot(), writeback.ServiceOptions{CoverRoot: s.dataRoot(), WorkQueue: s.storageQueue})
	summary, err := service.RunOnce(context.Background())
	if err != nil || summary.Planned != wantPlanned || summary.Failed != 0 || summary.Written+summary.Unchanged != wantPlanned {
		t.Fatalf("auto writeback = %+v, %v; want %d completed", summary, err, wantPlanned)
	}
}
