package web

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
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
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/asset_1/state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("default state status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var state ReaderStateDTO
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode default state: %v", err)
	}
	if state.AssetID != "asset_1" || state.WorkID != "w_1" || state.Progress != 0 || state.Locator.String() != "{}" || state.LastReadAt != 0 {
		t.Fatalf("default state = %+v", state)
	}
	if state.ReadingStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("default reading status = %+v", state.ReadingStatus)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/reader/assets/asset_1/touch", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("touch status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	state = ReaderStateDTO{}
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode touched state: %v", err)
	}
	if state.LastReadAt == 0 || state.Progress != 0 || state.Locator.String() != "{}" {
		t.Fatalf("touched state = %+v", state)
	}
	if !state.StatusChanged || state.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("touch reading status = %+v", state)
	}

	progress := 0.5
	locator := jsontext.Value(`{"engine":"foliate","cfi":"epubcfi(/6/4)","fraction":0.5}`)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/reader/assets/asset_1/state", readerStateRequest{
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
	if state.Progress != progress || state.Locator.String() != `{"cfi":"epubcfi(/6/4)","engine":"foliate","fraction":0.5}` || state.LastReadAt == 0 {
		t.Fatalf("saved state = %+v", state)
	}
	if state.StatusChanged || state.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("saved reading status = %+v", state)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/reader/assets/asset_1/state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("other-user state status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	state = ReaderStateDTO{}
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode other-user state: %v", err)
	}
	if state.Progress != 0 || state.Locator.String() != "{}" || state.LastReadAt != 0 {
		t.Fatalf("reader state leaked across users: %+v", state)
	}
	if state.ReadingStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("reading status leaked across users: %+v", state)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodDelete, "/api/reader/assets/asset_1/state", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("reset status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/asset_1/state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("state after reset status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	state = ReaderStateDTO{}
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode state after reset: %v", err)
	}
	if state.Progress != 0 || state.Locator.String() != "{}" || state.LastReadAt != 0 || state.UpdatedAt != 0 {
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
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/reader/assets/asset_1/touch", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("touch status = %d: %s", w.Code, w.Body.String())
	}
	progress := 0.995
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/asset_1/state", readerStateRequest{
		Progress: &progress,
		Locator:  jsontext.Value(`{"engine":"foliate","fraction":0.995}`),
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("finish status = %d: %s", w.Code, w.Body.String())
	}
	var state ReaderStateDTO
	if err := json.UnmarshalRead(w.Body, &state); err != nil {
		t.Fatalf("decode finish: %v", err)
	}
	if !state.StatusChanged || state.StatusTransitionID == "" || state.ReadingStatus.Status != db.ReadingStatusFinished {
		t.Fatalf("finish response = %+v", state)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/books/w_1/reading-status/undo", readingStatusUndoRequest{EventID: state.StatusTransitionID}))
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

	readerState, err := database.GetReaderState(user.ID, "asset_1")
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
	locator := jsontext.Value(`{"engine":"test"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/asset_1/state", readerStateRequest{
		Progress: &progress,
		Locator:  locator,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid progress status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	progress = 0.5
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/asset_1/state", readerStateRequest{
		Progress: &progress,
		Locator:  jsontext.Value(`"bad"`),
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid locator status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/reader/assets/missing/touch", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodDelete, "/api/reader/assets/missing/state", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("reset missing asset status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/assets/asset_1/state", nil))
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

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/asset_1/annotations", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list empty status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var list []AnnotationDTO
	if err := json.UnmarshalRead(w.Body, &list); err != nil {
		t.Fatalf("decode empty annotations: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("empty annotations = %+v", list)
	}

	req := annotationRequest{
		CFI:           "epubcfi(/6/2!/4/2)",
		Quote:         "highlighted text",
		ContextBefore: "before",
		ContextAfter:  "after",
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/reader/assets/asset_1/annotations", req))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var created AnnotationDTO
	if err := json.UnmarshalRead(w.Body, &created); err != nil {
		t.Fatalf("decode created annotation: %v", err)
	}
	if created.ID == "" || created.AssetID != "asset_1" || created.Kind != db.AnnotationKindHighlight || created.Color != db.AnnotationColorYellow || created.Quote != req.Quote {
		t.Fatalf("created annotation = %+v", created)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPatch, "/api/reader/assets/asset_1/annotations/"+created.ID, annotationNoteRequest{Note: "  my note  "}))
	if w.Code != http.StatusOK {
		t.Fatalf("update note status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var updated AnnotationDTO
	if err := json.UnmarshalRead(w.Body, &updated); err != nil {
		t.Fatalf("decode updated annotation: %v", err)
	}
	if updated.ID != created.ID || updated.Note != "my note" || updated.Quote != created.Quote {
		t.Fatalf("updated annotation = %+v", updated)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/reader/assets/asset_1/annotations", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("bob list status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	list = nil
	if err := json.UnmarshalRead(w.Body, &list); err != nil {
		t.Fatalf("decode bob annotations: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("annotation leaked to bob: %+v", list)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodPatch, "/api/reader/assets/asset_1/annotations/"+created.ID, annotationNoteRequest{Note: "stolen"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob update status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodDelete, "/api/reader/assets/asset_1/annotations/"+created.ID, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob delete status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodDelete, "/api/reader/assets/asset_1/annotations/"+created.ID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPost, "/api/reader/assets/asset_1/annotations", annotationRequest{CFI: "", Quote: "x"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid create status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPIContinueReading(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "reader", db.RoleMember)
	if _, err := database.Exec(`
			INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at)
			VALUES (?, 'asset_1', 0.42, '{"engine":"foliate","cfi":"epubcfi(/6/2)","fraction":0.42}', 100, 100);
			INSERT INTO user_work_reading_state (user_id, work_id, status, updated_at)
			VALUES (?, 'w_1', 'reading', 100)
		`, user.ID, user.ID); err != nil {
		t.Fatalf("insert reader state: %v", err)
	}

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
	if items[0].ID != "w_1" || items[0].AssetID != "asset_1" || items[0].Progress != 0.42 || len(items[0].Assets) != 1 {
		t.Fatalf("continue item = %+v", items[0])
	}

	if _, err := database.SaveUserSettings(user.ID, db.UserSettings{
		Theme:               db.ThemeSystem,
		HideContinueReading: true,
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

func TestAPIReaderPreferencesLifecycle(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/preferences", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("default prefs status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var prefs ReaderPreferencesDTO
	if err := json.UnmarshalRead(w.Body, &prefs); err != nil {
		t.Fatalf("decode default prefs: %v", err)
	}
	if prefs.EPUBFlow != db.ReaderFlowPaginated ||
		prefs.DisplayStyle != db.ReaderStylePaper ||
		prefs.FontScale != db.DefaultReaderFontScale ||
		prefs.CustomColumnWidth != db.DefaultReaderCustomColumnWidth ||
		prefs.CustomLineHeight != db.DefaultReaderCustomLineHeight ||
		prefs.UpdatedAt != 0 {
		t.Fatalf("default prefs = %+v", prefs)
	}

	flow := db.ReaderFlowScrolled
	style := db.ReaderStyleCustom
	fontScale := 2
	customWidth := 820
	customLine := 1.9
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/reader/preferences", readerPreferencesRequest{
		EPUBFlow:          &flow,
		DisplayStyle:      &style,
		FontScale:         &fontScale,
		CustomColumnWidth: &customWidth,
		CustomLineHeight:  &customLine,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("save prefs status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	prefs = ReaderPreferencesDTO{}
	if err := json.UnmarshalRead(w.Body, &prefs); err != nil {
		t.Fatalf("decode saved prefs: %v", err)
	}
	if prefs.EPUBFlow != db.ReaderFlowScrolled ||
		prefs.DisplayStyle != db.ReaderStyleCustom ||
		prefs.FontScale != fontScale ||
		prefs.CustomColumnWidth != customWidth ||
		prefs.CustomLineHeight != customLine ||
		prefs.UpdatedAt == 0 {
		t.Fatalf("saved prefs = %+v", prefs)
	}

	fontScale = -1
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodPut, "/api/reader/preferences", readerPreferencesRequest{
		FontScale: &fontScale,
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("partial prefs status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	prefs = ReaderPreferencesDTO{}
	if err := json.UnmarshalRead(w.Body, &prefs); err != nil {
		t.Fatalf("decode partial prefs: %v", err)
	}
	if prefs.EPUBFlow != db.ReaderFlowScrolled ||
		prefs.DisplayStyle != db.ReaderStyleCustom ||
		prefs.FontScale != fontScale ||
		prefs.CustomColumnWidth != customWidth ||
		prefs.CustomLineHeight != customLine {
		t.Fatalf("partial prefs did not preserve existing values: %+v", prefs)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/reader/preferences", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("bob prefs status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	prefs = ReaderPreferencesDTO{}
	if err := json.UnmarshalRead(w.Body, &prefs); err != nil {
		t.Fatalf("decode bob prefs: %v", err)
	}
	if prefs.EPUBFlow != db.ReaderFlowPaginated || prefs.DisplayStyle != db.ReaderStylePaper {
		t.Fatalf("reader prefs leaked across users: %+v", prefs)
	}
}

func TestAPIReaderPreferencesErrors(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "reader", db.RoleMember)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	flow := "sideways"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/preferences", readerPreferencesRequest{
		EPUBFlow: &flow,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid prefs status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	style := "neon"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/preferences", readerPreferencesRequest{
		DisplayStyle: &style,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid style status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	fontScale := 12
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/preferences", readerPreferencesRequest{
		FontScale: &fontScale,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid font scale status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/preferences", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth prefs status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
