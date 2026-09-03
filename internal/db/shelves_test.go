package db

import (
	"context"
	"errors"
	"testing"
)

func TestAddBooksToShelfHonorsContext(t *testing.T) {
	database := newTestDB(t)
	owner, err := database.CreateUser("owner", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	shelf, err := database.CreateShelf(owner.ID, ShelfPersonal, "Reading", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := database.AddBooksToShelf(ctx, shelf.ID, owner.ID, []string{"w_1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("add with canceled context err = %v, want context.Canceled", err)
	}
}

func TestUpdateUserAccessScopeShelfVisibility(t *testing.T) {
	database := newTestDB(t)

	curator, err := database.CreateUser("admin", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create curator: %v", err)
	}
	reader, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	curatorPrivate, err := database.CreateShelf(curator.ID, ShelfPersonal, "Curator Private", ShelfManual, "")
	if err != nil {
		t.Fatalf("create curator private shelf: %v", err)
	}
	readerPrivate, err := database.CreateShelf(reader.ID, ShelfPersonal, "Reader Private", ShelfManual, "")
	if err != nil {
		t.Fatalf("create reader private shelf: %v", err)
	}

	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{
		Role:         RoleReader,
		ContentScope: ContentScopeShelves,
		ShelfIDs:     []string{curatorPrivate.ID},
	}); !errors.Is(err, ErrScopeShelfNotVisible) {
		t.Fatalf("scope private shelf without viewer err = %v, want ErrScopeShelfNotVisible", err)
	}

	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{
		Role:          RoleReader,
		ContentScope:  ContentScopeShelves,
		ShelfIDs:      []string{curatorPrivate.ID},
		ShelfViewerID: curator.ID,
	}); err != nil {
		t.Fatalf("scope curator private shelf: %v", err)
	}

	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{
		Role:          RoleReader,
		ContentScope:  ContentScopeShelves,
		ShelfIDs:      []string{readerPrivate.ID},
		ShelfViewerID: curator.ID,
	}); !errors.Is(err, ErrScopeShelfNotVisible) {
		t.Fatalf("scope reader private shelf err = %v, want ErrScopeShelfNotVisible", err)
	}
}

func TestQueryShelfAccessRequiresCompleteFTSQuery(t *testing.T) {
	database := newTestDB(t)

	owner, err := database.CreateUser("owner", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	reader, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	safe, err := database.CreateShelf(owner.ID, ShelfShared, "Kids", ShelfQuery, "tag:kids")
	if err != nil {
		t.Fatalf("create FTS scope shelf: %v", err)
	}
	mixed, err := database.CreateShelf(owner.ID, ShelfShared, "Uncovered kids", ShelfQuery, "tag:kids no:cover")
	if err != nil {
		t.Fatalf("create mixed query shelf: %v", err)
	}
	status, err := database.CreateShelf(owner.ID, ShelfShared, "Unread", ShelfQuery, "status:unread")
	if err != nil {
		t.Fatalf("create status query shelf: %v", err)
	}

	if mixed.QueryMatch != "" || status.QueryMatch != "" {
		t.Fatalf("unsafe query matches = mixed:%q status:%q; want empty", mixed.QueryMatch, status.QueryMatch)
	}
	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{safe.ID}}); err != nil {
		t.Fatalf("assign FTS scope shelf: %v", err)
	}
	for _, test := range []struct {
		name   string
		shelf  *Shelf
		reason string
	}{
		{name: "missing metadata", shelf: mixed, reason: noCoverScopeShelfReason},
		{name: "reading status", shelf: status, reason: statusScopeShelfReason},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.UpdateUserAccess(reader.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{test.shelf.ID}})
			if !errors.Is(err, ErrScopeShelfNotEligible) {
				t.Fatalf("assign scope shelf err = %v; want ErrScopeShelfNotEligible", err)
			}
			if err.Error() != test.reason {
				t.Fatalf("assign scope shelf err = %q; want %q", err, test.reason)
			}
		})
	}
	assigned, err := UserScopeShelfIDs(database, reader.ID)
	if err != nil {
		t.Fatalf("load preserved scope: %v", err)
	}
	if len(assigned) != 1 || assigned[0] != safe.ID {
		t.Fatalf("scope after rejected update = %v; want [%s]", assigned, safe.ID)
	}

	if _, err := database.UpdateShelf(safe.ID, owner.ID, safe.Name, "tag:kids status:unread", safe.Visibility); err != nil {
		t.Fatalf("make assigned shelf ineligible: %v", err)
	}
	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{
		Role:         RoleReader,
		ContentScope: ContentScopeShelves,
		ShelfIDs:     []string{safe.ID},
	}); !errors.Is(err, ErrScopeShelfNotEligible) {
		t.Fatalf("retain newly ineligible scope shelf err = %v; want ErrScopeShelfNotEligible", err)
	}
	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves}); err != nil {
		t.Fatalf("remove newly ineligible scope shelf: %v", err)
	}
	assigned, err = UserScopeShelfIDs(database, reader.ID)
	if err != nil {
		t.Fatalf("load cleared scope: %v", err)
	}
	if len(assigned) != 0 {
		t.Fatalf("scope after removing ineligible shelf = %v; want empty", assigned)
	}
}

func TestUpdateUserAccessPreservesExistingHiddenScopeShelf(t *testing.T) {
	database := newTestDB(t)

	firstCurator, err := database.CreateUser("admin1", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create first curator: %v", err)
	}
	secondCurator, err := database.CreateUser("admin2", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create second curator: %v", err)
	}
	reader, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	hidden, err := database.CreateShelf(firstCurator.ID, ShelfPersonal, "Hidden Scope", ShelfManual, "")
	if err != nil {
		t.Fatalf("create hidden scope shelf: %v", err)
	}
	otherHidden, err := database.CreateShelf(firstCurator.ID, ShelfPersonal, "Other Hidden", ShelfManual, "")
	if err != nil {
		t.Fatalf("create other hidden shelf: %v", err)
	}

	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{
		Role:          RoleReader,
		ContentScope:  ContentScopeShelves,
		ShelfIDs:      []string{hidden.ID},
		ShelfViewerID: firstCurator.ID,
	}); err != nil {
		t.Fatalf("set hidden scope shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{
		Role:          RoleReader,
		ContentScope:  ContentScopeShelves,
		ShelfIDs:      []string{hidden.ID},
		ShelfViewerID: secondCurator.ID,
	}); err != nil {
		t.Fatalf("preserve hidden scope shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{
		Role:          RoleReader,
		ContentScope:  ContentScopeShelves,
		ShelfIDs:      []string{hidden.ID, otherHidden.ID},
		ShelfViewerID: secondCurator.ID,
	}); !errors.Is(err, ErrScopeShelfNotVisible) {
		t.Fatalf("add new hidden scope shelf err = %v, want ErrScopeShelfNotVisible", err)
	}
}

func TestUpdateUserAccessShelfScopeIsReaderOnly(t *testing.T) {
	database := newTestDB(t)

	member, err := database.CreateUser("member", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	shelf, err := database.CreateShelf(member.ID, ShelfShared, "Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}

	updated, err := database.UpdateUserAccess(member.ID, UserAccess{Role: RoleMember, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}})
	if err != nil {
		t.Fatalf("update member access: %v", err)
	}
	if updated.ContentScope != ContentScopeAll {
		t.Fatalf("member content scope = %q, want %q", updated.ContentScope, ContentScopeAll)
	}
	scopeShelves, err := UserScopeShelfIDs(database, member.ID)
	if err != nil {
		t.Fatalf("scope shelf ids: %v", err)
	}
	if len(scopeShelves) != 0 {
		t.Fatalf("member scope shelves = %+v, want none", scopeShelves)
	}

	if _, err := database.Exec(`UPDATE users SET content_scope = 'shelves' WHERE id = ?`, member.ID); err != nil {
		t.Fatalf("force stale member scope: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO user_scope_shelves (user_id, shelf_id) VALUES (?, ?)`, member.ID, shelf.ID); err != nil {
		t.Fatalf("force stale member scope shelf: %v", err)
	}
	scope, err := database.VisibilityScopeForUser(member.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}
	if !scope.IsFull() {
		t.Fatalf("member visibility scope = %+v, want full", scope)
	}
}

func TestUpdateShelfEditsQueryAndVisibility(t *testing.T) {
	database := newTestDB(t)

	admin, err := database.CreateUser("admin", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	shelf, err := database.CreateShelf(admin.ID, ShelfPersonal, "Kids", ShelfQuery, "tag:kids")
	if err != nil {
		t.Fatalf("create query shelf: %v", err)
	}

	updated, err := database.UpdateShelf(shelf.ID, admin.ID, "School", "tag:school", ShelfShared)
	if err != nil {
		t.Fatalf("update shelf: %v", err)
	}
	if updated.Name != "School" || updated.Query != "tag:school" || updated.QueryMatch != `tags:"school"*` {
		t.Fatalf("updated query shelf = %+v", updated)
	}
	if updated.OwnerID != admin.ID || updated.Visibility != ShelfShared {
		t.Fatalf("updated owner/visibility = %q/%q, want %q/%q", updated.OwnerID, updated.Visibility, admin.ID, ShelfShared)
	}
}

func TestListShelvesForScopedUser(t *testing.T) {
	database := newTestDB(t)

	reader, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	kids, err := database.CreateShelf(reader.ID, ShelfShared, "Kids", ShelfManual, "")
	if err != nil {
		t.Fatalf("create kids shelf: %v", err)
	}
	adult, err := database.CreateShelf(reader.ID, ShelfShared, "Adults", ShelfManual, "")
	if err != nil {
		t.Fatalf("create adult shelf: %v", err)
	}
	private, err := database.CreateShelf(reader.ID, ShelfPersonal, "Mine", ShelfManual, "")
	if err != nil {
		t.Fatalf("create private shelf: %v", err)
	}
	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{kids.ID}}); err != nil {
		t.Fatalf("scope reader: %v", err)
	}

	shelves, err := database.ListShelvesForUser(reader.ID)
	if err != nil {
		t.Fatalf("list scoped shelves: %v", err)
	}
	got := shelfNames(shelves)
	if len(got) != 2 || got[0] != kids.Name || got[1] != private.Name {
		t.Fatalf("scoped shelves = %+v, want Kids and Mine", got)
	}
	if _, err := database.GetShelfForUser(adult.ID, reader.ID); !errors.Is(err, ErrShelfNotFound) {
		t.Fatalf("get unassigned shared shelf err = %v, want ErrShelfNotFound", err)
	}
	if _, err := database.GetShelfForUser(kids.ID, reader.ID); err != nil {
		t.Fatalf("get assigned shared shelf: %v", err)
	}
}

func shelfNames(shelves []Shelf) []string {
	names := make([]string, 0, len(shelves))
	for _, shelf := range shelves {
		names = append(names, shelf.Name)
	}
	return names
}

func TestSharedShelfNamesOwnedBy(t *testing.T) {
	database := newTestDB(t)

	owner, err := database.CreateUser("owner", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}
	other, err := database.CreateUser("other", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}

	if _, err := database.CreateShelf(owner.ID, ShelfShared, "Zoo", ShelfManual, ""); err != nil {
		t.Fatalf("shared Zoo: %v", err)
	}
	if _, err := database.CreateShelf(owner.ID, ShelfShared, "Kids", ShelfManual, ""); err != nil {
		t.Fatalf("shared Kids: %v", err)
	}
	// A personal shelf and another owner's shared shelf must not appear.
	if _, err := database.CreateShelf(owner.ID, ShelfPersonal, "Mine", ShelfManual, ""); err != nil {
		t.Fatalf("personal Mine: %v", err)
	}
	if _, err := database.CreateShelf(other.ID, ShelfShared, "Theirs", ShelfManual, ""); err != nil {
		t.Fatalf("shared Theirs: %v", err)
	}

	names, err := SharedShelfNamesOwnedBy(database, owner.ID)
	if err != nil {
		t.Fatalf("SharedShelfNamesOwnedBy: %v", err)
	}
	want := []string{"Kids", "Zoo"} // shared only, name-ordered; no personal, no other owner
	if len(names) != len(want) {
		t.Fatalf("names = %v; want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v; want %v", names, want)
		}
	}
}
