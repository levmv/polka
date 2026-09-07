package db

import "testing"

func seedAccessBooks(t *testing.T, database *DB) {
	t.Helper()
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, tags) VALUES (142, 'Kid Book', 'Kid Book', 'kids');
		INSERT INTO books (id, title, sort_title, tags) VALUES (107, 'Adult Book', 'Adult Book', 'adult');
		INSERT INTO search (rowid, title, tags) VALUES (142, 'Kid Book', 'kids');
		INSERT INTO search (rowid, title, tags) VALUES (107, 'Adult Book', 'adult');
	`)

}

func TestVisibilityScopeManualShelf(t *testing.T) {
	database := newTestDB(t)
	seedAccessBooks(t, database)
	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	if err := database.AddBookToShelf(t.Context(), shelf.ID, 0, 142); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatalf("update access: %v", err)
	}

	scope, err := VisibilityScopeForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessBook(database.Read(t.Context()), scope, 142); err != nil || !ok {
		t.Fatalf("kid access = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CanAccessBook(database.Read(t.Context()), scope, 107); err != nil || ok {
		t.Fatalf("adult access = %v, %v; want false, nil", ok, err)
	}

	rows, err := ListBooks(database.Read(t.Context()), scope, 0, "", SortTitle, 10, 0)
	if err != nil {
		t.Fatalf("list scoped books: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 142 {
		t.Fatalf("scoped rows = %+v; want only 142", rows)
	}

	counts, err := GetCleanupCounts(database.Read(t.Context()), scope)
	if err != nil {
		t.Fatalf("scoped cleanup counts: %v", err)
	}
	if counts.MissingCover != 1 {
		t.Fatalf("scoped missing-cover count = %d, want 1", counts.MissingCover)
	}
	missingCover, err := ListBooks(database.Read(t.Context()), scope, user.ID, "no:cover", SortTitle, 10, 0)
	if err != nil {
		t.Fatalf("scoped missing-cover books: %v", err)
	}
	if len(missingCover) != 1 || missingCover[0].ID != 142 {
		t.Fatalf("scoped missing-cover rows = %+v; want only 142", missingCover)
	}
}

func TestVisibilityScopeIgnoresPrivateScopeShelfRows(t *testing.T) {
	database := newTestDB(t)
	seedAccessBooks(t, database)
	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfPersonal, "Private Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create private shelf: %v", err)
	}
	if err := database.AddBookToShelf(t.Context(), shelf.ID, user.ID, 142); err != nil {
		t.Fatalf("add book: %v", err)
	}
	mustExec(t, database, `UPDATE users SET content_scope = 'shelves' WHERE id = ?`, user.ID)
	mustExec(t, database, `INSERT INTO user_scope_shelves (user_id, shelf_id) VALUES (?, ?)`, user.ID, shelf.ID)

	scope, err := VisibilityScopeForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessBook(database.Read(t.Context()), scope, 142); err != nil || ok {
		t.Fatalf("private scope shelf access = %v, %v; want false, nil", ok, err)
	}
}

func TestVisibilityScopePrivateCuratorShelf(t *testing.T) {
	database := newTestDB(t)
	seedAccessBooks(t, database)
	curator, err := database.CreateUser(t.Context(), "admin", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create curator: %v", err)
	}
	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	shelf, err := database.CreateShelf(t.Context(), curator.ID, ShelfPersonal, "Private Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create private shelf: %v", err)
	}
	if err := database.AddBookToShelf(t.Context(), shelf.ID, curator.ID, 142); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{
		Role:          RoleReader,
		ContentScope:  ContentScopeShelves,
		ShelfIDs:      []string{shelf.ID},
		ShelfViewerID: curator.ID,
	}); err != nil {
		t.Fatalf("update access: %v", err)
	}

	scope, err := VisibilityScopeForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessBook(database.Read(t.Context()), scope, 142); err != nil || !ok {
		t.Fatalf("private curator shelf access = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CanAccessBook(database.Read(t.Context()), scope, 107); err != nil || ok {
		t.Fatalf("adult access = %v, %v; want false, nil", ok, err)
	}

	shelves, err := ListShelvesForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("list scoped shelves: %v", err)
	}
	if len(shelves) != 1 || shelves[0].Name != "Want to read" || shelves[0].OwnerID != user.ID {
		t.Fatalf("reader-visible shelves = %+v, want only the reader's default shelf", shelves)
	}
}

func TestVisibilityScopeQueryShelf(t *testing.T) {
	database := newTestDB(t)
	seedAccessBooks(t, database)
	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Kids query", ShelfQuery, "tag:kid")
	if err != nil {
		t.Fatalf("create query shelf: %v", err)
	}
	overlapShelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Kid title query", ShelfQuery, `title:"Kid Book"`)
	if err != nil {
		t.Fatalf("create overlapping query shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{
		Role:         RoleReader,
		ContentScope: ContentScopeShelves,
		ShelfIDs:     []string{shelf.ID, overlapShelf.ID},
	}); err != nil {
		t.Fatalf("update access: %v", err)
	}

	scope, err := VisibilityScopeForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessBook(database.Read(t.Context()), scope, 142); err != nil || !ok {
		t.Fatalf("kid access = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CanAccessBook(database.Read(t.Context()), scope, 107); err != nil || ok {
		t.Fatalf("adult access = %v, %v; want false, nil", ok, err)
	}
	rows, err := ListBooks(database.Read(t.Context()), scope, 0, "", SortAdded, 10, 0)
	if err != nil {
		t.Fatalf("list query-scoped books: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 142 {
		t.Fatalf("query-scoped rows = %+v; want only 142 once", rows)
	}
	rows, err = ListBooks(database.Read(t.Context()), scope, 0, "tag:adult", SortRelevance, 10, 0)
	if err != nil {
		t.Fatalf("search query-scoped books: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("query-scoped adult search = %+v; want none", rows)
	}
	sequence, err := BookSequenceInList(database.Read(t.Context()), scope, 0, 142, "", SortAdded, 1, 1)
	if err != nil {
		t.Fatalf("query-scoped sequence: %v", err)
	}
	assertSequenceWindow(t, sequence, 0, 142, 0)
}

func TestVisibilityScopeTrash(t *testing.T) {
	database := newTestDB(t)
	seedAccessBooks(t, database)
	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	if err := database.AddBookToShelf(t.Context(), shelf.ID, 0, 142); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatalf("update access: %v", err)
	}
	if err := SoftDeleteBook(database.Write(t.Context()), 142, user.ID); err != nil {
		t.Fatalf("trash kid: %v", err)
	}
	if err := SoftDeleteBook(database.Write(t.Context()), 107, user.ID); err != nil {
		t.Fatalf("trash adult: %v", err)
	}

	scope, err := VisibilityScopeForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessTrashedBook(database.Read(t.Context()), scope, 142); err != nil || !ok {
		t.Fatalf("trashed kid access = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CanAccessTrashedBook(database.Read(t.Context()), scope, 107); err != nil || ok {
		t.Fatalf("trashed adult access = %v, %v; want false, nil", ok, err)
	}

	rows, err := ListTrashedBooks(database.Read(t.Context()), scope)
	if err != nil {
		t.Fatalf("list scoped trash: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 142 {
		t.Fatalf("scoped trash = %+v; want only 142", rows)
	}
}
