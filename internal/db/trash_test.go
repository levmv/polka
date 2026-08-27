package db

import (
	"database/sql"
	"errors"
	"testing"
)

func seedTrashFixture(t *testing.T, d *DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO users (id, username, password_hash, role) VALUES ('u1','alice','x','admin')`,
		`INSERT INTO authors (id, name, sort_name) VALUES ('a1','Frank Herbert','Herbert, Frank')`,
		`INSERT INTO works (id, title, sort_title) VALUES ('w1','Dune','Dune')`,
		`INSERT INTO works (id, title, sort_title) VALUES ('w2','Hyperion','Hyperion')`,
		`INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1','a1',0)`,
		`INSERT INTO search (rowid, work_id, title, authors) VALUES (1,'w1','Dune','Frank Herbert')`,
		`INSERT INTO search (rowid, work_id, title, authors) VALUES (2,'w2','Hyperion','')`,
		`INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('as1','w1','H/Dune/as1.epub','as1.epub','.epub')`,
		`INSERT INTO shelves (id, name, kind, owner_id, visibility) VALUES ('s1','Faves','manual','u1','shared')`,
		`INSERT INTO shelf_books (shelf_id, work_id) VALUES ('s1','w1')`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
}

func newTrashTestDB(t *testing.T) *DB {
	t.Helper()
	d := newTestDB(t)
	seedTrashFixture(t, d)
	return d
}

func TestSoftDeleteHidesWorkEverywhere(t *testing.T) {
	d := newTrashTestDB(t)

	if got := len(mustListBooks(t, d, "")); got != 2 {
		t.Fatalf("baseline list = %d works, want 2", got)
	}

	if err := SoftDeleteWork(d, "w1", "u1"); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// Dropped from browse, FTS search, and manual-shelf listing.
	if got := mustListBooks(t, d, ""); len(got) != 1 || got[0].ID != "w2" {
		t.Fatalf("after delete, browse = %+v, want only w2", got)
	}
	if got := mustListBooks(t, d, "Dune"); len(got) != 0 {
		t.Fatalf("after delete, search 'Dune' = %d, want 0", len(got))
	}
	shelf, err := ListBooksInManualShelf(d, FullVisibilityScope(), "s1", SortRelevance, 50, 0)
	if err != nil {
		t.Fatalf("list shelf: %v", err)
	}
	if len(shelf) != 0 {
		t.Fatalf("after delete, shelf = %d, want 0", len(shelf))
	}

	// Detail look-up now misses, so the normal book page 404s.
	if _, err := GetBook(d, FullVisibilityScope(), "w1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetBook(trashed) err = %v, want ErrNoRows", err)
	}

	// Trash listing surfaces it with the deleter's display name.
	trashed, err := ListTrashedWorks(d, FullVisibilityScope())
	if err != nil {
		t.Fatalf("list trashed: %v", err)
	}
	if len(trashed) != 1 || trashed[0].ID != "w1" {
		t.Fatalf("trashed = %+v, want [w1]", trashed)
	}
	if trashed[0].DeletedByName != "alice" || trashed[0].DeletedAt == 0 {
		t.Fatalf("trashed[0] = %+v, want deleted_by alice and a timestamp", trashed[0])
	}

	// Re-deleting a trashed work is a no-op miss, not a double-delete.
	if err := SoftDeleteWork(d, "w1", "u1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("re-delete err = %v, want ErrNoRows", err)
	}
}

func TestRestoreBringsWorkBack(t *testing.T) {
	d := newTrashTestDB(t)
	if err := SoftDeleteWork(d, "w1", "u1"); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	if err := RestoreWork(d, "w1"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(mustListBooks(t, d, "")); got != 2 {
		t.Fatalf("after restore, browse = %d, want 2", got)
	}
	if _, err := GetBook(d, FullVisibilityScope(), "w1"); err != nil {
		t.Fatalf("GetBook(restored) err = %v, want nil", err)
	}

	// Restoring a live work is a no-op miss.
	if err := RestoreWork(d, "w1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("re-restore err = %v, want ErrNoRows", err)
	}
}

func TestPurgeRemovesRowsAndRefusesLiveWork(t *testing.T) {
	d := newTrashTestDB(t)

	// A live work cannot be purged — purge is the trash-only, irreversible half.
	tx, _ := d.Begin()
	if err := PurgeWork(tx, "w1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("purge live work err = %v, want ErrNoRows", err)
	}
	tx.Rollback()

	if err := SoftDeleteWork(d, "w1", "u1"); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	tx, _ = d.Begin()
	if err := PurgeWork(tx, "w1"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Work row, its assets (FK cascade), its FTS row, its shelf membership, and
	// the now-orphaned author are all gone.
	assertCount(t, d, 0, "SELECT count(*) FROM works WHERE id='w1'")
	assertCount(t, d, 0, "SELECT count(*) FROM assets WHERE work_id='w1'")
	assertCount(t, d, 0, "SELECT count(*) FROM search WHERE work_id='w1'")
	assertCount(t, d, 0, "SELECT count(*) FROM shelf_books WHERE work_id='w1'")
	assertCount(t, d, 0, "SELECT count(*) FROM authors WHERE id='a1'")

	if trashed, _ := ListTrashedWorks(d, FullVisibilityScope()); len(trashed) != 0 {
		t.Fatalf("after purge, trashed = %d, want 0", len(trashed))
	}
	// The untouched work survives.
	assertCount(t, d, 1, "SELECT count(*) FROM works WHERE id='w2'")
}

func TestPurgeAllTrashedWorksExceedsSQLiteParameterLimit(t *testing.T) {
	d := newTestDB(t)
	const trashedCount = 32767
	if _, err := d.Exec(`INSERT INTO works (id, title, sort_title) VALUES ('w-live', 'Live', 'Live')`); err != nil {
		t.Fatalf("seed live work: %v", err)
	}
	if _, err := d.Exec(`
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < 32767
		)
		INSERT INTO works (id, title, sort_title, deleted_at)
		SELECT printf('w-large-%05d', n), printf('Large %d', n), printf('Large %d', n), unixepoch()
		FROM seq
	`); err != nil {
		t.Fatalf("seed large trash: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO search (work_id, title, authors)
		SELECT id, title, '' FROM works WHERE id LIKE 'w-large-%'
	`); err != nil {
		t.Fatalf("seed large trash search rows: %v", err)
	}

	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin purge: %v", err)
	}
	purged, err := PurgeAllTrashedWorks(tx)
	if err != nil {
		tx.Rollback()
		t.Fatalf("purge all: %v", err)
	}
	if purged != trashedCount {
		tx.Rollback()
		t.Fatalf("purged = %d, want %d", purged, trashedCount)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit purge: %v", err)
	}

	assertCount(t, d, 0, "SELECT count(*) FROM works WHERE id LIKE 'w-large-%'")
	assertCount(t, d, 0, "SELECT count(*) FROM search WHERE work_id LIKE 'w-large-%'")
	assertCount(t, d, 1, "SELECT count(*) FROM works")
	assertCount(t, d, 1, "SELECT count(*) FROM works WHERE id='w-live'")
}

func mustListBooks(t *testing.T, d *DB, q string) []BookSummaryRow {
	t.Helper()
	books, err := ListBooks(d, FullVisibilityScope(), "", q, SortRelevance, 50, 0)
	if err != nil {
		t.Fatalf("list books %q: %v", q, err)
	}
	return books
}

func assertCount(t *testing.T, d *DB, want int, query string) {
	t.Helper()
	var got int
	if err := d.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("count %q = %d, want %d", query, got, want)
	}
}
