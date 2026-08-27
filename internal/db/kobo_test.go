package db

import (
	"context"
	"errors"
	"testing"
)

func seedKoboWork(t *testing.T, database *DB, workID, assetID, title, formatKey, tags string) {
	t.Helper()
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title, tags, language, publisher)
		VALUES (?, ?, ?, ?, 'en', 'Polka Press')
	`, workID, title, title, tags); err != nil {
		t.Fatalf("seed Kobo work: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO assets
		    (id, work_id, storage_path, filename, extension, format, is_primary, current_size)
		VALUES (?, ?, ?, ?, ?, ?, 1, 1234)
	`, assetID, workID, workID+"/"+assetID+"."+formatKey, assetID+"."+formatKey, formatKey, formatKey); err != nil {
		t.Fatalf("seed Kobo work: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO search (work_id, title, tags) VALUES (?, ?, ?)`, workID, title, tags); err != nil {
		t.Fatalf("seed Kobo search row: %v", err)
	}
}

func TestKoboConnectionIncrementalLifecycle(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("kobo-reader", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := database.CreateShelf(user.ID, ShelfPersonal, "On Kobo", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	seedKoboWork(t, database, "w_one", "a_epub", "One", "epub", "chosen")
	seedKoboWork(t, database, "w_two", "a_two", "Two", "epub", "outside")
	if _, err := database.Exec(`
		UPDATE assets SET is_primary = 0 WHERE id = 'a_epub';
		INSERT INTO assets
		    (id, work_id, storage_path, filename, extension, format, is_primary, current_size)
		VALUES ('a_kepub', 'w_one', 'w_one/a_kepub.kepub', 'a_kepub.kepub', 'kepub', 'kepub', 0, 1400);
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.AddBookToShelf(shelf.ID, user.ID, "w_one"); err != nil {
		t.Fatal(err)
	}

	connection, token, err := database.ReplaceKoboConnection(context.Background(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("empty raw token")
	}
	resolved, ok, err := database.KoboConnectionByToken(token)
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
	if changes[0].AssetID != "a_kepub" || !changes[0].Present || changes[0].Revision != changes[0].FirstRevision {
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

	if _, err := database.Exec(`UPDATE works SET title = 'One revised', updated_at = updated_at + 1 WHERE id = 'w_one'`); err != nil {
		t.Fatal(err)
	}
	changed, current, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Title != "One revised" || changed[0].Revision != 2 || changed[0].FirstRevision != 1 {
		t.Fatalf("metadata change = %+v", changed)
	}

	if _, err := database.Exec(`DELETE FROM shelf_books WHERE shelf_id = ? AND work_id = 'w_one'`, shelf.ID); err != nil {
		t.Fatal(err)
	}
	removed, current, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Present || removed[0].Revision != 3 {
		t.Fatalf("removal = %+v", removed)
	}
	if _, err := database.KoboPublicationForAsset(connection.ID, "a_kepub"); !errors.Is(err, ErrKoboConnectionNotFound) {
		t.Fatalf("removed asset lookup: %v", err)
	}
	if _, err := database.KoboPublicationForAsset(connection.ID, "a_two"); !errors.Is(err, ErrKoboConnectionNotFound) {
		t.Fatalf("outside asset lookup: %v", err)
	}

	if err := database.AddBookToShelf(shelf.ID, user.ID, "w_one"); err != nil {
		t.Fatal(err)
	}
	readded, _, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(readded) != 1 || !readded[0].Present || readded[0].Revision != 4 || readded[0].FirstRevision != 1 {
		t.Fatalf("re-add = %+v", readded)
	}

	replacement, replacementToken, err := database.ReplaceKoboConnection(context.Background(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == connection.ID || replacementToken == token {
		t.Fatal("connection replacement reused identity or token")
	}
	if _, ok, err := database.KoboConnectionByToken(token); err != nil || ok {
		t.Fatalf("old token still resolves: ok=%v err=%v", ok, err)
	}
	if _, ok, err := database.KoboConnectionByToken(replacementToken); err != nil || !ok {
		t.Fatalf("replacement token: ok=%v err=%v", ok, err)
	}
}

func TestKoboSyncPaginationQueryShelfAndCursorValidation(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("query-kobo", "pw", RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	seedKoboWork(t, database, "w_a", "a_a", "A", "epub", "send")
	seedKoboWork(t, database, "w_b", "a_b", "B", "epub", "send")
	seedKoboWork(t, database, "w_c", "a_c", "C", "epub", "skip")
	shelf, err := database.CreateShelf(user.ID, ShelfShared, "Send", ShelfQuery, "tag:send")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatal(err)
	}
	connection, _, err := database.ReplaceKoboConnection(context.Background(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}

	first, current, more, err := database.SyncKoboConnection(context.Background(), connection.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if current != 2 || !more || len(first) != 1 || first[0].AssetID != "a_a" {
		t.Fatalf("first page = %+v, current=%d more=%v", first, current, more)
	}
	second, _, more, err := database.SyncKoboConnection(context.Background(), connection.ID, first[0].Revision, 1)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(second) != 1 || second[0].AssetID != "a_b" {
		t.Fatalf("second page = %+v, more=%v", second, more)
	}
	if _, _, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current+1, 1); !errors.Is(err, ErrKoboInvalidCursor) {
		t.Fatalf("future cursor error = %v", err)
	}
}

func TestKoboConnectionCannotSelectInvisibleShelf(t *testing.T) {
	database := newTestDB(t)
	alice, err := database.CreateUser("alice-kobo", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := database.CreateUser("bob-kobo", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := database.CreateShelf(alice.ID, ShelfPersonal, "Alice only", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.ReplaceKoboConnection(context.Background(), bob.ID, shelf.ID); !errors.Is(err, ErrShelfNotFound) {
		t.Fatalf("invisible shelf error = %v", err)
	}
}
