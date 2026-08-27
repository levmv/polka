package db

import (
	"strings"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
)

func TestAuthorsWithCountsPageUsesStableTieBreaker(t *testing.T) {
	database := newTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO authors (id, name, sort_name) VALUES
			('a1', 'First Name', 'Same Sort'),
			('a2', 'Second Name', 'Same Sort');
		INSERT INTO works (id, title, sort_title) VALUES
			('w1', 'First', 'First'),
			('w2', 'Second', 'Second');
		INSERT INTO work_authors (work_id, author_id, author_order) VALUES
			('w1', 'a1', 0),
			('w2', 'a2', 0);
	`); err != nil {
		t.Fatalf("seed authors: %v", err)
	}

	first, err := ListAuthorCountsPage(database, FullVisibilityScope(), "", "", 1)
	if err != nil {
		t.Fatalf("first author page: %v", err)
	}
	if len(first) != 1 || first[0].ID != "a1" {
		t.Fatalf("first author page = %+v; want a1", first)
	}
	second, err := ListAuthorCountsPage(database, FullVisibilityScope(), first[0].SortName, first[0].ID, 1)
	if err != nil {
		t.Fatalf("second author page: %v", err)
	}
	if len(second) != 1 || second[0].ID != "a2" {
		t.Fatalf("second author page = %+v; want a2", second)
	}
}

func TestDeleteOrphanAuthors(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T', 'T')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a_keep', 'Kept Author', 'Author, Kept')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a_orphan', 'Orphan Author', 'Author, Orphan')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a_keep', 0)")

	n, err := DeleteOrphanAuthors(database)
	if err != nil {
		t.Fatalf("DeleteOrphanAuthors: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d authors, want 1", n)
	}

	var cnt int
	database.QueryRow("SELECT COUNT(*) FROM authors WHERE id = 'a_orphan'").Scan(&cnt)
	if cnt != 0 {
		t.Errorf("orphan author was not deleted")
	}
	database.QueryRow("SELECT COUNT(*) FROM authors WHERE id = 'a_keep'").Scan(&cnt)
	if cnt != 1 {
		t.Errorf("referenced author was wrongly deleted")
	}

	// Idempotent: a second sweep deletes nothing.
	n, err = DeleteOrphanAuthors(database)
	if err != nil || n != 0 {
		t.Errorf("second sweep: n=%d err=%v, want 0/nil", n, err)
	}
}

// TestUpsertWorkAuthors locks in the shared find-or-insert/link behavior, in
// particular that reusing an existing author adopts that row's persisted
// sort_name rather than the supplied one — the agreement import and edit must
// keep so the canonical path matches the author row (`polka check`).
func TestUpsertWorkAuthors(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T', 'T')")
	// An author whose persisted sort_name was overridden away from the naive
	// derivation (e.g. via the "Sort as" editor).
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a_lg', 'Ursula K. Le Guin', 'Le Guin, Ursula K.')")

	tx, _ := database.Begin()
	name, sort, err := UpsertWorkAuthors(tx, "w1", []bookmeta.AuthorMeta{
		{Name: "Ursula K. Le Guin", SortName: "WRONG, Sort", Role: "aut"},
		{Name: "New Coauthor", SortName: "Coauthor, New"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	tx.Commit()

	// Primary reflects the adopted (persisted) sort_name, not the supplied one.
	if name != "Ursula K. Le Guin" || sort != "Le Guin, Ursula K." {
		t.Errorf("primary = (%q, %q), want (Ursula K. Le Guin, Le Guin, Ursula K.)", name, sort)
	}
	var workSort string
	database.QueryRow("SELECT primary_author_sort FROM works WHERE id = 'w1'").Scan(&workSort)
	if workSort != "Le Guin, Ursula K." {
		t.Errorf("work primary_author_sort = %q, want Le Guin, Ursula K.", workSort)
	}
	// The existing row's sort_name is left untouched.
	var lgSort string
	database.QueryRow("SELECT sort_name FROM authors WHERE id = 'a_lg'").Scan(&lgSort)
	if lgSort != "Le Guin, Ursula K." {
		t.Errorf("existing author sort_name overwritten: %q", lgSort)
	}
	// The new author was inserted with the supplied sort_name.
	var newSort string
	if err := database.QueryRow("SELECT sort_name FROM authors WHERE name = 'New Coauthor'").Scan(&newSort); err != nil {
		t.Fatalf("new author not inserted: %v", err)
	}
	if newSort != "Coauthor, New" {
		t.Errorf("new author sort_name = %q, want Coauthor, New", newSort)
	}
	var newID string
	if err := database.QueryRow("SELECT id FROM authors WHERE name = 'New Coauthor'").Scan(&newID); err != nil {
		t.Fatalf("new author id: %v", err)
	}
	if !strings.HasPrefix(newID, "au_") {
		t.Errorf("new author id = %q, want au_ prefix", newID)
	}
	// Links recorded in slice order, with role carried through.
	got, _ := AuthorsByWorkIDs(database, []string{"w1"})
	if len(got["w1"]) != 2 || got["w1"][0].Name != "Ursula K. Le Guin" || got["w1"][1].Name != "New Coauthor" {
		t.Fatalf("links = %+v, want [Ursula K. Le Guin, New Coauthor]", got["w1"])
	}
	if got["w1"][0].Role != "aut" {
		t.Errorf("primary role = %q, want aut", got["w1"][0].Role)
	}

	// Calling again replaces the set (clears prior links) rather than appending.
	tx, _ = database.Begin()
	if _, _, err := UpsertWorkAuthors(tx, "w1", []bookmeta.AuthorMeta{{Name: "Solo Author", SortName: "Author, Solo"}}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	tx.Commit()
	got, _ = AuthorsByWorkIDs(database, []string{"w1"})
	if len(got["w1"]) != 1 || got["w1"][0].Name != "Solo Author" {
		t.Errorf("after replace, links = %+v, want [Solo Author]", got["w1"])
	}
	database.QueryRow("SELECT primary_author_sort FROM works WHERE id = 'w1'").Scan(&workSort)
	if workSort != "Author, Solo" {
		t.Errorf("after replace primary_author_sort = %q, want Author, Solo", workSort)
	}
}

// TestUpsertWorkAuthorsDeduplicates covers the case a real EPUB or an editor
// produces: the same author name listed twice. Both would resolve to one author
// row and collide on the work_authors PK, so the upsert must collapse them to a
// single link rather than fail the whole edit/import.
func TestUpsertWorkAuthorsDeduplicates(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T', 'T')")

	tx, _ := database.Begin()
	name, sort, err := UpsertWorkAuthors(tx, "w1", []bookmeta.AuthorMeta{
		{Name: "Ivan Ivanov", SortName: "Ivanov, Ivan", Role: "aut"},
		{Name: "Ivan Ivanov", SortName: "Ivanov, Ivan"},
		{Name: "Petr Petrov", SortName: "Petrov, Petr"},
	})
	if err != nil {
		t.Fatalf("upsert with duplicate author: %v", err)
	}
	tx.Commit()

	if name != "Ivan Ivanov" || sort != "Ivanov, Ivan" {
		t.Errorf("primary = (%q, %q), want (Ivan Ivanov, Ivanov, Ivan)", name, sort)
	}
	got, _ := AuthorsByWorkIDs(database, []string{"w1"})
	if len(got["w1"]) != 2 || got["w1"][0].Name != "Ivan Ivanov" || got["w1"][1].Name != "Petr Petrov" {
		t.Fatalf("links = %+v, want [Ivan Ivanov, Petr Petrov]", got["w1"])
	}
}

func TestAuthorsByWorkIDs(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T1', 'T1')")
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w2', 'T2', 'T2')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Ursula K. Le Guin', 'Le Guin, Ursula K.')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'Cixin Liu', 'Liu, Cixin')")
	// w1 has two authors in a deliberate order (a2 first, a1 second).
	database.Exec("INSERT INTO work_authors (work_id, author_id, role, author_order) VALUES ('w1', 'a2', 'author', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, role, author_order) VALUES ('w1', 'a1', '', 1)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a1', 0)")

	got, err := AuthorsByWorkIDs(database, []string{"w1", "w2"})
	if err != nil {
		t.Fatalf("AuthorsByWorkIDs: %v", err)
	}

	if len(got["w1"]) != 2 {
		t.Fatalf("w1: got %d authors, want 2", len(got["w1"]))
	}
	if got["w1"][0].Name != "Cixin Liu" || got["w1"][1].Name != "Ursula K. Le Guin" {
		t.Errorf("w1 order wrong: %q then %q", got["w1"][0].Name, got["w1"][1].Name)
	}
	if got["w1"][0].SortName != "Liu, Cixin" {
		t.Errorf("w1 sort_name wrong: %q", got["w1"][0].SortName)
	}
	if got["w1"][0].Role != "author" {
		t.Errorf("w1 role wrong: %q", got["w1"][0].Role)
	}
	if len(got["w2"]) != 1 || got["w2"][0].Name != "Ursula K. Le Guin" {
		t.Errorf("w2 wrong: %+v", got["w2"])
	}

	// Empty input yields an empty (non-nil) map.
	if m, err := AuthorsByWorkIDs(database, nil); err != nil || m == nil || len(m) != 0 {
		t.Errorf("empty input: m=%v err=%v", m, err)
	}
}

func TestListAuthorNames(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T', 'T')")
	database.Exec("INSERT INTO works (id, title, sort_title, deleted_at) VALUES ('w_deleted', 'Deleted', 'Deleted', 10)")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Isaac Asimov', 'Asimov, Isaac')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'Orphan Author', 'Author, Orphan')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a3', '50% Discount', 'Discount')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a_deleted', 'Trash Only', 'Trash Only')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a3', 1)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w_deleted', 'a_deleted', 0)")

	names := func(rows []AuthorRow) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.Name
		}
		return out
	}

	// No filter: only authors referenced by live works (orphan/trash excluded),
	// ordered by sort_name.
	all, err := ListAuthorNames(database, FullVisibilityScope(), "", 20)
	if err != nil {
		t.Fatalf("ListAuthorNames: %v", err)
	}
	got := names(all)
	if len(got) != 2 || got[0] != "Isaac Asimov" || got[1] != "50% Discount" {
		t.Errorf("unfiltered = %v, want [Isaac Asimov, 50%% Discount]", got)
	}

	// Substring filter.
	asi, _ := ListAuthorNames(database, FullVisibilityScope(), "asimov", 20)
	if len(asi) != 1 || asi[0].Name != "Isaac Asimov" {
		t.Errorf("q=asimov = %v", names(asi))
	}

	// LIKE wildcard in the query is matched literally, not as a wildcard.
	pct, _ := ListAuthorNames(database, FullVisibilityScope(), "%", 20)
	if len(pct) != 1 || pct[0].Name != "50% Discount" {
		t.Errorf("q=%% = %v, want only the literal-%% author", names(pct))
	}
}

func TestGetAuthorInfo(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'A', 'A')")
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w2', 'B', 'B')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Isaac Asimov', 'Asimov, Isaac')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'Orphan Author', 'Author, Orphan')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a1', 0)")

	// Referenced author: count reflects the linked works, sort_name is returned.
	info, ok, err := GetAuthorInfo(database, FullVisibilityScope(), "Isaac Asimov")
	if err != nil {
		t.Fatalf("GetAuthorInfo: %v", err)
	}
	if !ok || info.BookCount != 2 || info.SortName != "Asimov, Isaac" {
		t.Errorf("got %+v ok=%v, want count=2 sort=Asimov, Isaac", info, ok)
	}

	// Orphan authors are outside the content projection.
	orphan, ok, err := GetAuthorInfo(database, FullVisibilityScope(), "Orphan Author")
	if err != nil {
		t.Fatalf("GetAuthorInfo orphan: %v", err)
	}
	if ok || orphan.BookCount != 0 {
		t.Errorf("orphan = %+v ok=%v, want not found", orphan, ok)
	}

	// Exact-match identity: a different spelling is a different (absent) author.
	if _, ok, err := GetAuthorInfo(database, FullVisibilityScope(), "I. Asimov"); err != nil || ok {
		t.Errorf("I. Asimov: ok=%v err=%v, want not found", ok, err)
	}
}

func authorNames(t *testing.T, q Queryer) []string {
	rows, err := q.Query("SELECT name FROM authors ORDER BY name")
	if err != nil {
		t.Fatalf("query authors: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func TestRenameAuthorInPlace(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T', 'T')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'I. Asimov', 'Asimov, I.')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")

	tx, _ := database.Begin()
	affected, err := RenameOrMergeAuthor(tx, "I. Asimov", "Isaac Asimov", "Asimov, Isaac")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	tx.Commit()

	if len(affected) != 1 || affected[0] != "w1" {
		t.Errorf("affected = %v, want [w1]", affected)
	}
	if names := authorNames(t, database); len(names) != 1 || names[0] != "Isaac Asimov" {
		t.Errorf("authors = %v, want [Isaac Asimov]", names)
	}
	// Same row id, so the work link still resolves.
	var sort string
	database.QueryRow("SELECT sort_name FROM authors WHERE id = 'a1'").Scan(&sort)
	if sort != "Asimov, Isaac" {
		t.Errorf("sort_name = %q", sort)
	}
	var workSort string
	database.QueryRow("SELECT primary_author_sort FROM works WHERE id = 'w1'").Scan(&workSort)
	if workSort != "Asimov, Isaac" {
		t.Errorf("primary_author_sort = %q, want Asimov, Isaac", workSort)
	}
	var rev int
	database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w1'").Scan(&rev)
	if rev != 0 {
		t.Errorf("metadata_rev = %d, want DB mutation to leave bookkeeping to caller", rev)
	}
}

func TestMergeAuthor(t *testing.T) {
	database := newTestDB(t)

	for _, w := range []string{"w1", "w2", "w3"} {
		database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, 'T', 'T')", w)
	}
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'I. Asimov', 'Asimov, I.')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'Isaac Asimov', 'Asimov, Isaac')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a2', 0)")
	// w3 credits BOTH spellings (the merge must not create a duplicate link).
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w3', 'a1', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w3', 'a2', 1)")

	tx, _ := database.Begin()
	affected, err := RenameOrMergeAuthor(tx, "I. Asimov", "Isaac Asimov", "Asimov, Isaac")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	tx.Commit()

	if len(affected) != 2 {
		t.Errorf("affected = %v, want 2 works (w1, w3)", affected)
	}
	if names := authorNames(t, database); len(names) != 1 || names[0] != "Isaac Asimov" {
		t.Errorf("authors = %v, want only [Isaac Asimov]", names)
	}
	// No work credits the deleted source author.
	var oldLinks int
	database.QueryRow("SELECT COUNT(*) FROM work_authors WHERE author_id = 'a1'").Scan(&oldLinks)
	if oldLinks != 0 {
		t.Errorf("old author still linked to %d works", oldLinks)
	}
	// w3 ends with exactly one link to the target (no duplicate).
	var w3Links int
	database.QueryRow("SELECT COUNT(*) FROM work_authors WHERE work_id = 'w3' AND author_id = 'a2'").Scan(&w3Links)
	if w3Links != 1 {
		t.Errorf("w3 has %d links to target, want 1", w3Links)
	}
	var w1Sort, w3Sort string
	database.QueryRow("SELECT primary_author_sort FROM works WHERE id = 'w1'").Scan(&w1Sort)
	database.QueryRow("SELECT primary_author_sort FROM works WHERE id = 'w3'").Scan(&w3Sort)
	if w1Sort != "Asimov, Isaac" || w3Sort != "Asimov, Isaac" {
		t.Errorf("merged primary_author_sort = w1:%q w3:%q, want Asimov, Isaac", w1Sort, w3Sort)
	}
	var rev1, rev2, rev3 int
	database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w1'").Scan(&rev1)
	database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w2'").Scan(&rev2)
	database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w3'").Scan(&rev3)
	if rev1 != 0 || rev2 != 0 || rev3 != 0 {
		t.Errorf("metadata_rev = w1:%d w2:%d w3:%d, want DB mutation to leave bookkeeping to caller", rev1, rev2, rev3)
	}
}

func TestSetAuthorSortNameUpdatesPrimaryAuthorSort(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'Primary', 'Primary')")
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w2', 'Secondary', 'Secondary')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Author One', 'Author One')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'Other Author', 'Other Author')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a2', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a1', 1)")
	if err := updatePrimaryAuthorSorts(database, []string{"w1", "w2"}); err != nil {
		t.Fatalf("initial updatePrimaryAuthorSorts: %v", err)
	}

	tx, _ := database.Begin()
	affected, err := SetAuthorSortName(tx, "Author One", "One, Author")
	if err != nil {
		t.Fatalf("set sort name: %v", err)
	}
	tx.Commit()

	if len(affected) != 2 {
		t.Fatalf("affected = %v, want both linked works", affected)
	}
	var primarySort, secondarySort string
	database.QueryRow("SELECT primary_author_sort FROM works WHERE id = 'w1'").Scan(&primarySort)
	database.QueryRow("SELECT primary_author_sort FROM works WHERE id = 'w2'").Scan(&secondarySort)
	if primarySort != "One, Author" {
		t.Fatalf("primary work sort = %q; want One, Author", primarySort)
	}
	if secondarySort != "Other Author" {
		t.Fatalf("secondary work sort = %q; want Other Author", secondarySort)
	}
	var rev1, rev2 int
	database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w1'").Scan(&rev1)
	database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w2'").Scan(&rev2)
	if rev1 != 0 || rev2 != 0 {
		t.Fatalf("metadata_rev = %d/%d; want DB mutation to leave bookkeeping to caller", rev1, rev2)
	}
}

func TestSetAuthorSortNameNoOpDoesNotBumpMetadataRev(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'Primary', 'Primary')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Author One', 'Author One')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")

	tx, _ := database.Begin()
	affected, err := SetAuthorSortName(tx, "Author One", "Author One")
	if err != nil {
		t.Fatalf("set sort name: %v", err)
	}
	tx.Commit()

	if len(affected) != 0 {
		t.Fatalf("affected = %v, want none", affected)
	}
	var rev int
	database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w1'").Scan(&rev)
	if rev != 0 {
		t.Fatalf("metadata_rev = %d; want 0", rev)
	}
}
