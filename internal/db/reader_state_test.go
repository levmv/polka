package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReaderStateLifecycle(t *testing.T) {
	database := newTestDB(t)

	user, err := database.CreateUser("reader", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	other, err := database.CreateUser("other", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T1', 'T1')")
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension, is_primary) VALUES ('asset_1', 'w1', 'books/a.epub', 'a.epub', '.epub', 1)")

	state, err := database.GetReaderState(user.ID, "asset_1")
	if err != nil {
		t.Fatalf("GetReaderState default: %v", err)
	}
	if state.WorkID != "w1" || state.Progress != 0 || state.Locator.String() != "{}" || state.LastReadAt != 0 {
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
	annotation, err := database.CreateAnnotation(user.ID, "asset_1", AnnotationCreate{
		CFI:   "epubcfi(/6/2!/4/2)",
		Quote: "keep this highlight",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation before reset: %v", err)
	}
	if err := database.ResetReaderState(user.ID, "asset_1"); err != nil {
		t.Fatalf("ResetReaderState: %v", err)
	}
	state, err = database.GetReaderState(user.ID, "asset_1")
	if err != nil {
		t.Fatalf("GetReaderState after reset: %v", err)
	}
	if state.Progress != 0 || state.Locator.String() != "{}" || state.LastReadAt != 0 || state.UpdatedAt != 0 {
		t.Fatalf("reader state after reset = %+v", state)
	}
	annotations, err := database.ListAnnotations(user.ID, "asset_1")
	if err != nil {
		t.Fatalf("ListAnnotations after state reset: %v", err)
	}
	if len(annotations) != 1 || annotations[0].ID != annotation.ID {
		t.Fatalf("annotations after state reset = %+v, want annotation %q", annotations, annotation.ID)
	}
	otherState, err := database.GetReaderState(other.ID, "asset_1")
	if err != nil {
		t.Fatalf("GetReaderState other after reset: %v", err)
	}
	if otherState.Progress != 0.75 {
		t.Fatalf("other user progress after reset = %v, want 0.75", otherState.Progress)
	}
	if err := database.ResetReaderState(user.ID, "asset_1"); err != nil {
		t.Fatalf("second ResetReaderState: %v", err)
	}

	if _, _, err := database.SaveReaderStateAndAdvanceStatus(context.Background(), user.ID, "asset_1", 1.2, locator, ReadingStatusSourceWebReader); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid progress err = %v, want invalid reader input", err)
	}
	if _, _, err := database.SaveReaderStateAndAdvanceStatus(context.Background(), user.ID, "asset_1", 0.5, ReaderLocator(`"bad"`), ReadingStatusSourceWebReader); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid locator err = %v, want invalid reader input", err)
	}
	if _, err := database.GetReaderState(user.ID, "missing"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing asset err = %v, want ErrAssetNotFound", err)
	}
	if err := database.ResetReaderState(user.ID, "missing"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("reset missing asset err = %v, want ErrAssetNotFound", err)
	}
}

func TestTouchReaderStateAndAdvanceStatusRollsBackTogether(t *testing.T) {
	database := newTestDB(t)

	user, err := database.CreateUser("reader", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('w1', 'Book', 'Book');
		INSERT INTO assets (id, work_id, storage_path, filename, extension)
		VALUES ('a1', 'w1', 'book.epub', 'book.epub', '.epub');
		CREATE TRIGGER fail_reader_open_status
		BEFORE INSERT ON user_work_reading_events
		BEGIN
			SELECT RAISE(ABORT, 'status write failed');
		END;
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, _, err := database.TouchReaderStateAndAdvanceStatus(
		context.Background(), user.ID, "a1", ReadingStatusSourceWebReader,
	); err == nil {
		t.Fatal("touch succeeded despite status failure")
	}
	state, err := database.GetReaderState(user.ID, "a1")
	if err != nil {
		t.Fatalf("get state after rollback: %v", err)
	}
	if state.LastReadAt != 0 || state.UpdatedAt != 0 {
		t.Fatalf("reader touch survived status rollback: %+v", state)
	}
}

func TestSaveReaderStateAndStatusCommitTogether(t *testing.T) {
	database := newTestDB(t)

	user, err := database.CreateUser("reader-atomic", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('w_atomic', 'Atomic', 'Atomic');
		INSERT INTO assets (id, work_id, storage_path, filename, extension)
		VALUES ('a_atomic', 'w_atomic', 'atomic.epub', 'atomic.epub', '.epub');
		CREATE TRIGGER reject_atomic_status
		BEFORE INSERT ON user_work_reading_events
		BEGIN
			SELECT RAISE(ABORT, 'status write rejected');
		END;
	`); err != nil {
		t.Fatalf("seed atomic reader state: %v", err)
	}
	locator, err := NewReaderLocator([]byte(`{"engine":"foliate","fraction":0.4}`))
	if err != nil {
		t.Fatalf("locator: %v", err)
	}

	if _, _, err := database.SaveReaderStateAndAdvanceStatus(
		context.Background(), user.ID, "a_atomic", 0.4, locator, ReadingStatusSourceWebReader,
	); err == nil {
		t.Fatal("atomic save succeeded with rejecting status trigger")
	}
	state, err := database.GetReaderState(user.ID, "a_atomic")
	if err != nil {
		t.Fatalf("state after rollback: %v", err)
	}
	if state.Progress != 0 || state.LastReadAt != 0 || state.UpdatedAt != 0 {
		t.Fatalf("reader position survived rolled-back status write: %+v", state)
	}
	status, err := GetReadingStatus(database, user.ID, "w_atomic")
	if err != nil || status.Status != ReadingStatusUnread {
		t.Fatalf("status after rollback = %+v, err %v", status, err)
	}

	if _, err := database.Exec("DROP TRIGGER reject_atomic_status"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
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

	alice, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser("bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T1', 'T1')")
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension, is_primary) VALUES ('asset_1', 'w1', 'books/a.epub', 'a.epub', '.epub', 1)")

	created, err := database.CreateAnnotation(alice.ID, "asset_1", AnnotationCreate{
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

	updated, err := database.CreateAnnotation(alice.ID, "asset_1", AnnotationCreate{
		CFI:   created.CFI,
		Quote: "updated quote",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation duplicate: %v", err)
	}
	if updated.ID != created.ID || updated.Quote != "updated quote" {
		t.Fatalf("duplicate create = %+v, want same id with updated quote", updated)
	}

	rows, err := database.ListAnnotations(alice.ID, "asset_1")
	if err != nil {
		t.Fatalf("ListAnnotations alice: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != created.ID || rows[0].Quote != "updated quote" {
		t.Fatalf("alice annotations = %+v", rows)
	}

	noteUpdated, err := database.UpdateAnnotationNote(alice.ID, "asset_1", created.ID, AnnotationNoteUpdate{Note: "  remember this  "})
	if err != nil {
		t.Fatalf("UpdateAnnotationNote: %v", err)
	}
	if noteUpdated.ID != created.ID || noteUpdated.Note != "remember this" || noteUpdated.Quote != "updated quote" {
		t.Fatalf("note update = %+v", noteUpdated)
	}
	if _, err := database.UpdateAnnotationNote(bob.ID, "asset_1", created.ID, AnnotationNoteUpdate{Note: "stolen"}); !errors.Is(err, ErrAnnotationNotFound) {
		t.Fatalf("bob update err = %v, want ErrAnnotationNotFound", err)
	}
	if _, err := database.UpdateAnnotationNote(alice.ID, "asset_1", created.ID, AnnotationNoteUpdate{Note: strings.Repeat("я", MaxAnnotationNoteLength+1)}); !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("long note update err = %v, want ErrInvalidAnnotation", err)
	}
	duplicateAfterNote, err := database.CreateAnnotation(alice.ID, "asset_1", AnnotationCreate{
		CFI:   created.CFI,
		Quote: "quote after note",
	})
	if err != nil {
		t.Fatalf("CreateAnnotation duplicate after note: %v", err)
	}
	if duplicateAfterNote.ID != created.ID || duplicateAfterNote.Quote != "quote after note" || duplicateAfterNote.Note != "remember this" {
		t.Fatalf("duplicate after note = %+v, want note preserved", duplicateAfterNote)
	}

	rows, err = database.ListAnnotations(bob.ID, "asset_1")
	if err != nil {
		t.Fatalf("ListAnnotations bob: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("annotation leaked to bob: %+v", rows)
	}

	if _, err := database.CreateAnnotation(alice.ID, "asset_1", AnnotationCreate{CFI: "", Quote: "x"}); !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("missing cfi err = %v, want ErrInvalidAnnotation", err)
	}
	unicodeCreated, err := database.CreateAnnotation(alice.ID, "asset_1", AnnotationCreate{CFI: "epubcfi(/6/4)", Quote: strings.Repeat("я", MaxAnnotationQuoteLength)})
	if err != nil {
		t.Fatalf("unicode quote at limit err = %v", err)
	}
	if _, err := database.CreateAnnotation(alice.ID, "asset_1", AnnotationCreate{CFI: "epubcfi(/6/6)", Quote: strings.Repeat("я", MaxAnnotationQuoteLength+1)}); !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("unicode quote over limit err = %v, want ErrInvalidAnnotation", err)
	}
	if _, err := database.CreateAnnotation(alice.ID, "missing", AnnotationCreate{CFI: "epubcfi(/6/2)", Quote: "x"}); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing asset err = %v, want ErrAssetNotFound", err)
	}

	if err := database.DeleteAnnotation(bob.ID, "asset_1", created.ID); !errors.Is(err, ErrAnnotationNotFound) {
		t.Fatalf("bob delete err = %v, want ErrAnnotationNotFound", err)
	}
	if err := database.DeleteAnnotation(alice.ID, "asset_1", created.ID); err != nil {
		t.Fatalf("DeleteAnnotation: %v", err)
	}
	if err := database.DeleteAnnotation(alice.ID, "asset_1", unicodeCreated.ID); err != nil {
		t.Fatalf("DeleteAnnotation unicode: %v", err)
	}
	rows, err = database.ListAnnotations(alice.ID, "asset_1")
	if err != nil {
		t.Fatalf("ListAnnotations after delete: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("annotations after delete = %+v", rows)
	}
}

func TestListContinueReading(t *testing.T) {
	database := newTestDB(t)

	alice, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser("bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	mustExec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Author One', 'Author One')")
	mustExec("INSERT INTO works (id, title, sort_title, deleted_at) VALUES ('w1', 'Newest per Work', 'Newest per Work', NULL)")
	mustExec("INSERT INTO works (id, title, sort_title, deleted_at) VALUES ('w2', 'Opened at Start', 'Opened at Start', NULL)")
	mustExec("INSERT INTO works (id, title, sort_title, deleted_at) VALUES ('w_done', 'Done', 'Done', NULL)")
	mustExec("INSERT INTO works (id, title, sort_title, deleted_at) VALUES ('w_deleted', 'Deleted', 'Deleted', 123)")
	for _, workID := range []string{"w1", "w2", "w_done", "w_deleted"} {
		mustExec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'a1', 0)", workID)
	}
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_old', 'w1', 'old.epub', 'old.epub', '.epub')")
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_new', 'w1', 'new.fb2', 'new.fb2', '.fb2')")
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_zero', 'w2', 'zero.pdf', 'zero.pdf', '.pdf')")
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_done_incomplete', 'w_done', 'done.pdf', 'done.pdf', '.pdf')")
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_done', 'w_done', 'done.epub', 'done.epub', '.epub')")
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_deleted', 'w_deleted', 'deleted.epub', 'deleted.epub', '.epub')")

	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_old', 0.2, '{\"engine\":\"test\",\"id\":\"old\"}', 10, 10)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_new', 0.4, '{\"engine\":\"test\",\"id\":\"new\"}', 20, 20)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_zero', 0, '{}', 30, 30)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_done_incomplete', 0.8, '{\"engine\":\"test\",\"id\":\"done-incomplete\"}', 35, 35)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_done', 1, '{\"engine\":\"test\",\"id\":\"done\"}', 40, 40)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_deleted', 0.5, '{\"engine\":\"test\",\"id\":\"deleted\"}', 50, 50)", alice.ID)
	mustExec("INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at) VALUES (?, 'asset_new', 0.8, '{\"engine\":\"test\",\"id\":\"bob\"}', 60, 60)", bob.ID)
	mustExec("INSERT INTO user_work_reading_state (user_id, work_id, status) VALUES (?, 'w1', 'reading')", alice.ID)
	mustExec("INSERT INTO user_work_reading_state (user_id, work_id, status) VALUES (?, 'w2', 'reading')", alice.ID)
	mustExec("INSERT INTO user_work_reading_state (user_id, work_id, status) VALUES (?, 'w_done', 'finished')", alice.ID)
	mustExec("INSERT INTO user_work_reading_state (user_id, work_id, status) VALUES (?, 'w_deleted', 'reading')", alice.ID)
	mustExec("INSERT INTO user_work_reading_state (user_id, work_id, status) VALUES (?, 'w1', 'reading')", bob.ID)

	rows, err := ListContinueReading(database, FullVisibilityScope(), alice.ID, 10)
	if err != nil {
		t.Fatalf("ListContinueReading: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].ID != "w2" || rows[0].AssetID != "asset_zero" || rows[0].Progress != 0 {
		t.Fatalf("first row = %+v, want zero-progress w2", rows[0])
	}
	if rows[1].ID != "w1" || rows[1].AssetID != "asset_new" || rows[1].Progress != 0.4 {
		t.Fatalf("second row = %+v, want latest asset for w1", rows[1])
	}

	// Starting a reread makes the work eligible again while preserving the
	// truthful per-format positions. The completed EPUB stays at 100%, and the
	// latest incomplete asset becomes the continuation target.
	mustExec("UPDATE user_work_reading_state SET status = 'reading' WHERE user_id = ? AND work_id = 'w_done'", alice.ID)
	rows, err = ListContinueReading(database, FullVisibilityScope(), alice.ID, 10)
	if err != nil {
		t.Fatalf("ListContinueReading after reread: %v", err)
	}
	if len(rows) != 3 || rows[0].ID != "w_done" || rows[0].AssetID != "asset_done_incomplete" || rows[0].Progress != 0.8 {
		t.Fatalf("reread rows = %+v, want incomplete w_done asset first", rows)
	}
}

func TestReaderPreferencesLifecycle(t *testing.T) {
	database := newTestDB(t)

	alice, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser("bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	prefs, err := database.GetReaderPreferences(alice.ID)
	if err != nil {
		t.Fatalf("GetReaderPreferences default: %v", err)
	}
	if prefs.EPUBFlow != ReaderFlowPaginated ||
		prefs.DisplayStyle != ReaderStylePaper ||
		prefs.FontScale != 0 ||
		prefs.CustomColumnWidth != DefaultReaderCustomColumnWidth ||
		prefs.CustomLineHeight != DefaultReaderCustomLineHeight ||
		prefs.UpdatedAt != 0 {
		t.Fatalf("default reader preferences = %+v", prefs)
	}

	prefs, err = database.SaveReaderPreferences(alice.ID, ReaderPreferences{
		EPUBFlow:          ReaderFlowScrolled,
		DisplayStyle:      ReaderStyleCustom,
		FontScale:         2,
		CustomColumnWidth: 820,
		CustomLineHeight:  1.9,
	})
	if err != nil {
		t.Fatalf("SaveReaderPreferences: %v", err)
	}
	if prefs.EPUBFlow != ReaderFlowScrolled ||
		prefs.DisplayStyle != ReaderStyleCustom ||
		prefs.FontScale != 2 ||
		prefs.CustomColumnWidth != 820 ||
		prefs.CustomLineHeight != 1.9 ||
		prefs.UpdatedAt == 0 {
		t.Fatalf("saved reader preferences = %+v", prefs)
	}

	bobPrefs, err := database.GetReaderPreferences(bob.ID)
	if err != nil {
		t.Fatalf("GetReaderPreferences bob: %v", err)
	}
	if bobPrefs.EPUBFlow != ReaderFlowPaginated {
		t.Fatalf("reader preferences leaked across users: %+v", bobPrefs)
	}

	if _, err := database.SaveReaderPreferences(alice.ID, ReaderPreferences{EPUBFlow: "sideways"}); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid flow err = %v, want invalid reader input", err)
	}
	if _, err := database.SaveReaderPreferences(alice.ID, ReaderPreferences{EPUBFlow: ReaderFlowPaginated, DisplayStyle: "neon"}); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid style err = %v, want invalid reader input", err)
	}
	if _, err := database.SaveReaderPreferences(alice.ID, ReaderPreferences{EPUBFlow: ReaderFlowPaginated, FontScale: 12}); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid font scale err = %v, want invalid reader input", err)
	}
	if _, err := database.SaveReaderPreferences(alice.ID, ReaderPreferences{EPUBFlow: ReaderFlowPaginated, CustomColumnWidth: 200}); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid width err = %v, want invalid reader input", err)
	}
	if _, err := database.SaveReaderPreferences(alice.ID, ReaderPreferences{EPUBFlow: ReaderFlowPaginated, CustomLineHeight: 3}); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("invalid line height err = %v, want invalid reader input", err)
	}
}
