package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func seedTrashFixture(t *testing.T, d *DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO users (id, username, password_hash, role) VALUES (1,'alice','x','admin')`,
		`INSERT INTO authors (id, name, sort_name) VALUES ('a1','Frank Herbert','Herbert, Frank')`,
		`INSERT INTO books (id, title, sort_title) VALUES (1,'Dune','Dune')`,
		`INSERT INTO books (id, title, sort_title) VALUES (2,'Hyperion','Hyperion')`,
		`INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1,'a1',0)`,
		`INSERT INTO search (rowid, title, authors) VALUES (1,'Dune','Frank Herbert')`,
		`INSERT INTO search (rowid, title, authors) VALUES (2,'Hyperion','')`,
		`INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('as1',1,'H/Dune/as1.epub','as1.epub','.epub')`,
		`INSERT INTO shelves (id, name, kind, owner_id, visibility) VALUES ('s1','Faves','manual',1,'shared')`,
		`INSERT INTO shelf_books (shelf_id, book_id) VALUES ('s1',1)`,
	}
	for _, q := range stmts {
		mustExec(t, d, q)

	}
}

func newTrashTestDB(t *testing.T) *DB {
	t.Helper()
	d := newTestDB(t)
	seedTrashFixture(t, d)
	return d
}

func TestSoftDeleteHidesBookEverywhere(t *testing.T) {
	d := newTrashTestDB(t)

	if got := len(mustListBooks(t, d, "")); got != 2 {
		t.Fatalf("baseline list = %d books, want 2", got)
	}

	if err := SoftDeleteBook(d.Write(t.Context()), 1, 1); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// Dropped from browse, FTS search, and manual-shelf listing.
	if got := mustListBooks(t, d, ""); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("after delete, browse = %+v, want only 2", got)
	}
	if got := mustListBooks(t, d, "Dune"); len(got) != 0 {
		t.Fatalf("after delete, search 'Dune' = %d, want 0", len(got))
	}
	shelf, err := ListBooksInManualShelf(d.Read(t.Context()), FullVisibilityScope(), "s1", SortRelevance, 50, 0)
	if err != nil {
		t.Fatalf("list shelf: %v", err)
	}
	if len(shelf) != 0 {
		t.Fatalf("after delete, shelf = %d, want 0", len(shelf))
	}

	// Detail look-up now misses, so the normal book page 404s.
	if _, err := GetBook(d.Read(t.Context()), FullVisibilityScope(), 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetBook(trashed) err = %v, want ErrNoRows", err)
	}

	// Trash listing surfaces it with the deleter's display name.
	trashed, err := ListTrashedBooks(d.Read(t.Context()), FullVisibilityScope())
	if err != nil {
		t.Fatalf("list trashed: %v", err)
	}
	if len(trashed) != 1 || trashed[0].ID != 1 {
		t.Fatalf("trashed = %+v, want [1]", trashed)
	}
	if trashed[0].DeletedByName != "alice" || trashed[0].DeletedAt == 0 {
		t.Fatalf("trashed[0] = %+v, want deleted_by alice and a timestamp", trashed[0])
	}

	// Re-deleting a trashed book is a no-op miss, not a double-delete.
	if err := SoftDeleteBook(d.Write(t.Context()), 1, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("re-delete err = %v, want ErrNoRows", err)
	}
}

func TestRestoreBringsBookBack(t *testing.T) {
	d := newTrashTestDB(t)
	if err := SoftDeleteBook(d.Write(t.Context()), 1, 1); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	if err := RestoreBook(d.Write(t.Context()), 1); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(mustListBooks(t, d, "")); got != 2 {
		t.Fatalf("after restore, browse = %d, want 2", got)
	}
	if _, err := GetBook(d.Read(t.Context()), FullVisibilityScope(), 1); err != nil {
		t.Fatalf("GetBook(restored) err = %v, want nil", err)
	}

	// Restoring a live book is a no-op miss.
	if err := RestoreBook(d.Write(t.Context()), 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("re-restore err = %v, want ErrNoRows", err)
	}
}

func TestPurgeRemovesRowsAndRefusesLiveBook(t *testing.T) {
	d := newTrashTestDB(t)

	// A live book cannot be purged — purge is the trash-only, irreversible half.
	tx, _ := d.BeginWrite(context.Background())
	if err := PurgeBook(tx, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("purge live book err = %v, want ErrNoRows", err)
	}
	tx.Rollback()

	if err := SoftDeleteBook(d.Write(t.Context()), 1, 1); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	tx, _ = d.BeginWrite(context.Background())
	if err := PurgeBook(tx, 1); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Book row, its assets (FK cascade), its FTS row, its shelf membership, and
	// the now-orphaned author are all gone.
	assertCount(t, d, 0, "SELECT count(*) FROM books WHERE id=1")
	assertCount(t, d, 0, "SELECT count(*) FROM assets WHERE book_id=1")
	assertCount(t, d, 0, "SELECT count(*) FROM search WHERE rowid=1")
	assertCount(t, d, 0, "SELECT count(*) FROM shelf_books WHERE book_id=1")
	assertCount(t, d, 0, "SELECT count(*) FROM authors WHERE id='a1'")

	if trashed, _ := ListTrashedBooks(d.Read(t.Context()), FullVisibilityScope()); len(trashed) != 0 {
		t.Fatalf("after purge, trashed = %d, want 0", len(trashed))
	}
	// The untouched book survives.
	assertCount(t, d, 1, "SELECT count(*) FROM books WHERE id=2")
}

func TestPurgeAllTrashedBooksExceedsSQLiteParameterLimit(t *testing.T) {
	d := newTestDB(t)
	mustExec(t, d, `
		INSERT INTO books (id, title, sort_title, deleted_at) VALUES
			(1, 'Live', 'Live', NULL),
			(2, 'Trashed A', 'Trashed A', 100),
			(3, 'Trashed B', 'Trashed B', 100),
			(4, 'Trashed C', 'Trashed C', 100);
		INSERT INTO search (rowid, title) SELECT id, title FROM books;
	`)

	tx, err := d.BeginWrite(context.Background())
	if err != nil {
		t.Fatalf("begin purge: %v", err)
	}
	defer tx.Rollback()
	// Three trashed books must exceed the writer's parameter limit without
	// making a large fixture. This changes only this disposable connection.
	if _, err := sqlite.Limit(tx.conn, sqlite3.SQLITE_LIMIT_VARIABLE_NUMBER, 2); err != nil {
		t.Fatalf("lower parameter limit: %v", err)
	}
	purged, err := PurgeAllTrashedBooks(tx)
	if err != nil {
		t.Fatalf("purge all: %v", err)
	}
	if purged != 3 {
		t.Fatalf("purged = %d, want 3", purged)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit purge: %v", err)
	}

	assertCount(t, d, 1, "SELECT count(*) FROM books")
	assertCount(t, d, 1, "SELECT count(*) FROM books WHERE id=1")
	assertCount(t, d, 1, "SELECT count(*) FROM search")
	assertCount(t, d, 1, "SELECT count(*) FROM search WHERE search MATCH 'Live'")
}

func mustListBooks(t *testing.T, d *DB, q string) []BookSummaryRow {
	t.Helper()
	books, err := ListBooks(d.Read(t.Context()), FullVisibilityScope(), 0, q, SortRelevance, 50, 0)
	if err != nil {
		t.Fatalf("list books %q: %v", q, err)
	}
	return books
}

func assertCount(t *testing.T, d *DB, want int, query string) {
	t.Helper()
	var got int
	if err := d.Read(t.Context()).QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("count %q = %d, want %d", query, got, want)
	}
}
