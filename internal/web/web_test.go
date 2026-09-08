package web

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/koreader"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/testfixture"
)

var testReaderCurrentSHA256 = bytes.Repeat([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}, 4)

func setupTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "library.db")

	database, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	contents := []byte("epub content")
	fileHash := sha256.Sum256(contents)
	_, err = database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (1, 'The Hobbit', 'Hobbit, The');
		INSERT INTO books (id, title, sort_title) VALUES (2, 'Dune', 'Dune');

		INSERT INTO authors (id, name, sort_name) VALUES (1, 'J.R.R. Tolkien', 'Tolkien, J.R.R.');
		INSERT INTO authors (id, name, sort_name) VALUES (2, 'Frank Herbert', 'Herbert, Frank');

		INSERT INTO book_authors (book_id, author_id) VALUES (1, 1);
		INSERT INTO book_authors (book_id, author_id) VALUES (2, 2);

		INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (1, 1, 'Tolkien/The_Hobbit/a_1.epub', 'a_1.epub', '.epub', ?, ?);
	`, fileHash[:], fileHash[:])
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}
	mustUpdateSearchIndex(t, database, 1, 2)
	ensureTestStorageLayout(t, dir)

	fileDir := filepath.Join(dir, "Tolkien", "The_Hobbit")
	os.MkdirAll(fileDir, 0o755)
	os.WriteFile(filepath.Join(fileDir, "a_1.epub"), contents, 0o644)

	return database, dir
}

func mustUpdateSearchIndex(t *testing.T, database *db.DB, bookIDs ...int64) {
	t.Helper()
	tx, err := database.BeginWrite(context.Background())
	if err != nil {
		t.Fatalf("begin search-index update: %v", err)
	}
	defer tx.Rollback()
	for _, bookID := range bookIDs {
		if err := db.UpdateSearchIndex(tx, bookID); err != nil {
			t.Fatalf("update search index for %d: %v", bookID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit search-index update: %v", err)
	}
}

func newTestServer(database *db.DB, dataDir string) *Server {
	return &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
}

func mustUser(t *testing.T, database *db.DB, username, role string) *db.User {
	t.Helper()
	id := testfixture.SeedUser(t, database.Write(t.Context()).Exec, username, role)
	user, err := db.GetUserByID(database.Read(t.Context()), id)
	if err != nil || user == nil {
		t.Fatalf("get user %q: %v", username, err)
	}
	return user
}

func ensureTestStorageLayout(t *testing.T, dir string) {
	t.Helper()
	if err := storage.EnsureLayout(storage.NewRoot(dir)); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
}

func TestAPISearch(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	s := &Server{
		db:      database,
		dataDir: dir,
	}

	req := httptest.NewRequest("GET", "/api/books?q=Hobbit", nil)
	w := httptest.NewRecorder()

	s.handleAPIBooks(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var rawBooks []map[string]any
	if err := json.Unmarshal(body, &rawBooks); err != nil {
		t.Fatalf("failed to decode raw json: %v", err)
	}
	if len(rawBooks) > 0 {
		if _, ok := rawBooks[0]["description"]; ok {
			t.Fatalf("list row includes detail-only description field")
		}
		if _, ok := rawBooks[0]["description_source"]; ok {
			t.Fatalf("list row includes detail-only description_source field")
		}
		if _, ok := rawBooks[0]["description_html"]; ok {
			t.Fatalf("list row includes detail-only description_html field")
		}
	}

	var books []BookSummaryDTO
	if err := json.Unmarshal(body, &books); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}

	if len(books) != 1 {
		t.Fatalf("expected 1 book, got %d", len(books))
	}

	if books[0].ID != 1 {
		t.Errorf("expected book id 1, got %d", books[0].ID)
	}
}

func TestAPISearchTagAndSpecialChars(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	mustExec(t, database, "UPDATE books SET tags = 'fantasy, classics' WHERE id = 1")

	mustUpdateSearchIndex(t, database, 1)

	s := &Server{
		db:      database,
		dataDir: dir,
	}

	req := httptest.NewRequest("GET", "/api/books?q=fantasy", nil)
	w := httptest.NewRecorder()
	s.handleAPIBooks(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for tag search, got %d", w.Code)
	}
	var books []BookSummaryDTO
	json.UnmarshalRead(w.Body, &books)
	if len(books) != 1 || books[0].ID != 1 {
		t.Errorf("tag search failed, got %d books", len(books))
	}

	req2 := httptest.NewRequest("GET", "/api/books?q=foo:\"bar", nil)
	w2 := httptest.NewRecorder()
	s.handleAPIBooks(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("expected 200 for special chars search, got %d", w2.Code)
	}
}

func TestAPIBookSequenceLibraryContext(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	s := &Server{
		db:      database,
		dataDir: dir,
	}

	req := httptest.NewRequest("GET", "/api/books/2/sequence?from=library&sort=title&before=1&after=1", nil)
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	s.handleAPIBookSequence(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var sequence BookSequenceDTO
	if err := json.UnmarshalRead(w.Body, &sequence); err != nil {
		t.Fatalf("decode sequence: %v", err)
	}
	if sequence.CurrentIndex != 0 {
		t.Fatalf("current_index = %d, want 0", sequence.CurrentIndex)
	}
	if len(sequence.Items) != 2 || sequence.Items[0].ID != 2 || sequence.Items[1].ID != 1 {
		t.Fatalf("items = %+v, want [2 1]", sequence.Items)
	}
}

func TestDownloadHandler(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (135, 'Zipped FB2', 'Zipped FB2');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 135, 'FB2/Zipped/book.fb2.zip', 'book.fb2.zip', '.fb2.zip', 'fb2', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert fb2.zip fixture: %v", err)
	}
	fb2Dir := filepath.Join(dir, "FB2", "Zipped")
	if err := os.MkdirAll(fb2Dir, 0o755); err != nil {
		t.Fatalf("mkdir fb2.zip fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fb2Dir, "book.fb2.zip"), []byte("zip content"), 0o644); err != nil {
		t.Fatalf("write fb2.zip fixture: %v", err)
	}

	s := &Server{
		db:      database,
		dataDir: dir,
	}

	t.Run("first download while writer is held", func(t *testing.T) {
		tx, err := database.BeginWrite(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/download/1", nil)
		req.SetPathValue("id", "1")
		w := httptest.NewRecorder()
		s.handleDownload(w, req)
		if ctx.Err() != nil || w.Code != http.StatusOK || w.Body.String() != "epub content" {
			t.Fatalf("download with busy writer = %d, %q, %v; want complete file before deadline", w.Code, w.Body.String(), ctx.Err())
		}
	})

	req := httptest.NewRequest("GET", "/download/1", nil)
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()
	s.handleDownload(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}

	if res.Header.Get("Content-Disposition") != `attachment; filename="a_1.epub"; filename*=UTF-8''a_1.epub` {
		t.Errorf("unexpected content disposition: %s", res.Header.Get("Content-Disposition"))
	}
	if got := res.Header.Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("Content-Type = %q; want application/epub+zip", got)
	}
	wantHash, err := koreader.PartialMD5(bytes.NewReader([]byte("epub content")))
	if err != nil {
		t.Fatalf("PartialMD5 fixture: %v", err)
	}
	var gotHash string
	if err := database.Read(req.Context()).QueryRow("SELECT koreader_hash FROM assets WHERE id = 1").Scan(&gotHash); err != nil {
		t.Fatalf("select koreader hash: %v", err)
	}
	if gotHash != wantHash {
		t.Fatalf("koreader hash = %q; want %q", gotHash, wantHash)
	}

	// Once present, the DB-owned identity is reused. Supported file replacement
	// paths update or clear it themselves; an ordinary download is read-only.
	const persistedHash = "already-computed"
	mustExec(t, database, "UPDATE assets SET koreader_hash = ? WHERE id = 1", persistedHash)

	req = httptest.NewRequest("GET", "/download/1", nil)
	req.SetPathValue("id", "1")
	w = httptest.NewRecorder()
	s.handleDownload(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("repeat download status = %d, want 200", w.Code)
	}
	if err := database.Read(req.Context()).QueryRow("SELECT koreader_hash FROM assets WHERE id = 1").Scan(&gotHash); err != nil {
		t.Fatalf("select persisted koreader hash: %v", err)
	}
	if gotHash != persistedHash {
		t.Fatalf("repeat download koreader hash = %q; want persisted %q", gotHash, persistedHash)
	}

	req = httptest.NewRequest("GET", "/download/2", nil)
	req.SetPathValue("id", "2")
	w = httptest.NewRecorder()
	s.handleDownload(w, req)

	res = w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("fb2.zip download status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "application/zip" {
		t.Fatalf("fb2.zip Content-Type = %q; want application/zip", got)
	}

	req = httptest.NewRequest("GET", "/download/unknown_asset", nil)
	req.SetPathValue("id", "unknown_asset")
	w = httptest.NewRecorder()
	s.handleDownload(w, req)

	res = w.Result()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", res.StatusCode)
	}
}

func TestDownloadAsAZW4PDF(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (111, 'Print Replica', 'Print Replica');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 111, 'Kindle/Print/print-replica.azw4', 'print-replica.azw4', '.azw4', 'azw4', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert azw4 fixture: %v", err)
	}
	fileDir := filepath.Join(dir, "Kindle", "Print")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir azw4 fixture: %v", err)
	}
	pdf := []byte("%PDF-1.7\nbody\n%%EOF")
	if err := os.WriteFile(filepath.Join(fileDir, "print-replica.azw4"), append(testfixture.MinimalMOBI(), pdf...), 0o644); err != nil {
		t.Fatalf("write azw4 fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := httptest.NewRequest("GET", "/download/2/as/pdf", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("download-as status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="print-replica.pdf"; filename*=UTF-8''print-replica.pdf` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if !bytes.Equal(w.Body.Bytes(), pdf) {
		t.Fatalf("download-as body = %q, want %q", w.Body.Bytes(), pdf)
	}
}

func TestDownloadAsRejectsUnsupportedConversion(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (155, 'Legacy MOBI', 'Legacy MOBI');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 155, 'Kindle/Legacy/legacy.mobi', 'legacy.mobi', '.mobi', 'mobi', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert mobi fixture: %v", err)
	}
	fileDir := filepath.Join(dir, "Kindle", "Legacy")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir mobi fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "legacy.mobi"), testfixture.MinimalMOBI(), 0o644); err != nil {
		t.Fatalf("write mobi fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := httptest.NewRequest("GET", "/download/2/as/pdf", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported download-as status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "Unsupported conversion from MOBI to pdf") {
		t.Fatalf("unsupported download-as body = %q", body)
	}
}

func TestDownloadAsCBRToCBZ(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (118, 'RAR Comic', 'RAR Comic');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 118, 'Comics/RAR/rar-comic.cbr', 'rar-comic.cbr', '.cbr', 'cbr', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert CBR fixture: %v", err)
	}
	fileDir := filepath.Join(dir, "Comics", "RAR")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir CBR fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "rar-comic.cbr"), testfixture.CBR3(), 0o644); err != nil {
		t.Fatalf("write CBR fixture: %v", err)
	}

	user := mustUser(t, database, "cbr-reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	req := httptest.NewRequest("GET", "/download/2/as/cbz", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CBR download-as status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/vnd.comicbook+zip" {
		t.Fatalf("Content-Type = %q; want comic ZIP", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="rar-comic.cbz"; filename*=UTF-8''rar-comic.cbz` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	pages, err := format.ListCBZPages(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil || len(pages) != 2 {
		t.Fatalf("converted pages = %#v, %v; want two", pages, err)
	}
}

func TestDownloadAsAZW4WithoutPDFDoesNotSendAttachment(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (126, 'Empty Print Replica', 'Empty Print Replica');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 126, 'Kindle/Empty/empty.azw4', 'empty.azw4', '.azw4', 'azw4', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert empty azw4 fixture: %v", err)
	}
	fileDir := filepath.Join(dir, "Kindle", "Empty")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir empty azw4 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "empty.azw4"), testfixture.MinimalMOBI(), 0o644); err != nil {
		t.Fatalf("write empty azw4 fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := httptest.NewRequest("GET", "/download/2/as/pdf", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("download-as status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want empty for failed conversion", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "Asset cannot be converted to pdf") {
		t.Fatalf("download-as body = %q", body)
	}
}

func TestDownloadAsTXTToEPUB(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (177, 'Plain Notes', 'Plain Notes');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 177, 'Text/Plain/plain.txt', 'plain.txt', '.txt', 'txt', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert txt fixture: %v", err)
	}
	fileDir := filepath.Join(dir, "Text", "Plain")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir txt fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "plain.txt"), []byte("First paragraph.\n\nSecond paragraph.\n"), 0o644); err != nil {
		t.Fatalf("write txt fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := httptest.NewRequest("GET", "/download/2/as/epub", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("download-as status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("Content-Type = %q, want application/epub+zip", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="plain.epub"; filename*=UTF-8''plain.epub` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if body := w.Body.Bytes(); !bytes.Contains(body, []byte("OEBPS/text.xhtml")) {
		t.Fatalf("download-as body does not look like EPUB zip")
	}
}

func TestDownloadAsOversizedTXTReturns413(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (138, 'Huge Notes', 'Huge Notes');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 138, 'Text/Huge/huge.txt', 'huge.txt', '.txt', 'txt', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert huge txt fixture: %v", err)
	}
	fileDir := filepath.Join(dir, "Text", "Huge")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir huge txt fixture: %v", err)
	}
	path := filepath.Join(fileDir, "huge.txt")
	if err := os.WriteFile(path, []byte("plain text\n"), 0o644); err != nil {
		t.Fatalf("write huge txt fixture: %v", err)
	}
	if err := os.Truncate(path, 129<<20); err != nil {
		t.Fatalf("truncate huge txt fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := httptest.NewRequest("GET", "/download/2/as/epub", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("download-as status = %d, want 413; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want empty for failed conversion", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "Conversion input is too large") {
		t.Fatalf("download-as body = %q", body)
	}
}

func TestDownloadAsEPUBToKEPUB(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (128, 'Kobo Source', 'Kobo Source');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 128, 'EPUB/Kobo/source.epub', 'source.epub', '.epub', 'epub', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert epub fixture: %v", err)
	}
	mustExec(t, database, "UPDATE assets SET current_sha256 = ? WHERE id = 2", testReaderCurrentSHA256)

	fileDir := filepath.Join(dir, "EPUB", "Kobo")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir epub fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "source.epub"), testReadableEPUB(t, "Kobo Source", "Kobo body."), 0o644); err != nil {
		t.Fatalf("write epub fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	convertedVersion := conversionCacheVersion(testReaderCurrentSHA256)
	if convertedVersion == "" {
		t.Fatal("test build has no Polka version for converted asset cache key")
	}
	req := httptest.NewRequest("GET", "/download/2/as/kepub?v="+convertedVersion, nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("download-as status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("Content-Type = %q, want application/epub+zip", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="source.kepub.epub"; filename*=UTF-8''source.kepub.epub` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := w.Header().Get("Content-Length"); got != strconv.Itoa(w.Body.Len()) {
		t.Fatalf("Content-Length = %q, want %d", got, w.Body.Len())
	}
	if got := w.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable private cache", got)
	}
	xhtml := testZipEntry(t, w.Body.Bytes(), "OEBPS/text.xhtml")
	if !strings.Contains(xhtml, "koboSpan") || !strings.Contains(xhtml, "Kobo body.") {
		t.Fatalf("KEPUB text.xhtml missing Kobo spans/body:\n%s", xhtml)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "tmp", "conversion"))
	if err != nil {
		t.Fatalf("read conversion temp directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("conversion temp directory retained %d files", len(entries))
	}
}

func TestDownloadAsEPUBToRepairedEPUB(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (129, 'Repair Source', 'Repair Source');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 129, 'EPUB/Repair/source.epub', 'source.epub', '.epub', 'epub', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert EPUB rebuild fixture: %v", err)
	}
	fileDir := filepath.Join(dir, "EPUB", "Repair")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir EPUB rebuild fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "source.epub"), testReadableEPUB(t, "Repair Source", "Preserved body."), 0o644); err != nil {
		t.Fatalf("write EPUB rebuild fixture: %v", err)
	}

	user := mustUser(t, database, "repair-reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	req := httptest.NewRequest("GET", "/download/2/as/epub", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("download repaired EPUB status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="source.repaired.epub"; filename*=UTF-8''source.repaired.epub` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("open repaired EPUB response: %v", err)
	}
	if len(zr.File) < 2 || zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store || zr.File[1].Name != "META-INF/container.xml" {
		t.Fatalf("repaired EPUB prefix is not canonical")
	}
	if xhtml := testZipEntry(t, w.Body.Bytes(), "OEBPS/text.xhtml"); !strings.Contains(xhtml, "Preserved body.") {
		t.Fatalf("repaired EPUB lost body:\n%s", xhtml)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "tmp", "conversion"))
	if err != nil {
		t.Fatalf("read conversion temp directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("EPUB rebuild retained %d temporary files", len(entries))
	}
}

func TestDownloadAsConversionFailureDoesNotCommitAttachment(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (114, 'Broken EPUB', 'Broken EPUB');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (2, 114, 'EPUB/Broken/source.epub', 'source.epub', '.epub', 'epub', randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert broken EPUB fixture: %v", err)
	}
	fileDir := filepath.Join(dir, "EPUB", "Broken")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir broken EPUB fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "source.epub"), testEPUBWithCorruptLateEntry(t), 0o644); err != nil {
		t.Fatalf("write broken EPUB fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	req := httptest.NewRequest("GET", "/download/2/as/kepub", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("failed conversion status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("failed conversion Content-Disposition = %q, want empty", got)
	}
	if bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
		t.Fatal("failed conversion exposed a partial ZIP response")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "tmp", "conversion"))
	if err != nil {
		t.Fatalf("read conversion temp directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed conversion retained %d temp files", len(entries))
	}
}

func TestConvertedDownloadFilenameUsesBookExtensions(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		target   converter.Target
		want     string
	}{
		{name: "simple", filename: "plain.txt", target: converter.TargetEPUB, want: "plain.epub"},
		{name: "fb2 zip", filename: "book.fb2.zip", target: converter.TargetEPUB, want: "book.epub"},
		{name: "kepub target", filename: "book.epub", target: converter.TargetKEPUB, want: "book.kepub.epub"},
		{name: "kepub source", filename: "book.kepub.epub", target: converter.TargetEPUB, want: "book.epub"},
		{name: "repaired EPUB", filename: "book.epub", target: converter.TargetEPUB, want: "book.repaired.epub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convertedDownloadFilename(tt.filename, tt.target); got != tt.want {
				t.Fatalf("convertedDownloadFilename(%q, %q) = %q; want %q", tt.filename, tt.target, got, tt.want)
			}
		})
	}
}

func TestFileContentDisposition(t *testing.T) {
	tests := []struct {
		name         string
		filename     string
		wantFallback string
		wantEncoded  string
	}{
		{name: "spaces", filename: "A B.epub", wantFallback: "A B.epub", wantEncoded: "A%20B.epub"},
		{name: "cyrillic", filename: "Книга.epub", wantFallback: strings.Repeat("_", len("Книга")) + ".epub", wantEncoded: "%D0%9A%D0%BD%D0%B8%D0%B3%D0%B0.epub"},
		{name: "quote", filename: `say"hi.epub`, wantFallback: "say_hi.epub", wantEncoded: "say%22hi.epub"},
		{name: "backslash", filename: `a\b.epub`, wantFallback: "a_b.epub", wantEncoded: "a%5Cb.epub"},
		{name: "emoji", filename: "book-📚.epub", wantFallback: "book-____.epub", wantEncoded: "book-%F0%9F%93%9A.epub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := `attachment; filename="` + tt.wantFallback + `"; filename*=UTF-8''` + tt.wantEncoded
			if got := fileContentDisposition("attachment", tt.filename); got != want {
				t.Fatalf("fileContentDisposition(%q) = %q; want %q", tt.filename, got, want)
			}
		})
	}
}

func TestReaderRoutesServeReadablePrimaryAssets(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (165, 'PDF Book', 'PDF Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (2, 165, 'PDF/PDF_Book/asset_pdf.pdf', 'asset_pdf.pdf', '.pdf', 'pdf', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (127, 'EPUB Book', 'EPUB Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (3, 127, 'EPUB/EPUB_Book/asset_epub.epub', 'asset_epub.epub', '.epub', 'epub', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (4, 127, 'EPUB/EPUB_Book/asset_epub_alt.fb2', 'asset_epub_alt.fb2', '.fb2', 'fb2', 0, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (153, 'Missing EPUB', 'Missing EPUB');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (5, 153, 'EPUB/Missing/asset_missing.epub', 'asset_missing.epub', '.epub', 'epub', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (141, 'KEPUB Book', 'KEPUB Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (6, 141, 'KEPUB/KEPUB_Book/asset_kepub.kepub.epub', 'asset_kepub.kepub.epub', '.kepub.epub', 'kepub', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (154, 'MOBI Book', 'MOBI Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (7, 154, 'MOBI/MOBI_Book/asset_mobi.mobi', 'asset_mobi.mobi', '.mobi', 'mobi', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (130, 'FB2 Book', 'FB2 Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (8, 130, 'FB2/FB2_Book/asset_fb2.fb2', 'asset_fb2.fb2', '.fb2', 'fb2', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (134, 'Zipped FB2 Book', 'Zipped FB2 Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (9, 134, 'FB2Zip/FB2Zip_Book/asset_fb2zip.fb2.zip', 'asset_fb2zip.fb2.zip', '.fb2.zip', 'fb2', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (131, 'Mislabeled Zipped FB2 Book', 'Mislabeled Zipped FB2 Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (10, 131, 'FB2Mislabeled/FB2Mislabeled_Book/asset_fb2_mislabeled.fb2', 'asset_fb2_mislabeled.fb2', '.fb2', 'fb2', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (133, 'Gzipped FB2 Book', 'Gzipped FB2 Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (11, 133, 'FB2Gzip/FB2Gzip_Book/asset_fb2gz.fb2.gz', 'asset_fb2gz.fb2.gz', '.fb2.gz', 'fb2', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (119, 'CBZ Book', 'CBZ Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (12, 119, 'CBZ/CBZ_Book/asset_cbz.cbz', 'asset_cbz.cbz', '.cbz', 'cbz', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (117, 'CBR Book', 'CBR Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (13, 117, 'CBR/CBR_Book/asset_cbr.cbr', 'asset_cbr.cbr', '.cbr', 'cbr', 1, 1, randomblob(32), randomblob(32));
		INSERT INTO books (id, title, sort_title) VALUES (116, 'CB7 Book', 'CB7 Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (14, 116, 'CB7/CB7_Book/asset_cb7.cb7', 'asset_cb7.cb7', '.cb7', 'cb7', 1, 1, randomblob(32), randomblob(32));
		`)
	if err != nil {
		t.Fatalf("insert reader fixtures: %v", err)
	}
	mustExec(t, database, "UPDATE assets SET current_sha256 = ? WHERE id IN (3, 5, 13, 14)", testReaderCurrentSHA256)

	fileDir := filepath.Join(dir, "PDF", "PDF_Book")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir pdf fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "asset_pdf.pdf"), []byte("%PDF-1.7\n"), 0o644); err != nil {
		t.Fatalf("write pdf fixture: %v", err)
	}
	epubDir := filepath.Join(dir, "EPUB", "EPUB_Book")
	if err := os.MkdirAll(epubDir, 0o755); err != nil {
		t.Fatalf("mkdir epub fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(epubDir, "asset_epub.epub"), []byte("epub"), 0o644); err != nil {
		t.Fatalf("write epub fixture: %v", err)
	}
	kepubDir := filepath.Join(dir, "KEPUB", "KEPUB_Book")
	if err := os.MkdirAll(kepubDir, 0o755); err != nil {
		t.Fatalf("mkdir kepub fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kepubDir, "asset_kepub.kepub.epub"), []byte("kepub"), 0o644); err != nil {
		t.Fatalf("write kepub fixture: %v", err)
	}
	mobiDir := filepath.Join(dir, "MOBI", "MOBI_Book")
	if err := os.MkdirAll(mobiDir, 0o755); err != nil {
		t.Fatalf("mkdir mobi fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mobiDir, "asset_mobi.mobi"), testfixture.MinimalMOBI(), 0o644); err != nil {
		t.Fatalf("write mobi fixture: %v", err)
	}
	fb2Dir := filepath.Join(dir, "FB2", "FB2_Book")
	if err := os.MkdirAll(fb2Dir, 0o755); err != nil {
		t.Fatalf("mkdir fb2 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fb2Dir, "asset_fb2.fb2"), []byte("<FictionBook/>"), 0o644); err != nil {
		t.Fatalf("write fb2 fixture: %v", err)
	}
	fb2ZipDir := filepath.Join(dir, "FB2Zip", "FB2Zip_Book")
	if err := os.MkdirAll(fb2ZipDir, 0o755); err != nil {
		t.Fatalf("mkdir zipped fb2 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fb2ZipDir, "asset_fb2zip.fb2.zip"), testFB2ZipUpload(t), 0o644); err != nil {
		t.Fatalf("write zipped fb2 fixture: %v", err)
	}
	fb2MislabeledDir := filepath.Join(dir, "FB2Mislabeled", "FB2Mislabeled_Book")
	if err := os.MkdirAll(fb2MislabeledDir, 0o755); err != nil {
		t.Fatalf("mkdir mislabeled zipped fb2 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fb2MislabeledDir, "asset_fb2_mislabeled.fb2"), testFB2ZipUpload(t), 0o644); err != nil {
		t.Fatalf("write mislabeled zipped fb2 fixture: %v", err)
	}
	fb2GzipDir := filepath.Join(dir, "FB2Gzip", "FB2Gzip_Book")
	if err := os.MkdirAll(fb2GzipDir, 0o755); err != nil {
		t.Fatalf("mkdir gzipped fb2 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fb2GzipDir, "asset_fb2gz.fb2.gz"), testFB2Gzip(t, []byte("<FictionBook/>")), 0o644); err != nil {
		t.Fatalf("write gzipped fb2 fixture: %v", err)
	}
	cbzDir := filepath.Join(dir, "CBZ", "CBZ_Book")
	if err := os.MkdirAll(cbzDir, 0o755); err != nil {
		t.Fatalf("mkdir cbz fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cbzDir, "asset_cbz.cbz"), testZip(t, map[string]string{
		"page001.png": string(testTinyPNG()),
	}), 0o644); err != nil {
		t.Fatalf("write cbz fixture: %v", err)
	}
	cbrDir := filepath.Join(dir, "CBR", "CBR_Book")
	if err := os.MkdirAll(cbrDir, 0o755); err != nil {
		t.Fatalf("mkdir cbr fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cbrDir, "asset_cbr.cbr"), testfixture.CBR3(), 0o644); err != nil {
		t.Fatalf("write cbr fixture: %v", err)
	}
	cb7Dir := filepath.Join(dir, "CB7", "CB7_Book")
	if err := os.MkdirAll(cb7Dir, 0o755); err != nil {
		t.Fatalf("mkdir cb7 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cb7Dir, "asset_cb7.cb7"), testfixture.CB7(), 0o644); err != nil {
		t.Fatalf("write cb7 fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := httptest.NewRequest("GET", "/read/165", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/2?v=`) || !strings.Contains(body, `reader-pdf-stage`) || !strings.Contains(body, `data-pdf-page-input`) || !strings.Contains(body, `/static/pdf-reader.js`) {
		t.Fatalf("read page did not render the PDF.js reader shell: %s", body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "object-src 'none'") {
		t.Fatalf("reader Content-Security-Policy = %q", csp)
	}

	req = httptest.NewRequest("GET", "/read/127", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("epub read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	convertedVersion := conversionCacheVersion(testReaderCurrentSHA256)
	if convertedVersion == "" {
		t.Fatal("test build has no Polka version for converted asset cache key")
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/3?v=0123456789abcdef"`) || !strings.Contains(body, `data-reader-fallback-url="/download/3/as/kepub?v=`+convertedVersion+`"`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `/static/reader.js`) {
		t.Fatalf("read page did not render EPUB reader shell: %s", body)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("reader page Cache-Control = %q, want private revalidation", got)
	}

	req = httptest.NewRequest("GET", "/read/assets/3?v=0123456789abcdef", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("versioned EPUB asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("versioned EPUB Cache-Control = %q, want immutable private cache", got)
	}

	req = httptest.NewRequest("GET", "/read/assets/3?v=stale", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if got := w.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("stale EPUB Cache-Control = %q, want private revalidation", got)
	}

	req = httptest.NewRequest("GET", "/read/assets/5?v=0123456789abcdef", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing versioned EPUB status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if got := w.Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
		t.Fatalf("missing versioned EPUB Cache-Control = %q, must not cache an error as immutable", got)
	}

	req = httptest.NewRequest("GET", "/read/asset/4", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("asset-specific read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/4?v=`) || !strings.Contains(body, `data-reader-format="fb2"`) || !strings.Contains(body, `href="/book/127"`) {
		t.Fatalf("asset-specific page did not retain the requested non-primary asset: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/141", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("kepub read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/6?v=`) || strings.Contains(body, `data-reader-fallback-url=`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `data-reader-format="kepub"`) {
		t.Fatalf("read page did not render KEPUB reader shell: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/6", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read kepub asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/epub+zip" {
		t.Fatalf("KEPUB Content-Type = %q, want application/epub+zip", got)
	}

	req = httptest.NewRequest("GET", "/read/154", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("mobi read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/7?v=`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `data-reader-format="mobi"`) {
		t.Fatalf("read page did not render MOBI reader shell: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/7", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read mobi asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/x-mobipocket-ebook" {
		t.Fatalf("MOBI Content-Type = %q, want application/x-mobipocket-ebook", got)
	}

	req = httptest.NewRequest("GET", "/read/130", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("fb2 read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	// FB2 shares the foliate reader shell with EPUB; data-reader-format retains
	// the transport/container behavior within that already-selected engine.
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/8?v=`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `data-reader-format="fb2"`) {
		t.Fatalf("read page did not render FB2 reader shell: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/8", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read fb2 asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/x-fictionbook+xml" {
		t.Fatalf("FB2 Content-Type = %q, want application/x-fictionbook+xml", got)
	}

	req = httptest.NewRequest("GET", "/read/134", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("zipped fb2 read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/9?v=`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `data-reader-format="fb2"`) {
		t.Fatalf("read page did not render zipped FB2 reader shell: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/9", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read zipped fb2 asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/x-fictionbook+xml" {
		t.Fatalf("zipped FB2 Content-Type = %q, want application/x-fictionbook+xml", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `inline; filename="asset_fb2zip.fb2"; filename*=UTF-8''asset_fb2zip.fb2` {
		t.Fatalf("zipped FB2 Content-Disposition = %q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "<FictionBook") {
		t.Fatalf("zipped FB2 response did not stream inner book: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/10", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read mislabeled zipped fb2 asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/x-fictionbook+xml" {
		t.Fatalf("mislabeled zipped FB2 Content-Type = %q, want application/x-fictionbook+xml", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `inline; filename="asset_fb2_mislabeled.fb2"; filename*=UTF-8''asset_fb2_mislabeled.fb2` {
		t.Fatalf("mislabeled zipped FB2 Content-Disposition = %q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "<FictionBook") {
		t.Fatalf("mislabeled zipped FB2 response did not stream inner book: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/133", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("gzipped fb2 read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/11?v=`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `data-reader-format="fb2"`) {
		t.Fatalf("read page did not render gzipped FB2 reader shell: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/11", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read gzipped fb2 asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/x-fictionbook+xml" {
		t.Fatalf("gzipped FB2 Content-Type = %q, want application/x-fictionbook+xml", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `inline; filename="asset_fb2gz.fb2"; filename*=UTF-8''asset_fb2gz.fb2` {
		t.Fatalf("gzipped FB2 Content-Disposition = %q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "<FictionBook") {
		t.Fatalf("gzipped FB2 response did not stream inner book: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/119", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("cbz read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/12?v=`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `data-reader-format="cbz"`) {
		t.Fatalf("read page did not render CBZ reader shell: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/12", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read cbz asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/vnd.comicbook+zip" {
		t.Fatalf("CBZ Content-Type = %q, want application/vnd.comicbook+zip", got)
	}

	req = httptest.NewRequest("GET", "/read/117", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("cbr read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/13?v=`+convertedVersion+`"`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `data-reader-format="cbz"`) {
		t.Fatalf("read page did not render normalized CBR reader shell: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/13?v="+convertedVersion, nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "/download/13/as/cbz?v="+convertedVersion {
		t.Fatalf("CBR read asset status/location = %d/%q; want 307 conversion redirect", w.Code, w.Header().Get("Location"))
	}
	if got := w.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("CBR redirect Cache-Control = %q, want immutable private cache", got)
	}

	req = httptest.NewRequest("GET", "/read/116", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cb7 read page status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, `data-reader-url="/read/assets/14?v=`+convertedVersion+`"`) || !strings.Contains(body, `reader-epub-stage`) || !strings.Contains(body, `data-reader-format="cbz"`) {
		t.Fatalf("read page did not render normalized CB7 reader shell: %s", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/14?v="+convertedVersion, nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "/download/14/as/cbz?v="+convertedVersion {
		t.Fatalf("CB7 read asset status/location = %d/%q; want 307 conversion redirect", w.Code, w.Header().Get("Location"))
	}

	req = httptest.NewRequest("GET", "/read/assets/2", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("read asset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); got != `inline; filename="asset_pdf.pdf"; filename*=UTF-8''asset_pdf.pdf` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", got)
	}

	req = httptest.NewRequest("GET", "/read/assets/2", nil)
	req.Header.Set("Range", "bytes=0-3")
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusPartialContent {
		t.Fatalf("read asset range status = %d, want %d; body: %s", w.Code, http.StatusPartialContent, w.Body.String())
	}
	if got := w.Header().Get("Content-Range"); !strings.HasPrefix(got, "bytes 0-3/") {
		t.Fatalf("Content-Range = %q, want bytes 0-3/*", got)
	}
	if got := w.Body.String(); got != "%PDF" {
		t.Fatalf("range body = %q, want %%PDF", got)
	}
}

func TestReaderRoutesRequireReadableStoredFormat(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (172, 'Stale Reader', 'Stale Reader');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256)
			VALUES (2, 172, 'Stale/Stale_Reader/asset_stale_reader.chm', 'asset_stale_reader.chm', '.chm', 'chm', 1, 1, randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert stale reader fixture: %v", err)
	}

	fileDir := filepath.Join(dir, "Stale", "Stale_Reader")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir stale reader fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "asset_stale_reader.chm"), []byte("ITSF\x03\x00\x00\x00"), 0o644); err != nil {
		t.Fatalf("write stale reader fixture: %v", err)
	}

	user := mustUser(t, database, "stale-reader", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	req := httptest.NewRequest("GET", "/read/172", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("stale read page status = %d, want %d; body: %s", w.Code, http.StatusUnprocessableEntity, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "Primary asset is not readable") {
		t.Fatalf("stale read page body = %q, want not-readable explanation", body)
	}

	req = httptest.NewRequest("GET", "/read/assets/2", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("stale read asset status = %d, want %d; body: %s", w.Code, http.StatusUnprocessableEntity, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "Asset is not readable") {
		t.Fatalf("stale read asset body = %q, want not-readable explanation", body)
	}
}

func TestReadZippedFB2AssetRejectsAmbiguousArchive(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_, err := database.Write(t.Context()).Exec(`
		INSERT INTO books (id, title, sort_title) VALUES (156, 'Ambiguous FB2', 'Ambiguous FB2');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, original_sha256, current_sha256) VALUES (2, 156, 'FB2/Multi/ambiguous.fb2.zip', 'ambiguous.fb2.zip', '.fb2.zip', 'fb2', 1, randomblob(32), randomblob(32));
	`)
	if err != nil {
		t.Fatalf("insert ambiguous fb2 fixture: %v", err)
	}

	fileDir := filepath.Join(dir, "FB2", "Multi")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatalf("mkdir ambiguous fb2 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "ambiguous.fb2.zip"), testZip(t, map[string]string{
		"one.fb2": "<FictionBook><body>one</body></FictionBook>",
		"two.fb2": "<FictionBook><body>two</body></FictionBook>",
	}), 0o644); err != nil {
		t.Fatalf("write ambiguous fb2 fixture: %v", err)
	}

	user := mustUser(t, database, "reader", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	book, err := s.bookDetailDTO(t.Context(), db.FullVisibilityScope(), user.ID, 156, false)
	if err != nil {
		t.Fatalf("bookDetailDTO ambiguous fb2: %v", err)
	}
	if len(book.Assets) != 1 || book.Assets[0].CanRead {
		t.Fatalf("ambiguous zipped FB2 can_read = %+v; want one unreadable asset", book.Assets)
	}

	req := httptest.NewRequest("GET", "/read/156", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ambiguous zipped FB2 read page status = %d, want %d; body: %s", w.Code, http.StatusUnprocessableEntity, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/read/assets/2", nil)
	addSessionCookie(t, s, req, user.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ambiguous zipped FB2 status = %d, want %d; body: %s", w.Code, http.StatusUnprocessableEntity, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "Asset is not readable") {
		t.Fatalf("ambiguous zipped FB2 body = %q, want explanation", body)
	}
}

func testZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func testReadableEPUB(t *testing.T, title, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := w.Write([]byte("application/epub+zip")); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}
	for name, content := range map[string]string{
		"META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + title + `</dc:title><dc:language>en</dc:language></metadata>
  <manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="text"/></spine>
</package>`,
		"OEBPS/text.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>` + title + `</title></head><body><p>` + body + `</p></body></html>`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return buf.Bytes()
}

func testEPUBWithCorruptLateEntry(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entries := []struct {
		name   string
		body   string
		method uint16
	}{
		{"mimetype", "application/epub+zip", zip.Store},
		{"META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, zip.Deflate},
		{"OEBPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="text"/></spine></package>`, zip.Deflate},
		{"OEBPS/text.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>Readable before corruption.</p></body></html>`, zip.Deflate},
		{"OEBPS/corrupt.bin", "late corrupt payload", zip.Store},
	}
	for _, entry := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: entry.name, Method: entry.method})
		if err != nil {
			t.Fatalf("create EPUB entry %s: %v", entry.name, err)
		}
		if _, err := io.WriteString(w, entry.body); err != nil {
			t.Fatalf("write EPUB entry %s: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close corrupt EPUB fixture: %v", err)
	}

	raw := buf.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open corrupt EPUB fixture: %v", err)
	}
	for _, entry := range zr.File {
		if entry.Name != "OEBPS/corrupt.bin" {
			continue
		}
		offset, err := entry.DataOffset()
		if err != nil {
			t.Fatalf("locate corrupt EPUB entry: %v", err)
		}
		raw[offset] ^= 0xff
		return raw
	}
	t.Fatal("corrupt EPUB fixture entry not found")
	return nil
}

func testZipEntry(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", name, err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read zip entry %s: %v", name, err)
		}
		return string(body)
	}
	t.Fatalf("zip entry %s not found", name)
	return ""
}

func testFB2Gzip(t *testing.T, fb2 []byte) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := gzip.NewWriter(buf)
	if _, err := w.Write(fb2); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func testTinyPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x62, 0xf8, 0xcf, 0xc0, 0xf0,
		0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
}
