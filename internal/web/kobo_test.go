package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func seedKoboWebBook(t *testing.T, database *db.DB, dir string, bookID, assetID int64, title string) {
	t.Helper()
	storagePath := filepath.ToSlash(filepath.Join("Kobo", strconv.FormatInt(bookID, 10), strconv.FormatInt(assetID, 10)+".epub"))
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, language, publisher)
		VALUES (?, ?, ?, 'en', 'Polka Press')
	`, bookID, title, title)
	mustExec(t, database, `
		INSERT INTO assets
		    (id, book_id, storage_path, filename, extension, format, is_primary, current_size, original_sha256, current_sha256)
		VALUES (?, ?, ?, ?, '.epub', 'epub', 1, 1024, randomblob(32), randomblob(32))
	`, assetID, bookID, storagePath, strconv.FormatInt(assetID, 10)+".epub")

	fullPath := filepath.Join(dir, filepath.FromSlash(storagePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, testReadableEPUB(t, title, "Kobo body."), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKoboNativeLibraryRoutesAndRevocation(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "native-kobo", db.RoleMember)
	shelf, err := database.CreateShelf(t.Context(), user.ID, db.ShelfPersonal, "On Kobo", db.ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	seedKoboWebBook(t, database, dir, 143, 2, "Kobo Book")
	seedKoboWebBook(t, database, dir, 162, 3, "Outside")
	if err := database.AddBookToShelf(t.Context(), shelf.ID, user.ID, 143); err != nil {
		t.Fatal(err)
	}
	connection, err := database.ReplaceKoboConnection(context.Background(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	basePath := "/kobo/" + url.PathEscape(connection.Token)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, basePath+"/v1/initialization", nil)
	req.Host = "library.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("initialization status = %d; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Kobo-ApiToken"); got != "e30=" {
		t.Fatalf("api token header = %q", got)
	}
	var initialization struct {
		Resources map[string]any `json:"Resources"`
	}
	if err := json.UnmarshalRead(w.Body, &initialization); err != nil {
		t.Fatal(err)
	}
	if got := initialization.Resources["library_sync"]; got != "https://library.example"+basePath+"/v1/library/sync" {
		t.Fatalf("library_sync = %v", got)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		basePath+"/v1/auth/device",
		strings.NewReader(`{"UserKey":"device-user"}`),
	)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("device auth status = %d; body: %s", w.Code, w.Body.String())
	}
	var deviceAuth map[string]string
	if err := json.UnmarshalRead(w.Body, &deviceAuth); err != nil {
		t.Fatal(err)
	}
	if deviceAuth["UserKey"] != "device-user" || deviceAuth["AccessToken"] == "" || len(deviceAuth["TrackingId"]) != 36 {
		t.Fatalf("device auth = %+v", deviceAuth)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, basePath+"/v1/library/sync", nil)
	req.Host = "library.example"
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sync status = %d; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Kobo-Synctoken"); got != "1" {
		t.Fatalf("sync token = %q", got)
	}
	var items []map[string]jsontext.Value
	if err := json.UnmarshalRead(w.Body, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0]["NewEntitlement"] == nil {
		t.Fatalf("sync items = %+v", items)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, basePath+"/v1/library/sync", nil)
	req.Header.Set("X-Kobo-Synctoken", "1")
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("acknowledged sync = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, basePath+"/v1/library/2/metadata", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Title":"Kobo Book"`) {
		t.Fatalf("metadata = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, basePath+"/v1/library/3/metadata", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("outside metadata status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, basePath+"/2/300/450/false/image.jpg", nil))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/") {
		t.Fatalf("cover = %d %q; body: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, basePath+"/download/2/kepub", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("download = %d; body: %s", w.Code, w.Body.String())
	}
	reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("download is not an EPUB zip: %v", err)
	}
	var convertedXHTML string
	for _, file := range reader.File {
		if file.Name == "OEBPS/text.xhtml" {
			rc, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			convertedXHTML = string(body)
		}
	}
	if !strings.Contains(convertedXHTML, "koboSpan") {
		t.Fatalf("download lacks KEPUB spans: %s", convertedXHTML)
	}

	if err := database.DeleteKoboConnection(t.Context(), user.ID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, basePath+"/v1/library/sync", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d", w.Code)
	}
	if _, err := db.KoboConnectionForUser(database.Read(req.Context()), connection.UserID); !errors.Is(err, db.ErrKoboConnectionNotFound) {
		t.Fatalf("connection remains after revoke: %v", err)
	}
}

func TestKoboPathDoesNotFallBackToBrowserSession(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "cookie-kobo", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	req := httptest.NewRequest(http.MethodGet, "/kobo/not-a-token/v1/library/sync", nil)
	addSessionCookie(t, s, req, user.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("session bypass status = %d", w.Code)
	}
}

func TestKoboSyncPageHonorsByteBoundaryWithoutSkippingCursor(t *testing.T) {
	changes := make([]db.KoboChange, 30)
	for i := range changes {
		changes[i] = db.KoboChange{
			AssetID:       int64(i + 1),
			Title:         "Book",
			Description:   strings.Repeat("large description ", 5000),
			Revision:      int64(i + 1),
			FirstRevision: int64(i + 1),
			Present:       true,
		}
	}
	body, cursor, more, err := marshalKoboSyncPage(changes, 0, "https://books.test/kobo/token", 30, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > maxKoboSyncResponseBytes {
		t.Fatalf("page is %d bytes", len(body))
	}
	if !more || cursor <= 0 || cursor >= 30 {
		t.Fatalf("cursor=%d more=%v", cursor, more)
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	if int64(len(items)) != cursor {
		t.Fatalf("items=%d cursor=%d", len(items), cursor)
	}
}

func TestKoboSyncPageDoesNotContinueAfterReturningEverything(t *testing.T) {
	changes := []db.KoboChange{
		{AssetID: 1, Revision: 1, FirstRevision: 1, Present: true},
		{AssetID: 2, Revision: 2, FirstRevision: 2, Present: true},
	}
	body, cursor, more, err := marshalKoboSyncPage(changes, 0, "https://books.test/kobo/token", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if more || cursor != 2 {
		t.Fatalf("cursor=%d more=%v body=%s", cursor, more, body)
	}
}

func TestKoboSyncPageTreatsUnseenChangedItemAsNew(t *testing.T) {
	changes := []db.KoboChange{{
		AssetID:       1,
		Revision:      4,
		FirstRevision: 2,
		Present:       true,
	}}
	body, _, _, err := marshalKoboSyncPage(changes, 1, "https://books.test/kobo/token", 4, false)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0]["NewEntitlement"] == nil {
		t.Fatalf("unseen changed item = %s; want NewEntitlement", body)
	}
}

func TestKoboMetadataRequiresCurrentUserScope(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	curator := mustUser(t, database, "kobo-scope-curator", db.RoleMember)
	reader := mustUser(t, database, "kobo-scoped-reader", db.RoleReader)
	shelf, err := database.CreateShelf(t.Context(), curator.ID, db.ShelfShared, "Reader Kobo", db.ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	seedKoboWebBook(t, database, dir, 168, 2, "Scoped Kobo")
	if err := database.AddBookToShelf(t.Context(), shelf.ID, curator.ID, 168); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), reader.ID, db.UserAccess{
		Role:         db.RoleReader,
		ContentScope: db.ContentScopeShelves,
		ShelfIDs:     []int64{shelf.ID},
	}); err != nil {
		t.Fatal(err)
	}
	connection, err := database.ReplaceKoboConnection(context.Background(), reader.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.SyncKoboConnection(context.Background(), connection.ID, 0, db.KoboSyncPageLimit); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	path := "/kobo/" + url.PathEscape(connection.Token) + "/v1/library/2/metadata"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("metadata before scope removal = %d %s", w.Code, w.Body.String())
	}

	if _, err := database.UpdateUserAccess(t.Context(), reader.ID, db.UserAccess{Role: db.RoleReader, ContentScope: db.ContentScopeShelves}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("metadata after scope removal = %d %s; want 404", w.Code, w.Body.String())
	}
}
