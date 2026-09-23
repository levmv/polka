package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
)

func TestConversionGateHonorsWaitingContext(t *testing.T) {
	s := &Server{conversionSlots: make(chan struct{}, 1)}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.withConversionSlot(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := s.withConversionSlot(ctx, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting conversion error = %v; want context.Canceled", err)
	}
	if called {
		t.Fatal("conversion callback ran without an available slot")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first conversion error = %v", err)
	}

	called = false
	err = s.withConversionSlot(ctx, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("already-canceled conversion = %v, called = %v; want context.Canceled without callback", err, called)
	}
}

func TestPreparedDownloadKeepsResultAndChecksAccess(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "reader", db.RoleReader)
	other := mustUser(t, database, "other", db.RoleReader)
	mustExec(t, database, `UPDATE assets SET format = 'html', extension = '.html', filename = 'book.html' WHERE id = 1`)
	source := filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")
	if err := os.WriteFile(source, []byte(`<html><body><p>Readable chapter</p><img src="missing.png" alt="Diagram"/><p>Ending</p></body></html>`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(database, dir)
	t.Cleanup(func() { s.preparedDownloads.expire(time.Now().Add(preparedDownloadTTL)) })
	handler := testRoutes(t, s)
	request := func(userID int64, method, url string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, userID, method, url, nil))
		return w
	}
	w := request(user.ID, http.MethodPost, "/api/assets/1/download/epub")
	if w.Code != http.StatusOK {
		t.Fatalf("prepare = %d: %s", w.Code, w.Body)
	}
	var result struct {
		DownloadURL string `json:"download_url"`
		HasWarnings bool   `json:"has_warnings"`
	}
	decodeJSON(t, w, &result)
	if !result.HasWarnings {
		t.Fatal("missing conversion warning")
	}
	if got := request(other.ID, http.MethodGet, result.DownloadURL); got.Code != http.StatusNotFound {
		t.Fatalf("another user's download = %d", got.Code)
	}
	// A download uses the prepared snapshot even if the source has disappeared;
	// it must not perform a second conversion with a potentially different result.
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	w = request(user.ID, http.MethodGet, result.DownloadURL)
	if w.Code != http.StatusOK || w.Header().Get("X-Polka-Conversion-Warnings") != "true" || !bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
		t.Fatalf("download = %d, headers %v", w.Code, w.Header())
	}
	first := bytes.Clone(w.Body.Bytes())
	if retry := request(user.ID, http.MethodGet, result.DownloadURL); !bytes.Equal(retry.Body.Bytes(), first) {
		t.Fatal("download retry changed the file")
	}
	s.preparedDownloads.expire(time.Now().Add(preparedDownloadTTL))
	files, err := os.ReadDir(filepath.Join(dir, "tmp", "conversion"))
	if err != nil || len(files) != 0 {
		t.Fatalf("expired files remain: %v, %v", files, err)
	}
}

func TestPreparedDownloadsBoundDiskAndKeepActiveReaders(t *testing.T) {
	var store preparedDownloadStore
	t.Cleanup(func() { store.expire(time.Now().Add(preparedDownloadTTL)) })
	dir := t.TempDir()
	add := func(userID, size int64) (string, string) {
		t.Helper()
		file, err := os.CreateTemp(dir, "download-*")
		if err != nil {
			t.Fatal(err)
		}
		// Sparse files exercise the disk budget without writing large fixtures.
		if err := file.Truncate(size); err != nil {
			t.Fatal(err)
		}
		token, err := store.add(&preparedDownload{userID: userID, file: file, size: size, cleanup: func() {
			file.Close()
			os.Remove(file.Name())
		}})
		if err != nil {
			file.Close()
			t.Fatal(err)
		}
		return token, file.Name()
	}
	oldToken, oldPath := add(1, maxPreparedDownloadBytes/2)
	_, _ = add(2, maxPreparedDownloadBytes/2)
	token, path := add(3, 16)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("oldest idle file was not removed when disk budget filled: %v", err)
	}
	if file, _ := store.acquire(oldToken, 1); file != nil {
		t.Fatal("retired link still works")
	}
	file, release := store.acquire(token, 3)
	if file == nil {
		t.Fatal("new download unavailable")
	}
	_, releaseRetry := store.acquire(token, 3)
	store.expire(time.Now().Add(preparedDownloadTTL))
	if expired, _ := store.acquire(token, 3); expired != nil {
		t.Fatal("expired link accepts new readers")
	}
	var buf [16]byte
	if _, err := file.file.ReadAt(buf[:], 0); err != nil {
		t.Fatalf("expiry interrupted active transfer: %v", err)
	}
	release()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file removed before last transfer completed: %v", err)
	}
	releaseRetry()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired file remains after transfers completed: %v", err)
	}
}
