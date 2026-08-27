package db

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestListBookJumpsUsesVisibleSortBoundaries(t *testing.T) {
	database := newTestDB(t)

	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title, primary_author_sort) VALUES
			('w_num', '1 Book', '1 Book', 'Zulu'),
			('w_a1', 'alpha', 'alpha', ''),
			('w_a2', 'Apple', 'Apple', 'Amy'),
			('w_b', 'Beta', 'Beta', 'Boris'),
			('w_lower', 'lower title', 'lower title', 'alpha'),
			('w_cyr', 'Ёж', 'Ёж', 'Ёлкин'),
			('w_deleted', 'Deleted', 'Deleted', 'Deleted');
		UPDATE works SET deleted_at = unixepoch() WHERE id = 'w_deleted';
	`); err != nil {
		t.Fatalf("seed works: %v", err)
	}

	titleJumps, total, err := ListBookJumps(database, FullVisibilityScope(), SortTitle)
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

	authorJumps, total, err := ListBookJumps(database, FullVisibilityScope(), SortAuthor)
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

	if _, _, err := ListBookJumps(database, FullVisibilityScope(), SortAdded); err == nil {
		t.Fatal("added-order jumps unexpectedly succeeded")
	}
}

func TestListBookJumpsRespectsVisibilityScope(t *testing.T) {
	database := newTestDB(t)
	seedAccessWorks(t, database)
	user, err := database.CreateUser("jump-reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	shelf, err := database.CreateShelf(user.ID, ShelfShared, "Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	if err := database.AddBookToShelf(shelf.ID, "", "w_kid"); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, err := database.UpdateUserAccess(user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatalf("scope user: %v", err)
	}
	scope, err := database.VisibilityScopeForUser(user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}

	jumps, total, err := ListBookJumps(database, scope, SortTitle)
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

	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i <= maxBookJumpBuckets; i++ {
		initial := string(rune(0x4e00 + i))
		if _, err := tx.Exec(
			`INSERT INTO works (id, title, sort_title) VALUES (?, ?, ?)`,
			fmt.Sprintf("w_%03d", i),
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

	jumps, total, err := ListBookJumps(database, FullVisibilityScope(), SortTitle)
	if err != nil {
		t.Fatalf("jumps: %v", err)
	}
	if total != maxBookJumpBuckets+1 || len(jumps) != 0 {
		t.Fatalf("pathological jumps = %+v, total %d; want none, total %d", jumps, total, maxBookJumpBuckets+1)
	}
}

func TestListBooksFiltersReadingStatusPerUser(t *testing.T) {
	database := newTestDB(t)
	alice, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser("bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title, added_at) VALUES
			('w1', 'Alpha Needle', 'Alpha Needle', 1),
			('w2', 'Beta Needle', 'Beta Needle', 2),
			('w3', 'Gamma', 'Gamma', 3);
		INSERT INTO search (work_id, title, authors) VALUES
			('w1', 'Alpha Needle', ''), ('w2', 'Beta Needle', ''), ('w3', 'Gamma', '');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), alice.ID, "w2", ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("set alice status: %v", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), bob.ID, "w1", ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("set bob status: %v", err)
	}

	finished, err := ListBooks(database, FullVisibilityScope(), alice.ID, "status:finished", SortRelevance, 10, 0)
	if err != nil || len(finished) != 1 || finished[0].ID != "w2" {
		t.Fatalf("alice finished = %v, err %v; want w2", bookIDs(finished), err)
	}
	combined, err := ListBooks(database, FullVisibilityScope(), alice.ID, "status:finished needle", SortRelevance, 10, 0)
	if err != nil || len(combined) != 1 || combined[0].ID != "w2" {
		t.Fatalf("combined status search = %v, err %v; want w2", bookIDs(combined), err)
	}
	unread, err := ListBooks(database, FullVisibilityScope(), alice.ID, "status:unread", SortTitle, 10, 0)
	if err != nil || !reflect.DeepEqual(bookIDs(unread), []string{"w1", "w3"}) {
		t.Fatalf("alice unread = %v, err %v; want w1,w3", bookIDs(unread), err)
	}
	sequence, err := BookSequenceInList(database, FullVisibilityScope(), alice.ID, "w1", "status:unread", SortTitle, 1, 1)
	if err != nil || sequence.Total != 2 || len(sequence.Items) != 2 || sequence.Items[1].ID != "w3" {
		t.Fatalf("status sequence = %+v, err %v", sequence, err)
	}
}

func TestListBooksSort(t *testing.T) {
	database := newTestDB(t)

	// Seed authors
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Z Author', 'Z')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'A Author', 'A')")

	// Seed works
	database.Exec("INSERT INTO works (id, title, sort_title, added_at, published_date) VALUES ('w1', 'The B Title', 'B', 1672531200, '2000-01-01')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")

	database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w2', 'A Title', 'A', 1672617600)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a2', 0)")

	database.Exec("INSERT INTO works (id, title, sort_title, added_at, published_date) VALUES ('w3', 'C Title', 'C', 1672704000, '2020-01-01')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w3', 'a2', 0)")
	database.Exec("INSERT INTO works (id, title, sort_title, added_at, deleted_at) VALUES ('w_deleted', 'Deleted', 'Deleted', 1672790400, 1672790400)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w_deleted', 'a2', 0)")
	if err := updatePrimaryAuthorSorts(database, []string{"w1", "w2", "w3", "w_deleted"}); err != nil {
		t.Fatalf("updatePrimaryAuthorSorts: %v", err)
	}

	tests := []struct {
		sort     BookSort
		expected []string
	}{
		{SortAdded, []string{"w3", "w2", "w1"}},
		{SortTitle, []string{"w2", "w1", "w3"}},
		{SortAuthor, []string{"w2", "w3", "w1"}},
		{SortYear, []string{"w3", "w1", "w2"}},
	}

	for _, tt := range tests {
		t.Run(string(tt.sort), func(t *testing.T) {
			books, err := ListBooks(database, FullVisibilityScope(), "", "", tt.sort, 10, 0)
			if err != nil {
				t.Fatalf("ListBooks failed: %v", err)
			}
			if len(books) != 3 {
				t.Fatalf("Expected 3 books, got %d", len(books))
			}
			for i, id := range tt.expected {
				if books[i].ID != id {
					t.Errorf("At index %d expected %s, got %s", i, id, books[i].ID)
				}
			}
		})
	}
}

func TestBookSequenceInListSorts(t *testing.T) {
	database := newTestDB(t)

	must := func(query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	must("INSERT INTO works (id, title, sort_title, primary_author_sort, added_at, published_date) VALUES ('w1', 'The B Title', 'B Title', 'Zed', 10, '2000-01-01')")
	must("INSERT INTO works (id, title, sort_title, primary_author_sort, added_at) VALUES ('w2', 'A Title', 'A Title', 'Alpha', 20)")
	must("INSERT INTO works (id, title, sort_title, primary_author_sort, added_at, published_date) VALUES ('w3', 'C Title', 'C Title', 'Alpha', 30, '2020-01-01')")
	must("INSERT INTO works (id, title, sort_title, primary_author_sort, added_at, published_date) VALUES ('w4', 'D Title', 'D Title', 'Middle', 20, '2020-01-01')")
	must("INSERT INTO works (id, title, sort_title, primary_author_sort, added_at, published_date, deleted_at) VALUES ('w_deleted', '0 Deleted', '0 Deleted', 'Aardvark', 40, '2030-01-01', 40)")

	tests := []struct {
		name string
		sort BookSort
		work string
		prev string
		next string
	}{
		{"added", SortAdded, "w2", "w3", "w4"},
		{"relevance without query falls back to added", SortRelevance, "w2", "w3", "w4"},
		{"title", SortTitle, "w1", "w2", "w3"},
		{"author", SortAuthor, "w3", "w2", "w4"},
		{"year middle before null", SortYear, "w1", "w4", "w2"},
		{"year first", SortYear, "w3", "", "w4"},
		{"year null last", SortYear, "w2", "w1", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BookSequenceInList(database, FullVisibilityScope(), "", tt.work, "", tt.sort, 1, 1)
			if err != nil {
				t.Fatalf("BookSequenceInList: %v", err)
			}
			assertSequenceWindow(t, got, tt.prev, tt.work, tt.next)
		})
	}

	got, err := BookSequenceInList(database, FullVisibilityScope(), "", "w_deleted", "", SortAdded, 1, 1)
	if err != nil {
		t.Fatalf("deleted BookSequenceInList: %v", err)
	}
	if len(got.Items) != 0 || got.CurrentIndex != -1 {
		t.Fatalf("deleted work sequence = %+v; want empty", got)
	}
}

func TestBookSequenceInSearchList(t *testing.T) {
	database := newTestDB(t)

	must := func(query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	must("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w1', 'Alpha', 'Alpha', 1)")
	must("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w2', 'Beta', 'Beta', 2)")
	must("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w3', 'Gamma', 'Gamma', 3)")
	must("INSERT INTO search (rowid, work_id, title, authors) VALUES (1, 'w1', 'needle Alpha', '')")
	must("INSERT INTO search (rowid, work_id, title, authors) VALUES (2, 'w2', 'needle Beta', '')")
	must("INSERT INTO search (rowid, work_id, title, authors) VALUES (3, 'w3', 'needle Gamma', '')")

	got, err := BookSequenceInList(database, FullVisibilityScope(), "", "w2", "needle", SortTitle, 1, 1)
	if err != nil {
		t.Fatalf("BookSequenceInList search: %v", err)
	}
	assertSequenceWindow(t, got, "w1", "w2", "w3")

	relevance, err := BookSequenceInList(database, FullVisibilityScope(), "", "w2", "needle", SortRelevance, 1, 1)
	if err != nil {
		t.Fatalf("BookSequenceInList relevance search: %v", err)
	}
	if relevance.CurrentIndex < 0 || relevance.CurrentIndex >= len(relevance.Items) || relevance.Items[relevance.CurrentIndex].ID != "w2" {
		t.Fatalf("relevance sequence = %+v; want current w2 in window", relevance)
	}
}

func assertSequenceWindow(t *testing.T, got BookSequenceWindow, wantPrev, wantCurrent, wantNext string) {
	t.Helper()
	want := make([]string, 0, 3)
	if wantPrev != "" {
		want = append(want, wantPrev)
	}
	want = append(want, wantCurrent)
	if wantNext != "" {
		want = append(want, wantNext)
	}
	if got.CurrentIndex < 0 || got.CurrentIndex >= len(got.Items) || got.Items[got.CurrentIndex].ID != wantCurrent {
		t.Fatalf("current = index %d in %+v; want %s", got.CurrentIndex, got.Items, wantCurrent)
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
