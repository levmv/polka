package db

import "testing"

func TestShelvesVisibilityAndMembership(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w1', 'One', 'One', 1)")
	database.Exec("INSERT INTO works (id, title, sort_title, added_at) VALUES ('w2', 'Two', 'Two', 2)")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Author', 'Author')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a1', 0)")

	user, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	shared, err := database.CreateShelf(user.ID, ShelfShared, "Shared", ShelfManual, "")
	if err != nil {
		t.Fatalf("CreateShelf shared: %v", err)
	}
	private, err := database.CreateShelf(user.ID, ShelfPersonal, "Private", ShelfManual, "")
	if err != nil {
		t.Fatalf("CreateShelf private: %v", err)
	}
	if _, err := database.CreateShelf(user.ID, ShelfPersonal, "Author search", ShelfQuery, "author:Author"); err != nil {
		t.Fatalf("CreateShelf query: %v", err)
	}

	visibleSharedOnly, err := database.ListShelves("")
	if err != nil {
		t.Fatalf("ListShelves shared: %v", err)
	}
	if len(visibleSharedOnly) != 1 || visibleSharedOnly[0].ID != shared.ID {
		t.Fatalf("shared-only shelves = %+v, want only %s", visibleSharedOnly, shared.ID)
	}

	visibleToUser, err := database.ListShelves(user.ID)
	if err != nil {
		t.Fatalf("ListShelves user: %v", err)
	}
	if len(visibleToUser) != 3 {
		t.Fatalf("user-visible shelves = %d, want 3", len(visibleToUser))
	}

	if err := database.AddBookToShelf(private.ID, user.ID, "w2"); err != nil {
		t.Fatalf("AddBookToShelf w2: %v", err)
	}
	if err := database.AddBookToShelf(private.ID, user.ID, "w1"); err != nil {
		t.Fatalf("AddBookToShelf w1: %v", err)
	}

	books, err := ListBooksInManualShelf(database, FullVisibilityScope(), private.ID, SortAdded, 10, 0)
	if err != nil {
		t.Fatalf("ListBooksInManualShelf: %v", err)
	}
	if len(books) != 2 || books[0].ID != "w2" || books[1].ID != "w1" {
		t.Fatalf("manual shelf order = %+v, want [w2 w1]", books)
	}

	memberships, err := database.ListBookShelfMemberships(user.ID, "w2")
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
