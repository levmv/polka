package db

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func seedKoboBook(t *testing.T, database *DB, bookID, assetID int64, title string, formatKey string, tags string) {
	t.Helper()
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, language, publisher)
		VALUES (?, ?, ?, 'en', 'Polka Press')
	`, bookID, title, title)
	mustSetTags(t, database, bookID, tags)
	mustExec(t, database, `
		INSERT INTO assets
		    (id, book_id, storage_path, filename, extension, format, is_primary, current_size, original_hash, current_hash)
		VALUES (?, ?, ?, ?, ?, ?, 1, 1234, randomblob(16), randomblob(16))
	`, assetID, bookID, strconv.FormatInt(bookID, 10)+"/"+strconv.FormatInt(assetID, 10)+"."+formatKey, strconv.FormatInt(assetID, 10)+"."+formatKey, formatKey, formatKey)
	mustExec(t, database, `INSERT INTO search (rowid, title, tags) VALUES (?1, ?2, ?3)`, bookID, title, tags)

}

func TestKoboReadingOrderAndAtomicity(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "kobo-reading", RoleMember)
	seedKoboBook(t, database, 1, 1, "Reading", "epub", "")
	input := KoboReadingUpdate{DeviceID: "device", DeviceName: "Kobo",
		Position: &KoboPosition{Source: "chapter.xhtml", Type: "KoboSpan", Fragment: "kobo.8.1"},
		Progress: new(.8), PositionUpdatedAt: 100, Status: ReadingStatusReading, StatusUpdatedAt: 100}
	for _, step := range []struct {
		name         string
		stamp        int64
		fragment     string
		progress     float64
		wantRevision int64
		wantProgress float64
	}{
		{"first upload", 100, "kobo.8.1", .8, 1, .8},
		{"rewind", 200, "kobo.2.1", .2, 2, .2},
		{"late upload", 150, "kobo.9.1", .9, 2, .2},
		{"retry", 200, "kobo.2.1", .2, 2, .2},
	} {
		t.Run(step.name, func(t *testing.T) {
			input.Position.Fragment, input.Progress, input.PositionUpdatedAt = step.fragment, new(step.progress), step.stamp
			if _, err := database.SaveKoboReading(t.Context(), user.ID, 1, input); err != nil {
				t.Fatal(err)
			}
			state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
			if err != nil || state.Revision != step.wantRevision || state.Progress != step.wantProgress {
				t.Fatalf("state = %+v, %v", state, err)
			}
		})
	}
	web, _, err := database.SaveReaderState(t.Context(), user.ID, 1,
		testReaderWrite(.6, Locator{CFI: "epubcfi(/6/2!/4/2/1:6)"}, 2), ReadingStatusSourceWebReader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetReadingStatus(t.Context(), user.ID, 1, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatal(err)
	}
	if result, err := database.SaveKoboReading(t.Context(), user.ID, 1, input); err != nil || result != (KoboReadingResult{}) {
		t.Fatalf("replay after web reading = %+v, %v", result, err)
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil || state.Revision != web.Revision || !state.Locator.Equal(web.Locator) || !state.KoboPosition.IsZero() {
		t.Fatalf("web position overwritten: %+v, %v", state, err)
	}
	if err := database.ResetReaderState(t.Context(), user.ID, 1); err != nil {
		t.Fatal(err)
	}
	if result, err := database.SaveKoboReading(t.Context(), user.ID, 1, input); err != nil || result != (KoboReadingResult{}) {
		t.Fatalf("replay after reset = %+v, %v", result, err)
	}
	progress, err := GetReaderProgress(database.Read(t.Context()), user.ID, 1)
	if err != nil || progress.Progress != nil || progress.ReadingStatus.Status != ReadingStatusFinished {
		t.Fatalf("reset/manual status lost: %+v, %v", progress, err)
	}
	// Bookmark and status share a commit: a failed status write must not leave
	// the device's new position behind with a successful-looking partial save.
	mustExec(t, database, `CREATE TRIGGER reject_kobo_status BEFORE INSERT ON user_book_reading_events
	    BEGIN SELECT RAISE(ABORT, 'status write failed'); END`)
	input.PositionUpdatedAt, input.StatusUpdatedAt = time.Now().Unix(), time.Now().Unix()
	if _, err := database.SaveKoboReading(t.Context(), user.ID, 1, input); err == nil {
		t.Fatal("save succeeded despite status failure")
	}
	state, err = GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil || !state.positionIsReset() {
		t.Fatalf("partial save survived: %+v, %v", state, err)
	}
	mustExec(t, database, "DROP TRIGGER reject_kobo_status")
	if result, err := database.SaveKoboReading(t.Context(), user.ID, 1, input); err != nil || !result.PositionChanged || !result.StatusChanged {
		t.Fatalf("fresh reading after reset = %+v, %v", result, err)
	}
}

func TestKoboBookmarkAdvancesStatusAtBookmarkTime(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "kobo-bookmark-status", RoleMember)
	seedKoboBook(t, database, 1, 1, "Reading", "epub", "")
	for _, step := range []struct {
		name               string
		stamp              int64
		progress           float64
		status, wantStatus string
		wantStamp          int64
	}{
		{"bookmark starts reading", 100, .2, "", ReadingStatusReading, 100},
		{"newer explicit status", 200, 0, ReadingStatusUnread, ReadingStatusUnread, 200},
		{"delayed bookmark", 150, .5, "", ReadingStatusUnread, 200},
	} {
		t.Run(step.name, func(t *testing.T) {
			input := KoboReadingUpdate{Status: step.status, StatusUpdatedAt: step.stamp}
			if step.status == "" {
				input.Position = &KoboPosition{Source: "chapter.xhtml", Type: "KoboSpan", Fragment: "kobo." + strconv.FormatInt(step.stamp, 10) + ".1"}
				input.Progress, input.PositionUpdatedAt = new(step.progress), step.stamp
				input.DeviceID, input.DeviceName = "device", "Kobo"
			}
			if result, err := database.SaveKoboReading(t.Context(), user.ID, 1, input); err != nil || result.PositionChanged != (step.status == "") {
				t.Fatalf("save = %+v, %v", result, err)
			}
			status, err := GetReadingStatus(database.Read(t.Context()), user.ID, 1)
			if err != nil || status.Status != step.wantStatus || status.UpdatedAt != step.wantStamp {
				t.Fatalf("status = %+v, %v; want %s at %d", status, err, step.wantStatus, step.wantStamp)
			}
		})
	}
}

func TestKoboBookmarkWithoutLocationUpdatesProgress(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "kobo-percent", RoleMember)
	seedKoboBook(t, database, 1, 1, "Reading", "epub", "")
	input := KoboReadingUpdate{
		Position: &KoboPosition{ChapterProgressPercent: new(50.0)},
		Progress: new(.2), PositionUpdatedAt: 100,
		Status: ReadingStatusReading, StatusUpdatedAt: 100,
	}
	if _, err := database.SaveKoboReading(t.Context(), user.ID, 1, input); err != nil {
		t.Fatal(err)
	}
	input.Progress, input.PositionUpdatedAt = new(.6), 200
	if _, err := database.SaveKoboReading(t.Context(), user.ID, 1, input); err != nil {
		t.Fatal(err)
	}
	state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
	if err != nil || state.Progress != .6 {
		t.Fatalf("position = %+v, %v; want progress=0.6", state, err)
	}
}

func TestKoboConnectionIncrementalLifecycle(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "kobo-reader", RoleMember)
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfPersonal, "On Kobo", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	seedKoboBook(t, database, 161, 1, "One", "epub", "chosen")
	seedKoboBook(t, database, 176, 2, "Two", "epub", "outside")
	mustExec(t, database, `
		UPDATE assets SET is_primary = 0 WHERE id = 1;
		INSERT INTO assets
		    (id, book_id, storage_path, filename, extension, format, is_primary, current_size, original_hash, current_hash)
		VALUES (3, 161, '161/a_kepub.kepub', 'a_kepub.kepub', 'kepub', 'kepub', 0, 1400, randomblob(16), randomblob(16));
	`)

	if err := database.AddBookToShelf(t.Context(), shelf.ID, user.ID, 161); err != nil {
		t.Fatal(err)
	}

	connection, err := database.CreateKoboConnection(context.Background(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	token := connection.Token
	resolved, ok, err := database.KoboConnectionByToken(t.Context(), strings.ToUpper(token))
	if err != nil || !ok || resolved.ID != connection.ID {
		t.Fatalf("resolve token = %+v, %v, %v", resolved, ok, err)
	}

	changes, current, more, err := database.SyncKoboConnection(context.Background(), connection.ID, 0, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if more || current != 1 || len(changes) != 1 {
		t.Fatalf("initial sync = %+v, current=%d more=%v", changes, current, more)
	}
	if changes[0].AssetID != 3 || !changes[0].Present || changes[0].Revision != changes[0].FirstRevision {
		t.Fatalf("initial change = %+v", changes[0])
	}

	// Retrying an unacknowledged cursor returns the same logical revision.
	retry, retryCurrent, _, err := database.SyncKoboConnection(context.Background(), connection.ID, 0, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if retryCurrent != current || len(retry) != 1 || retry[0].Revision != changes[0].Revision {
		t.Fatalf("retry = %+v, current=%d", retry, retryCurrent)
	}
	acknowledged, _, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current, KoboSyncPageLimit)
	if err != nil || len(acknowledged) != 0 {
		t.Fatalf("acknowledged sync = %+v, %v", acknowledged, err)
	}
	mustExec(t, database, `UPDATE books SET title = 'One revised', updated_at = updated_at + 1 WHERE id = 161`)

	changed, current, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Title != "One revised" || changed[0].Revision != 2 || changed[0].FirstRevision != 1 {
		t.Fatalf("metadata change = %+v", changed)
	}
	mustExec(t, database, `DELETE FROM shelf_books WHERE shelf_id = ? AND book_id = 161`, shelf.ID)

	removed, current, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Present || removed[0].Revision != 3 {
		t.Fatalf("removal = %+v", removed)
	}
	if _, err := KoboAssetForConnection(database.Read(t.Context()), FullVisibilityScope(), connection.ID, 3); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("removed asset lookup: %v", err)
	}
	if _, err := KoboAssetForConnection(database.Read(t.Context()), FullVisibilityScope(), connection.ID, 2); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("outside asset lookup: %v", err)
	}

	if err := database.AddBookToShelf(t.Context(), shelf.ID, user.ID, 161); err != nil {
		t.Fatal(err)
	}
	readded, current, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(readded) != 1 || !readded[0].Present || readded[0].Revision != 4 || readded[0].FirstRevision != 4 {
		t.Fatalf("re-add = %+v", readded)
	}

	newShelf, err := database.CreateShelf(t.Context(), user.ID, ShelfPersonal, "Other books", ShelfQuery, "tag:outside")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := database.SetKoboConnectionShelf(t.Context(), user.ID, newShelf.ID)
	if err != nil || updated.ID != connection.ID || updated.Token != token || updated.Revision != current {
		t.Fatalf("changed shelf = %+v, err %v; want preserved identity and cursor", updated, err)
	}
	changes, _, _, err = database.SyncKoboConnection(t.Context(), connection.ID, current, KoboSyncPageLimit)
	if err != nil || len(changes) != 2 || changes[0].AssetID != 2 || !changes[0].Present ||
		changes[1].AssetID != 3 || changes[1].Present {
		t.Fatalf("shelf change sync = %+v, err %v", changes, err)
	}
	if _, err := database.CreateKoboConnection(t.Context(), user.ID, shelf.ID); !errors.Is(err, ErrKoboConnectionExists) {
		t.Fatalf("duplicate create = %v; want existing connection preserved", err)
	}
	if _, ok, err := database.KoboConnectionByToken(t.Context(), token); err != nil || !ok {
		t.Fatalf("original token: ok=%v err=%v", ok, err)
	}
	if err := database.DeleteKoboConnection(t.Context(), user.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := database.CreateKoboConnection(context.Background(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == connection.ID || replacement.Token == token {
		t.Fatal("connection replacement reused identity or token")
	}
	if _, ok, err := database.KoboConnectionByToken(t.Context(), token); err != nil || ok {
		t.Fatalf("old token still resolves: ok=%v err=%v", ok, err)
	}
	if _, ok, err := database.KoboConnectionByToken(t.Context(), replacement.Token); err != nil || !ok {
		t.Fatalf("replacement token: ok=%v err=%v", ok, err)
	}
}

func TestKoboSyncPaginationQueryShelfAndCursorValidation(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "query-kobo", RoleReader)
	seedKoboBook(t, database, 103, 1, "A", "epub", "send")
	seedKoboBook(t, database, 112, 2, "B", "epub", "send")
	seedKoboBook(t, database, 115, 3, "C", "epub", "skip")
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Send", ShelfQuery, "tag:send")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []int64{shelf.ID}}); err != nil {
		t.Fatal(err)
	}
	connection, err := database.CreateKoboConnection(context.Background(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}

	first, current, more, err := database.SyncKoboConnection(context.Background(), connection.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if current != 2 || !more || len(first) != 1 || first[0].AssetID != 1 {
		t.Fatalf("first page = %+v, current=%d more=%v", first, current, more)
	}
	second, _, more, err := database.SyncKoboConnection(context.Background(), connection.ID, first[0].Revision, 1)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(second) != 1 || second[0].AssetID != 2 {
		t.Fatalf("second page = %+v, more=%v", second, more)
	}
	if _, _, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current+1, 1); !errors.Is(err, ErrKoboInvalidCursor) {
		t.Fatalf("future cursor error = %v", err)
	}
}

func TestKoboConnectionCannotSelectInvisibleShelf(t *testing.T) {
	database := newTestDB(t)
	alice := mustUser(t, database, "alice-kobo", RoleMember)
	bob := mustUser(t, database, "bob-kobo", RoleMember)
	shelf, err := database.CreateShelf(t.Context(), alice.ID, ShelfPersonal, "Alice only", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateKoboConnection(context.Background(), bob.ID, shelf.ID); !errors.Is(err, ErrShelfNotFound) {
		t.Fatalf("invisible shelf error = %v", err)
	}
	ownShelf, err := database.CreateShelf(t.Context(), bob.ID, ShelfPersonal, "Bob only", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.CreateKoboConnection(t.Context(), bob.ID, ownShelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetKoboConnectionShelf(t.Context(), bob.ID, shelf.ID); !errors.Is(err, ErrShelfNotFound) {
		t.Fatalf("invisible replacement shelf error = %v", err)
	}
	retained, err := KoboConnectionForUser(database.Read(t.Context()), bob.ID)
	if err != nil || retained.Token != connection.Token || retained.ShelfID != connection.ShelfID {
		t.Fatalf("connection after rejected shelf change = %+v, err %v", retained, err)
	}
}

func TestKoboSyncAfterShelfDeletion(t *testing.T) {
	for _, deleteOwner := range []bool{false, true} {
		name := "shelf"
		if deleteOwner {
			name = "shelf owner"
		}
		t.Run(name, func(t *testing.T) {
			database := newTestDB(t)
			owner := mustUser(t, database, "owner", RoleMember)
			reader := mustUser(t, database, "reader", RoleReader)
			shelf, err := database.CreateShelf(t.Context(), owner.ID, ShelfShared, "Shared", ShelfManual, "")
			if err != nil {
				t.Fatal(err)
			}
			for id := int64(1); id <= 2; id++ {
				seedKoboBook(t, database, id, id, "Book", "epub", "")
				if err := database.AddBookToShelf(t.Context(), shelf.ID, owner.ID, id); err != nil {
					t.Fatal(err)
				}
			}
			connection, err := database.CreateKoboConnection(t.Context(), reader.ID, shelf.ID)
			if err != nil {
				t.Fatal(err)
			}
			initial, cursor, _, err := database.SyncKoboConnection(t.Context(), connection.ID, 0, KoboSyncPageLimit)
			if err != nil || len(initial) != 2 {
				t.Fatalf("initial sync = %+v, err %v", initial, err)
			}
			if deleteOwner {
				err = database.DeleteUser(t.Context(), owner.ID)
			} else {
				err = database.DeleteShelf(t.Context(), shelf.ID, owner.ID)
			}
			if err != nil {
				t.Fatalf("delete %s: %v", name, err)
			}
			detached, ok, err := database.KoboConnectionByToken(t.Context(), connection.Token)
			if err != nil || !ok || detached.ID != connection.ID || detached.ShelfID.Valid {
				t.Fatalf("detached connection = %+v, ok %v, err %v", detached, ok, err)
			}
			for id := int64(1); id <= 2; id++ {
				changes, _, more, err := database.SyncKoboConnection(t.Context(), connection.ID, cursor, 1)
				if err != nil || len(changes) != 1 || changes[0].Present || changes[0].AssetID != id || more != (id == 1) {
					t.Fatalf("removal page = %+v, more %v, err %v", changes, more, err)
				}
				retry, _, _, err := database.SyncKoboConnection(t.Context(), connection.ID, cursor, 1)
				if err != nil || len(retry) != 1 || retry[0].Revision != changes[0].Revision || retry[0].Present {
					t.Fatalf("retry = %+v, err %v", retry, err)
				}
				cursor = changes[0].Revision
			}
			changes, current, more, err := database.SyncKoboConnection(t.Context(), connection.ID, cursor, KoboSyncPageLimit)
			if err != nil || len(changes) != 0 || current != cursor || more {
				t.Fatalf("settled sync = %+v, current %d, more %v, err %v", changes, current, more, err)
			}
			newShelf, err := database.CreateShelf(t.Context(), reader.ID, ShelfPersonal, "New shelf", ShelfManual, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := database.AddBookToShelf(t.Context(), newShelf.ID, reader.ID, 1); err != nil {
				t.Fatal(err)
			}
			reattached, err := database.SetKoboConnectionShelf(t.Context(), reader.ID, newShelf.ID)
			if err != nil || reattached.ID != connection.ID || reattached.Token != connection.Token || reattached.Revision != cursor {
				t.Fatalf("reattached connection = %+v, err %v", reattached, err)
			}
			changes, _, _, err = database.SyncKoboConnection(t.Context(), connection.ID, cursor, KoboSyncPageLimit)
			if err != nil || len(changes) != 1 || changes[0].AssetID != 1 || !changes[0].Present || changes[0].FirstRevision <= cursor {
				t.Fatalf("new shelf sync = %+v, err %v", changes, err)
			}
			if err := database.DeleteUser(t.Context(), reader.ID); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := database.KoboConnectionByToken(t.Context(), connection.Token); err != nil || ok {
				t.Fatalf("deleted account token: ok %v, err %v", ok, err)
			}
		})
	}
}
