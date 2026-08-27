package db

import "testing"

func seedAccessWorks(t *testing.T, database *DB) {
	t.Helper()
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title, tags) VALUES ('w_kid', 'Kid Book', 'Kid Book', 'kids');
		INSERT INTO works (id, title, sort_title, tags) VALUES ('w_adult', 'Adult Book', 'Adult Book', 'adult');
		INSERT INTO search (work_id, title, tags) VALUES ('w_kid', 'Kid Book', 'kids');
		INSERT INTO search (work_id, title, tags) VALUES ('w_adult', 'Adult Book', 'adult');
	`); err != nil {
		t.Fatalf("seed works: %v", err)
	}
}

func TestVisibilityScopeManualShelf(t *testing.T) {
	database := newTestDB(t)
	seedAccessWorks(t, database)
	user, err := database.CreateUser("reader", "pw", RoleReader)
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
		t.Fatalf("update access: %v", err)
	}

	scope, err := database.VisibilityScopeForUser(user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessWork(database, scope, "w_kid"); err != nil || !ok {
		t.Fatalf("kid access = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CanAccessWork(database, scope, "w_adult"); err != nil || ok {
		t.Fatalf("adult access = %v, %v; want false, nil", ok, err)
	}

	rows, err := ListBooks(database, scope, "", "", SortTitle, 10, 0)
	if err != nil {
		t.Fatalf("list scoped books: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "w_kid" {
		t.Fatalf("scoped rows = %+v; want only w_kid", rows)
	}

	counts, err := GetCleanupCounts(database, scope)
	if err != nil {
		t.Fatalf("scoped cleanup counts: %v", err)
	}
	if counts.MissingCover != 1 {
		t.Fatalf("scoped missing-cover count = %d, want 1", counts.MissingCover)
	}
	missingCover, err := ListBooks(database, scope, user.ID, "no:cover", SortTitle, 10, 0)
	if err != nil {
		t.Fatalf("scoped missing-cover books: %v", err)
	}
	if len(missingCover) != 1 || missingCover[0].ID != "w_kid" {
		t.Fatalf("scoped missing-cover rows = %+v; want only w_kid", missingCover)
	}
}

func TestVisibilityScopeIgnoresPrivateScopeShelfRows(t *testing.T) {
	database := newTestDB(t)
	seedAccessWorks(t, database)
	user, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	shelf, err := database.CreateShelf(user.ID, ShelfPersonal, "Private Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create private shelf: %v", err)
	}
	if err := database.AddBookToShelf(shelf.ID, user.ID, "w_kid"); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, err := database.Exec(`UPDATE users SET content_scope = 'shelves' WHERE id = ?`, user.ID); err != nil {
		t.Fatalf("scope user: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO user_scope_shelves (user_id, shelf_id) VALUES (?, ?)`, user.ID, shelf.ID); err != nil {
		t.Fatalf("insert private scope row: %v", err)
	}

	scope, err := database.VisibilityScopeForUser(user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessWork(database, scope, "w_kid"); err != nil || ok {
		t.Fatalf("private scope shelf access = %v, %v; want false, nil", ok, err)
	}
}

func TestVisibilityScopePrivateCuratorShelf(t *testing.T) {
	database := newTestDB(t)
	seedAccessWorks(t, database)
	curator, err := database.CreateUser("admin", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create curator: %v", err)
	}
	user, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	shelf, err := database.CreateShelf(curator.ID, ShelfPersonal, "Private Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create private shelf: %v", err)
	}
	if err := database.AddBookToShelf(shelf.ID, curator.ID, "w_kid"); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, err := database.UpdateUserAccess(user.ID, UserAccess{
		Role:          RoleReader,
		ContentScope:  ContentScopeShelves,
		ShelfIDs:      []string{shelf.ID},
		ShelfViewerID: curator.ID,
	}); err != nil {
		t.Fatalf("update access: %v", err)
	}

	scope, err := database.VisibilityScopeForUser(user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessWork(database, scope, "w_kid"); err != nil || !ok {
		t.Fatalf("private curator shelf access = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CanAccessWork(database, scope, "w_adult"); err != nil || ok {
		t.Fatalf("adult access = %v, %v; want false, nil", ok, err)
	}

	shelves, err := database.ListShelvesForUser(user.ID)
	if err != nil {
		t.Fatalf("list scoped shelves: %v", err)
	}
	if len(shelves) != 0 {
		t.Fatalf("reader-visible shelves = %+v, want hidden private scope shelf", shelves)
	}
}

func TestVisibilityScopeQueryShelf(t *testing.T) {
	database := newTestDB(t)
	seedAccessWorks(t, database)
	user, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	shelf, err := database.CreateShelf(user.ID, ShelfShared, "Kids query", ShelfQuery, "tag:kids")
	if err != nil {
		t.Fatalf("create query shelf: %v", err)
	}
	overlapShelf, err := database.CreateShelf(user.ID, ShelfShared, "Kid title query", ShelfQuery, `title:"Kid Book"`)
	if err != nil {
		t.Fatalf("create overlapping query shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(user.ID, UserAccess{
		Role:         RoleReader,
		ContentScope: ContentScopeShelves,
		ShelfIDs:     []string{shelf.ID, overlapShelf.ID},
	}); err != nil {
		t.Fatalf("update access: %v", err)
	}

	scope, err := database.VisibilityScopeForUser(user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessWork(database, scope, "w_kid"); err != nil || !ok {
		t.Fatalf("kid access = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CanAccessWork(database, scope, "w_adult"); err != nil || ok {
		t.Fatalf("adult access = %v, %v; want false, nil", ok, err)
	}
	rows, err := ListBooks(database, scope, "", "", SortAdded, 10, 0)
	if err != nil {
		t.Fatalf("list query-scoped books: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "w_kid" {
		t.Fatalf("query-scoped rows = %+v; want only w_kid once", rows)
	}
	rows, err = ListBooks(database, scope, "", "tag:adult", SortRelevance, 10, 0)
	if err != nil {
		t.Fatalf("search query-scoped books: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("query-scoped adult search = %+v; want none", rows)
	}
	sequence, err := BookSequenceInList(database, scope, "", "w_kid", "", SortAdded, 1, 1)
	if err != nil {
		t.Fatalf("query-scoped sequence: %v", err)
	}
	assertSequenceWindow(t, sequence, "", "w_kid", "")
}

func TestVisibilityScopeTrash(t *testing.T) {
	database := newTestDB(t)
	seedAccessWorks(t, database)
	user, err := database.CreateUser("reader", "pw", RoleReader)
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
		t.Fatalf("update access: %v", err)
	}
	if err := SoftDeleteWork(database, "w_kid", user.ID); err != nil {
		t.Fatalf("trash kid: %v", err)
	}
	if err := SoftDeleteWork(database, "w_adult", user.ID); err != nil {
		t.Fatalf("trash adult: %v", err)
	}

	scope, err := database.VisibilityScopeForUser(user.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if ok, err := CanAccessTrashedWork(database, scope, "w_kid"); err != nil || !ok {
		t.Fatalf("trashed kid access = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CanAccessTrashedWork(database, scope, "w_adult"); err != nil || ok {
		t.Fatalf("trashed adult access = %v, %v; want false, nil", ok, err)
	}

	rows, err := ListTrashedWorks(database, scope)
	if err != nil {
		t.Fatalf("list scoped trash: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "w_kid" {
		t.Fatalf("scoped trash = %+v; want only w_kid", rows)
	}
}
