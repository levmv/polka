package db

import (
	"context"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
)

func TestAuthorsWithCountsPageUsesStableTieBreaker(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, `
		INSERT INTO authors (id, name, sort_name) VALUES
			(2, 'First Name', 'Same Sort'),
			(1, 'Second Name', 'Same Sort');
		INSERT INTO books (id, title, sort_title) VALUES
			(1, 'First', 'First'),
			(2, 'Second', 'Second');
		INSERT INTO book_authors (book_id, author_id, author_order) VALUES
			(1, 2, 0),
			(2, 1, 0);
	`)

	first, err := ListAuthorCountsPage(database.Read(t.Context()), FullVisibilityScope(), "", "", 1)
	if err != nil {
		t.Fatalf("first author page: %v", err)
	}
	if len(first) != 1 || first[0].Name != "First Name" {
		t.Fatalf("first author page = %+v; want First Name", first)
	}
	second, err := ListAuthorCountsPage(database.Read(t.Context()), FullVisibilityScope(), first[0].SortName, first[0].Name, 1)
	if err != nil {
		t.Fatalf("second author page: %v", err)
	}
	if len(second) != 1 || second[0].Name != "Second Name" {
		t.Fatalf("second author page = %+v; want Second Name", second)
	}
}

func TestDeleteOrphanAuthors(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T', 'T')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Kept Author', 'Author, Kept')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (2, 'Orphan Author', 'Author, Orphan')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")

	n, err := DeleteOrphanAuthors(database.Write(t.Context()))
	if err != nil {
		t.Fatalf("DeleteOrphanAuthors: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d authors, want 1", n)
	}

	var cnt int
	database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM authors WHERE id = 2").Scan(&cnt)
	if cnt != 0 {
		t.Errorf("orphan author was not deleted")
	}
	database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM authors WHERE id = 1").Scan(&cnt)
	if cnt != 1 {
		t.Errorf("referenced author was wrongly deleted")
	}

	// Idempotent: a second sweep deletes nothing.
	n, err = DeleteOrphanAuthors(database.Write(t.Context()))
	if err != nil || n != 0 {
		t.Errorf("second sweep: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestUpsertBookAuthors(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T', 'T')")
	// Reusing an author must preserve the sort name from the library.
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Ursula K. Le Guin', 'Le Guin, Ursula K.')")

	tx, err := database.BeginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	name, sort, err := UpsertBookAuthors(tx, 1, []bookmeta.AuthorMeta{
		{Name: "Ursula K. Le Guin", SortName: "WRONG, Sort", Role: "aut"},
		{Name: "New Coauthor", SortName: "Coauthor, New"},
		{Name: "Ursula K. Le Guin", SortName: "WRONG, Sort"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Primary reflects the adopted (persisted) sort_name, not the supplied one.
	if name != "Ursula K. Le Guin" || sort != "Le Guin, Ursula K." {
		t.Errorf("primary = (%q, %q), want (Ursula K. Le Guin, Le Guin, Ursula K.)", name, sort)
	}
	var bookSort string
	if err := database.Read(t.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = 1").Scan(&bookSort); err != nil {
		t.Fatal(err)
	}
	if bookSort != "Le Guin, Ursula K." {
		t.Errorf("book primary_author_sort = %q, want Le Guin, Ursula K.", bookSort)
	}
	// Links recorded in slice order, with role carried through.
	got, err := AuthorsByBookIDs(database.Read(t.Context()), []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[1]) != 2 || got[1][0].Name != "Ursula K. Le Guin" || got[1][1].Name != "New Coauthor" {
		t.Fatalf("links = %+v, want [Ursula K. Le Guin, New Coauthor]", got[1])
	}
	if got[1][0].SortName != "Le Guin, Ursula K." || got[1][1].SortName != "Coauthor, New" {
		t.Errorf("author sort names = %+v; want stored sort for existing author and supplied sort for new author", got[1])
	}
	if got[1][0].Role != "aut" {
		t.Errorf("primary role = %q, want aut", got[1][0].Role)
	}

	// Calling again replaces the set (clears prior links) rather than appending.
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		_, _, err := UpsertBookAuthors(tx, 1, []bookmeta.AuthorMeta{{Name: "Solo Author", SortName: "Author, Solo"}})
		return err
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err = AuthorsByBookIDs(database.Read(t.Context()), []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[1]) != 1 || got[1][0].Name != "Solo Author" {
		t.Errorf("after replace, links = %+v, want [Solo Author]", got[1])
	}
	if err := database.Read(t.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = 1").Scan(&bookSort); err != nil {
		t.Fatal(err)
	}
	if bookSort != "Author, Solo" {
		t.Errorf("after replace primary_author_sort = %q, want Author, Solo", bookSort)
	}
}

func TestAuthorsByBookIDs(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T1', 'T1')")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (2, 'T2', 'T2')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Ursula K. Le Guin', 'Le Guin, Ursula K.')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (2, 'Cixin Liu', 'Liu, Cixin')")
	// The first book credits Cixin Liu before Ursula K. Le Guin.
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, role, author_order) VALUES (1, 2, 'author', 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, role, author_order) VALUES (1, 1, '', 1)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 1, 0)")

	got, err := AuthorsByBookIDs(database.Read(t.Context()), []int64{1, 2})
	if err != nil {
		t.Fatalf("AuthorsByBookIDs: %v", err)
	}

	if len(got[1]) != 2 {
		t.Fatalf("1: got %d authors, want 2", len(got[1]))
	}
	if got[1][0].Name != "Cixin Liu" || got[1][1].Name != "Ursula K. Le Guin" {
		t.Errorf("1 order wrong: %q then %q", got[1][0].Name, got[1][1].Name)
	}
	if got[1][0].SortName != "Liu, Cixin" {
		t.Errorf("1 sort_name wrong: %q", got[1][0].SortName)
	}
	if got[1][0].Role != "author" {
		t.Errorf("1 role wrong: %q", got[1][0].Role)
	}
	if len(got[2]) != 1 || got[2][0].Name != "Ursula K. Le Guin" {
		t.Errorf("2 wrong: %+v", got[2])
	}

	// Empty input yields an empty (non-nil) map.
	if m, err := AuthorsByBookIDs(database.Read(t.Context()), nil); err != nil || m == nil || len(m) != 0 {
		t.Errorf("empty input: m=%v err=%v", m, err)
	}
}

func TestListAuthorNames(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T', 'T')")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, deleted_at) VALUES (122, 'Deleted', 'Deleted', 10)")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Isaac Asimov', 'Asimov, Isaac')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (2, 'Orphan Author', 'Author, Orphan')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (3, '50% Discount', 'Discount')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (4, 'Trash Only', 'Trash Only')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 3, 1)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (122, 4, 0)")

	names := func(rows []AuthorNameRow) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.Name
		}
		return out
	}

	// No filter: only authors referenced by live books (orphan/trash excluded),
	// ordered by sort_name.
	all, err := ListAuthorNames(database.Read(t.Context()), FullVisibilityScope(), "", 20)
	if err != nil {
		t.Fatalf("ListAuthorNames: %v", err)
	}
	got := names(all)
	if len(got) != 2 || got[0] != "Isaac Asimov" || got[1] != "50% Discount" {
		t.Errorf("unfiltered = %v, want [Isaac Asimov, 50%% Discount]", got)
	}

	// Substring filter.
	asi, _ := ListAuthorNames(database.Read(t.Context()), FullVisibilityScope(), "asimov", 20)
	if len(asi) != 1 || asi[0].Name != "Isaac Asimov" {
		t.Errorf("q=asimov = %v", names(asi))
	}

	// LIKE wildcard in the query is matched literally, not as a wildcard.
	pct, _ := ListAuthorNames(database.Read(t.Context()), FullVisibilityScope(), "%", 20)
	if len(pct) != 1 || pct[0].Name != "50% Discount" {
		t.Errorf("q=%% = %v, want only the literal-%% author", names(pct))
	}
}

func TestGetAuthorInfo(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'A', 'A')")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (2, 'B', 'B')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Isaac Asimov', 'Asimov, Isaac')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (2, 'Orphan Author', 'Author, Orphan')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 1, 0)")

	// Referenced author: count reflects the linked books, sort_name is returned.
	info, ok, err := GetAuthorInfo(database.Read(t.Context()), FullVisibilityScope(), "Isaac Asimov")
	if err != nil {
		t.Fatalf("GetAuthorInfo: %v", err)
	}
	if !ok || info.BookCount != 2 || info.SortName != "Asimov, Isaac" {
		t.Errorf("got %+v ok=%v, want count=2 sort=Asimov, Isaac", info, ok)
	}

	// Orphan authors are outside the content projection.
	orphan, ok, err := GetAuthorInfo(database.Read(t.Context()), FullVisibilityScope(), "Orphan Author")
	if err != nil {
		t.Fatalf("GetAuthorInfo orphan: %v", err)
	}
	if ok || orphan.BookCount != 0 {
		t.Errorf("orphan = %+v ok=%v, want not found", orphan, ok)
	}

	// Exact-match identity: a different spelling is a different (absent) author.
	if _, ok, err := GetAuthorInfo(database.Read(t.Context()), FullVisibilityScope(), "I. Asimov"); err != nil || ok {
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
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T', 'T')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'I. Asimov', 'Asimov, I.')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")

	tx, _ := database.BeginWrite(context.Background())
	affected, err := RenameOrMergeAuthor(tx, "I. Asimov", "Isaac Asimov", "Asimov, Isaac")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	tx.Commit()

	if len(affected) != 1 || affected[0] != 1 {
		t.Errorf("affected = %v, want [1]", affected)
	}
	if names := authorNames(t, database.Read(t.Context())); len(names) != 1 || names[0] != "Isaac Asimov" {
		t.Errorf("authors = %v, want [Isaac Asimov]", names)
	}
	// Same row id, so the book link still resolves.
	var sort string
	database.Read(t.Context()).QueryRow("SELECT sort_name FROM authors WHERE id = 1").Scan(&sort)
	if sort != "Asimov, Isaac" {
		t.Errorf("sort_name = %q", sort)
	}
	var bookSort string
	database.Read(t.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = 1").Scan(&bookSort)
	if bookSort != "Asimov, Isaac" {
		t.Errorf("primary_author_sort = %q, want Asimov, Isaac", bookSort)
	}
}

func TestMergeAuthor(t *testing.T) {
	database := newTestDB(t)

	for _, w := range []int64{1, 2, 3} {
		mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, 'T', 'T')", w)
	}
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'I. Asimov', 'Asimov, I.')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (2, 'Isaac Asimov', 'Asimov, Isaac')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 2, 0)")
	// Book 3 credits both spellings; merging must not create a duplicate link.
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (3, 1, 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (3, 2, 1)")

	tx, _ := database.BeginWrite(context.Background())
	affected, err := RenameOrMergeAuthor(tx, "I. Asimov", "Isaac Asimov", "Asimov, Isaac")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	tx.Commit()

	if len(affected) != 2 {
		t.Errorf("affected = %v, want 2 books (1, 3)", affected)
	}
	if names := authorNames(t, database.Read(t.Context())); len(names) != 1 || names[0] != "Isaac Asimov" {
		t.Errorf("authors = %v, want only [Isaac Asimov]", names)
	}
	// No book credits the deleted source author.
	var oldLinks int
	database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM book_authors WHERE author_id = 1").Scan(&oldLinks)
	if oldLinks != 0 {
		t.Errorf("old author still linked to %d books", oldLinks)
	}
	// Book 3 ends with exactly one link to the target.
	var w3Links int
	database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM book_authors WHERE book_id = 3 AND author_id = 2").Scan(&w3Links)
	if w3Links != 1 {
		t.Errorf("3 has %d links to target, want 1", w3Links)
	}
	var w1Sort, w3Sort string
	database.Read(t.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = 1").Scan(&w1Sort)
	database.Read(t.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = 3").Scan(&w3Sort)
	if w1Sort != "Asimov, Isaac" || w3Sort != "Asimov, Isaac" {
		t.Errorf("merged primary_author_sort = 1:%q 3:%q, want Asimov, Isaac", w1Sort, w3Sort)
	}
}

func TestSetAuthorSortNameUpdatesPrimaryAuthorSort(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'Primary', 'Primary')")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (2, 'Secondary', 'Secondary')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Author One', 'Author One')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (2, 'Other Author', 'Other Author')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 2, 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 1, 1)")
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		return updatePrimaryAuthorSorts(tx, []int64{1, 2})
	}); err != nil {
		t.Fatalf("initial updatePrimaryAuthorSorts: %v", err)
	}

	if err := database.Transact(t.Context(), func(tx *Tx) error {
		affected, err := SetAuthorSortName(tx, "Author One", "One, Author")
		if len(affected) != 2 {
			t.Errorf("affected = %v, want both linked books", affected)
		}
		return err
	}); err != nil {
		t.Fatalf("set sort name: %v", err)
	}
	var primarySort, secondarySort string
	database.Read(t.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = 1").Scan(&primarySort)
	database.Read(t.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = 2").Scan(&secondarySort)
	if primarySort != "One, Author" {
		t.Fatalf("primary book sort = %q; want One, Author", primarySort)
	}
	if secondarySort != "Other Author" {
		t.Fatalf("secondary book sort = %q; want Other Author", secondarySort)
	}
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		affected, err := SetAuthorSortName(tx, "Author One", "One, Author")
		if len(affected) != 0 {
			t.Errorf("repeated sort change affects %v; want none", affected)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
