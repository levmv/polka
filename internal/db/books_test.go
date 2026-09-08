package db

import (
	"context"
	"reflect"
	"testing"
)

func TestListBookJumpsUsesVisibleSortBoundaries(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, primary_author_sort) VALUES
			(160, '1 Book', '1 Book', 'Zulu'),
			(104, 'alpha', 'alpha', ''),
			(105, 'Apple', 'Apple', 'Amy'),
			(112, 'Beta', 'Beta', 'Boris'),
			(148, 'lower title', 'lower title', 'alpha'),
			(121, 'Ёж', 'Ёж', 'Ёлкин'),
			(122, 'Deleted', 'Deleted', 'Deleted');
		UPDATE books SET deleted_at = unixepoch() WHERE id = 122;
	`)

	titleJumps, total, err := ListBookJumps(database.Read(t.Context()), FullVisibilityScope(), SortTitle)
	if err != nil {
		t.Fatalf("title jumps: %v", err)
	}
	wantTitle := []BookJump{
		{Label: "0–9", Offset: 0},
		{Label: "A", Offset: 1},
		{Label: "B", Offset: 3},
		{Label: "L", Offset: 4},
		{Label: "Ё", Offset: 5},
	}
	if total != 6 || !reflect.DeepEqual(titleJumps, wantTitle) {
		t.Fatalf("title jumps = %+v, total %d; want %+v, total 6", titleJumps, total, wantTitle)
	}

	authorJumps, total, err := ListBookJumps(database.Read(t.Context()), FullVisibilityScope(), SortAuthor)
	if err != nil {
		t.Fatalf("author jumps: %v", err)
	}
	wantAuthor := []BookJump{
		{Label: "#", Offset: 0},
		{Label: "A", Offset: 1},
		{Label: "B", Offset: 2},
		{Label: "Z", Offset: 3},
		// Lower-case "alpha" sorts after the upper-case Latin values under
		// author order. Its normalized A label is deliberately de-duplicated,
		// while its row must still count toward the following boundary.
		{Label: "Ё", Offset: 5},
	}
	if total != 6 || !reflect.DeepEqual(authorJumps, wantAuthor) {
		t.Fatalf("author jumps = %+v, total %d; want %+v, total 6", authorJumps, total, wantAuthor)
	}

	if _, _, err := ListBookJumps(database.Read(t.Context()), FullVisibilityScope(), SortAdded); err == nil {
		t.Fatal("added-order jumps unexpectedly succeeded")
	}
}

func TestListBookJumpsRespectsVisibilityScope(t *testing.T) {
	database := newTestDB(t)
	seedAccessBooks(t, database)
	user := mustUser(t, database, "jump-reader", RoleReader)
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	if err := database.AddBookToShelf(t.Context(), shelf.ID, 0, 142); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []int64{shelf.ID}}); err != nil {
		t.Fatalf("scope user: %v", err)
	}
	scope, err := VisibilityScopeForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}

	jumps, total, err := ListBookJumps(database.Read(t.Context()), scope, SortTitle)
	if err != nil {
		t.Fatalf("scoped jumps: %v", err)
	}
	want := []BookJump{{Label: "K", Offset: 0}}
	if total != 1 || !reflect.DeepEqual(jumps, want) {
		t.Fatalf("scoped jumps = %+v, total %d; want %+v, total 1", jumps, total, want)
	}
}

func TestListBookJumpsDropsPathologicalBucketSets(t *testing.T) {
	database := newTestDB(t)

	tx, err := database.BeginWrite(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i <= maxBookJumpBuckets; i++ {
		initial := string(rune(0x4e00 + i))
		if _, err := tx.Exec(
			`INSERT INTO books (id, title, sort_title) VALUES (?, ?, ?)`,
			i+1,
			initial+" book",
			initial+" book",
		); err != nil {
			tx.Rollback()
			t.Fatalf("insert bucket %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	jumps, total, err := ListBookJumps(database.Read(t.Context()), FullVisibilityScope(), SortTitle)
	if err != nil {
		t.Fatalf("jumps: %v", err)
	}
	if total != maxBookJumpBuckets+1 || len(jumps) != 0 {
		t.Fatalf("pathological jumps = %+v, total %d; want none, total %d", jumps, total, maxBookJumpBuckets+1)
	}
}

func TestListBooksFiltersReadingStatusPerUser(t *testing.T) {
	database := newTestDB(t)
	alice := mustUser(t, database, "alice", RoleMember)
	bob := mustUser(t, database, "bob", RoleMember)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, added_at) VALUES
			(1, 'Alpha Needle', 'Alpha Needle', 1),
			(2, 'Beta Needle', 'Beta Needle', 2),
			(3, 'Gamma', 'Gamma', 3);
		INSERT INTO search (rowid, title, authors) VALUES
			(1, 'Alpha Needle', ''), (2, 'Beta Needle', ''), (3, 'Gamma', '');
	`)

	if _, err := database.SetReadingStatus(context.Background(), alice.ID, 2, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("set alice status: %v", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), bob.ID, 1, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("set bob status: %v", err)
	}

	finished, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), alice.ID, "status:finished", SortRelevance, 10, 0)
	if err != nil || len(finished) != 1 || finished[0].ID != 2 {
		t.Fatalf("alice finished = %v, err %v; want 2", bookIDs(finished), err)
	}
	combined, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), alice.ID, "status:finished needle", SortRelevance, 10, 0)
	if err != nil || len(combined) != 1 || combined[0].ID != 2 {
		t.Fatalf("combined status search = %v, err %v; want 2", bookIDs(combined), err)
	}
	unread, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), alice.ID, "status:unread", SortTitle, 10, 0)
	if err != nil || !reflect.DeepEqual(bookIDs(unread), []int64{1, 3}) {
		t.Fatalf("alice unread = %v, err %v; want 1,3", bookIDs(unread), err)
	}
	sequence, err := BookSequenceInList(database.Read(t.Context()), FullVisibilityScope(), alice.ID, 1, "status:unread", SortTitle, 1, 1)
	if err != nil || sequence.Total != 2 || len(sequence.Items) != 2 || sequence.Items[1].ID != 3 {
		t.Fatalf("status sequence = %+v, err %v", sequence, err)
	}
}

func TestListBooksSort(t *testing.T) {
	database := newTestDB(t)

	// Seed authors
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Z Author', 'Z')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (2, 'A Author', 'A')")

	// Seed books
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at, published_date) VALUES (1, 'The B Title', 'B', 1672531200, '2000-01-01')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at) VALUES (2, 'A Title', 'A', 1672617600)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 2, 0)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at, published_date) VALUES (3, 'C Title', 'C', 1672704000, '2020-01-01')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (3, 2, 0)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, added_at, deleted_at) VALUES (122, 'Deleted', 'Deleted', 1672790400, 1672790400)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (122, 2, 0)")
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		return updatePrimaryAuthorSorts(tx, []int64{1, 2, 3, 122})
	}); err != nil {
		t.Fatalf("updatePrimaryAuthorSorts: %v", err)
	}

	tests := []struct {
		sort     BookSort
		expected []int64
	}{
		{SortAdded, []int64{3, 2, 1}},
		{SortTitle, []int64{2, 1, 3}},
		{SortAuthor, []int64{2, 3, 1}},
		{SortYear, []int64{3, 1, 2}},
	}

	for _, tt := range tests {
		t.Run(string(tt.sort), func(t *testing.T) {
			books, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), 0, "", tt.sort, 10, 0)
			if err != nil {
				t.Fatalf("ListBooks failed: %v", err)
			}
			if len(books) != 3 {
				t.Fatalf("Expected 3 books, got %d", len(books))
			}
			for i, id := range tt.expected {
				if books[i].ID != id {
					t.Errorf("At index %d expected %d, got %d", i, id, books[i].ID)
				}
			}
		})
	}
}

func TestBookSequenceInListSorts(t *testing.T) {
	database := newTestDB(t)

	must := func(query string, args ...any) {
		t.Helper()
		mustExec(t, database, query, args...)

	}
	must("INSERT INTO books (id, title, sort_title, primary_author_sort, added_at, published_date) VALUES (1, 'The B Title', 'B Title', 'Zed', 10, '2000-01-01')")
	must("INSERT INTO books (id, title, sort_title, primary_author_sort, added_at) VALUES (2, 'A Title', 'A Title', 'Alpha', 20)")
	must("INSERT INTO books (id, title, sort_title, primary_author_sort, added_at, published_date) VALUES (3, 'C Title', 'C Title', 'Alpha', 30, '2020-01-01')")
	must("INSERT INTO books (id, title, sort_title, primary_author_sort, added_at, published_date) VALUES (4, 'D Title', 'D Title', 'Middle', 20, '2020-01-01')")
	must("INSERT INTO books (id, title, sort_title, primary_author_sort, added_at, published_date, deleted_at) VALUES (122, '0 Deleted', '0 Deleted', 'Aardvark', 40, '2030-01-01', 40)")

	tests := []struct {
		name string
		sort BookSort
		book int64
		prev int64
		next int64
	}{
		{"added", SortAdded, 2, 3, 4},
		{"relevance without query falls back to added", SortRelevance, 2, 3, 4},
		{"title", SortTitle, 1, 2, 3},
		{"author", SortAuthor, 3, 2, 4},
		{"year middle before null", SortYear, 1, 4, 2},
		{"year first", SortYear, 3, 0, 4},
		{"year null last", SortYear, 2, 1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BookSequenceInList(database.Read(t.Context()), FullVisibilityScope(), 0, tt.book, "", tt.sort, 1, 1)
			if err != nil {
				t.Fatalf("BookSequenceInList: %v", err)
			}
			assertSequenceWindow(t, got, tt.prev, tt.book, tt.next)
		})
	}

	got, err := BookSequenceInList(database.Read(t.Context()), FullVisibilityScope(), 0, 122, "", SortAdded, 1, 1)
	if err != nil {
		t.Fatalf("deleted BookSequenceInList: %v", err)
	}
	if len(got.Items) != 0 || got.CurrentIndex != -1 {
		t.Fatalf("deleted book sequence = %+v; want empty", got)
	}
}

func TestBookSequenceInSearchList(t *testing.T) {
	database := newTestDB(t)

	must := func(query string, args ...any) {
		t.Helper()
		mustExec(t, database, query, args...)

	}
	must("INSERT INTO books (id, title, sort_title, added_at) VALUES (1, 'Alpha', 'Alpha', 1)")
	must("INSERT INTO books (id, title, sort_title, added_at) VALUES (2, 'Beta', 'Beta', 2)")
	must("INSERT INTO books (id, title, sort_title, added_at) VALUES (3, 'Gamma', 'Gamma', 3)")
	must("INSERT INTO search (rowid, title, authors) VALUES (1, 'needle Alpha', '')")
	must("INSERT INTO search (rowid, title, authors) VALUES (2, 'needle Beta', '')")
	must("INSERT INTO search (rowid, title, authors) VALUES (3, 'needle Gamma', '')")

	got, err := BookSequenceInList(database.Read(t.Context()), FullVisibilityScope(), 0, 2, "needle", SortTitle, 1, 1)
	if err != nil {
		t.Fatalf("BookSequenceInList search: %v", err)
	}
	assertSequenceWindow(t, got, 1, 2, 3)

	relevance, err := BookSequenceInList(database.Read(t.Context()), FullVisibilityScope(), 0, 2, "needle", SortRelevance, 1, 1)
	if err != nil {
		t.Fatalf("BookSequenceInList relevance search: %v", err)
	}
	if relevance.CurrentIndex < 0 || relevance.CurrentIndex >= len(relevance.Items) || relevance.Items[relevance.CurrentIndex].ID != 2 {
		t.Fatalf("relevance sequence = %+v; want current 2 in window", relevance)
	}
}

func assertSequenceWindow(t *testing.T, got BookSequenceWindow, wantPrev, wantCurrent, wantNext int64) {
	t.Helper()
	want := make([]int64, 0, 3)
	if wantPrev != 0 {
		want = append(want, wantPrev)
	}
	want = append(want, wantCurrent)
	if wantNext != 0 {
		want = append(want, wantNext)
	}
	if got.CurrentIndex < 0 || got.CurrentIndex >= len(got.Items) || got.Items[got.CurrentIndex].ID != wantCurrent {
		t.Fatalf("current = index %d in %+v; want %d", got.CurrentIndex, got.Items, wantCurrent)
	}
	if len(got.Items) != len(want) {
		t.Fatalf("sequence items = %+v; want ids %v", got.Items, want)
	}
	for i, id := range want {
		if got.Items[i].ID != id {
			t.Fatalf("sequence items = %+v; want ids %v", got.Items, want)
		}
	}
}
