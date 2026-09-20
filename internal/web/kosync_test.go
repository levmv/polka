package web

import (
	"bytes"
	"crypto/md5"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/koreader"
	"github.com/levmv/polka/internal/opds"
	"github.com/levmv/polka/internal/storage"
)

func TestKOReaderSyncRoutes(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)
	aliceToken := mustAppToken(t, database, alice.ID)
	bobToken := mustAppToken(t, database, bob.ID)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := serveKOReader(t, handler, "GET", "/kosync/users/auth", aliceToken, nil)
	var auth koReaderAuthDTO
	decodeJSON(t, w, &auth)
	if auth.Username != "alice" {
		t.Fatalf("auth username = %q, want alice", auth.Username)
	}

	for _, tc := range []struct{ name, password string }{
		{"wrong password", "wrong-password"},
		{"account password", "pw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serveKOReader(t, handler, "GET", "/kosync/users/auth", tc.password, nil)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("auth = %d, want 401", w.Code)
			}
			var failure struct {
				Message string `json:"message"`
			}
			if err := json.UnmarshalRead(w.Body, &failure); err != nil || failure.Message == "" {
				t.Fatalf("missing KOSync login error: %+v, %v", failure, err)
			}
		})
	}

	saveReq := koReaderProgressRequest{
		Document:   "doc1",
		Metadata:   []byte(`{"filename":"book.epub","title":"Book","authors":"Test Author"}`),
		Progress:   "/body/DocFragment[1]",
		Percentage: 0.37,
		Device:     "KOReader",
		DeviceID:   "dev1",
	}
	w = serveKOReader(t, handler, http.MethodPut, "/kosync/syncs/progress", "wrong-password", saveReq)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token save status = %d, want 401", w.Code)
	}
	w = serveKOReader(t, handler, "PUT", "/kosync/syncs/progress", aliceToken, saveReq)
	var saved koReaderProgressDTO
	decodeJSON(t, w, &saved)
	if saved.Document != "doc1" || saved.Timestamp == 0 {
		t.Fatalf("saved progress = %+v, want doc1 with timestamp", saved)
	}

	w = serveKOReader(t, handler, "GET", "/kosync/syncs/progress/doc1", aliceToken, nil)
	var got koReaderProgressDTO
	decodeJSON(t, w, &got)
	if got.Document != "doc1" || got.Progress != saveReq.Progress || got.Percentage != saveReq.Percentage ||
		got.Device != saveReq.Device || got.DeviceID != saveReq.DeviceID || got.Timestamp == 0 {
		t.Fatalf("got progress = %+v", got)
	}

	w = serveKOReader(t, handler, "GET", "/kosync/syncs/progress/doc1", bobToken, nil)
	var missing map[string]any
	decodeJSON(t, w, &missing)
	if len(missing) != 0 {
		t.Fatalf("bob progress = %+v, want empty object", missing)
	}

	mustExec(t, database, "INSERT INTO koreader_hashes(asset_id, hash) VALUES (1, unhex('77777777777777777777777777777777'))")
	for _, tc := range []struct {
		percentage float64
		want       string
	}{
		{0.4, db.ReadingStatusReading},
		{1, db.ReadingStatusFinished},
	} {
		w = serveKOReader(t, handler, http.MethodPut, "/kosync/syncs/progress", aliceToken, koReaderProgressRequest{
			Document:   "77777777777777777777777777777777",
			Progress:   "mapped-" + tc.want,
			Percentage: tc.percentage,
			Device:     "KOReader",
			DeviceID:   "dev1",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("mapped save %.2f status = %d: %s", tc.percentage, w.Code, w.Body.String())
		}
		status, err := db.GetReadingStatus(database.Read(t.Context()), alice.ID, 1)
		if err != nil || status.Status != tc.want {
			t.Fatalf("mapped status %.2f = %+v, err %v; want %s", tc.percentage, status, err, tc.want)
		}
	}
	bobStatus, err := db.GetReadingStatus(database.Read(t.Context()), bob.ID, 1)
	if err != nil || bobStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("mapped KOSync leaked to bob: %+v, err %v", bobStatus, err)
	}

	w = serveKOReader(t, handler, "PUT", "/kosync/syncs/progress", aliceToken, koReaderProgressRequest{
		Document: "doc2",
		Progress: "p",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid save status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestKOReaderDocumentAccess(t *testing.T) {
	for _, name := range []string{"one asset", "multiple assets of one book"} {
		t.Run(name, func(t *testing.T) {
			database, dir := setupTestDB(t)
			defer database.Close()
			user := mustUser(t, database, "reader", db.RoleReader)
			password := mustAppToken(t, database, user.ID)
			if name == "multiple assets of one book" {
				mustExec(t, database, `INSERT INTO assets
                    (id, book_id, storage_path, filename, extension, original_hash, current_hash)
                    VALUES (2, 1, 'other.epub', 'other.epub', '.epub', randomblob(16), randomblob(16))`)
			}
			const document = "11111111111111111111111111111111"
			mustExec(t, database, "INSERT INTO koreader_hashes(asset_id, hash) SELECT id, unhex(?) FROM assets", document)
			handler := testRoutes(t, newTestServer(database, dir))
			base := "/kosync/syncs/progress"
			for _, tc := range []struct {
				scope string
				want  int
			}{
				{db.ContentScopeAll, http.StatusOK},
				{db.ContentScopeShelves, http.StatusNotFound},
			} {
				if _, err := database.UpdateUserAccess(t.Context(), user.ID, db.UserAccess{
					Role: db.RoleReader, ContentScope: tc.scope,
				}); err != nil {
					t.Fatal(err)
				}
				for _, method := range []string{http.MethodPut, http.MethodGet} {
					path := base
					var payload any = koReaderProgressRequest{
						Document: document, Progress: "chapter-1", Percentage: .4, Device: "KOReader", DeviceID: "reader-a",
					}
					if method == http.MethodGet {
						path += "/" + document
						payload = nil
					}
					w := serveKOReader(t, handler, method, path, password, payload)
					if w.Code != tc.want {
						t.Fatalf("%s with scope %s = %d, want %d: %s", method, tc.scope, w.Code, tc.want, w.Body.String())
					}
				}
			}
		})
	}
}

func TestKOReaderExternalPositionFallback(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "reader", db.RoleMember)
	password := mustAppToken(t, database, user.ID)
	input := db.KOReaderProgress{DocumentHash: "11111111111111111111111111111111",
		Position: "chapter-3", Progress: .6, DeviceName: "KOReader", DeviceID: "reader a/b"}
	external, err := database.SaveKOReaderProgress(t.Context(), user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	handler := testRoutes(t, newTestServer(database, dir))
	assertProgress := func(want koReaderProgressDTO) {
		t.Helper()
		w := serveKOReader(t, handler, http.MethodGet, "/kosync/syncs/progress/"+input.DocumentHash, password, nil)
		var got koReaderProgressDTO
		decodeJSON(t, w, &got)
		if got != want {
			t.Fatalf("KOSync response = %+v; want %+v", got, want)
		}
	}
	wantExternal := koReaderProgressDTO{Document: input.DocumentHash,
		Progress: input.Position, Percentage: input.Progress, Device: input.DeviceName,
		DeviceID: input.DeviceID, Timestamp: external.UpdatedAt}
	assertProgress(wantExternal)
	mustExec(t, database, "INSERT INTO koreader_hashes(asset_id, hash) VALUES (1, unhex(?))", input.DocumentHash)
	assertProgress(wantExternal)
	if err := database.TouchReader(t.Context(), user.ID, 1, db.ReadingStatusSourceWebReader); err != nil {
		t.Fatal(err)
	}
	assertProgress(wantExternal)
	// A library position takes precedence even when its CFI cannot be converted
	// against this fixture's unavailable EPUB content. Reset also suppresses fallback.
	if _, _, err := database.SaveReaderState(t.Context(), user.ID, 1, db.ReaderPositionWrite{
		Progress: .8, Locator: db.Locator{CFI: "epubcfi(/6/4)"}, DeviceID: "urn:reader:web", DeviceName: "Browser",
	}, db.ReadingStatusSourceWebReader); err != nil {
		t.Fatal(err)
	}
	assertProgress(koReaderProgressDTO{})
	if err := database.ResetReaderState(t.Context(), user.ID, 1); err != nil {
		t.Fatal(err)
	}
	assertProgress(koReaderProgressDTO{})
}

func TestKOReaderSharedPosition(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "reader", db.RoleMember)
	password := mustAppToken(t, database, user.ID)
	epub := testReadableEPUB(t, "Reading", "Alpha beta gamma.", "Second paragraph.", "A  B.")
	file := filepath.Join(dir, "Tolkien/The_Hobbit/a_1.epub")
	if err := os.WriteFile(file, epub, 0o644); err != nil {
		t.Fatal(err)
	}
	sha := storage.Sum(epub)
	mustExec(t, database, "UPDATE assets SET format='epub', current_hash=? WHERE id=1", sha[:])
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	download := func() string {
		t.Helper()
		// Register the actual delivered file through the download route.
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/download/1", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("download = %d: %s", w.Code, w.Body.String())
		}
		hash, err := koreader.PartialMD5(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	hash := download()
	base := "/kosync/syncs/progress"
	push := func(xp string, percentage float64, device string) {
		t.Helper()
		w := serveKOReader(t, handler, http.MethodPut, base, password, koReaderProgressRequest{
			Document: hash, Progress: xp, Percentage: percentage, Device: "My reader", DeviceID: device,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("push = %d: %s", w.Code, w.Body.String())
		}
	}
	pull := func() koReaderProgressDTO {
		t.Helper()
		w := serveKOReader(t, handler, http.MethodGet, base+"/"+hash, password, nil)
		var got koReaderProgressDTO
		decodeJSON(t, w, &got)
		return got
	}
	shared := func() db.ReaderState {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/api/reader/assets/1/position", nil))
		var got ReaderPositionDTO
		decodeJSON(t, w, &got)
		state, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
		if err != nil || got.Revision != state.Revision || got.Progress != state.Progress || !got.Locator.Equal(state.Locator) {
			t.Fatalf("position response = %+v, stored = %+v: %v", got, state, err)
		}
		return *state
	}
	webSave := func(cfi string, percentage float64) db.ReaderState {
		t.Helper()
		current := shared()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPut, "/api/reader/assets/1/position", readerPositionRequest{
			Revision: &current.Revision, DeviceID: "urn:test:web", DeviceName: "Browser",
			Progress: &percentage, Locator: &db.Locator{CFI: cfi},
		}))
		if w.Code != http.StatusOK {
			t.Fatalf("web save = %d: %s", w.Code, w.Body.String())
		}
		return shared()
	}
	const native = "/body/DocFragment[1]/body/p[1]/text().6"
	const firstCFI = "epubcfi(/6/2[main]!/4/2/1:6)"
	push(native, 0.25, "reader-a")
	mustExec(t, database, "UPDATE reading_positions SET updated_at=100 WHERE user_id=? AND asset_id=1", user.ID)
	stored, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil || !stored.Locator.IsZero() || stored.KOReaderPosition != native {
		t.Fatalf("native-only reading should not require a locator: %+v, %v", stored, err)
	}
	if got := pull(); got.Progress != native || got.DeviceID != "reader-a" || got.Timestamp != 100 {
		t.Fatalf("native round trip: %+v", got)
	}
	// Browsing progress and recording an opening use the stored observation.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/api/reader/assets/1/progress", nil))
	var progress ReaderProgressDTO
	decodeJSON(t, w, &progress)
	if progress.Progress == nil || *progress.Progress != .25 || progress.ReadingStatus.Status != db.ReadingStatusReading {
		t.Fatalf("native progress = %+v", progress)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodPost, "/api/reader/assets/1/touch", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("web open = %d", w.Code)
	}
	afterOpen, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil || !reflect.DeepEqual(afterOpen, stored) {
		t.Fatalf("browsing or opening changed the native observation: %+v, %v", afterOpen, err)
	}
	// OPDS is also a consumer of the shared locator, without opening the web reader.
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, user.ID, http.MethodGet, "/opds/progression/1", nil))
	var progression opds.Progression
	decodeJSON(t, w, &progression)
	locator, err := progressionLocator(progression.References)
	if err != nil || locator.CFI != firstCFI {
		t.Fatalf("native to OPDS = %+v, %v", progression, err)
	}
	first := shared()
	if first.Locator.CFI != firstCFI || first.Revision != 1 {
		t.Fatalf("native to web = %+v", first)
	}
	if got := pull(); got.Progress != native || got.DeviceID != "reader-a" || got.Timestamp != 100 {
		t.Fatalf("reopening changed the native position: %+v", got)
	}
	// A range is a real web-reader locator. A native echo of its start must not
	// replace the original range, origin or percentage with the device's layout.
	second := webSave("epubcfi(/6/2[main]!/4/4,/1:7,/1:12)", 0.6)
	got := pull()
	if got.Progress != "/body[1]/DocFragment[1]/body[1]/p[2]/text()[1].7" || got.DeviceID != "urn:test:web" {
		t.Fatalf("web to native = %+v", got)
	}
	push(got.Progress, 0.64, "reader-a")
	if reopened := pull(); reopened.Progress != got.Progress {
		t.Fatalf("reopening after receiving web progress moved again: %+v", reopened)
	}
	if state := shared(); state.Revision != second.Revision || !state.Locator.Equal(second.Locator) || state.DeviceID != second.DeviceID || state.Progress != second.Progress {
		t.Fatalf("native echo replaced the shared observation: %+v", state)
	}
	// Whitespace makes the character offset ambiguous, but the paragraph is
	// known. Reopening and another reader's echo must preserve the native place.
	const approximate = "/body/DocFragment[1]/body/p[3]/text().2"
	const paragraphCFI = "epubcfi(/6/2[main]!/4/6)"
	const paragraphXP = "/body[1]/DocFragment[1]/body[1]/p[3]"
	precise := webSave("epubcfi(/6/2[main]!/4/6/1:3)", 0.86)
	if got := pull(); got.Progress != paragraphXP {
		t.Fatalf("web position did not fall back to its paragraph: %+v", got)
	}
	push(paragraphXP, 0.8, "reader-b")
	if state := shared(); state.Revision != precise.Revision || !state.Locator.Equal(precise.Locator) {
		t.Fatalf("paragraph echo lost the precise web position: %+v", state)
	}
	push(approximate, 0.8, "reader-a")
	paragraph := shared()
	if paragraph.Locator.CFI != paragraphCFI {
		t.Fatalf("native paragraph was not shared: %+v", paragraph)
	}
	push(approximate, 0.81, "reader-a")
	push(paragraphXP, 0.79, "reader-b")
	if state := shared(); state.Revision != paragraph.Revision || state.DeviceID != paragraph.DeviceID {
		t.Fatalf("paragraph retry or echo changed the shared observation: %+v", state)
	}
	if got := pull(); got.Progress != approximate || got.DeviceID != "reader-a" {
		t.Fatalf("paragraph echo displaced the original native address: %+v", got)
	}
	const advanced = "/body/DocFragment[1]/body/p[3]/text().3"
	push(advanced, 0.85, "reader-b")
	if got := pull(); got.Progress != advanced || got.DeviceID != "reader-b" {
		t.Fatalf("further reading in the same paragraph was lost: %+v", got)
	}
	beforeRewind := shared()
	push(approximate, 0.8, "reader-b")
	rewound := shared()
	if rewound.Revision != beforeRewind.Revision+1 || rewound.Locator.CFI != paragraphCFI {
		t.Fatalf("returning to a previous native place was lost: %+v", rewound)
	}
	const unsupported = "/body/DocFragment[1]/body/p[99]/text().2"
	push(unsupported, 0.8, "reader-a")
	if state := shared(); state.Revision != rewound.Revision+1 || !state.Locator.IsZero() {
		t.Fatalf("unresolved input kept coordinates from a previous position: %+v", state)
	}
	if got := pull(); got.Progress != unsupported {
		t.Fatalf("unresolved native place was replaced: %+v", got)
	}
	// A temporarily unavailable managed file must not make an older shared
	// position win when the native reader next opens the book.
	if err := os.Rename(file, file+".away"); err != nil {
		t.Fatal(err)
	}
	const pending = "/body/DocFragment[1]/body/p[2]/text().9"
	push(pending, 0.65, "reader-a")
	if got := pull(); got.Progress != pending {
		t.Fatalf("native sync requires the managed file: %+v", got)
	}
	if state := shared(); !state.Locator.IsZero() || state.Progress != .65 {
		t.Fatalf("missing file should use current progress: %+v", state)
	}
	if err := os.Rename(file+".away", file); err != nil {
		t.Fatal(err)
	}
	if got := pull(); got.Progress != pending {
		t.Fatalf("conversion outage moved the native reader backwards: %+v", got)
	}
	push(pending, 0.65, "reader-a")
	if state := shared(); state.Locator.CFI != "epubcfi(/6/2[main]!/4/4/1:9)" {
		t.Fatalf("position after conversion recovery was not shared: %+v", state)
	}
	push(advanced, .85, "reader-b")
	newer := shared()
	push("/body[1]/DocFragment[1]/body[1]/p[2]/text()[1].9", .65, "reader-a")
	if state := shared(); state.Revision != newer.Revision || state.DeviceID != newer.DeviceID {
		t.Fatalf("unchanged close from another native reader rewound the book: %+v", state)
	}
	webSave("epubcfi(/6/2[main]!/4/2/1:0)", 0)
	w = serveKOReader(t, handler, http.MethodGet, base+"/"+hash, password, nil)
	var start struct {
		Percentage *float64 `json:"percentage"`
		Progress   string   `json:"progress"`
	}
	decodeJSON(t, w, &start)
	if start.Percentage == nil || *start.Percentage != 0 || start.Progress != "/body[1]/DocFragment[1]/body[1]/p[1]/text()[1].0" {
		t.Fatalf("beginning is not a usable KOSync response: %+v", start)
	}

	// Metadata write-back changes the bytes, but old downloads still translate
	// positions in both directions, before the updated file is downloaded again.
	admin := mustUser(t, database, "admin", db.RoleAdmin)
	writebackIdentityJSON(t, s, handler, admin.ID, http.MethodPatch, "/api/books/1", map[string]any{"publisher": "Updated publisher"})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/books/1/writeback", nil))
	var written bookWritebackResultDTO
	decodeJSON(t, w, &written)
	if written.Written != 1 || written.Failed != 0 {
		t.Fatalf("metadata write-back = %+v", written)
	}
	const afterEdit = "/body/DocFragment[1]/body/p[2]/text().10"
	push(afterEdit, .55, "reader-a")
	if got := pull(); got.Progress != afterEdit || got.Percentage != .55 {
		t.Fatalf("old downloaded copy lost its position: %+v", got)
	}
	if state := shared(); state.Progress != .55 || state.Locator.CFI != "epubcfi(/6/2[main]!/4/4/1:10)" {
		t.Fatalf("old download did not translate after metadata write-back: %+v", state)
	}
	web := webSave("epubcfi(/6/2[main]!/4/4/1:12)", .7)
	if got := pull(); got.Progress != "/body[1]/DocFragment[1]/body[1]/p[2]/text()[1].12" || got.Percentage != .7 {
		t.Fatalf("old download did not resume web reading after metadata write-back: %+v", got)
	}
	push(afterEdit, .55, "reader-a")
	if state := shared(); state.Revision != web.Revision || state.Progress != .7 {
		t.Fatalf("old copy's repeated upload displaced web reading: %+v", state)
	}
	newHash := download()
	if newHash == hash {
		t.Fatalf("metadata edit did not change the downloaded hash: %q", newHash)
	}
	oldHash := hash
	hash = newHash
	resumed := pull()
	if resumed.Progress != "/body[1]/DocFragment[1]/body[1]/p[2]/text()[1].12" || resumed.Percentage != .7 {
		t.Fatalf("new download did not resume the shared web position: %+v", resumed)
	}
	push(resumed.Progress, .71, "reader-c")
	if state := shared(); state.Revision != web.Revision ||
		state.Progress != web.Progress || !state.Locator.Equal(web.Locator) {
		t.Fatalf("new download's echo replaced the shared observation: %+v", state)
	}
	// Known copies reuse the received native address, even when conversion
	// would lose precision or cannot resolve it at all.
	for _, point := range []struct {
		address  string
		progress float64
	}{
		{approximate, .8},
		{unsupported, .8},
	} {
		hash = newHash
		push(point.address, point.progress, "reader-c")
		before, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, requestedHash := range []string{oldHash, newHash} {
			hash = requestedHash
			if got := pull(); got.Document != hash || got.Progress != point.address || got.Percentage != point.progress || got.DeviceID != "reader-c" {
				t.Fatalf("download %s did not reuse native position %q: %+v", hash, point.address, got)
			}
		}
		after, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
		if err != nil || after.Revision != before.Revision || after.UpdatedAt != before.UpdatedAt ||
			after.KOReaderPosition != before.KOReaderPosition || !after.Locator.Equal(before.Locator) {
			t.Fatalf("native lookup changed the observation: %+v, %v", after, err)
		}
	}
}

func serveKOReader(t *testing.T, handler http.Handler, method, target, password string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.MarshalWrite(&body, payload); err != nil {
			t.Fatalf("encode payload: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, &body)
	setKOReaderAuth(req, "polka", password)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func setKOReaderAuth(req *http.Request, username, password string) {
	req.Header.Set("x-auth-user", username)
	req.Header.Set("x-auth-key", fmt.Sprintf("%x", md5.Sum([]byte(password))))
}
