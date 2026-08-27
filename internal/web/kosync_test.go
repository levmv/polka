package web

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestKOReaderSyncRoutes(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)
	aliceToken, err := database.CreateAppToken(alice.ID, "koreader-a")
	if err != nil {
		t.Fatalf("create alice token: %v", err)
	}
	bobToken, err := database.CreateAppToken(bob.ID, "koreader-b")
	if err != nil {
		t.Fatalf("create bob token: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := serveKOReader(t, handler, "GET", "/kosync/"+aliceToken+"/users/auth", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("auth status = %d; body: %s", w.Code, w.Body.String())
	}
	var auth koReaderAuthDTO
	if err := json.UnmarshalRead(w.Body, &auth); err != nil {
		t.Fatalf("decode auth: %v", err)
	}
	if auth.Username != "alice" {
		t.Fatalf("auth username = %q, want alice", auth.Username)
	}

	w = serveKOReader(t, handler, "GET", "/kosync/not-a-token/users/auth", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want 401", w.Code)
	}

	saveReq := koReaderProgressRequest{
		Document:   "doc1",
		Progress:   "/body/DocFragment[1]",
		Percentage: 0.37,
		Device:     "KOReader",
		DeviceID:   "dev1",
	}
	w = serveKOReader(t, handler, "PUT", "/kosync/"+aliceToken+"/syncs/progress", saveReq)
	if w.Code != http.StatusOK {
		t.Fatalf("save status = %d; body: %s", w.Code, w.Body.String())
	}
	var saved koReaderProgressDTO
	if err := json.UnmarshalRead(w.Body, &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if saved.Document != "doc1" || saved.Timestamp == 0 {
		t.Fatalf("saved progress = %+v, want doc1 with timestamp", saved)
	}

	w = serveKOReader(t, handler, "GET", "/kosync/"+aliceToken+"/syncs/progress/doc1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d; body: %s", w.Code, w.Body.String())
	}
	var got koReaderProgressDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.Document != "doc1" || got.Progress != saveReq.Progress || got.Percentage != saveReq.Percentage ||
		got.Device != saveReq.Device || got.DeviceID != saveReq.DeviceID || got.Timestamp == 0 {
		t.Fatalf("got progress = %+v", got)
	}

	w = serveKOReader(t, handler, "GET", "/kosync/"+bobToken+"/syncs/progress/doc1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("bob get status = %d; body: %s", w.Code, w.Body.String())
	}
	var missing map[string]any
	if err := json.UnmarshalRead(w.Body, &missing); err != nil {
		t.Fatalf("decode bob missing: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("bob progress = %+v, want empty object", missing)
	}

	if err := db.SetAssetKOReaderHash(database, "asset_1", "mapped-doc"); err != nil {
		t.Fatalf("set mapped hash: %v", err)
	}
	for _, tc := range []struct {
		percentage float64
		want       string
	}{
		{0.4, db.ReadingStatusReading},
		{1, db.ReadingStatusFinished},
	} {
		w = serveKOReader(t, handler, http.MethodPut, "/kosync/"+aliceToken+"/syncs/progress", koReaderProgressRequest{
			Document:   "mapped-doc",
			Progress:   "mapped-position",
			Percentage: tc.percentage,
			Device:     "KOReader",
			DeviceID:   "dev1",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("mapped save %.2f status = %d: %s", tc.percentage, w.Code, w.Body.String())
		}
		status, err := db.GetReadingStatus(database, alice.ID, "w_1")
		if err != nil || status.Status != tc.want {
			t.Fatalf("mapped status %.2f = %+v, err %v; want %s", tc.percentage, status, err, tc.want)
		}
	}
	bobStatus, err := db.GetReadingStatus(database, bob.ID, "w_1")
	if err != nil || bobStatus.Status != db.ReadingStatusUnread {
		t.Fatalf("mapped KOSync leaked to bob: %+v, err %v", bobStatus, err)
	}

	w = serveKOReader(t, handler, "PUT", "/kosync/"+aliceToken+"/syncs/progress", koReaderProgressRequest{
		Document: "doc2",
		Progress: "p",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid save status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestKOReaderOfflineReplaySequence(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	user := mustUser(t, database, "reader", db.RoleMember)
	token, err := database.CreateAppToken(user.ID, "koreader")
	if err != nil {
		t.Fatalf("create app token: %v", err)
	}
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	update := func(progress string, percentage float64) koReaderProgressDTO {
		t.Helper()
		w := serveKOReader(t, handler, http.MethodPut, "/kosync/"+token+"/syncs/progress", map[string]any{
			"document":   "offline-document",
			"metadata":   map[string]any{"filename": "book.epub", "title": "Offline Book", "authors": "Test Author"},
			"progress":   progress,
			"percentage": percentage,
			"device":     "KOReader",
			"device_id":  "offline-device",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("save %q status = %d; body: %s", progress, w.Code, w.Body.String())
		}
		var saved koReaderProgressDTO
		if err := json.UnmarshalRead(w.Body, &saved); err != nil {
			t.Fatalf("decode save %q: %v", progress, err)
		}
		return saved
	}

	// KOReader drains queued entries in order and resumes at the first request
	// that did not reach the server. Replaying the remaining entry must leave
	// the newest queued position as the durable value.
	update("chapter-1", 0.2)
	update("chapter-3", 0.6)
	update("chapter-3", 0.6)
	update("chapter-5", 0.9)

	w := serveKOReader(t, handler, http.MethodGet, "/kosync/"+token+"/syncs/progress/offline-document", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get replayed progress status = %d; body: %s", w.Code, w.Body.String())
	}
	var got koReaderProgressDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode replayed progress: %v", err)
	}
	if got.Progress != "chapter-5" || got.Percentage != 0.9 {
		t.Fatalf("replayed progress = %+v; want newest queued position", got)
	}

	// The SQLite clock is second-resolution. Exercise two updates that receive
	// the same timestamp and ensure request order, not timestamp uniqueness,
	// decides the final position.
	sameSecond := false
	for range 3 {
		first := update("rapid-old", 0.91)
		last := update("rapid-new", 0.92)
		if first.Timestamp == last.Timestamp {
			sameSecond = true
			break
		}
	}
	if !sameSecond {
		t.Fatal("could not exercise two rapid updates in one wall-clock second")
	}
	w = serveKOReader(t, handler, http.MethodGet, "/kosync/"+token+"/syncs/progress/offline-document", nil)
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode rapid progress: %v", err)
	}
	if got.Progress != "rapid-new" || got.Percentage != 0.92 {
		t.Fatalf("rapid progress = %+v; want last request", got)
	}

	// Authentication failures never reach the progress handler and therefore
	// cannot create state that a later valid request can observe.
	w = serveKOReader(t, handler, http.MethodPut, "/kosync/not-a-token/syncs/progress", map[string]any{
		"document":   "unauthorized-document",
		"progress":   "chapter-1",
		"percentage": 0.1,
		"device":     "KOReader",
		"device_id":  "offline-device",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token save status = %d, want 401", w.Code)
	}
	w = serveKOReader(t, handler, http.MethodGet, "/kosync/"+token+"/syncs/progress/unauthorized-document", nil)
	var missing map[string]any
	if err := json.UnmarshalRead(w.Body, &missing); err != nil {
		t.Fatalf("decode unauthorized-document lookup: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("unauthorized update created progress: %+v", missing)
	}
}

func serveKOReader(t *testing.T, handler http.Handler, method, target string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		var buf bytes.Buffer
		if err := json.MarshalWrite(&buf, payload); err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		body = bytes.NewReader(buf.Bytes())
	}
	req := httptest.NewRequest(method, target, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}
