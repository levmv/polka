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

func TestAPIReaderPositionLifecycle(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	position := func(userID int64) ReaderPositionDTO {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, userID, http.MethodGet, "/api/reader/assets/1/position", nil))
		var got ReaderPositionDTO
		decodeJSON(t, w, &got)
		return got
	}
	progress := func(userID int64) ReaderProgressDTO {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, userID, http.MethodGet, "/api/reader/assets/1/progress", nil))
		var got ReaderProgressDTO
		decodeJSON(t, w, &got)
		return got
	}
	if got := position(alice.ID); got.Revision != 0 || got.Progress != 0 || !got.Locator.IsZero() {
		t.Fatalf("default position = %+v", got)
	}
	if got := progress(alice.ID); got.Progress != nil || got.ReadingStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("unopened book = %+v", got)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/reader/assets/1/touch", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("touch = %d: %s", w.Code, w.Body.String())
	}
	if got := position(alice.ID); got.Revision != 0 || got.Progress != 0 || !got.Locator.IsZero() {
		t.Fatalf("opening created a position: %+v", got)
	}
	if got := progress(alice.ID); got.Progress == nil || *got.Progress != 0 || got.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("opened book should show zero progress: %+v", got)
	}

	percentage := 0.5
	locator := &db.Locator{CFI: "epubcfi(/6/4)"}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/reader/assets/1/position", readerPositionRequest{
		Revision: new(int64(0)),
		DeviceID: "urn:test:reader", DeviceName: "Test reader",
		Progress: &percentage,
		Locator:  locator,
	}))
	var saved ReaderPositionSaveDTO
	decodeJSON(t, w, &saved)
	if saved.Revision != 1 || saved.BookID != 1 || saved.StatusChanged || saved.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("save result = %+v", saved)
	}
	if got := position(alice.ID); got.Revision != saved.Revision || got.Progress != percentage || !got.Locator.Equal(*locator) {
		t.Fatalf("saved position = %+v", got)
	}
	if got := progress(alice.ID); got.Progress == nil || *got.Progress != percentage || got.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("saved progress = %+v", got)
	}
	if got := position(bob.ID); got.Revision != 0 || got.Progress != 0 || !got.Locator.IsZero() {
		t.Fatalf("position leaked across users: %+v", got)
	}
	if got := progress(bob.ID); got.Progress != nil || got.ReadingStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("progress leaked across users: %+v", got)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodDelete, "/api/reader/assets/1/position", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("reset status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if got := position(alice.ID); got.Progress != 0 || !got.Locator.IsZero() || got.Revision != 2 {
		t.Fatalf("reset position = %+v", got)
	}
	if got := progress(alice.ID); got.Progress != nil || got.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("reset progress = %+v", got)
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
	if w.Code != http.StatusNoContent {
		t.Fatalf("touch status = %d: %s", w.Code, w.Body.String())
	}
	progress := 0.995
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/1/position", readerPositionRequest{
		Revision: new(int64(0)),
		DeviceID: "urn:test:reader", DeviceName: "Test reader",
		Progress: &progress,
		Locator:  &db.Locator{},
	}))
	var state ReaderPositionSaveDTO
	decodeJSON(t, w, &state)
	if !state.StatusChanged || state.StatusTransitionID == 0 || state.ReadingStatus.Status != db.ReadingStatusFinished {
		t.Fatalf("finish response = %+v", state)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/books/1/reading-status/undo", readingStatusUndoRequest{EventID: state.StatusTransitionID}))
	var restored ReadingStatusDTO
	decodeJSON(t, w, &restored)
	if restored.Status != db.ReadingStatusReading {
		t.Fatalf("restored status = %+v", restored)
	}

	readerState, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil || readerState.Progress != progress {
		t.Fatalf("undo changed reader position = %+v, err %v", readerState, err)
	}
}

func TestAPIReaderPositionErrors(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "reader", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	progress := 1.5
	locator := &db.Locator{}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/1/position", readerPositionRequest{
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
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/1/position", readerPositionRequest{
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
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodDelete, "/api/reader/assets/999/position", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("reset missing asset status = %d, want %d", w.Code, http.StatusNotFound)
	}

	for _, resource := range []string{"position", "progress"} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/assets/1/"+resource, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauth %s status = %d", resource, w.Code)
		}
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/api/reader/assets/999/"+resource, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("missing asset %s status = %d", resource, w.Code)
		}
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
        INSERT INTO assets (id, book_id, storage_path, filename, original_filename, extension, original_hash, current_hash)
        VALUES (2, 1, 'second.fb2', 'second.fb2', 'second.fb2', '.fb2', randomblob(16), randomblob(16)),
               (3, 2, 'other.epub', 'other.epub', 'other.epub', '.epub', randomblob(16), randomblob(16));
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
	if _, err := database.SaveUserSettings(t.Context(), user.ID, db.UserSettingsPatch{
		ShowContinueReading: new(false),
	}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, database, `
			INSERT INTO reading_positions (user_id, asset_id, progress, locator, updated_at)
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
}
