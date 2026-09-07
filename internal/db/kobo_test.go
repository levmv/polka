package db

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func seedKoboBook(t *testing.T, database *DB, bookID int64, assetID string, title string, formatKey string, tags string) {
	t.Helper()
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, tags, language, publisher)
		VALUES (?, ?, ?, ?, 'en', 'Polka Press')
	`, bookID, title, title, tags)
	mustExec(t, database, `
		INSERT INTO assets
		    (id, book_id, storage_path, filename, extension, format, is_primary, current_size)
		VALUES (?, ?, ?, ?, ?, ?, 1, 1234)
	`, assetID, bookID, strconv.FormatInt(bookID, 10)+"/"+assetID+"."+formatKey, assetID+"."+formatKey, formatKey, formatKey)
	mustExec(t, database, `INSERT INTO search (rowid, title, tags) VALUES (?1, ?2, ?3)`, bookID, title, tags)

}

func TestKoboConnectionIncrementalLifecycle(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser(t.Context(), "kobo-reader", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfPersonal, "On Kobo", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	seedKoboBook(t, database, 161, "a_epub", "One", "epub", "chosen")
	seedKoboBook(t, database, 176, "a_two", "Two", "epub", "outside")
	mustExec(t, database, `
		UPDATE assets SET is_primary = 0 WHERE id = 'a_epub';
		INSERT INTO assets
		    (id, book_id, storage_path, filename, extension, format, is_primary, current_size)
		VALUES ('a_kepub', 161, '161/a_kepub.kepub', 'a_kepub.kepub', 'kepub', 'kepub', 0, 1400);
	`)

	if err := database.AddBookToShelf(t.Context(), shelf.ID, user.ID, 161); err != nil {
		t.Fatal(err)
	}

	connection, err := database.ReplaceKoboConnection(context.Background(), user.ID, shelf.ID)
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
	if _, err := KoboPublicationForAsset(database.Read(t.Context()), connection.ID, "a_kepub"); !errors.Is(err, ErrKoboConnectionNotFound) {
		t.Fatalf("removed asset lookup: %v", err)
	}
	if _, err := KoboPublicationForAsset(database.Read(t.Context()), connection.ID, "a_two"); !errors.Is(err, ErrKoboConnectionNotFound) {
		t.Fatalf("outside asset lookup: %v", err)
	}

	if err := database.AddBookToShelf(t.Context(), shelf.ID, user.ID, 161); err != nil {
		t.Fatal(err)
	}
	readded, _, _, err := database.SyncKoboConnection(context.Background(), connection.ID, current, KoboSyncPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(readded) != 1 || !readded[0].Present || readded[0].Revision != 4 || readded[0].FirstRevision != 1 {
		t.Fatalf("re-add = %+v", readded)
	}

	replacement, err := database.ReplaceKoboConnection(context.Background(), user.ID, shelf.ID)
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
	user, err := database.CreateUser(t.Context(), "query-kobo", "pw", RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	seedKoboBook(t, database, 103, "a_a", "A", "epub", "send")
	seedKoboBook(t, database, 112, "a_b", "B", "epub", "send")
	seedKoboBook(t, database, 115, "a_c", "C", "epub", "skip")
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Send", ShelfQuery, "tag:send")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatal(err)
	}
	connection, err := database.ReplaceKoboConnection(context.Background(), user.ID, shelf.ID)
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
	alice, err := database.CreateUser(t.Context(), "alice-kobo", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := database.CreateUser(t.Context(), "bob-kobo", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := database.CreateShelf(t.Context(), alice.ID, ShelfPersonal, "Alice only", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ReplaceKoboConnection(context.Background(), bob.ID, shelf.ID); !errors.Is(err, ErrShelfNotFound) {
		t.Fatalf("invisible shelf error = %v", err)
	}
}
