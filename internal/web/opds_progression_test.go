package web

import (
	"encoding/json/v2"
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/opds"
)

func TestOPDSFormatsKeepBookMetadataAndSeparatePositions(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "opds-formats", db.RoleReader)
	_, err := database.Write(t.Context()).Exec(`
		UPDATE books SET updated_at = 1700000000, language = 'en', publisher = 'Example Press' WHERE id = 1;
		INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (2, 1, 'book.pdf', 'book.pdf', '.pdf', randomblob(32), randomblob(32));`)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	type publication struct {
		ID        string      `xml:"id"`
		Title     string      `xml:"title"`
		Updated   string      `xml:"updated"`
		Language  string      `xml:"http://purl.org/dc/elements/1.1/ language"`
		Publisher string      `xml:"http://purl.org/dc/elements/1.1/ publisher"`
		Links     []opds.Link `xml:"link"`
	}
	get := func(path string, value any) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, "GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if err := xml.Unmarshal(w.Body.Bytes(), value); err != nil {
			t.Fatal(err)
		}
	}
	var feed struct {
		Entries []publication `xml:"entry"`
	}
	get("/opds/books", &feed)
	if len(feed.Entries) != 1 {
		t.Fatalf("catalog entries: %+v", feed.Entries)
	}
	book := feed.Entries[0]
	if book.Updated != time.Unix(1700000000, 0).UTC().Format(time.RFC3339) || book.Language != "en" || book.Publisher != "Example Press" {
		t.Fatalf("catalog metadata: %+v", book)
	}
	ids := map[string]bool{book.ID: true}
	for _, assetID := range []string{"1", "2"} {
		var entry publication
		get("/opds/publications/"+assetID, &entry)
		if entry.ID == "" || ids[entry.ID] {
			t.Fatalf("format shares a publication ID: %q", entry.ID)
		}
		ids[entry.ID] = true
		if entry.Title != book.Title || entry.Updated != book.Updated || entry.Language != book.Language || entry.Publisher != book.Publisher {
			t.Fatalf("format lost book metadata: %+v", entry)
		}
		links := map[string]string{}
		for _, link := range entry.Links {
			links[link.Rel] = link.Href
		}
		if !strings.HasSuffix(links[opds.ProgressionRel], "/opds/progression/"+assetID) || !strings.HasSuffix(links[opds.AcquisitionRel], "/download/"+assetID) {
			t.Fatalf("format points to another asset: %+v", entry.Links)
		}
	}
}

func TestOPDSProgressionProtocolAndDiscovery(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "opds-progress", db.RoleReader)
	other := mustUser(t, database, "opds-other", db.RoleReader)
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	request := func(uid int64, method string, payload any, etag string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := jsonRequest(t, s, uid, method, "/opds/progression/1", payload)
		if etag != "" {
			r.Header.Set("If-Match", etag)
		}
		handler.ServeHTTP(w, r)
		return w
	}
	empty := request(user.ID, "GET", nil, "")
	if empty.Code != 200 || empty.Body.Len() != 0 || empty.Header().Get("ETag") != `"0"` {
		t.Fatalf("initial: %d %s", empty.Code, empty.Body.String())
	}
	document := opds.Progression{Title: "Chapter 2", Modified: "2026-01-01T12:00:00.123456789+02:00", Device: opds.ProgressionDevice{ID: "urn:reader:example", Name: "An external reader"}, Progression: new(.25), References: []string{"chapter.xhtml#paragraph", "#epubcfi(/6/2!/4/2)"}}
	beforeSave := time.Now().Unix()
	saved := request(user.ID, "PUT", document, `"0"`)
	if saved.Code != 200 || saved.Header().Get("Content-Type") != opds.ProgressionType || saved.Header().Get("ETag") != `"1"` {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	var got opds.Progression
	if err := json.Unmarshal(saved.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	state, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if state.UpdatedAt < beforeSave || state.UpdatedAt > time.Now().Unix() || got.Modified != time.Unix(state.UpdatedAt, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("progression did not use server time: %+v, state: %+v", got, state)
	}
	if len(got.References) != 2 || got.References[0] != "#epubcfi(/6/2!/4/2)" || got.References[1] != "chapter.xhtml" || state.Locator.CFI != "epubcfi(/6/2!/4/2)" {
		t.Fatalf("normalized OPDS address: %+v", got)
	}
	if _, err := database.SetReadingStatus(t.Context(), user.ID, 1, db.ReadingStatusUnread, db.ReadingStatusSourceManual); err != nil {
		t.Fatal(err)
	}
	// An equivalent position can have a different client date or device. Only
	// server recency changes, preserving the revision, source and manual status.
	mustExec(t, database, `UPDATE user_asset_state SET updated_at = 100 WHERE user_id = ? AND asset_id = 1`, user.ID)
	retryDocument := document
	retryDocument.References = []string{"#epubcfi(/6/2!/4/2)", "chapter.xhtml"}
	retryDocument.Modified = "2025-01-01T00:00:00Z"
	retryDocument.Device = opds.ProgressionDevice{ID: "urn:reader:another", Name: "Another external reader"}
	retry := request(user.ID, "PUT", retryDocument, `"0"`)
	if retry.Code != 200 || retry.Header().Get("ETag") != `"1"` {
		t.Fatalf("retry: %d %s", retry.Code, retry.Body.String())
	}
	status, err := db.GetReadingStatus(database.Read(t.Context()), user.ID, 1)
	if err != nil || status.Status != db.ReadingStatusUnread {
		t.Fatalf("retry undid manual status: %+v %v", status, err)
	}
	if otherState := request(other.ID, "GET", nil, ""); otherState.Body.Len() != 0 {
		t.Fatal("progression leaked to another account")
	}
	state, err = db.GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil || state.Revision != 1 || state.Progress != .25 || state.UpdatedAt <= 100 {
		t.Fatalf("retry must only refresh recency: %+v %v", state, err)
	}
	if state.DeviceID != document.Device.ID || state.DeviceName != document.Device.Name {
		t.Fatalf("equivalent save replaced the position source: %+v", state)
	}
	// Opening in the web reader refreshes OPDS modified without invalidating a
	// client's position token. The next real change can use the pre-open ETag.
	mustExec(t, database, `UPDATE user_asset_state SET updated_at = 100 WHERE user_id = ? AND asset_id = 1`, user.ID)
	if _, _, err := database.TouchReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, db.ReadingStatusSourceWebReader); err != nil {
		t.Fatal(err)
	}
	opened := request(user.ID, "GET", nil, "")
	if opened.Code != 200 || opened.Header().Get("ETag") != saved.Header().Get("ETag") {
		t.Fatalf("opening invalidated position token: %d %s", opened.Code, opened.Body.String())
	}
	if err := json.Unmarshal(opened.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	modified, err := time.Parse(time.RFC3339, got.Modified)
	if err != nil || modified.Unix() <= 100 || got.Progression == nil || *got.Progression != .25 {
		t.Fatalf("opening did not refresh progression recency: %+v %v", got, err)
	}
	backward := document
	backward.Progression = new(.1)
	if moved := request(user.ID, "PUT", backward, saved.Header().Get("ETag")); moved.Code != 200 || moved.Header().Get("ETag") != `"2"` {
		t.Fatalf("opening conflicted with backward move: %d %s", moved.Code, moved.Body.String())
	}
	serverTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		progress   float64
		dateOffset time.Duration
		etag       string
		wantStatus int
	}{
		{"old backward move", .1, -6 * time.Minute, "", 409},
		{"backward move with small clock skew", .1, -4 * time.Minute, "", 200},
		{"forward move with an old clock", .5, -24 * time.Hour, "", 200},
		{"future date cannot override stale revision", .5, 24 * time.Hour, `"0"`, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustExec(t, database, `UPDATE user_asset_state SET progress = 0.25, revision = 1, updated_at = ? WHERE user_id = ? AND asset_id = 1`, serverTime.Unix(), user.ID)
			input := document
			input.Progression = new(tc.progress)
			input.Modified = serverTime.Add(tc.dateOffset).In(time.FixedZone("reader", 2*60*60)).Format(time.RFC3339)
			response := request(user.ID, "PUT", input, tc.etag)
			if response.Code != tc.wantStatus {
				t.Fatalf("save: %d %s", response.Code, response.Body.String())
			}
			state, err := db.GetReaderState(database.Read(t.Context()), user.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus == 409 {
				if response.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(response.Body.String(), "progression-date") {
					t.Fatalf("conflict payload: %s", response.Body.String())
				}
				if state.Progress != .25 || state.Revision != 1 || state.UpdatedAt != serverTime.Unix() {
					t.Fatalf("conflict changed state: %+v", state)
				}
			} else if state.Progress != tc.progress || state.Revision != 2 {
				t.Fatalf("lost accepted position: %+v", state)
			}
		})
	}
	invalidDate := document
	invalidDate.Modified = "not a date"
	for _, payload := range []any{map[string]any{"modified": "2027-01-01T00:00:00Z"}, invalidDate} {
		invalid := request(user.ID, "PUT", payload, "")
		if invalid.Code != 400 || !strings.Contains(invalid.Body.String(), "progression-invalid-payload") {
			t.Fatalf("invalid: %d %s", invalid.Code, invalid.Body.String())
		}
	}
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest("GET", "/opds/progression/1", nil))
	if anonymous.Code != 401 || anonymous.Header().Get("Content-Type") != "application/opds-authentication+json" || !strings.Contains(anonymous.Body.String(), "http://opds-spec.org/auth/basic") {
		t.Fatalf("authentication: %d %s", anonymous.Code, anonymous.Body.String())
	}
	crossOrigin := httptest.NewRecorder()
	crossRequest := jsonRequest(t, s, other.ID, "PUT", "/opds/progression/1", document)
	crossRequest.Header.Set("Origin", "https://other.example")
	handler.ServeHTTP(crossOrigin, crossRequest)
	if crossOrigin.Code != 403 || crossOrigin.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("cross-origin rejection: %d %s", crossOrigin.Code, crossOrigin.Body.String())
	}
	if state, err := db.GetReaderState(database.Read(t.Context()), other.ID, 1); err != nil || state.Revision != 0 {
		t.Fatalf("cross-origin request saved a position: %+v, %v", state, err)
	}
	for _, path := range []string{"/opds/books", "/opds/publications/1"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, user.ID, "GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), opds.ProgressionRel) || !strings.Contains(w.Body.String(), opds.ProgressionType) {
			t.Fatalf("discovery %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	// Zero percent without an exact anchor is still an explicit saved position.
	beginning := document
	beginning.Progression = new(0.0)
	beginning.References = nil
	savedBeginning := request(other.ID, "PUT", beginning, `"0"`)
	if savedBeginning.Code != 200 || savedBeginning.Header().Get("ETag") != `"1"` || savedBeginning.Body.Len() == 0 {
		t.Fatalf("percentage-only beginning was discarded: %d %s", savedBeginning.Code, savedBeginning.Body.String())
	}
	if repeated := request(other.ID, "PUT", beginning, `"0"`); repeated.Code != 200 || repeated.Header().Get("ETag") != `"1"` {
		t.Fatalf("percentage-only retry: %d %s", repeated.Code, repeated.Body.String())
	}
	// Moving between external anchors at the same percentage must persist the
	// new fragment even though the web reader only understands the percentage.
	for _, reference := range []string{"chapter.xhtml#first", "chapter.xhtml#second", "chapter.xhtml#:~:text=an%20example"} {
		anchored := beginning
		anchored.References = []string{reference}
		if saved := request(other.ID, "PUT", anchored, ""); saved.Code != 200 {
			t.Fatalf("save anchor: %d %s", saved.Code, saved.Body.String())
		}
		read := request(other.ID, "GET", nil, "")
		if read.Code != 200 {
			t.Fatalf("read anchor: %d %s", read.Code, read.Body.String())
		}
		if err := json.Unmarshal(read.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.References) != 1 || got.References[0] != reference {
			t.Fatalf("external anchor lost: %+v, want %q", got.References, reference)
		}
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, db.UserAccess{Role: db.RoleReader, ContentScope: db.ContentScopeShelves}); err != nil {
		t.Fatal(err)
	}
	forbidden := request(user.ID, "GET", nil, "")
	if forbidden.Code != 404 || forbidden.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("scope: %d %s", forbidden.Code, forbidden.Body.String())
	}
}

func TestOPDSLocatorNormalization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		references []string
		want       db.Locator
	}{
		{"percentage only", nil, db.Locator{}},
		{"unsupported fragment", []string{"#unknown=anchor"}, db.Locator{Fragment: "unknown=anchor"}},
		{"HTML anchor", []string{"OPS/chapter%20one.xhtml#paragraph"}, db.Locator{Path: "OPS/chapter one.xhtml", Fragment: "paragraph"}},
		{"text fragment", []string{"chapter.xhtml#:~:text=an%20example"}, db.Locator{Path: "chapter.xhtml", Fragment: ":~:text=an example"}},
		{"resource replaces a standalone fragment", []string{"#standalone", "chapter.xhtml#paragraph"}, db.Locator{Path: "chapter.xhtml", Fragment: "paragraph"}},
		{"fragment belongs to its resource", []string{"chapter.xhtml", "another.xhtml#paragraph"}, db.Locator{Path: "chapter.xhtml"}},
		{"another publication", []string{"https://example.org/other.epub#epubcfi(/6/2)"}, db.Locator{}},
		{"PDF page", []string{"#page=00042"}, db.Locator{Page: 42}},
		{"EPUB CFI and resource", []string{"OPS/chapter%20one.xhtml#paragraph", "#epubcfi(/6/2!/4/2)"}, db.Locator{CFI: "epubcfi(/6/2!/4/2)", Path: "OPS/chapter one.xhtml"}},
		{"CFI with URI escapes", []string{"#epubcfi(/6/2[chapter%2541]!/4/2[part%23one])"}, db.Locator{CFI: "epubcfi(/6/2[chapter%41]!/4/2[part#one])"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locator, err := progressionLocator(tc.references)
			if err != nil || !locator.Equal(tc.want) {
				t.Fatalf("parsed %+v, %v; want %+v", locator, err, tc.want)
			}
			emitted := readerProgression(&db.ReaderState{Locator: locator})
			restored, err := progressionLocator(emitted.References)
			if err != nil || !restored.Equal(locator) {
				t.Fatalf("round trip lost supported address: %+v %v", restored, err)
			}
		})
	}
}
