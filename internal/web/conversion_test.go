package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/testfixture"
)

func TestConvertedDownloadAndDeliveryUseCatalogMetadata(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	src := testfixture.EPUB(t, []byte(`<package version="3.0" xmlns="http://www.idpf.org/2007/opf">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Source title</dc:title><dc:language>en</dc:language><dc:date>1990</dc:date></metadata>
<manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="text"/></spine></package>`), map[string][]byte{
		"OEBPS/text.xhtml": []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter</title></head><body><p>Book text.</p></body></html>`),
	})
	path := filepath.Join(dir, "Tolkien", "The_Hobbit", "a_1.epub")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := storage.HashReader(t.Context(), bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, database, "UPDATE assets SET current_hash = ?, format = 'epub' WHERE id = 1", hash)
	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(t, database, dir)
	handler := testRoutes(t, s)
	get := func(url string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, url, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", url, w.Code, w.Body)
		}
		return w
	}
	readerURL := readerFallbackURL(1, format.FormatEPUB, hash)
	for _, date := range []string{"2000", ""} {
		mustExec(t, database, "UPDATE books SET title = 'Catalog title', published_date = ?, updated_at = 1800000000 WHERE id = 1", date)
		// Even a source-versioned URL must revalidate catalog-dependent output.
		download := get("/download/1/as/kepub?v=" + conversionCacheVersion(hash))
		if got := download.Header().Get("Cache-Control"); got != "private, no-cache" {
			t.Fatalf("catalog-dependent download cache = %q", got)
		}
		meta, err := format.ExtractEPUBMetadata(bytes.NewReader(download.Body.Bytes()), int64(download.Body.Len()))
		if err != nil || meta == nil || meta.Title != "Catalog title" || meta.Date != date {
			t.Fatalf("converted download metadata = %+v, %v; want catalog title and date %q", meta, err, date)
		}
		job := createQueuedDeliveryJob(t, database, user.ID, sql.NullString{String: "kepub", Valid: true})
		copy, cleanup, err := s.prepareDeliveryCopy(t.Context(), *job, nil)
		if err != nil {
			t.Fatal(err)
		}
		delivered, err := os.ReadFile(copy.Path)
		cleanup()
		if err != nil || !bytes.Equal(delivered, download.Body.Bytes()) {
			t.Fatalf("delivery and download differ: %v", err)
		}
		readerCopy := get(readerURL)
		if !bytes.Equal(download.Body.Bytes(), readerCopy.Body.Bytes()) || readerCopy.Header().Get("Cache-Control") != "private, no-cache" {
			t.Fatal("reader conversion did not use the same catalog snapshot and cache policy")
		}
	}
	if original, err := os.ReadFile(path); err != nil || !bytes.Equal(original, src) {
		t.Fatalf("conversion changed the library file: %v", err)
	}
}

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
	s := newTestServer(t, database, dir)
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
