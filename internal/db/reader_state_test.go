package db

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestResetReaderStatePreservesAnnotationsAndOtherUsers(t *testing.T) {
	database := newTestDB(t)

	user := mustUser(t, database, "reader", RoleMember)
	other := mustUser(t, database, "other", RoleMember)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T1', 'T1')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, is_primary, original_sha256, current_sha256) VALUES (1, 1, 'books/a.epub', 'a.epub', '.epub', 1, randomblob(32), randomblob(32))")

	locator := Locator{CFI: "epubcfi(/6/2)"}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, testReaderWrite(0.42, locator, 0), ReadingStatusSourceWebReader); err != nil {
		t.Fatalf("SaveReaderStateAndAdvanceStatus: %v", err)
	}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), other.ID, 1, testReaderWrite(0.75, locator, 0), ReadingStatusSourceWebReader); err != nil {
		t.Fatalf("SaveReaderStateAndAdvanceStatus other: %v", err)
	}
	annotation, err := database.CreateAnnotation(t.Context(), user.ID, 1, testAnnotation("epubcfi(/6/2!/4/2)", "keep this highlight"))
	if err != nil {
		t.Fatalf("CreateAnnotation before reset: %v", err)
	}
	if err := database.ResetReaderState(t.Context(), user.ID, 1, 1); err != nil {
		t.Fatalf("ResetReaderState: %v", err)
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil {
		t.Fatalf("GetReaderState after reset: %v", err)
	}
	if state.Progress != 0 || !state.Locator.IsZero() || state.Revision != 2 || state.UpdatedAt != 0 {
		t.Fatalf("reader state after reset = %+v", state)
	}
	annotations, err := ListAnnotations(database.Read(t.Context()), user.ID, 1)
	if err != nil {
		t.Fatalf("ListAnnotations after state reset: %v", err)
	}
	if len(annotations) != 1 || annotations[0].ID != annotation.ID {
		t.Fatalf("annotations after state reset = %+v, want annotation %d", annotations, annotation.ID)
	}
	otherState, err := GetReaderState(database.Read(t.Context()), other.ID, 1)
	if err != nil {
		t.Fatalf("GetReaderState other after reset: %v", err)
	}
	if otherState.Progress != 0.75 {
		t.Fatalf("other user progress after reset = %v, want 0.75", otherState.Progress)
	}
}

func TestTouchReaderStateAndAdvanceStatusRollsBackTogether(t *testing.T) {
	database := newTestDB(t)

	user := mustUser(t, database, "reader", RoleMember)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (1, 1, 'book.epub', 'book.epub', '.epub', randomblob(32), randomblob(32));
		CREATE TRIGGER fail_reader_open_status
		BEFORE INSERT ON user_book_reading_events
		BEGIN
			SELECT RAISE(ABORT, 'status write failed');
		END;
	`)

	if _, _, err := database.TouchReaderStateAndAdvanceStatus(
		context.Background(), user.ID, 1, ReadingStatusSourceWebReader,
	); err == nil {
		t.Fatal("touch succeeded despite status failure")
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil {
		t.Fatalf("get state after rollback: %v", err)
	}
	if state.UpdatedAt != 0 {
		t.Fatalf("reader touch survived status rollback: %+v", state)
	}
}

func TestSaveReaderStateAndStatusCommitTogether(t *testing.T) {
	database := newTestDB(t)

	user := mustUser(t, database, "reader-atomic", RoleReader)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (108, 'Atomic', 'Atomic');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (1, 108, 'atomic.epub', 'atomic.epub', '.epub', randomblob(32), randomblob(32));
		CREATE TRIGGER reject_atomic_status
		BEFORE INSERT ON user_book_reading_events
		BEGIN
			SELECT RAISE(ABORT, 'status write rejected');
		END;
	`)

	locator := Locator{CFI: "epubcfi(/6/2)"}

	if _, _, err := database.SaveReaderStateAndAdvanceStatus(
		context.Background(), user.ID, 1, testReaderWrite(0.4, locator, 0), ReadingStatusSourceWebReader,
	); err == nil {
		t.Fatal("atomic save succeeded with rejecting status trigger")
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil {
		t.Fatalf("state after rollback: %v", err)
	}
	if state.Progress != 0 || state.UpdatedAt != 0 {
		t.Fatalf("reader position survived rolled-back status write: %+v", state)
	}
	status, err := GetReadingStatus(database.Read(t.Context()), user.ID, 108)
	if err != nil || status.Status != ReadingStatusUnread {
		t.Fatalf("status after rollback = %+v, err %v", status, err)
	}
	mustExec(t, database, "DROP TRIGGER reject_atomic_status")

	saved, change, err := database.SaveReaderStateAndAdvanceStatus(
		context.Background(), user.ID, 1, testReaderWrite(0.4, locator, 0), ReadingStatusSourceWebReader,
	)
	if err != nil {
		t.Fatalf("atomic save: %v", err)
	}
	if saved.Progress != 0.4 || !change.Changed || change.State.Status != ReadingStatusReading {
		t.Fatalf("atomic result state=%+v change=%+v", saved, change)
	}
}

func testReaderWrite(progress float64, locator Locator, revision int64) ReaderPositionWrite {
	return ReaderPositionWrite{Revision: revision,
		Progress: progress, Locator: locator,
		DeviceID: "urn:uuid:test-reader", DeviceName: "Test reader"}
}

func testAnnotation(cfi, quote string) AnnotationCreate {
	return AnnotationCreate{Locator: Locator{CFI: cfi, Path: "OPS/chapter.xhtml"}, Quote: quote}
}

func TestAnnotationSelectionRetriesConflictsAndDeletion(t *testing.T) {
	database := newTestDB(t)
	alice := mustUser(t, database, "alice", RoleMember)
	mustExec(t, database, "INSERT INTO books (id,title,sort_title) VALUES (1,'Book','Book')")
	mustExec(t, database, "INSERT INTO assets (id,book_id,storage_path,filename,extension,original_sha256,current_sha256) VALUES (1,1,'a.epub','a.epub','.epub',randomblob(32),randomblob(32))")
	input := testAnnotation("epubcfi(/6/2!/4/2)", "  "+strings.Repeat("Я𐐀\n", 1200)+"  ")
	input.ContextBefore, input.ContextAfter = " before\n", "\n after "
	created, err := database.CreateAnnotation(t.Context(), alice.ID, 1, input)
	if err != nil || created.Quote != input.Quote || created.Locator.CFI != input.Locator.CFI || created.Locator.Path != input.Locator.Path || created.ContextBefore != input.ContextBefore || created.ContextAfter != input.ContextAfter {
		t.Fatalf("full annotation: %+v %v", created, err)
	}
	edit := AnnotationUpdate{Revision: 1, Note: new("  keep whitespace\n"), Color: new("blue")}
	updated, err := database.UpdateAnnotation(t.Context(), alice.ID, 1, created.ID, edit)
	if err != nil || updated.Revision != 2 || updated.Note != *edit.Note {
		t.Fatalf("edit: %+v %v", updated, err)
	}
	mustExec(t, database, "UPDATE user_annotations SET updated_at = 100 WHERE id = ?", created.ID)
	for _, revision := range []int64{1, 2} {
		edit.Revision = revision
		retry, err := database.UpdateAnnotation(t.Context(), alice.ID, 1, created.ID, edit)
		if err != nil || retry.Revision != 2 || retry.UpdatedAt != 100 {
			t.Fatalf("no-op edit: %+v %v", retry, err)
		}
	}
	// A repeat selection cannot overwrite a later edit, even if the client
	// submits a different note/color or has stale quote/context information.
	input.Note, input.Color = "old note", "pink"
	input.Locator.Path = "" // The CFI still identifies the same selection.
	retry, err := database.CreateAnnotation(t.Context(), alice.ID, 1, input)
	if err != nil || retry.ID != created.ID || retry.Note != updated.Note || retry.Color != updated.Color || retry.Revision != 2 || retry.UpdatedAt != 100 {
		t.Fatalf("late create retry: %+v %v", retry, err)
	}
	updated, err = database.UpdateAnnotation(t.Context(), alice.ID, 1, created.ID, AnnotationUpdate{Revision: 2, Color: new("green")})
	if err != nil || updated.Revision != 3 {
		t.Fatalf("concurrent color edit: %+v %v", updated, err)
	}
	retry, err = database.UpdateAnnotation(t.Context(), alice.ID, 1, created.ID, AnnotationUpdate{Revision: 1, Note: edit.Note})
	if err != nil || retry.Revision != 3 || retry.Color != "green" {
		t.Fatalf("note retry overwrote another field: %+v %v", retry, err)
	}
	for _, stale := range []AnnotationUpdate{
		{Revision: 1, Note: new("stale")},
		{Revision: 1, Note: edit.Note, Color: new("blue")},
	} {
		if _, err := database.UpdateAnnotation(t.Context(), alice.ID, 1, created.ID, stale); !errors.Is(err, ErrReadingConflict) {
			t.Fatalf("stale update: %v", err)
		}
	}
	if err := database.DeleteAnnotation(t.Context(), alice.ID, 1, created.ID, 2); !errors.Is(err, ErrReadingConflict) {
		t.Fatalf("stale deletion: %v", err)
	}
	if err := database.DeleteAnnotation(t.Context(), alice.ID, 1, created.ID, 3); err != nil {
		t.Fatal(err)
	}
	mustExec(t, database, "UPDATE user_annotations SET updated_at = 123 WHERE id = ?", created.ID)
	if err := database.DeleteAnnotation(t.Context(), alice.ID, 1, created.ID, 1); err != nil {
		t.Fatal(err)
	}
	deleted, err := getAnnotation(database.Read(t.Context()), alice.ID, 1, created.ID)
	if err != nil || !deleted.Deleted || deleted.Revision != 4 || deleted.UpdatedAt != 123 || deleted.Quote != "" || deleted.Note != "" || deleted.Locator.CFI != "" || deleted.Locator.Path != "" || deleted.ContextBefore != "" || deleted.ContextAfter != "" {
		t.Fatalf("tombstone: %+v %v", deleted, err)
	}
	recreated, err := database.CreateAnnotation(t.Context(), alice.ID, 1, input)
	if err != nil || recreated.ID == created.ID || recreated.Revision != 1 {
		t.Fatalf("new selection after deletion: %+v %v", recreated, err)
	}
	if err := database.DeleteAnnotation(t.Context(), alice.ID, 1, created.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateAnnotation(t.Context(), alice.ID, 1, created.ID, edit); !errors.Is(err, ErrReadingConflict) {
		t.Fatalf("edited deleted annotation: %v", err)
	}
	live, err := ListAnnotations(database.Read(t.Context()), alice.ID, 1)
	if err != nil || len(live) != 1 || live[0].ID != recreated.ID {
		t.Fatalf("old deletion affected new selection: %+v %v", live, err)
	}
	for _, length := range []int{MaxAnnotationQuoteLength, MaxAnnotationQuoteLength + 1} {
		_, err := database.CreateAnnotation(t.Context(), alice.ID, 1, testAnnotation("epubcfi(/6/4)", strings.Repeat("𐐀", length)))
		if (length <= MaxAnnotationQuoteLength) != (err == nil) {
			t.Fatalf("quote limit %d: %v", length, err)
		}
	}
}

func TestListContinueReading(t *testing.T) {
	database := newTestDB(t)

	alice := mustUser(t, database, "alice", RoleMember)
	bob := mustUser(t, database, "bob", RoleMember)

	mustExec := func(query string, args ...any) {
		t.Helper()
		mustExec(t, database, query, args...)

	}

	mustExec("INSERT INTO authors (id, name, sort_name) VALUES (1, 'Author One', 'Author One')")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (1, 'Newest per Book', 'Newest per Book', NULL)")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (2, 'Opened at Start', 'Opened at Start', NULL)")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (125, 'Done', 'Done', NULL)")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (122, 'Deleted', 'Deleted', 123)")
	for _, bookID := range []int64{1, 2, 125, 122} {
		mustExec("INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 1, 0)", bookID)
	}
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256) VALUES (1, 1, 'old.epub', 'old.epub', '.epub', randomblob(32), randomblob(32))")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256) VALUES (2, 1, 'new.fb2', 'new.fb2', '.fb2', randomblob(32), randomblob(32))")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256) VALUES (3, 2, 'zero.pdf', 'zero.pdf', '.pdf', randomblob(32), randomblob(32))")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256) VALUES (4, 125, 'done.pdf', 'done.pdf', '.pdf', randomblob(32), randomblob(32))")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256) VALUES (5, 125, 'done.epub', 'done.epub', '.epub', randomblob(32), randomblob(32))")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256) VALUES (6, 122, 'deleted.epub', 'deleted.epub', '.epub', randomblob(32), randomblob(32))")

	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, updated_at) VALUES (?, 1, 0.2, 10)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, updated_at) VALUES (?, 2, 0.4, 20)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, updated_at) VALUES (?, 3, 0, 30)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, updated_at) VALUES (?, 4, 0.8, 35)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, updated_at) VALUES (?, 5, 1, 40)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, updated_at) VALUES (?, 6, 0.5, 50)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, updated_at) VALUES (?, 2, 0.8, 60)", bob.ID)
	mustExec("INSERT INTO user_book_reading_state (user_id, book_id, status) VALUES (?, 1, 'reading')", alice.ID)
	mustExec("INSERT INTO user_book_reading_state (user_id, book_id, status) VALUES (?, 2, 'reading')", alice.ID)
	mustExec("INSERT INTO user_book_reading_state (user_id, book_id, status) VALUES (?, 125, 'finished')", alice.ID)
	mustExec("INSERT INTO user_book_reading_state (user_id, book_id, status) VALUES (?, 122, 'reading')", alice.ID)
	mustExec("INSERT INTO user_book_reading_state (user_id, book_id, status) VALUES (?, 1, 'reading')", bob.ID)

	rows, err := ListContinueReading(database.Read(t.Context()), FullVisibilityScope(), alice.ID, 10)
	if err != nil {
		t.Fatalf("ListContinueReading: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].ID != 2 || rows[0].AssetID != 3 || rows[0].Progress != 0 {
		t.Fatalf("first row = %+v, want zero-progress 2", rows[0])
	}
	if rows[1].ID != 1 || rows[1].AssetID != 2 || rows[1].Progress != 0.4 {
		t.Fatalf("second row = %+v, want latest asset for 1", rows[1])
	}

	// Starting a reread makes the book eligible again while preserving the
	// truthful per-format positions. The completed EPUB stays at 100%, and the
	// latest incomplete asset becomes the continuation target.
	mustExec("UPDATE user_book_reading_state SET status = 'reading' WHERE user_id = ? AND book_id = 125", alice.ID)
	rows, err = ListContinueReading(database.Read(t.Context()), FullVisibilityScope(), alice.ID, 10)
	if err != nil {
		t.Fatalf("ListContinueReading after reread: %v", err)
	}
	if len(rows) != 3 || rows[0].ID != 125 || rows[0].AssetID != 4 || rows[0].Progress != 0.8 {
		t.Fatalf("reread rows = %+v, want incomplete 125 asset first", rows)
	}
}

func TestReaderPositionRevisionRetryAndReset(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "revision-reader", RoleReader)
	mustExec(t, database, `INSERT INTO books(id,title,sort_title) VALUES(1,'Book','Book');
        INSERT INTO assets(id,book_id,storage_path,filename,extension,original_sha256,current_sha256)
        VALUES(1,1,'book.epub','book.epub','.epub',randomblob(32),randomblob(32));`)
	input := testReaderWrite(.3, Locator{CFI: "epubcfi(/6/2)"}, 0)
	first, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, input, ReadingStatusSourceWebReader)
	if err != nil || first.Revision != 1 {
		t.Fatalf("first save: %+v %v", first, err)
	}
	if _, err := database.SetReadingStatus(t.Context(), user.ID, 1, ReadingStatusUnread, ReadingStatusSourceManual); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		revision   int64
		deviceID   string
		deviceName string
	}{
		{"same device, stale revision", 0, input.DeviceID, input.DeviceName},
		{"same device, current revision", 1, input.DeviceID, input.DeviceName},
		{"another device", 0, "urn:reader:another", input.DeviceName},
		{"renamed device", 0, input.DeviceID, "Another name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustExec(t, database, "UPDATE user_asset_state SET updated_at=100 WHERE user_id=? AND asset_id=1", user.ID)
			equivalent := input
			equivalent.Revision = tc.revision
			equivalent.DeviceID = tc.deviceID
			equivalent.DeviceName = tc.deviceName
			equivalent.Locator = Locator{CFI: "epubcfi(/6/2)"}
			retry, change, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, equivalent, ReadingStatusSourceWebReader)
			if err != nil || retry.Revision != 1 || retry.UpdatedAt <= 100 || retry.Progress != first.Progress || !retry.Locator.Equal(first.Locator) || change.Changed || change.State.Status != ReadingStatusUnread {
				t.Fatalf("equivalent save must only refresh recency: %+v %+v %v", retry, change, err)
			}
			if retry.DeviceID != first.DeviceID || retry.DeviceName != first.DeviceName {
				t.Fatalf("equivalent save replaced the position source: %+v", retry)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*ReaderPositionWrite)
	}{
		{"progress", func(input *ReaderPositionWrite) { input.Progress = .8 }},
		{"locator", func(input *ReaderPositionWrite) {
			input.Locator = Locator{CFI: "epubcfi(/6/4)"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stale := input
			tc.change(&stale)
			if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, stale, ReadingStatusSourceWebReader); !errors.Is(err, ErrReadingConflict) {
				t.Fatalf("stale save: %v", err)
			}
		})
	}
	// Opening refreshes recency without invalidating the revision already read
	// by a client, including one about to save an intentional backward move.
	mustExec(t, database, "UPDATE user_asset_state SET updated_at=100 WHERE user_id=? AND asset_id=1", user.ID)
	opened, _, err := database.TouchReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, ReadingStatusSourceWebReader)
	if err != nil || opened.Revision != first.Revision || opened.Progress != first.Progress || !opened.Locator.Equal(first.Locator) || opened.UpdatedAt <= 100 {
		t.Fatalf("reopen: %+v %v", opened, err)
	}
	next := input
	next.Revision = first.Revision
	next.Progress = .2
	next.DeviceID = "urn:reader:another"
	next.DeviceName = "Another reader"
	beforeReset, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, next, ReadingStatusSourceWebReader)
	if err != nil || beforeReset.Revision != first.Revision+1 || beforeReset.Progress != next.Progress || !beforeReset.Locator.Equal(first.Locator) {
		t.Fatalf("overwrite: %+v %v", beforeReset, err)
	}
	if beforeReset.DeviceID != next.DeviceID || beforeReset.DeviceName != next.DeviceName {
		t.Fatalf("new position kept the previous source: %+v", beforeReset)
	}
	var resetState *ReaderState
	for range 2 {
		if err := database.ResetReaderState(t.Context(), user.ID, 1, beforeReset.Revision); err != nil {
			t.Fatal(err)
		}
		state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
		if err != nil || state.Revision != beforeReset.Revision+1 || state.Progress != 0 || !state.Locator.IsZero() || state.UpdatedAt != 0 {
			t.Fatalf("reset: %+v %v", state, err)
		}
		resetState = state
	}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, input, ReadingStatusSourceWebReader); !errors.Is(err, ErrReadingConflict) {
		t.Fatalf("late save undid reset: %v", err)
	}
	next.Revision = resetState.Revision
	next.Progress = .1
	continued, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, next, ReadingStatusSourceWebReader)
	if err != nil || continued.Revision != resetState.Revision+1 || continued.Progress != .1 {
		t.Fatalf("reading after reset: %+v %v", continued, err)
	}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, input, ReadingStatusSourceWebReader); !errors.Is(err, ErrReadingConflict) {
		t.Fatalf("late save overwrote reading after reset: %v", err)
	}
	if err := database.ResetReaderState(t.Context(), user.ID, 1, beforeReset.Revision); !errors.Is(err, ErrReadingConflict) {
		t.Fatalf("late reset erased new position: %v", err)
	}
}

func TestResetEmptyReaderStateAdvancesRevision(t *testing.T) {
	for _, name := range []string{"unopened", "opened"} {
		t.Run(name, func(t *testing.T) {
			database := newTestDB(t)
			user := mustUser(t, database, "reset-reader", RoleReader)
			mustExec(t, database, `INSERT INTO books(id,title,sort_title) VALUES(1,'Book','Book');
                INSERT INTO assets(id,book_id,storage_path,filename,extension,original_sha256,current_sha256)
                VALUES(1,1,'book.epub','book.epub','.epub',randomblob(32),randomblob(32));`)
			if name == "opened" {
				if _, _, err := database.TouchReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, ReadingStatusSourceWebReader); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.ResetReaderState(t.Context(), user.ID, 1, 0); err != nil {
				t.Fatal(err)
			}
			state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
			if err != nil || state.Revision != 1 || state.UpdatedAt != 0 {
				t.Fatalf("empty reset: %+v %v", state, err)
			}
			pending := testReaderWrite(.3, Locator{CFI: "epubcfi(/6/2)"}, 0)
			if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, pending, ReadingStatusSourceWebReader); !errors.Is(err, ErrReadingConflict) {
				t.Fatalf("pending save undid reset: %v", err)
			}
		})
	}
}

func TestPDFAnnotationLocatorValidationAndRetry(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "pdf-annotations", RoleReader)
	mustExec(t, database, `INSERT INTO books(id,title,sort_title) VALUES(1,'PDF','PDF');
        INSERT INTO assets(id,book_id,storage_path,filename,extension,original_sha256,current_sha256)
        VALUES(1,1,'book.pdf','book.pdf','.pdf',randomblob(32),randomblob(32));`)
	locator := Locator{Page: 2, Rects: []Rect{{X: -10, Y: 100, Width: 80, Height: 12}, {X: -10, Y: 84, Width: 40, Height: 12}}}
	created, err := database.CreateAnnotation(t.Context(), user.ID, 1, AnnotationCreate{Locator: locator, Quote: "Two lines"})
	if err != nil || !created.Locator.Equal(locator) {
		t.Fatalf("create: %+v %v", created, err)
	}
	updated, err := database.UpdateAnnotation(t.Context(), user.ID, 1, created.ID, AnnotationUpdate{Revision: 1, Note: new("Keep this note")})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := database.CreateAnnotation(t.Context(), user.ID, 1, AnnotationCreate{Locator: locator, Quote: "Two lines", Note: "Stale note"})
	if err != nil || retry.ID != created.ID || retry.Note != updated.Note || !retry.Locator.Equal(locator) {
		t.Fatalf("retry lost annotation data: %+v %v", retry, err)
	}
	nextPage := locator
	nextPage.Page++
	other, err := database.CreateAnnotation(t.Context(), user.ID, 1, AnnotationCreate{Locator: nextPage, Quote: "Two lines"})
	if err != nil || other.ID == created.ID {
		t.Fatalf("different page: %+v %v", other, err)
	}
	for _, tc := range []struct {
		name    string
		locator Locator
	}{
		{"page without selection", Locator{Page: 2}},
		{"rectangles without page", Locator{Rects: locator.Rects}},
		{"negative page", Locator{Page: -2, Rects: locator.Rects}},
		{"mixed addresses", Locator{CFI: "epubcfi(/6/2)", Page: 2, Rects: locator.Rects}},
		{"empty rectangle", Locator{Page: 2, Rects: []Rect{{Width: 0, Height: 12}}}},
		{"infinite rectangle", Locator{Page: 2, Rects: []Rect{{X: math.Inf(1), Width: 10, Height: 12}}}},
		{"not a number", Locator{Page: 2, Rects: []Rect{{Y: math.NaN(), Width: 10, Height: 12}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := database.CreateAnnotation(t.Context(), user.ID, 1, AnnotationCreate{Locator: tc.locator, Quote: "Invalid"}); !errors.Is(err, ErrInvalidAnnotation) {
				t.Fatalf("invalid selection accepted: %v", err)
			}
		})
	}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, testReaderWrite(.5, Locator{Page: 2}, 0), ReadingStatusSourceWebReader); err != nil {
		t.Fatalf("page-only reading position: %v", err)
	}
}
