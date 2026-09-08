package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestGetPossibleDuplicates(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Isaac Asimov', 'Asimov, Isaac')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (2, 'Other Author', 'Other Author')")

	// Books 1 and 2 are duplicates (different case, punctuation).
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at) VALUES (1, 'Foundation', 'Foundation', 30)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at) VALUES (2, 'foundation!', 'Foundation', 20)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 1, 0)")

	// Book 3 is unrelated.
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at) VALUES (3, 'Other Book', 'Other Book', 10)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (3, 1, 0)")

	// Same normalized title, different primary author: not a duplicate.
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at) VALUES (4, 'Foundation', 'Foundation', 25)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (4, 2, 0)")

	// Deleted rows do not create duplicate cleanup items.
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at, deleted_at) VALUES (122, 'foundation?', 'Foundation', 40, 40)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (122, 1, 0)")

	count, groups, err := GetPossibleDuplicates(database.Read(t.Context()), FullVisibilityScope(), 10)
	if err != nil {
		t.Fatalf("GetPossibleDuplicates failed: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 duplicate group, got %d", count)
	}
	if len(groups) != 1 {
		t.Fatalf("Expected 1 group returned, got %d", len(groups))
	}
	if len(groups[0].Books) != 2 {
		t.Errorf("Expected 2 books in group, got %d", len(groups[0].Books))
	}
	if groups[0].Books[0].ID != 1 || groups[0].Books[1].ID != 2 {
		t.Errorf("Expected duplicate books in added_at order [1 2], got [%d %d]", groups[0].Books[0].ID, groups[0].Books[1].ID)
	}
	if groups[0].Reason != DuplicateReasonTitleAuthor {
		t.Errorf("Unexpected duplicate reason: %s", groups[0].Reason)
	}
	if groups[0].Key != "foundation|isaac asimov" {
		t.Errorf("Unexpected normalized key: %s", groups[0].Key)
	}
}

func TestGetPossibleDuplicatesCountsBeyondLimit(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Author', 'Author')")
	for i, title := range []string{"Alpha", "Beta", "Gamma"} {
		for j := range 2 {
			bookID := int64(i*2 + j + 1)
			mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at) VALUES (?, ?, ?, ?)", bookID, title, title, 100-i*10-j)
			mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 1, 0)", bookID)
		}
	}

	count, groups, err := GetPossibleDuplicates(database.Read(t.Context()), FullVisibilityScope(), 2)
	if err != nil {
		t.Fatalf("GetPossibleDuplicates failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d; want 3", count)
	}
	if len(groups) != 2 {
		t.Fatalf("returned groups = %d; want 2", len(groups))
	}
	if groups[0].Key != "alpha|author" || groups[1].Key != "beta|author" {
		t.Fatalf("groups = %q, %q; want alpha, beta", groups[0].Key, groups[1].Key)
	}
}

func TestDismissDuplicateGroupHidesOnlyCoveredCurrentSet(t *testing.T) {
	database := newTestDB(t)
	insertDuplicateBooks(t, database, 1, 2)

	user := mustUser(t, database, "curator", RoleMember)
	for _, bookIDs := range [][]int64{{2, 1}, {1, 2}} {
		if err := database.Transact(t.Context(), func(tx *Tx) error {
			return DismissDuplicateGroup(tx, FullVisibilityScope(), bookIDs, user.ID)
		}); err != nil {
			t.Fatalf("DismissDuplicateGroup: %v", err)
		}
	}
	var dismissals int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM duplicate_dismissals").Scan(&dismissals); err != nil {
		t.Fatal(err)
	}
	if dismissals != 1 {
		t.Fatalf("dismissals = %d; want 1 after repeated dismissal", dismissals)
	}

	count, groups, err := GetPossibleDuplicates(database.Read(t.Context()), FullVisibilityScope(), 10)
	if err != nil {
		t.Fatalf("GetPossibleDuplicates after dismiss: %v", err)
	}
	if count != 0 || len(groups) != 0 {
		t.Fatalf("dismissed duplicates count=%d groups=%d, want none", count, len(groups))
	}

	insertDuplicateBook(t, database, 3, "Foundation?", 10)
	count, groups, err = GetPossibleDuplicates(database.Read(t.Context()), FullVisibilityScope(), 10)
	if err != nil {
		t.Fatalf("GetPossibleDuplicates after third copy: %v", err)
	}
	if count != 1 || len(groups) != 1 || len(groups[0].Books) != 3 {
		t.Fatalf("duplicates after third copy count=%d groups=%d books=%d, want one 3-book group", count, len(groups), len(groups[0].Books))
	}
}

func TestMergeDuplicateBooksMovesAssetsShelvesAndSafeFillIns(t *testing.T) {
	database := newTestDB(t)
	insertDuplicateBooks(t, database, 1, 2)

	user := mustUser(t, database, "curator", RoleMember)
	mustExec(t, database, "UPDATE books SET description = NULL, cover_version = 0 WHERE id = 1")
	mustExec(t, database, "UPDATE books SET description = 'Loser description', cover_version = 2 WHERE id = 2")
	mustExec(t, database, `
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, created_at, original_sha256, current_sha256)
		VALUES
			(1, 1, 'A/Foundation/asset_epub.epub', 'asset_epub.epub', '.epub', 'epub', 1, 0, 10, randomblob(32), randomblob(32)),
			(2, 2, 'A/Foundation/asset_pdf.pdf', 'asset_pdf.pdf', '.pdf', 'pdf', 1, 1, 20, randomblob(32), randomblob(32))
	`)
	mustExec(t, database, "INSERT INTO shelves (id, name, kind, owner_id, position) VALUES (2, 'Shelf', 'manual', ?, 1)", user.ID)
	mustExec(t, database, "INSERT INTO shelf_books (shelf_id, book_id, position) VALUES (2, 2, 5)")
	mustExec(t, database, `
		INSERT INTO delivery_jobs (id, user_id, device_name, device_email, preset, book_id, asset_id, title, filename)
		VALUES (1, ?, 'Device', 'reader@example.test', 'generic', 2, 2, 'Foundation', 'asset_pdf.pdf')
	`, user.ID)
	mustExec(t, database, `
		INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at)
		VALUES (?, 2, 0.4, '{}', 20, 20)
	`, user.ID)
	mustExec(t, database, `
		INSERT INTO user_annotations (id, user_id, asset_id, cfi, quote, note)
		VALUES (1, ?, 2, '/6/2', 'Quote', 'Note')
	`, user.ID)

	var result DuplicateMergeResult
	if err := database.Transact(context.Background(), func(tx *Tx) error {
		var err error
		result, err = MergeDuplicateBooks(tx, FullVisibilityScope(), DuplicateMergeRequest{
			SurvivorID:  1,
			BookIDs:     []int64{1, 2},
			DeletedBy:   user.ID,
			CoverFromID: 2,
		})
		return err
	}); err != nil {
		t.Fatalf("MergeDuplicateBooks: %v", err)
	}
	if !result.FilledDescription || !result.FilledCover {
		t.Fatalf("merge fill-ins = desc:%v cover:%v, want both", result.FilledDescription, result.FilledCover)
	}

	var deletedAt sql.NullInt64
	if err := database.Read(t.Context()).QueryRow("SELECT deleted_at FROM books WHERE id = 2").Scan(&deletedAt); err != nil {
		t.Fatalf("query loser deleted_at: %v", err)
	}
	if !deletedAt.Valid {
		t.Fatalf("loser was not moved to trash")
	}

	var description string
	var coverVersion int
	if err := database.Read(t.Context()).QueryRow("SELECT description, cover_version FROM books WHERE id = 1").Scan(&description, &coverVersion); err != nil {
		t.Fatalf("query survivor fill-ins: %v", err)
	}
	if description != "Loser description" || coverVersion != 1 {
		t.Fatalf("survivor desc=%q cover_version=%d, want loser description and version 1", description, coverVersion)
	}
	var assetCount, primaryCount int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*), SUM(is_primary) FROM assets WHERE book_id = 1").Scan(&assetCount, &primaryCount); err != nil {
		t.Fatalf("query survivor assets: %v", err)
	}
	if assetCount != 2 || primaryCount != 1 {
		t.Fatalf("survivor assets=%d primaries=%d, want 2 assets and 1 primary", assetCount, primaryCount)
	}
	var primaryAssetID int64
	if err := database.Read(t.Context()).QueryRow("SELECT id FROM assets WHERE book_id = 1 AND is_primary = 1").Scan(&primaryAssetID); err != nil {
		t.Fatalf("query survivor primary asset: %v", err)
	}
	if primaryAssetID != 2 {
		t.Fatalf("survivor primary asset = %d, want moved readable asset_pdf", primaryAssetID)
	}

	var shelfRows int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM shelf_books WHERE shelf_id = 2 AND book_id = 1").Scan(&shelfRows); err != nil {
		t.Fatalf("query shelf membership: %v", err)
	}
	if shelfRows != 1 {
		t.Fatalf("survivor shelf rows = %d, want 1", shelfRows)
	}
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM shelf_books WHERE book_id = 2").Scan(&shelfRows); err != nil {
		t.Fatalf("query loser shelf membership: %v", err)
	}
	if shelfRows != 0 {
		t.Fatalf("loser shelf rows = %d, want 0", shelfRows)
	}

	var deliveryBookID int64
	if err := database.Read(t.Context()).QueryRow("SELECT book_id FROM delivery_jobs WHERE id = 1").Scan(&deliveryBookID); err != nil {
		t.Fatalf("query delivery job: %v", err)
	}
	if deliveryBookID != 1 {
		t.Fatalf("delivery job book_id = %d, want 1", deliveryBookID)
	}

	readerState, err := GetReaderState(database.Read(t.Context()), user.ID, 2)
	if err != nil || readerState.BookID != 1 || readerState.Progress != 0.4 {
		t.Fatalf("moved asset reader state = %+v, err %v; want stable asset state on survivor", readerState, err)
	}
	annotations, err := ListAnnotations(database.Read(t.Context()), user.ID, 2)
	if err != nil || len(annotations) != 1 || annotations[0].ID != 1 {
		t.Fatalf("moved asset annotations = %+v, err %v; want annotation retained", annotations, err)
	}
}

func TestMergeDuplicateBooksPreservesExistingMetadata(t *testing.T) {
	database := newTestDB(t)
	insertDuplicateBooks(t, database, 1, 2)
	user := mustUser(t, database, "curator", RoleMember)
	mustExec(t, database, "UPDATE books SET description = 'Survivor description', cover_version = 1 WHERE id = 1")
	mustExec(t, database, "UPDATE books SET description = 'Loser description', cover_version = 0 WHERE id = 2")

	var result DuplicateMergeResult
	if err := database.Transact(context.Background(), func(tx *Tx) error {
		var err error
		result, err = MergeDuplicateBooks(tx, FullVisibilityScope(), DuplicateMergeRequest{
			SurvivorID: 1,
			BookIDs:    []int64{1, 2},
			DeletedBy:  user.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("MergeDuplicateBooks: %v", err)
	}
	if result.FilledDescription || result.FilledCover {
		t.Fatalf("merge fill-ins = desc:%v cover:%v, want none", result.FilledDescription, result.FilledCover)
	}

	var description string
	var coverVersion int
	if err := database.Read(t.Context()).QueryRow("SELECT description, cover_version FROM books WHERE id = 1").Scan(&description, &coverVersion); err != nil {
		t.Fatal(err)
	}
	if description != "Survivor description" || coverVersion != 1 {
		t.Fatalf("survivor metadata overwritten: description=%q, cover_version=%d", description, coverVersion)
	}
}

func TestMergeDuplicateBooksMergesPerUserReadingStateAndHistories(t *testing.T) {
	database := newTestDB(t)
	insertDuplicateBooks(t, database, 1, 2, 3)

	alice := mustUser(t, database, "alice", RoleMember)
	bob := mustUser(t, database, "bob", RoleMember)
	mustExec(t, database, `
		INSERT INTO user_book_reading_events
			(id, user_id, book_id, previous_event_id, from_status, to_status, source, occurred_at)
		VALUES
			(1, ?, 1, NULL, 'unread', 'reading', 'manual', 10),
			(2, ?, 2, NULL, 'unread', 'reading', 'web_reader', 20),
			(3, ?, 2, 2, 'reading', 'finished', 'web_reader', 21),
			(4, ?, 1, NULL, 'unread', 'dropped', 'manual', 30),
			(5, ?, 3, NULL, 'reading', 'finished', 'manual', 30)
	`, alice.ID, alice.ID, alice.ID, bob.ID, bob.ID)
	mustExec(t, database, `
		INSERT INTO user_book_reading_state
			(user_id, book_id, status, last_event_id, updated_at)
		VALUES
			(?, 1, 'reading', 1, 10),
			(?, 2, 'finished', 3, 21),
			(?, 1, 'dropped', 4, 30),
			(?, 3, 'finished', 5, 30)
	`, alice.ID, alice.ID, bob.ID, bob.ID)

	if err := database.Transact(context.Background(), func(tx *Tx) error {
		_, err := MergeDuplicateBooks(tx, FullVisibilityScope(), DuplicateMergeRequest{
			SurvivorID: 1,
			BookIDs:    []int64{1, 2, 3},
			DeletedBy:  alice.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("MergeDuplicateBooks: %v", err)
	}

	aliceState, err := GetReadingStatus(database.Read(t.Context()), alice.ID, 1)
	if err != nil || aliceState.Status != ReadingStatusFinished || aliceState.LastEventID != 3 || aliceState.UpdatedAt != 21 {
		t.Fatalf("alice merged state = %+v, err %v; want newest loser state", aliceState, err)
	}
	bobState, err := GetReadingStatus(database.Read(t.Context()), bob.ID, 1)
	if err != nil || bobState.Status != ReadingStatusDropped || bobState.LastEventID != 4 || bobState.UpdatedAt != 30 {
		t.Fatalf("bob merged state = %+v, err %v; want survivor on timestamp tie", bobState, err)
	}

	var loserStates, survivorEvents int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM user_book_reading_state WHERE book_id IN (2, 3)").Scan(&loserStates); err != nil {
		t.Fatalf("count loser reading states: %v", err)
	}
	if loserStates != 0 {
		t.Fatalf("loser reading states = %d, want 0", loserStates)
	}
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM user_book_reading_events WHERE book_id = 1").Scan(&survivorEvents); err != nil {
		t.Fatalf("count survivor reading events: %v", err)
	}
	if survivorEvents != 5 {
		t.Fatalf("survivor reading events = %d, want all 5 histories", survivorEvents)
	}

	undone, err := database.UndoAutomaticReadingStatus(context.Background(), alice.ID, 1, 3)
	if err != nil || undone.State.Status != ReadingStatusReading || undone.State.LastEventID != 2 {
		t.Fatalf("undo selected merged history = %+v, err %v", undone, err)
	}
	if err := database.Transact(context.Background(), func(tx *Tx) error {
		if err := PurgeBook(tx, 2); err != nil {
			return err
		}
		return PurgeBook(tx, 3)
	}); err != nil {
		t.Fatalf("purge merged losers: %v", err)
	}
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM user_book_reading_events WHERE book_id = 1").Scan(&survivorEvents); err != nil {
		t.Fatalf("count reading events after purge: %v", err)
	}
	if survivorEvents != 5 {
		t.Fatalf("reading events after loser purge = %d, want 5", survivorEvents)
	}
}

func TestDuplicateMatchKey(t *testing.T) {
	tests := []struct {
		title  string
		author string
		want   string
	}{
		{"Foundation", "Isaac Asimov", "foundation|isaac asimov"},
		{"foundation!", "isaac   asimov", "foundation|isaac asimov"},
		{"Мастер и Маргарита", "Михаил Булгаков", "мастер и маргарита|михаил булгаков"},
		{"The 3-Body Problem", "Cixin Liu", "the 3body problem|cixin liu"},
		{"A.B.C.", "X Y", "abc|x y"},
		{"   Spaces   ", "  Author  ", "spaces|author"},
		{"123", "456", "123|456"},
		{"Mixed_#_*Chars", "Author, Name", "mixedchars|author name"},
	}

	for _, tt := range tests {
		got := duplicateMatchKey(tt.title, tt.author)
		if got != tt.want {
			t.Errorf("duplicateMatchKey(%q, %q) = %q; want %q", tt.title, tt.author, got, tt.want)
		}
	}
}

func insertDuplicateBooks(t *testing.T, database *DB, ids ...int64) {
	t.Helper()
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Isaac Asimov', 'Asimov, Isaac')")
	for i, id := range ids {
		insertDuplicateBook(t, database, id, "Foundation", 100-i)
	}
}

func insertDuplicateBook(t *testing.T, database *DB, bookID int64, title string, addedAt int) {
	t.Helper()
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at) VALUES (?, ?, ?, ?)", bookID, title, title, addedAt)
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 1, 0)", bookID)
}
