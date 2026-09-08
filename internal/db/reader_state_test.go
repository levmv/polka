package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResetReaderStatePreservesAnnotationsAndOtherUsers(t *testing.T) {
	database := newTestDB(t)

	user := mustUser(t, database, "reader", RoleMember)
	other := mustUser(t, database, "other", RoleMember)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T1', 'T1')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, is_primary, original_sha256, current_sha256) VALUES (1, 1, 'books/a.epub', 'a.epub', '.epub', 1, randomblob(32), randomblob(32))")

	locator, err := NewReaderLocator([]byte(`{"engine":"foliate","cfi":"epubcfi(/6/2)","fraction":0.42}`))
	if err != nil {
		t.Fatalf("NewReaderLocator: %v", err)
	}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), user.ID, 1, 0.42, locator, ReadingStatusSourceWebReader); err != nil {
		t.Fatalf("SaveReaderStateAndAdvanceStatus: %v", err)
	}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(t.Context(), other.ID, 1, 0.75, locator, ReadingStatusSourceWebReader); err != nil {
		t.Fatalf("SaveReaderStateAndAdvanceStatus other: %v", err)
	}
	annotation, err := database.CreateAnnotation(t.Context(), user.ID, 1, AnnotationCreate{
		CFI:   "epubcfi(/6/2!/4/2)",
		Quote: "keep this highlight",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation before reset: %v", err)
	}
	if err := database.ResetReaderState(t.Context(), user.ID, 1); err != nil {
		t.Fatalf("ResetReaderState: %v", err)
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil {
		t.Fatalf("GetReaderState after reset: %v", err)
	}
	if state.Progress != 0 || state.Locator.String() != "{}" || state.LastReadAt != 0 || state.UpdatedAt != 0 {
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
	if state.LastReadAt != 0 || state.UpdatedAt != 0 {
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

	locator, err := NewReaderLocator([]byte(`{"engine":"foliate","fraction":0.4}`))
	if err != nil {
		t.Fatalf("locator: %v", err)
	}

	if _, _, err := database.SaveReaderStateAndAdvanceStatus(
		context.Background(), user.ID, 1, 0.4, locator, ReadingStatusSourceWebReader,
	); err == nil {
		t.Fatal("atomic save succeeded with rejecting status trigger")
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil {
		t.Fatalf("state after rollback: %v", err)
	}
	if state.Progress != 0 || state.LastReadAt != 0 || state.UpdatedAt != 0 {
		t.Fatalf("reader position survived rolled-back status write: %+v", state)
	}
	status, err := GetReadingStatus(database.Read(t.Context()), user.ID, 108)
	if err != nil || status.Status != ReadingStatusUnread {
		t.Fatalf("status after rollback = %+v, err %v", status, err)
	}
	mustExec(t, database, "DROP TRIGGER reject_atomic_status")

	saved, change, err := database.SaveReaderStateAndAdvanceStatus(
		context.Background(), user.ID, 1, 0.4, locator, ReadingStatusSourceWebReader,
	)
	if err != nil {
		t.Fatalf("atomic save: %v", err)
	}
	if saved.Progress != 0.4 || !change.Changed || change.State.Status != ReadingStatusReading {
		t.Fatalf("atomic result state=%+v change=%+v", saved, change)
	}
}

func TestAnnotationUpsertAndTextLimits(t *testing.T) {
	database := newTestDB(t)

	alice := mustUser(t, database, "alice", RoleMember)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T1', 'T1')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, is_primary, original_sha256, current_sha256) VALUES (1, 1, 'books/a.epub', 'a.epub', '.epub', 1, randomblob(32), randomblob(32))")

	created, err := database.CreateAnnotation(t.Context(), alice.ID, 1, AnnotationCreate{
		CFI:           " epubcfi(/6/2!/4/2) ",
		Quote:         " highlighted text ",
		ContextBefore: " before ",
		ContextAfter:  " after ",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation: %v", err)
	}
	if created.CFI != "epubcfi(/6/2!/4/2)" || created.Quote != "highlighted text" || created.ContextBefore != "before" || created.ContextAfter != "after" {
		t.Fatalf("normalized annotation = %+v", created)
	}

	if _, err := database.UpdateAnnotationNote(t.Context(), alice.ID, 1, created.ID, AnnotationNoteUpdate{Note: "remember this"}); err != nil {
		t.Fatalf("UpdateAnnotationNote: %v", err)
	}
	if _, err := database.UpdateAnnotationNote(t.Context(), alice.ID, 1, created.ID, AnnotationNoteUpdate{Note: strings.Repeat("я", MaxAnnotationNoteLength+1)}); !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("long note update err = %v, want ErrInvalidAnnotation", err)
	}
	duplicateAfterNote, err := database.CreateAnnotation(t.Context(), alice.ID, 1, AnnotationCreate{
		CFI:   created.CFI,
		Quote: "quote after note",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation duplicate after note: %v", err)
	}
	if duplicateAfterNote.ID != created.ID || duplicateAfterNote.Quote != "quote after note" || duplicateAfterNote.Note != "remember this" {
		t.Fatalf("duplicate after note = %+v, want note preserved", duplicateAfterNote)
	}

	if _, err := database.CreateAnnotation(t.Context(), alice.ID, 1, AnnotationCreate{CFI: "epubcfi(/6/4)", Quote: strings.Repeat("я", MaxAnnotationQuoteLength)}); err != nil {
		t.Fatalf("unicode quote at limit err = %v", err)
	}
	if _, err := database.CreateAnnotation(t.Context(), alice.ID, 1, AnnotationCreate{CFI: "epubcfi(/6/6)", Quote: strings.Repeat("я", MaxAnnotationQuoteLength+1)}); !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("unicode quote over limit err = %v, want ErrInvalidAnnotation", err)
	}
	if _, err := database.CreateAnnotation(t.Context(), alice.ID, 999, AnnotationCreate{CFI: "epubcfi(/6/2)", Quote: "x"}); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing asset err = %v, want ErrAssetNotFound", err)
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

	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 1, 0.2, '{\"engine\":\"test\",\"id\":\"old\"}', 10, 10)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 2, 0.4, '{\"engine\":\"test\",\"id\":\"new\"}', 20, 20)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 3, 0, '{}', 30, 30)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 4, 0.8, '{\"engine\":\"test\",\"id\":\"done-incomplete\"}', 35, 35)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 5, 1, '{\"engine\":\"test\",\"id\":\"done\"}', 40, 40)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 6, 0.5, '{\"engine\":\"test\",\"id\":\"deleted\"}', 50, 50)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 2, 0.8, '{\"engine\":\"test\",\"id\":\"bob\"}', 60, 60)", bob.ID)
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
