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
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
	kobowire "github.com/levmv/polka/internal/kobo"
)

func seedKoboWebBook(t *testing.T, database *db.DB, dir string, bookID, assetID int64, title string, paragraphs ...string) {
	t.Helper()
	storagePath := filepath.ToSlash(filepath.Join("Kobo", strconv.FormatInt(bookID, 10), strconv.FormatInt(assetID, 10)+".epub"))
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, language, publisher)
		VALUES (?, ?, ?, 'en', 'Polka Press')
	`, bookID, title, title)
	mustExec(t, database, `
		INSERT INTO assets
		    (id, book_id, storage_path, filename, extension, format, is_primary, current_size, original_hash, current_hash)
		VALUES (?, ?, ?, ?, '.epub', 'epub', 1, 1024, randomblob(16), randomblob(16))
	`, assetID, bookID, storagePath, strconv.FormatInt(assetID, 10)+".epub")

	fullPath := filepath.Join(dir, filepath.FromSlash(storagePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if len(paragraphs) == 0 {
		paragraphs = []string{"Kobo body."}
	}
	if err := os.WriteFile(fullPath, testReadableEPUB(t, title, paragraphs...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKoboSharedReadingPosition(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "kobo-position", db.RoleMember)
	shelf, err := database.CreateShelf(t.Context(), user.ID, db.ShelfPersonal, "Reading", db.ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	title := "Kobo Book"
	seedKoboWebBook(t, database, dir, 143, 2, title, "Alpha beta gamma.", "Second paragraph.")
	if err := database.AddBookToShelf(t.Context(), shelf.ID, user.ID, 143); err != nil {
		t.Fatal(err)
	}
	seedKoboWebBook(t, database, dir, 144, 3, "Unchanged Book")
	if err := database.AddBookToShelf(t.Context(), shelf.ID, user.ID, 144); err != nil {
		t.Fatal(err)
	}
	connection, err := database.CreateKoboConnection(t.Context(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	base := "/kobo/" + connection.Token
	serve := func(method, path, body, cursor string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, base+path, strings.NewReader(body))
		request.Header.Set("X-Kobo-Synctoken", cursor)
		request.Header.Set("X-Kobo-DeviceId", "reader")
		request.Header.Set("X-Kobo-DeviceModel", "Kobo")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	pull := func() *kobowire.ReadingState {
		t.Helper()
		var states []*kobowire.ReadingState
		decodeJSON(t, serve(http.MethodGet, "/v1/library/2/state", "", ""), &states)
		if len(states) != 1 {
			t.Fatalf("reading states = %+v", states)
		}
		return states[0]
	}
	syncReading := func(cursor string) (*kobowire.ReadingState, string) {
		t.Helper()
		w := serve(http.MethodGet, "/v1/library/sync", "", cursor)
		var changes []kobowire.SyncItem
		decodeJSON(t, w, &changes)
		if len(changes) != 2 || changes[0].ChangedProductMetadata == nil || changes[1].ChangedReadingState == nil {
			t.Fatalf("book change = %+v", changes)
		}
		metadata := changes[0].ChangedProductMetadata.BookMetadata
		if metadata == nil || metadata.EntitlementID != "2" || metadata.Title != title {
			t.Fatalf("changed metadata = %+v", metadata)
		}
		return changes[1].ChangedReadingState.ReadingState, w.Header().Get("X-Kobo-Synctoken")
	}
	initial := serve(http.MethodGet, "/v1/library/sync", "", "")
	cursor := initial.Header().Get("X-Kobo-Synctoken")
	const upload = `{"ReadingStates":[{
	    "EntitlementId":"2", "LastModified":"2024-01-01T12:00:00Z",
	    "CurrentBookmark":{"LastModified":"2024-01-01T12:00:00Z","ProgressPercent":57,
	        "ContentSourceProgressPercent":0,"Location":{"Source":"OEBPS/text.xhtml","Type":"KoboSpan","Value":"kobo.1.1"}},
	    "StatusInfo":{"LastModified":"2024-01-01T12:00:00Z","Status":"Reading","TimesStartedReading":1},
	    "Statistics":{"SpentReadingMinutes":12,"RemainingTimeMinutes":45}
	}]}`
	var saved kobowire.ReadingUpdateResponse
	decodeJSON(t, serve(http.MethodPut, "/v1/library/2/state", upload, ""), &saved)
	if saved.RequestResult != "Success" || len(saved.UpdateResults) != 1 || saved.UpdateResults[0].StatisticsResult.Result != "Ignored" {
		t.Fatalf("save result = %+v", saved)
	}
	stored, err := db.GetReaderState(database.Read(t.Context()), user.ID, 2)
	if err != nil || stored.Revision != 1 || !stored.Locator.IsZero() || stored.Progress != .57 {
		t.Fatalf("native upload = %+v, %v", stored, err)
	}
	native := pull()
	if native.CurrentBookmark.Location.Value != "kobo.1.1" || *native.CurrentBookmark.ProgressPercent != 57 {
		t.Fatalf("native round trip = %+v; progress = %v", native, *native.CurrentBookmark.ProgressPercent)
	}
	progress, err := db.GetReaderProgress(database.Read(t.Context()), user.ID, 2)
	if err != nil || progress.Progress == nil || *progress.Progress != .57 {
		t.Fatalf("progress card = %+v, %v", progress, err)
	}
	first, cursor := syncReading(cursor)
	if !reflect.DeepEqual(first, native) {
		t.Fatalf("feed and native pull disagree: %+v, %+v", first, native)
	}
	web, err := s.readerPosition(t.Context(), user.ID, 2)
	if err != nil || web.Locator.CFI != "epubcfi(/6/2[main]!/4/2/1:0)" || web.Revision != stored.Revision {
		t.Fatalf("Kobo to web = %+v, %v", web, err)
	}
	ko, err := s.koReaderState(t.Context(), user.ID, 2, "")
	if err != nil || ko.Position != "/body[1]/DocFragment[1]/body[1]/p[1]/text()[1].0" {
		t.Fatalf("Kobo to KOReader = %+v, %v", ko, err)
	}
	const preciseCFI = "epubcfi(/6/2[main]!/4/4,/1:7,/1:12)"
	web, _, err = database.SaveReaderState(t.Context(), user.ID, 2, db.ReaderPositionWrite{
		Revision: web.Revision, Progress: .60125, Locator: db.Locator{CFI: preciseCFI}, DeviceID: "urn:test:web", DeviceName: "Browser",
	}, db.ReadingStatusSourceWebReader)
	if err != nil {
		t.Fatal(err)
	}
	converted, cursor := syncReading(cursor)
	if converted.CurrentBookmark.Location.Value != "kobo.2.1" || *converted.CurrentBookmark.ProgressPercent != 60.125 {
		t.Fatalf("web to Kobo = %+v", converted.CurrentBookmark)
	}
	converted.CurrentBookmark.ProgressPercent = new(64.0) // Device pagination differs.
	encoded, err := json.Marshal(kobowire.ReadingStateUpdate{ReadingStates: []kobowire.ReadingState{*converted}})
	if err != nil {
		t.Fatal(err)
	}
	serve(http.MethodPut, "/v1/library/2/state", string(encoded), "")
	state, err := db.GetReaderState(database.Read(t.Context()), user.ID, 2)
	if err != nil || state.Revision != web.Revision || state.Locator.CFI != preciseCFI || state.Progress != .60125 {
		t.Fatalf("Kobo echo replaced precise position: %+v, %v", state, err)
	}
	if w := serve(http.MethodGet, "/v1/library/sync", "", cursor); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("echo or conversion created another change: %s", w.Body.String())
	}
	beforeEdit := pull()
	title = "Revised Kobo Book"
	mustExec(t, database, "UPDATE books SET title = ? WHERE id = 143", title)
	afterEdit, cursor := syncReading(cursor)
	if !reflect.DeepEqual(afterEdit, beforeEdit) {
		t.Fatalf("metadata edit changed reading state: %+v; want %+v", afterEdit, beforeEdit)
	}
	if _, err := database.SetReadingStatus(t.Context(), user.ID, 143, db.ReadingStatusFinished, db.ReadingStatusSourceManual); err != nil {
		t.Fatal(err)
	}
	finished, cursor := syncReading(cursor)
	if finished.StatusInfo.Status != "Finished" {
		t.Fatalf("status-only change = %+v", finished.StatusInfo)
	}
	if err := database.ResetReaderState(t.Context(), user.ID, 2); err != nil {
		t.Fatal(err)
	}
	reset, _ := syncReading(cursor)
	if *reset.CurrentBookmark.ProgressPercent != 0 || reset.CurrentBookmark.Location != nil || reset.LastModified.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("reset = %+v", reset)
	}
	// Preserve an unfamiliar native address even when other readers can only
	// use its percentage. An omitted chapter percentage must stay omitted.
	unknown := kobowire.ReadingState{EntitlementID: "2", CurrentBookmark: &kobowire.Bookmark{
		LastModified: time.Now().UTC(), ProgressPercent: new(31.0),
		Location: &kobowire.Location{Source: "OEBPS/text.xhtml", Type: "OtherLocation", Value: "native-address"},
	}}
	encoded, err = json.Marshal(kobowire.ReadingStateUpdate{ReadingStates: []kobowire.ReadingState{unknown}})
	if err != nil {
		t.Fatal(err)
	}
	serve(http.MethodPut, "/v1/library/2/state", string(encoded), "")
	got := pull().CurrentBookmark
	if !reflect.DeepEqual(got.Location, unknown.CurrentBookmark.Location) || got.ContentSourceProgressPercent != nil {
		t.Fatalf("unknown bookmark changed: %+v", got)
	}
	web, err = s.readerPosition(t.Context(), user.ID, 2)
	if err != nil || web.Progress != .31 || !web.Locator.IsZero() {
		t.Fatalf("unknown location fallback = %+v, %v", web, err)
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
	connection, err := database.CreateKoboConnection(context.Background(), user.ID, shelf.ID)
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
	cursor := w.Header().Get("X-Kobo-Synctoken")
	if cursor == "" {
		t.Fatal("missing sync token")
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
	req.Header.Set("X-Kobo-Synctoken", cursor)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("acknowledged sync = %d %s", w.Code, w.Body.String())
	}
	for _, token := range []string{"e30=.e30=", cursor + "000"} {
		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, basePath+"/v1/library/sync", nil)
		req.Header.Set("X-Kobo-Synctoken", token)
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Header().Get("X-Kobo-Synctoken") != cursor {
			t.Fatalf("recover token %q = %d %s", token, w.Code, w.Body.String())
		}
		decodeJSON(t, w, &items)
		if len(items) != 1 || items[0]["NewEntitlement"] == nil {
			t.Fatalf("recover token %q: want new entitlement, got %+v", token, items)
		}
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, basePath+"/v1/library/2/metadata", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Title":"Kobo Book"`) {
		t.Fatalf("metadata = %d %s", w.Code, w.Body.String())
	}
	var metadata []kobowire.Metadata
	decodeJSON(t, w, &metadata)
	coverID := metadata[0].CoverImageID

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, basePath+"/v1/library/3/metadata", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("outside metadata status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, basePath+"/"+coverID+"/300/450/false/image.jpg", nil))
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
	replacement, err := database.CreateKoboConnection(t.Context(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/kobo/"+replacement.Token+"/v1/library/sync", nil)
	req.Header.Set("X-Kobo-Synctoken", cursor)
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("replacement connection sync = %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w, &items)
	if len(items) != 1 || items[0]["NewEntitlement"] == nil || w.Header().Get("X-Kobo-Synctoken") == cursor {
		t.Fatalf("old cursor acknowledged replacement connection: %+v", items)
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
			Revision:      int64(10 * (i + 1)),
			FirstRevision: 1,
			Present:       true,
		}
	}
	currentRevision := changes[len(changes)-1].Revision
	for after, seen := int64(1), 0; seen < len(changes); {
		remaining := changes[seen:]
		body, cursor, more, err := marshalKoboSyncPage(remaining, after, "https://books.test/kobo/token", currentRevision, false,
			func(change db.KoboChange) (*kobowire.ReadingState, error) {
				return &kobowire.ReadingState{EntitlementID: strconv.FormatInt(change.AssetID, 10)}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > maxKoboSyncResponseBytes {
			t.Fatalf("page is %d bytes", len(body))
		}
		var items []kobowire.SyncItem
		if err := json.Unmarshal(body, &items); err != nil {
			t.Fatal(err)
		}
		count := len(items) / 2
		if count == 0 || count > len(remaining) || len(items)%2 != 0 {
			t.Fatalf("page after %d has %d events for %d remaining books", after, len(items), len(remaining))
		}
		if seen == 0 && count == len(changes) {
			t.Fatal("fixture must require multiple pages")
		}
		for i := 0; i < len(items); i += 2 {
			publication, reading := items[i].ChangedProductMetadata, items[i+1].ChangedReadingState
			id := strconv.FormatInt(remaining[i/2].AssetID, 10)
			if publication == nil || reading == nil || reading.ReadingState == nil ||
				publication.BookEntitlement.ID != id || reading.ReadingState.EntitlementID != id {
				t.Fatalf("expected paired events for book %s: %+v", id, items[i:i+2])
			}
		}
		if cursor != remaining[count-1].Revision || more != (count < len(remaining)) {
			t.Fatalf("page after %d: cursor=%d more=%v, sent=%d remaining=%d", after, cursor, more, count, len(remaining))
		}
		after, seen = cursor, seen+count
	}
}

func TestKoboContentRequiresCurrentUserScope(t *testing.T) {
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
	connection, err := database.CreateKoboConnection(context.Background(), reader.ID, shelf.ID)
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
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/library/2/metadata"},
		{http.MethodGet, "/v1/library/2/state"},
		{http.MethodPut, "/v1/library/2/state"},
		{http.MethodGet, "/2-1/300/450/false/image.jpg"},
		{http.MethodGet, "/download/2/kepub"},
	} {
		w = httptest.NewRecorder()
		path := "/kobo/" + connection.Token + route.path
		handler.ServeHTTP(w, httptest.NewRequest(route.method, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s after scope removal = %d %s; want 404", route.method, route.path, w.Code, w.Body.String())
		}
	}
}
