package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestReadUsesPreferredPrimaryWithoutMovingReadingData(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	alice := mustUser(t, database, "alice", db.RoleReader)
	bob := mustUser(t, database, "bob", db.RoleReader)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (125, 'Several formats', 'Several formats');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, original_hash, current_hash) VALUES
			(2, 125, 'book.pdf', 'book.pdf', '.pdf', 'pdf', 1, randomblob(16), randomblob(16)),
			(3, 125, 'book.epub', 'book.epub', '.epub', 'epub', 0, randomblob(16), randomblob(16));
	`)
	position, _, err := database.SaveReaderState(t.Context(), alice.ID, 2, db.ReaderPositionWrite{
		Progress: .4, Locator: db.Locator{Page: 2},
		DeviceID: "urn:test:reader", DeviceName: "Test reader",
	}, db.ReadingStatusSourceWebReader)
	if err != nil {
		t.Fatal(err)
	}
	annotation, err := database.CreateAnnotation(t.Context(), alice.ID, 2, db.AnnotationCreate{
		Locator: db.Locator{Page: 2, Rects: []db.Rect{{X: 10, Y: 10, Width: 20, Height: 10}}},
		Quote:   "PDF passage", Note: "Keep this note",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transact(t.Context(), func(tx *db.Tx) error {
		return db.EnsurePreferredPrimaryAsset(tx, 125)
	}); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t, database, dir)
	handler := testRoutes(t, s)
	for _, userID := range []int64{alice.ID, bob.ID} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, jsonRequest(t, s, userID, http.MethodGet, "/read/125", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `data-reader-asset-id="3"`) {
			t.Fatalf("Read for user %d: %d %s; want primary EPUB", userID, w.Code, w.Body.String())
		}
	}
	// Continue reading is an explicit link to the old PDF, not a hidden override
	// of the book's Read action. Selecting a new primary does not move its state.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/continue", nil))
	var continued []ContinueReadingDTO
	decodeJSON(t, w, &continued)
	if len(continued) != 1 || continued[0].AssetID != 2 || continued[0].Progress != .4 {
		t.Fatalf("Continue reading = %+v; want the original PDF", continued)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/read/asset/2", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `data-reader-asset-id="2"`) {
		t.Fatalf("explicit PDF read: %d %s", w.Code, w.Body.String())
	}
	got, err := db.GetReaderState(database.Read(t.Context()), alice.ID, 2)
	if err != nil || got.Revision != position.Revision || got.Progress != position.Progress || !got.Locator.Equal(position.Locator) {
		t.Fatalf("PDF position changed: %+v, %v", got, err)
	}
	fresh, err := db.GetReaderProgress(database.Read(t.Context()), alice.ID, 3)
	if err != nil || fresh.Progress != nil {
		t.Fatalf("PDF progress copied to EPUB: %+v, %v", fresh, err)
	}
	notes, err := db.ListBookAnnotations(database.Read(t.Context()), alice.ID, 125)
	if err != nil || len(notes) != 1 || notes[0].ID != annotation.ID || notes[0].AssetID != 2 || !notes[0].Locator.Equal(annotation.Locator) {
		t.Fatalf("PDF annotation changed: %+v, %v", notes, err)
	}
}
