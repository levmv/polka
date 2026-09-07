package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReaderStateLifecycle(t *testing.T) {
	database := newTestDB(t)

	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	other, err := database.CreateUser(t.Context(), "other", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T1', 'T1')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, is_primary) VALUES ('asset_1', 1, 'books/a.epub', 'a.epub', '.epub', 1)")

	state, err := GetReaderState(database.Read(t.Context()), user.ID, "asset_1")
	if err != nil {
		t.Fatalf("GetReaderState default: %v", err)
	}
	if state.BookID != 1 || state.Progress != 0 || state.Locator.String() != "{}" || state.LastReadAt != 0 {
		t.Fatalf("default reader state = %+v", state)
	}

	state, _, err = database.TouchReaderStateAndAdvanceStatus(context.Background(), user.ID, "asset_1", ReadingStatusSourceWebReader)
	if err != nil {
		t.Fatalf("TouchReaderState: %v", err)
	}
	if state.LastReadAt == 0 || state.UpdatedAt == 0 {
		t.Fatalf("touch state did not set timestamps: %+v", state)
	}

	locator, err := NewReaderLocator([]byte(`{"engine":"foliate","cfi":"epubcfi(/6/2)","fraction":0.42}`))
	if err != nil {
		t.Fatalf("NewReaderLocator: %v", err)
	}
	state, _, err = database.SaveReaderStateAndAdvanceStatus(context.Background(), user.ID, "asset_1", 0.42, locator, ReadingStatusSourceWebReader)
	if err != nil {
		t.Fatalf("SaveReaderStateAndAdvanceStatus: %v", err)
	}
	if state.Progress != 0.42 || state.Locator.String() != locator.String() || state.LastReadAt == 0 {
		t.Fatalf("saved reader state = %+v", state)
	}

	if _, _, err := database.SaveReaderStateAndAdvanceStatus(context.Background(), other.ID, "asset_1", 0.75, locator, ReadingStatusSourceWebReader); err != nil {
		t.Fatalf("SaveReaderStateAndAdvanceStatus other: %v", err)
	}
	annotation, err := database.CreateAnnotation(t.Context(), user.ID, "asset_1", AnnotationCreate{
		CFI:   "epubcfi(/6/2!/4/2)",
		Quote: "keep this highlight",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation before reset: %v", err)
	}
	if err := database.ResetReaderState(t.Context(), user.ID, "asset_1"); err != nil {
		t.Fatalf("ResetReaderState: %v", err)
	}
	state, err = GetReaderState(database.Read(t.Context()), user.ID, "asset_1")
	if err != nil {
		t.Fatalf("GetReaderState after reset: %v", err)
	}
	if state.Progress != 0 || state.Locator.String() != "{}" || state.LastReadAt != 0 || state.UpdatedAt != 0 {
		t.Fatalf("reader state after reset = %+v", state)
	}
	annotations, err := ListAnnotations(database.Read(t.Context()), user.ID, "asset_1")
	if err != nil {
		t.Fatalf("ListAnnotations after state reset: %v", err)
	}
	if len(annotations) != 1 || annotations[0].ID != annotation.ID {
		t.Fatalf("annotations after state reset = %+v, want annotation %q", annotations, annotation.ID)
	}
	otherState, err := GetReaderState(database.Read(t.Context()), other.ID, "asset_1")
	if err != nil {
		t.Fatalf("GetReaderState other after reset: %v", err)
	}
	if otherState.Progress != 0.75 {
		t.Fatalf("other user progress after reset = %v, want 0.75", otherState.Progress)
	}
	if err := database.ResetReaderState(t.Context(), user.ID, "asset_1"); err != nil {
		t.Fatalf("second ResetReaderState: %v", err)
	}

	if _, _, err := database.SaveReaderStateAndAdvanceStatus(context.Background(), user.ID, "asset_1", 1.2, locator, ReadingStatusSourceWebReader); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid progress err = %v, want invalid reader input", err)
	}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(context.Background(), user.ID, "asset_1", 0.5, ReaderLocator(`"bad"`), ReadingStatusSourceWebReader); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid locator err = %v, want invalid reader input", err)
	}
	if _, err := GetReaderState(database.Read(t.Context()), user.ID, "missing"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing asset err = %v, want ErrAssetNotFound", err)
	}
	if err := database.ResetReaderState(t.Context(), user.ID, "missing"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("reset missing asset err = %v, want ErrAssetNotFound", err)
	}
}

func TestTouchReaderStateAndAdvanceStatusRollsBackTogether(t *testing.T) {
	database := newTestDB(t)

	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension)
		VALUES ('a1', 1, 'book.epub', 'book.epub', '.epub');
		CREATE TRIGGER fail_reader_open_status
		BEFORE INSERT ON user_book_reading_events
		BEGIN
			SELECT RAISE(ABORT, 'status write failed');
		END;
	`)

	if _, _, err := database.TouchReaderStateAndAdvanceStatus(
		context.Background(), user.ID, "a1", ReadingStatusSourceWebReader,
	); err == nil {
		t.Fatal("touch succeeded despite status failure")
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, "a1")
	if err != nil {
		t.Fatalf("get state after rollback: %v", err)
	}
	if state.LastReadAt != 0 || state.UpdatedAt != 0 {
		t.Fatalf("reader touch survived status rollback: %+v", state)
	}
}

func TestSaveReaderStateAndStatusCommitTogether(t *testing.T) {
	database := newTestDB(t)

	user, err := database.CreateUser(t.Context(), "reader-atomic", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (108, 'Atomic', 'Atomic');
		INSERT INTO assets (id, book_id, storage_path, filename, extension)
		VALUES ('a_atomic', 108, 'atomic.epub', 'atomic.epub', '.epub');
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
		context.Background(), user.ID, "a_atomic", 0.4, locator, ReadingStatusSourceWebReader,
	); err == nil {
		t.Fatal("atomic save succeeded with rejecting status trigger")
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, "a_atomic")
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
		context.Background(), user.ID, "a_atomic", 0.4, locator, ReadingStatusSourceWebReader,
	)
	if err != nil {
		t.Fatalf("atomic save: %v", err)
	}
	if saved.Progress != 0.4 || !change.Changed || change.State.Status != ReadingStatusReading {
		t.Fatalf("atomic result state=%+v change=%+v", saved, change)
	}
}

func TestAnnotationsLifecycle(t *testing.T) {
	database := newTestDB(t)

	alice, err := database.CreateUser(t.Context(), "alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser(t.Context(), "bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T1', 'T1')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, is_primary) VALUES ('asset_1', 1, 'books/a.epub', 'a.epub', '.epub', 1)")

	created, err := database.CreateAnnotation(t.Context(), alice.ID, "asset_1", AnnotationCreate{
		CFI:           " epubcfi(/6/2!/4/2) ",
		Quote:         " highlighted text ",
		ContextBefore: " before ",
		ContextAfter:  " after ",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation: %v", err)
	}
	if created.ID == "" || created.Kind != AnnotationKindHighlight || created.Color != AnnotationColorYellow {
		t.Fatalf("created annotation = %+v", created)
	}
	if created.CFI != "epubcfi(/6/2!/4/2)" || created.Quote != "highlighted text" || created.ContextBefore != "before" || created.ContextAfter != "after" {
		t.Fatalf("normalized annotation = %+v", created)
	}

	updated, err := database.CreateAnnotation(t.Context(), alice.ID, "asset_1", AnnotationCreate{
		CFI:   created.CFI,
		Quote: "updated quote",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation duplicate: %v", err)
	}
	if updated.ID != created.ID || updated.Quote != "updated quote" {
		t.Fatalf("duplicate create = %+v, want same id with updated quote", updated)
	}

	rows, err := ListAnnotations(database.Read(t.Context()), alice.ID, "asset_1")
	if err != nil {
		t.Fatalf("ListAnnotations alice: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != created.ID || rows[0].Quote != "updated quote" {
		t.Fatalf("alice annotations = %+v", rows)
	}

	noteUpdated, err := database.UpdateAnnotationNote(t.Context(), alice.ID, "asset_1", created.ID, AnnotationNoteUpdate{Note: "  remember this  "})
	if err != nil {
		t.Fatalf("UpdateAnnotationNote: %v", err)
	}
	if noteUpdated.ID != created.ID || noteUpdated.Note != "remember this" || noteUpdated.Quote != "updated quote" {
		t.Fatalf("note update = %+v", noteUpdated)
	}
	if _, err := database.UpdateAnnotationNote(t.Context(), bob.ID, "asset_1", created.ID, AnnotationNoteUpdate{Note: "stolen"}); !errors.Is(err, ErrAnnotationNotFound) {
		t.Fatalf("bob update err = %v, want ErrAnnotationNotFound", err)
	}
	if _, err := database.UpdateAnnotationNote(t.Context(), alice.ID, "asset_1", created.ID, AnnotationNoteUpdate{Note: strings.Repeat("я", MaxAnnotationNoteLength+1)}); !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("long note update err = %v, want ErrInvalidAnnotation", err)
	}
	duplicateAfterNote, err := database.CreateAnnotation(t.Context(), alice.ID, "asset_1", AnnotationCreate{
		CFI:   created.CFI,
		Quote: "quote after note",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation duplicate after note: %v", err)
	}
	if duplicateAfterNote.ID != created.ID || duplicateAfterNote.Quote != "quote after note" || duplicateAfterNote.Note != "remember this" {
		t.Fatalf("duplicate after note = %+v, want note preserved", duplicateAfterNote)
	}

	rows, err = ListAnnotations(database.Read(t.Context()), bob.ID, "asset_1")
	if err != nil {
		t.Fatalf("ListAnnotations bob: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("annotation leaked to bob: %+v", rows)
	}

	if _, err := database.CreateAnnotation(t.Context(), alice.ID, "asset_1", AnnotationCreate{CFI: "", Quote: "x"}); !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("missing cfi err = %v, want ErrInvalidAnnotation", err)
	}
	unicodeCreated, err := database.CreateAnnotation(t.Context(), alice.ID, "asset_1", AnnotationCreate{CFI: "epubcfi(/6/4)", Quote: strings.Repeat("я", MaxAnnotationQuoteLength)})
	if err != nil {
		t.Fatalf("unicode quote at limit err = %v", err)
	}
	if _, err := database.CreateAnnotation(t.Context(), alice.ID, "asset_1", AnnotationCreate{CFI: "epubcfi(/6/6)", Quote: strings.Repeat("я", MaxAnnotationQuoteLength+1)}); !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("unicode quote over limit err = %v, want ErrInvalidAnnotation", err)
	}
	if _, err := database.CreateAnnotation(t.Context(), alice.ID, "missing", AnnotationCreate{CFI: "epubcfi(/6/2)", Quote: "x"}); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing asset err = %v, want ErrAssetNotFound", err)
	}

	if err := database.DeleteAnnotation(t.Context(), bob.ID, "asset_1", created.ID); !errors.Is(err, ErrAnnotationNotFound) {
		t.Fatalf("bob delete err = %v, want ErrAnnotationNotFound", err)
	}
	if err := database.DeleteAnnotation(t.Context(), alice.ID, "asset_1", created.ID); err != nil {
		t.Fatalf("DeleteAnnotation: %v", err)
	}
	if err := database.DeleteAnnotation(t.Context(), alice.ID, "asset_1", unicodeCreated.ID); err != nil {
		t.Fatalf("DeleteAnnotation unicode: %v", err)
	}
	rows, err = ListAnnotations(database.Read(t.Context()), alice.ID, "asset_1")
	if err != nil {
		t.Fatalf("ListAnnotations after delete: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("annotations after delete = %+v", rows)
	}
}

func TestListContinueReading(t *testing.T) {
	database := newTestDB(t)

	alice, err := database.CreateUser(t.Context(), "alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser(t.Context(), "bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	mustExec := func(query string, args ...any) {
		t.Helper()
		mustExec(t, database, query, args...)

	}

	mustExec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Author One', 'Author One')")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (1, 'Newest per Book', 'Newest per Book', NULL)")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (2, 'Opened at Start', 'Opened at Start', NULL)")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (125, 'Done', 'Done', NULL)")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (122, 'Deleted', 'Deleted', 123)")
	for _, bookID := range []int64{1, 2, 125, 122} {
		mustExec("INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 'a1', 0)", bookID)
	}
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_old', 1, 'old.epub', 'old.epub', '.epub')")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_new', 1, 'new.fb2', 'new.fb2', '.fb2')")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_zero', 2, 'zero.pdf', 'zero.pdf', '.pdf')")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_done_incomplete', 125, 'done.pdf', 'done.pdf', '.pdf')")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_done', 125, 'done.epub', 'done.epub', '.epub')")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_deleted', 122, 'deleted.epub', 'deleted.epub', '.epub')")

	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_old', 0.2, '{\"engine\":\"test\",\"id\":\"old\"}', 10, 10)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_new', 0.4, '{\"engine\":\"test\",\"id\":\"new\"}', 20, 20)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_zero', 0, '{}', 30, 30)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_done_incomplete', 0.8, '{\"engine\":\"test\",\"id\":\"done-incomplete\"}', 35, 35)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_done', 1, '{\"engine\":\"test\",\"id\":\"done\"}', 40, 40)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_deleted', 0.5, '{\"engine\":\"test\",\"id\":\"deleted\"}', 50, 50)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_new', 0.8, '{\"engine\":\"test\",\"id\":\"bob\"}', 60, 60)", bob.ID)
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
	if rows[0].ID != 2 || rows[0].AssetID != "asset_zero" || rows[0].Progress != 0 {
		t.Fatalf("first row = %+v, want zero-progress 2", rows[0])
	}
	if rows[1].ID != 1 || rows[1].AssetID != "asset_new" || rows[1].Progress != 0.4 {
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
	if len(rows) != 3 || rows[0].ID != 125 || rows[0].AssetID != "asset_done_incomplete" || rows[0].Progress != 0.8 {
		t.Fatalf("reread rows = %+v, want incomplete 125 asset first", rows)
	}
}
