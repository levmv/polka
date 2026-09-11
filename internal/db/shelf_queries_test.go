package db

import "testing"

func TestShelvesVisibilityAndMembership(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index, added_at) VALUES (1, 'One', 'One', 'Series', 1, 2)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index, added_at) VALUES (2, 'Two', 'Two', 'Series', 2, 1)")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Author', 'Author')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 1, 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 1, 0)")

	user := mustUser(t, database, "alice", RoleMember)

	shared, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Shared", ShelfManual, "")
	if err != nil {
		t.Fatalf("CreateShelf shared: %v", err)
	}
	private, err := database.CreateShelf(t.Context(), user.ID, ShelfPersonal, "Private", ShelfManual, "")
	if err != nil {
		t.Fatalf("CreateShelf private: %v", err)
	}
	if _, err := database.CreateShelf(t.Context(), user.ID, ShelfPersonal, "Author search", ShelfQuery, "author:Author"); err != nil {
		t.Fatalf("CreateShelf query: %v", err)
	}

	visibleSharedOnly, err := ListShelves(database.Read(t.Context()), 0)
	if err != nil {
		t.Fatalf("ListShelves shared: %v", err)
	}
	if len(visibleSharedOnly) != 1 || visibleSharedOnly[0].ID != shared.ID {
		t.Fatalf("shared-only shelves = %+v, want only %d", visibleSharedOnly, shared.ID)
	}

	visibleToUser, err := ListShelves(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("ListShelves user: %v", err)
	}
	if len(visibleToUser) != 4 {
		t.Fatalf("user-visible shelves = %d, want 4", len(visibleToUser))
	}

	// Shelf position takes precedence over when a book was added to the library.
	if err := database.AddBookToShelf(t.Context(), private.ID, user.ID, 2); err != nil {
		t.Fatalf("AddBookToShelf 2: %v", err)
	}
	if err := database.AddBookToShelf(t.Context(), private.ID, user.ID, 1); err != nil {
		t.Fatalf("AddBookToShelf 1: %v", err)
	}

	for _, tt := range []struct {
		sort BookSort
		want [2]int64
	}{
		{SortAdded, [2]int64{2, 1}},
		{SortSeries, [2]int64{1, 2}},
	} {
		books, err := ListBooksInManualShelf(database.Read(t.Context()), FullVisibilityScope(), private.ID, tt.sort, 10, 0)
		if err != nil {
			t.Fatalf("ListBooksInManualShelf: %v", err)
		}
		if len(books) != 2 || books[0].ID != tt.want[0] || books[1].ID != tt.want[1] {
			t.Fatalf("manual shelf order by %s = %+v, want %v", tt.sort, books, tt.want)
		}
	}

	memberships, err := ListBookShelfMemberships(database.Read(t.Context()), user.ID, 2)
	if err != nil {
		t.Fatalf("ListBookShelfMemberships: %v", err)
	}
	var sawPrivate, privateInShelf, sawQuery bool
	for _, m := range memberships {
		if m.ID == private.ID {
			sawPrivate = true
			privateInShelf = m.InShelf
		}
		if m.Kind == ShelfQuery {
			sawQuery = true
		}
	}
	if !sawPrivate || !privateInShelf {
		t.Fatalf("private shelf membership missing or false: %+v", memberships)
	}
	if sawQuery {
		t.Fatalf("query shelf appeared in manual membership list: %+v", memberships)
	}
}
