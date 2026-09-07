package db

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestUserLifecycle(t *testing.T) {
	database := newTestDB(t)

	if n, err := CountUsers(database.Read(t.Context())); err != nil || n != 0 {
		t.Fatalf("fresh db: count=%d err=%v, want 0", n, err)
	}

	u, err := database.CreateUser(t.Context(), "Alice", "s3cret", RoleMember)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("username not normalized: %q", u.Username)
	}
	if u.PasswordHash == "s3cret" || u.PasswordHash == "" {
		t.Errorf("password not hashed: %q", u.PasswordHash)
	}

	shelves, err := ListShelves(database.Read(t.Context()), u.ID)
	if err != nil || len(shelves) != 1 {
		t.Fatalf("new account shelves = %+v, %v; want one", shelves, err)
	}
	shelf := shelves[0]
	if shelf.Name != "Want to read" || shelf.Kind != ShelfManual || shelf.Visibility != ShelfPersonal || shelf.OwnerID != u.ID {
		t.Fatalf("default shelf = %+v", shelf)
	}

	// Username uniqueness is case-insensitive.
	if _, err := database.CreateUser(t.Context(), "ALICE", "other", RoleMember); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate username: got %v, want ErrUserExists", err)
	}

	if got, _ := Authenticate(database.Read(t.Context()), "alice", "s3cret"); got == nil || got.ID != u.ID {
		t.Errorf("authenticate valid: got %v", got)
	}
	if got, _ := Authenticate(database.Read(t.Context()), "ALICE", "s3cret"); got == nil {
		t.Errorf("authenticate is not case-insensitive on username")
	}
	if got, _ := Authenticate(database.Read(t.Context()), "alice", "wrong"); got != nil {
		t.Errorf("authenticate wrong password: got %v, want nil", got)
	}
	if got, _ := Authenticate(database.Read(t.Context()), "ghost", "whatever"); got != nil {
		t.Errorf("authenticate unknown user: got %v, want nil", got)
	}

	if err := database.SetUserPassword(t.Context(), u.ID, "newpass"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if got, _ := Authenticate(database.Read(t.Context()), "alice", "s3cret"); got != nil {
		t.Errorf("old password still books after change")
	}
	if got, _ := Authenticate(database.Read(t.Context()), "alice", "newpass"); got == nil {
		t.Errorf("new password rejected after change")
	}

	if err := database.DeleteUser(t.Context(), u.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := CountUsers(database.Read(t.Context())); n != 0 {
		t.Errorf("count after delete = %d, want 0", n)
	}
	// A stale account URL must not address a later account, even if its name
	// is reused and the deleted account was the database's highest ID.
	replacement, err := database.CreateUser(t.Context(), "Alice", "replacement", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == u.ID {
		t.Fatal("deleted account identity was reused")
	}
	if err := database.SetUserPassword(t.Context(), u.ID, "stale request"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("password change for a deleted identity: %v", err)
	}
}

func TestLastAdminCannotBeDemotedOrDeleted(t *testing.T) {
	database := newTestDB(t)

	admin, err := database.CreateUser(t.Context(), "Admin", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), admin.ID, UserAccess{Role: RoleMember, ContentScope: ContentScopeAll}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: got %v, want ErrLastAdmin", err)
	}
	if err := database.DeleteUser(t.Context(), admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete last admin: got %v, want ErrLastAdmin", err)
	}

	other, err := database.CreateUser(t.Context(), "Other", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create other admin: %v", err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), admin.ID, UserAccess{Role: RoleMember, ContentScope: ContentScopeAll}); err != nil {
		t.Fatalf("demote admin with replacement: %v", err)
	}
	if err := database.DeleteUser(t.Context(), other.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete new last admin: got %v, want ErrLastAdmin", err)
	}
}

func TestDeleteUserPreservesTrashedBookWithoutAttribution(t *testing.T) {
	database := newTestDB(t)

	member, err := database.CreateUser(t.Context(), "deleter", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	mustExec(t, database, `INSERT INTO books (id, title, sort_title) VALUES (99, 'Trashed', 'Trashed')`)

	if err := SoftDeleteBook(database.Write(t.Context()), 99, member.ID); err != nil {
		t.Fatalf("soft delete book: %v", err)
	}
	if err := database.DeleteUser(t.Context(), member.ID); err != nil {
		t.Fatalf("delete attributed user: %v", err)
	}

	var deletedAtSet, attributionCleared bool
	if err := database.Read(t.Context()).QueryRow(`
		SELECT deleted_at IS NOT NULL, deleted_by IS NULL
		FROM books WHERE id = 99
	`).Scan(&deletedAtSet, &attributionCleared); err != nil {
		t.Fatalf("query preserved trash row: %v", err)
	}
	if !deletedAtSet || !attributionCleared {
		t.Fatalf("trash state = deleted:%v attribution-cleared:%v; want true, true", deletedAtSet, attributionCleared)
	}
}

func TestCreateUserValidation(t *testing.T) {
	database := newTestDB(t)
	if _, err := database.CreateUser(t.Context(), "", "pw", RoleAdmin); !errors.Is(err, ErrInvalidUserInput) {
		t.Errorf("empty username error = %v; want ErrInvalidUserInput", err)
	}
	if _, err := database.CreateUser(t.Context(), "bob", "", RoleAdmin); !errors.Is(err, ErrInvalidUserInput) {
		t.Errorf("empty password error = %v; want ErrInvalidUserInput", err)
	}
	if _, err := database.CreateUser(t.Context(), "bob", "pw", "superadmin"); !errors.Is(err, ErrInvalidUserInput) {
		t.Errorf("invalid role error = %v; want ErrInvalidUserInput", err)
	}
	if _, err := database.CreateUser(t.Context(), "bob", strings.Repeat("x", 73), RoleAdmin); !errors.Is(err, ErrInvalidUserInput) {
		t.Errorf("long password error = %v; want ErrInvalidUserInput", err)
	}
}

func TestDefaultShelfFailureRollsBackAccount(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, `CREATE TRIGGER fail_shelf BEFORE INSERT ON shelves
		BEGIN SELECT RAISE(ABORT, 'shelf unavailable'); END`)

	if _, err := database.CreateInitialAdmin(t.Context(), "owner", "pw"); err == nil {
		t.Fatal("account creation succeeded despite shelf failure")
	}
	if n, err := CountUsers(database.Read(t.Context())); err != nil || n != 0 {
		t.Fatalf("accounts after failed creation = %d, %v; want zero", n, err)
	}
	mustExec(t, database, `DROP TRIGGER fail_shelf`)

	if _, err := database.CreateInitialAdmin(t.Context(), "owner", "pw"); err != nil {
		t.Fatalf("retry initial setup: %v", err)
	}
}

func TestRoleAtLeastFailsClosed(t *testing.T) {
	if !RoleAtLeast(RoleAdmin, RoleReader) {
		t.Fatal("admin should satisfy reader")
	}
	if RoleAtLeast(RoleReader, RoleMember) {
		t.Fatal("reader should not satisfy member")
	}
	if RoleAtLeast(RoleReader, "typo") {
		t.Fatal("unknown minimum role should not allow access")
	}
	if RoleAtLeast("typo", RoleReader) {
		t.Fatal("unknown user role should not allow access")
	}
}
