package db

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func TestGetPossibleDuplicates(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Isaac Asimov', 'Asimov, Isaac')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'Other Author', 'Other Author')")

	// w1 and w2 are duplicates (different case, punctuation)
	database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w1', 'Foundation', 'Foundation', 30)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")

	database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w2', 'foundation!', 'Foundation', 20)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a1', 0)")

	// w3 is unrelated
	database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w3', 'Other Book', 'Other Book', 10)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w3', 'a1', 0)")

	// Same normalized title, different primary author: not a duplicate.
	database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w4', 'Foundation', 'Foundation', 25)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w4', 'a2', 0)")

	// Deleted rows do not create duplicate cleanup items.
	database.Exec("INSERT INTO works (id, title, sort_title, added_at, deleted_at) VALUES ('w_deleted', 'foundation?', 'Foundation', 40, 40)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w_deleted', 'a1', 0)")

	count, groups, err := GetPossibleDuplicates(database, FullVisibilityScope(), 10)
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
	if groups[0].Books[0].ID != "w1" || groups[0].Books[1].ID != "w2" {
		t.Errorf("Expected duplicate books in added_at order [w1 w2], got [%s %s]", groups[0].Books[0].ID, groups[0].Books[1].ID)
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

	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Author', 'Author')")
	for i, title := range []string{"Alpha", "Beta", "Gamma"} {
		for j := range 2 {
			workID := fmt.Sprintf("w_%d_%d", i, j)
			database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES (?, ?, ?, ?)", workID, title, title, 100-i*10-j)
			database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'a1', 0)", workID)
		}
	}

	count, groups, err := GetPossibleDuplicates(database, FullVisibilityScope(), 2)
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
	insertDuplicateWorks(t, database, "w1", "w2")

	user, err := database.CreateUser("curator", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		return DismissDuplicateGroup(tx, FullVisibilityScope(), []string{"w1", "w2"}, user.ID)
	}); err != nil {
		t.Fatalf("DismissDuplicateGroup: %v", err)
	}

	count, groups, err := GetPossibleDuplicates(database, FullVisibilityScope(), 10)
	if err != nil {
		t.Fatalf("GetPossibleDuplicates after dismiss: %v", err)
	}
	if count != 0 || len(groups) != 0 {
		t.Fatalf("dismissed duplicates count=%d groups=%d, want none", count, len(groups))
	}

	insertDuplicateWork(t, database, "w3", "Foundation?", 10)
	count, groups, err = GetPossibleDuplicates(database, FullVisibilityScope(), 10)
	if err != nil {
		t.Fatalf("GetPossibleDuplicates after third copy: %v", err)
	}
	if count != 1 || len(groups) != 1 || len(groups[0].Books) != 3 {
		t.Fatalf("duplicates after third copy count=%d groups=%d books=%d, want one 3-book group", count, len(groups), len(groups[0].Books))
	}
}

func TestMergeDuplicateWorksMovesAssetsShelvesAndSafeFillIns(t *testing.T) {
	database := newTestDB(t)
	insertDuplicateWorks(t, database, "w1", "w2")

	user, err := database.CreateUser("curator", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	database.Exec("UPDATE works SET description = NULL, cover_version = 0 WHERE id = 'w1'")
	database.Exec("UPDATE works SET description = 'Loser description', cover_version = 2 WHERE id = 'w2'")
	database.Exec(`
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, is_primary, can_read, created_at)
		VALUES
			('asset_epub', 'w1', 'A/Foundation/asset_epub.epub', 'asset_epub.epub', '.epub', 'epub', 1, 0, 10),
			('asset_pdf', 'w2', 'A/Foundation/asset_pdf.pdf', 'asset_pdf.pdf', '.pdf', 'pdf', 1, 1, 20)
	`)
	database.Exec("INSERT INTO shelves (id, name, kind, owner_id, position) VALUES ('s1', 'Shelf', 'manual', ?, 1)", user.ID)
	database.Exec("INSERT INTO shelf_books (shelf_id, work_id, position) VALUES ('s1', 'w2', 5)")
	database.Exec(`
		INSERT INTO delivery_jobs (id, user_id, device_name, device_email, preset, work_id, asset_id, title, filename)
		VALUES ('dj1', ?, 'Device', 'reader@example.test', 'generic', 'w2', 'asset_pdf', 'Foundation', 'asset_pdf.pdf')
	`, user.ID)
	if _, err := database.Exec(`
		INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at)
		VALUES (?, 'asset_pdf', 0.4, '{}', 20, 20)
	`, user.ID); err != nil {
		t.Fatalf("seed loser reader state: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO user_annotations (id, user_id, asset_id, cfi, quote, note)
		VALUES ('ann_pdf', ?, 'asset_pdf', '/6/2', 'Quote', 'Note')
	`, user.ID); err != nil {
		t.Fatalf("seed loser annotation: %v", err)
	}

	var result DuplicateMergeResult
	if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		var err error
		result, err = MergeDuplicateWorks(tx, FullVisibilityScope(), DuplicateMergeRequest{
			SurvivorID:  "w1",
			WorkIDs:     []string{"w1", "w2"},
			DeletedBy:   user.ID,
			CoverFromID: "w2",
		})
		return err
	}); err != nil {
		t.Fatalf("MergeDuplicateWorks: %v", err)
	}
	if !result.FilledDescription || !result.FilledCover {
		t.Fatalf("merge fill-ins = desc:%v cover:%v, want both", result.FilledDescription, result.FilledCover)
	}

	var deletedAt sql.NullInt64
	if err := database.QueryRow("SELECT deleted_at FROM works WHERE id = 'w2'").Scan(&deletedAt); err != nil {
		t.Fatalf("query loser deleted_at: %v", err)
	}
	if !deletedAt.Valid {
		t.Fatalf("loser was not moved to trash")
	}

	var description string
	var coverVersion int
	if err := database.QueryRow("SELECT description, cover_version FROM works WHERE id = 'w1'").Scan(&description, &coverVersion); err != nil {
		t.Fatalf("query survivor fill-ins: %v", err)
	}
	if description != "Loser description" || coverVersion != 1 {
		t.Fatalf("survivor desc=%q cover_version=%d, want loser description and version 1", description, coverVersion)
	}
	var metadataRev int
	if err := database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w1'").Scan(&metadataRev); err != nil {
		t.Fatalf("query survivor metadata_rev: %v", err)
	}
	if metadataRev != 0 {
		t.Fatalf("survivor metadata_rev = %d, want DB mutation to leave bookkeeping to caller", metadataRev)
	}

	var assetCount, primaryCount int
	if err := database.QueryRow("SELECT COUNT(*), SUM(is_primary) FROM assets WHERE work_id = 'w1'").Scan(&assetCount, &primaryCount); err != nil {
		t.Fatalf("query survivor assets: %v", err)
	}
	if assetCount != 2 || primaryCount != 1 {
		t.Fatalf("survivor assets=%d primaries=%d, want 2 assets and 1 primary", assetCount, primaryCount)
	}
	var primaryAssetID string
	if err := database.QueryRow("SELECT id FROM assets WHERE work_id = 'w1' AND is_primary = 1").Scan(&primaryAssetID); err != nil {
		t.Fatalf("query survivor primary asset: %v", err)
	}
	if primaryAssetID != "asset_pdf" {
		t.Fatalf("survivor primary asset = %q, want moved readable asset_pdf", primaryAssetID)
	}

	var shelfRows int
	if err := database.QueryRow("SELECT COUNT(*) FROM shelf_books WHERE shelf_id = 's1' AND work_id = 'w1'").Scan(&shelfRows); err != nil {
		t.Fatalf("query shelf membership: %v", err)
	}
	if shelfRows != 1 {
		t.Fatalf("survivor shelf rows = %d, want 1", shelfRows)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM shelf_books WHERE work_id = 'w2'").Scan(&shelfRows); err != nil {
		t.Fatalf("query loser shelf membership: %v", err)
	}
	if shelfRows != 0 {
		t.Fatalf("loser shelf rows = %d, want 0", shelfRows)
	}

	var deliveryWorkID string
	if err := database.QueryRow("SELECT work_id FROM delivery_jobs WHERE id = 'dj1'").Scan(&deliveryWorkID); err != nil {
		t.Fatalf("query delivery job: %v", err)
	}
	if deliveryWorkID != "w1" {
		t.Fatalf("delivery job work_id = %s, want w1", deliveryWorkID)
	}

	readerState, err := database.GetReaderState(user.ID, "asset_pdf")
	if err != nil || readerState.WorkID != "w1" || readerState.Progress != 0.4 {
		t.Fatalf("moved asset reader state = %+v, err %v; want stable asset state on survivor", readerState, err)
	}
	annotations, err := database.ListAnnotations(user.ID, "asset_pdf")
	if err != nil || len(annotations) != 1 || annotations[0].ID != "ann_pdf" {
		t.Fatalf("moved asset annotations = %+v, err %v; want annotation retained", annotations, err)
	}
}

func TestMergeDuplicateWorksLeavesMetadataRevToCaller(t *testing.T) {
	database := newTestDB(t)
	insertDuplicateWorks(t, database, "w1", "w2")
	user, err := database.CreateUser("curator", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	database.Exec("UPDATE works SET description = 'Survivor description', cover_version = 1 WHERE id = 'w1'")
	database.Exec("UPDATE works SET description = 'Loser description', cover_version = 0 WHERE id = 'w2'")
	database.Exec(`
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, is_primary, writeback_rev, created_at)
		VALUES ('asset_loser_epub', 'w2', 'A/Foundation/asset_loser_epub.epub', 'asset_loser_epub.epub', '.epub', 'epub', 1, 0, 20)
	`)

	var result DuplicateMergeResult
	if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		var err error
		result, err = MergeDuplicateWorks(tx, FullVisibilityScope(), DuplicateMergeRequest{
			SurvivorID: "w1",
			WorkIDs:    []string{"w1", "w2"},
			DeletedBy:  user.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("MergeDuplicateWorks: %v", err)
	}
	if result.FilledDescription || result.FilledCover {
		t.Fatalf("merge fill-ins = desc:%v cover:%v, want none", result.FilledDescription, result.FilledCover)
	}

	var metadataRev int
	if err := database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w1'").Scan(&metadataRev); err != nil {
		t.Fatalf("query survivor metadata_rev: %v", err)
	}
	if metadataRev != 0 {
		t.Fatalf("survivor metadata_rev = %d, want DB mutation to leave bookkeeping to caller", metadataRev)
	}
	counts, err := CountDirtyMetadataWritebackAssets(database, FullVisibilityScope())
	if err != nil {
		t.Fatalf("CountDirtyMetadataWritebackAssets: %v", err)
	}
	if counts.Dirty != 0 {
		t.Fatalf("dirty writeback assets = %d, want DB mutation to leave bookkeeping to caller", counts.Dirty)
	}
}

func TestMergeDuplicateWorksMergesPerUserReadingStateAndHistories(t *testing.T) {
	database := newTestDB(t)
	insertDuplicateWorks(t, database, "w1", "w2", "w3")

	alice, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser("bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO user_work_reading_events
			(id, user_id, work_id, previous_event_id, from_status, to_status, source, occurred_at)
		VALUES
			('alice-survivor-reading', ?, 'w1', NULL, 'unread', 'reading', 'manual', 10),
			('alice-loser-reading', ?, 'w2', NULL, 'unread', 'reading', 'web_reader', 20),
			('alice-loser-finished', ?, 'w2', 'alice-loser-reading', 'reading', 'finished', 'web_reader', 21),
			('bob-survivor-dropped', ?, 'w1', NULL, 'unread', 'dropped', 'manual', 30),
			('bob-loser-finished', ?, 'w3', NULL, 'reading', 'finished', 'manual', 30)
	`, alice.ID, alice.ID, alice.ID, bob.ID, bob.ID); err != nil {
		t.Fatalf("seed reading events: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO user_work_reading_state
			(user_id, work_id, status, last_event_id, updated_at)
		VALUES
			(?, 'w1', 'reading', 'alice-survivor-reading', 10),
			(?, 'w2', 'finished', 'alice-loser-finished', 21),
			(?, 'w1', 'dropped', 'bob-survivor-dropped', 30),
			(?, 'w3', 'finished', 'bob-loser-finished', 30)
	`, alice.ID, alice.ID, bob.ID, bob.ID); err != nil {
		t.Fatalf("seed reading state: %v", err)
	}

	if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		_, err := MergeDuplicateWorks(tx, FullVisibilityScope(), DuplicateMergeRequest{
			SurvivorID: "w1",
			WorkIDs:    []string{"w1", "w2", "w3"},
			DeletedBy:  alice.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("MergeDuplicateWorks: %v", err)
	}

	aliceState, err := GetReadingStatus(database, alice.ID, "w1")
	if err != nil || aliceState.Status != ReadingStatusFinished || aliceState.LastEventID != "alice-loser-finished" || aliceState.UpdatedAt != 21 {
		t.Fatalf("alice merged state = %+v, err %v; want newest loser state", aliceState, err)
	}
	bobState, err := GetReadingStatus(database, bob.ID, "w1")
	if err != nil || bobState.Status != ReadingStatusDropped || bobState.LastEventID != "bob-survivor-dropped" || bobState.UpdatedAt != 30 {
		t.Fatalf("bob merged state = %+v, err %v; want survivor on timestamp tie", bobState, err)
	}

	var loserStates, survivorEvents int
	if err := database.QueryRow("SELECT COUNT(*) FROM user_work_reading_state WHERE work_id IN ('w2', 'w3')").Scan(&loserStates); err != nil {
		t.Fatalf("count loser reading states: %v", err)
	}
	if loserStates != 0 {
		t.Fatalf("loser reading states = %d, want 0", loserStates)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM user_work_reading_events WHERE work_id = 'w1'").Scan(&survivorEvents); err != nil {
		t.Fatalf("count survivor reading events: %v", err)
	}
	if survivorEvents != 5 {
		t.Fatalf("survivor reading events = %d, want all 5 histories", survivorEvents)
	}

	undone, err := database.UndoAutomaticReadingStatus(context.Background(), alice.ID, "w1", "alice-loser-finished")
	if err != nil || undone.State.Status != ReadingStatusReading || undone.State.LastEventID != "alice-loser-reading" {
		t.Fatalf("undo selected merged history = %+v, err %v", undone, err)
	}
	if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		if err := PurgeWork(tx, "w2"); err != nil {
			return err
		}
		return PurgeWork(tx, "w3")
	}); err != nil {
		t.Fatalf("purge merged losers: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM user_work_reading_events WHERE work_id = 'w1'").Scan(&survivorEvents); err != nil {
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

func insertDuplicateWorks(t *testing.T, database *DB, ids ...string) {
	t.Helper()
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a_dup', 'Isaac Asimov', 'Asimov, Isaac')")
	for i, id := range ids {
		insertDuplicateWork(t, database, id, "Foundation", 100-i)
	}
}

func insertDuplicateWork(t *testing.T, database *DB, workID, title string, addedAt int) {
	t.Helper()
	database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES (?, ?, ?, ?)", workID, title, title, addedAt)
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'a_dup', 0)", workID)
}
