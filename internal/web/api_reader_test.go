package web

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIReaderStateLifecycle(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/1/state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("default state status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var state ReaderStateDTO
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode default state: %v", err)
	}
	if state.AssetID != 1 || state.BookID != 1 || state.Progress != 0 || !state.Locator.IsZero() || state.UpdatedAt != 0 {
		t.Fatalf("default state = %+v", state)
	}
	if state.ReadingStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("default reading status = %+v", state.ReadingStatus)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/reader/assets/1/touch", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("touch status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	state = ReaderStateDTO{}
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode touched state: %v", err)
	}
	if state.UpdatedAt == 0 || state.Revision != 0 || state.Progress != 0 || !state.Locator.IsZero() {
		t.Fatalf("touched state = %+v", state)
	}
	if !state.StatusChanged || state.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("touch reading status = %+v", state)
	}

	progress := 0.5
	locator := &db.Locator{CFI: "epubcfi(/6/4)"}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/reader/assets/1/state", readerStateRequest{
		Revision: new(int64(0)),
		DeviceID: "urn:test:reader", DeviceName: "Test reader",
		Progress: &progress,
		Locator:  locator,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("save status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	state = ReaderStateDTO{}
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode saved state: %v", err)
	}
	if state.Progress != progress || !state.Locator.Equal(*locator) || state.UpdatedAt == 0 {
		t.Fatalf("saved state = %+v", state)
	}
	if state.StatusChanged || state.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("saved reading status = %+v", state)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/reader/assets/1/state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("other-user state status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	state = ReaderStateDTO{}
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode other-user state: %v", err)
	}
	if state.Progress != 0 || !state.Locator.IsZero() || state.UpdatedAt != 0 {
		t.Fatalf("reader state leaked across users: %+v", state)
	}
	if state.ReadingStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("reading status leaked across users: %+v", state)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodDelete, "/api/reader/assets/1/state", map[string]int64{"revision": 1}))
	if w.Code != http.StatusNoContent {
		t.Fatalf("reset status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/1/state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("state after reset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	state = ReaderStateDTO{}
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode state after reset: %v", err)
	}
	if state.Progress != 0 || !state.Locator.IsZero() || state.Revision != 2 || state.UpdatedAt != 0 {
		t.Fatalf("state after reset = %+v", state)
	}
	if state.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("reset position changed status: %+v", state.ReadingStatus)
	}
}

func TestAPIReaderAutoFinishCanBeUndone(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "reader", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/reader/assets/1/touch", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("touch status = %d: %s", w.Code, w.Body.String())
	}
	progress := 0.995
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/1/state", readerStateRequest{
		Revision: new(int64(0)),
		DeviceID: "urn:test:reader", DeviceName: "Test reader",
		Progress: &progress,
		Locator:  &db.Locator{},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("finish status = %d: %s", w.Code, w.Body.String())
	}
	var state ReaderStateDTO
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode finish: %v", err)
	}
	if !state.StatusChanged || state.StatusTransitionID == 0 || state.ReadingStatus.Status != db.ReadingStatusFinished {
		t.Fatalf("finish response = %+v", state)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/books/1/reading-status/undo", readingStatusUndoRequest{EventID: state.StatusTransitionID}))
	if w.Code != http.StatusOK {
		t.Fatalf("undo status = %d: %s", w.Code, w.Body.String())
	}
	var restored ReadingStatusDTO
	if err := json.UnmarshalRead(w.Body, &restored); err != nil {
		t.Fatalf("decode undo: %v", err)
	}
	if restored.Status != db.ReadingStatusReading {
		t.Fatalf("restored status = %+v", restored)
	}

	readerState, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil || readerState.Progress != progress {
		t.Fatalf("undo changed reader position = %+v, err %v", readerState, err)
	}
}

func TestAPIReaderStateErrors(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "reader", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	progress := 1.5
	locator := &db.Locator{}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/1/state", readerStateRequest{
		Revision: new(int64(0)),
		DeviceID: "urn:test:reader", DeviceName: "Test reader",
		Progress: &progress,
		Locator:  locator,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid progress status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	progress = 0.5
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/1/state", readerStateRequest{
		Revision: new(int64(0)),
		DeviceID: "urn:test:reader", DeviceName: "Test reader",
		Progress: &progress,
		Locator:  &db.Locator{Page: -1},
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid locator status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/reader/assets/999/touch", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodDelete, "/api/reader/assets/999/state", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("reset missing asset status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/assets/1/state", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth state status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAPIAnnotationsLifecycle(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	listFor := func(userID int64) []AnnotationDTO {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, userID, http.MethodGet, "/api/reader/assets/1/annotations", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("list status = %d; body: %s", w.Code, w.Body.String())
		}
		var list []AnnotationDTO
		if err := json.UnmarshalRead(w.Body, &list); err != nil {
			t.Fatalf("decode annotations: %v", err)
		}
		return list
	}

	req := annotationRequest{
		Locator:       db.Locator{CFI: "epubcfi(/6/2!/4/2)", Path: "OPS/chapter.xhtml"},
		Quote:         "highlighted text",
		ContextBefore: "before",
		ContextAfter:  "after",
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/reader/assets/1/annotations", req))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var created AnnotationDTO
	if err := json.UnmarshalRead(w.Body, &created); err != nil {
		t.Fatalf("decode created annotation: %v", err)
	}
	if created.ID <= 0 || created.AssetID != 1 || created.Locator.Path != req.Locator.Path || created.Color != db.AnnotationColorYellow || created.Quote != req.Quote {
		t.Fatalf("created annotation = %+v", created)
	}

	updated := created
	for _, edit := range []struct {
		name        string
		patch       annotationUpdateRequest
		note, color string
	}{
		{"set note and color", annotationUpdateRequest{Note: new("  my note  "), Color: new("blue")}, "  my note  ", "blue"},
		{"color preserves note", annotationUpdateRequest{Color: new("purple")}, "  my note  ", "purple"},
		{"clear note preserves color", annotationUpdateRequest{Note: new("")}, "", "purple"},
	} {
		edit.patch.Revision = updated.Revision
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPatch, "/api/reader/assets/1/annotations/"+strconv.FormatInt(created.ID, 10), edit.patch))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", edit.name, w.Code, w.Body.String())
		}
		updated = AnnotationDTO{}
		if err := json.UnmarshalRead(w.Body, &updated); err != nil {
			t.Fatal(err)
		}
		if updated.Note != edit.note || updated.Color != edit.color || updated.ID != created.ID || updated.Locator.CFI != created.Locator.CFI || updated.Quote != created.Quote || updated.CreatedAt != created.CreatedAt {
			t.Fatalf("%s overwrote annotation content: %+v", edit.name, updated)
		}
	}
	for _, patch := range []annotationUpdateRequest{
		{Note: new("must not overwrite"), Color: new("red")},
		{Note: new(strings.Repeat("я", db.MaxAnnotationNoteLength+1)), Color: new("green")},
		{},
	} {
		patch.Revision = updated.Revision
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPatch, "/api/reader/assets/1/annotations/"+strconv.FormatInt(created.ID, 10), patch))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid update: %d %s", w.Code, w.Body.String())
		}
		if list := listFor(alice.ID); len(list) != 1 || !reflect.DeepEqual(list[0], updated) {
			t.Fatalf("invalid update overwrote annotation: %+v", list)
		}
	}

	if list := listFor(bob.ID); len(list) != 0 {
		t.Fatalf("annotation leaked to bob: %+v", list)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodPatch, "/api/reader/assets/1/annotations/"+strconv.FormatInt(created.ID, 10), annotationUpdateRequest{Revision: updated.Revision, Note: new("stolen")}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob update status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodDelete, "/api/reader/assets/1/annotations/"+strconv.FormatInt(created.ID, 10), map[string]any{"revision": updated.Revision}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob delete status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if list := listFor(alice.ID); len(list) != 1 || !reflect.DeepEqual(list[0], updated) {
		t.Fatalf("another user changed the annotation: %+v; want %+v", list, updated)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodDelete, "/api/reader/assets/1/annotations/"+strconv.FormatInt(created.ID, 10), map[string]any{"revision": updated.Revision}))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if list := listFor(alice.ID); len(list) != 0 {
		t.Fatalf("annotations after delete = %+v; want empty", list)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/reader/assets/1/annotations", annotationRequest{Quote: "x"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid create status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPIBookAnnotationsIncludeAllFilesAndRespectAccess(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	alice := mustUser(t, database, "alice-book-notes", db.RoleReader)
	bob := mustUser(t, database, "bob-book-notes", db.RoleReader)
	mustExec(t, database, `
        INSERT INTO assets (id, book_id, storage_path, filename, original_filename, extension, original_sha256, current_sha256)
        VALUES (2, 1, 'second.fb2', 'second.fb2', 'second.fb2', '.fb2', randomblob(32), randomblob(32)),
               (3, 2, 'other.epub', 'other.epub', 'other.epub', '.epub', randomblob(32), randomblob(32));
    `)
	for _, fixture := range []struct {
		userID, assetID int64
		quote           string
	}{
		{alice.ID, 1, "First private quote"},
		{alice.ID, 2, "Second private quote"},
		{alice.ID, 3, "Another book quote"},
		{bob.ID, 1, "Another user quote"},
	} {
		if _, err := database.CreateAnnotation(t.Context(), fixture.userID, fixture.assetID, db.AnnotationCreate{
			Locator: db.Locator{CFI: "epubcfi(/6/2)"}, Quote: fixture.quote, Note: "Personal note", Color: "blue",
		}); err != nil {
			t.Fatal(err)
		}
	}
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	paths := []string{
		"/api/books/1/annotations",
		"/api/books/1/annotations/export",
		"/api/books/1/annotations/export?format=markdown",
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		body := w.Body.String()
		for _, quote := range []string{"First private quote", "Second private quote"} {
			if !strings.Contains(body, quote) {
				t.Errorf("%s missing %q", path, quote)
			}
		}
		for _, quote := range []string{"Another book quote", "Another user quote"} {
			if strings.Contains(body, quote) {
				t.Errorf("%s leaked %q", path, quote)
			}
		}
		if strings.Contains(path, "/export") && !strings.Contains(body, "second.fb2") {
			t.Errorf("%s lost the source file name", path)
		}
	}
	if _, err := database.UpdateUserAccess(t.Context(), alice.ID, db.UserAccess{
		Role: db.RoleReader, ContentScope: db.ContentScopeShelves,
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s after access revoked: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestAPIContinueReading(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "reader", db.RoleMember)
	mustExec(t, database, `
			INSERT INTO user_asset_state (user_id, asset_id, progress, locator, updated_at)
			VALUES (?, 1, 0.42, '{"cfi":"epubcfi(/6/2)"}', 100);
			INSERT INTO user_book_reading_state (user_id, book_id, status, updated_at)
			VALUES (?, 1, 'reading', 100)
		`, user.ID, user.ID)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/api/reader/continue", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("continue status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var items []ContinueReadingDTO
	if err := json.UnmarshalRead(w.Body, &items); err != nil {
		t.Fatalf("decode continue items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d continue items, want 1", len(items))
	}
	if items[0].ID != 1 || items[0].AssetID != 1 || items[0].Progress != 0.42 || len(items[0].Assets) != 1 {
		t.Fatalf("continue item = %+v", items[0])
	}

	if _, err := database.SaveUserSettings(t.Context(), user.ID, db.UserSettingsPatch{
		ShowContinueReading: new(false),
	}); err != nil {
		t.Fatalf("hide continue reading: %v", err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/api/reader/continue", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("hidden continue status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	items = nil
	if err := json.UnmarshalRead(w.Body, &items); err != nil {
		t.Fatalf("decode hidden continue items: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d hidden continue items, want 0", len(items))
	}
}
