package db

import (
	"errors"
	"strings"
	"testing"
)

func TestUserLifecycle(t *testing.T) {
	database := newTestDB(t)

	if n, err := database.CountUsers(); err != nil || n != 0 {
		t.Fatalf("fresh db: count=%d err=%v, want 0", n, err)
	}

	u, err := database.CreateUser("Alice", "s3cret", RoleMember)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("username not normalized: %q", u.Username)
	}
	if u.PasswordHash == "s3cret" || u.PasswordHash == "" {
		t.Errorf("password not hashed: %q", u.PasswordHash)
	}

	// Username uniqueness is case-insensitive.
	if _, err := database.CreateUser("ALICE", "other", RoleMember); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate username: got %v, want ErrUserExists", err)
	}

	// Authenticate: right password (any case username) succeeds, wrong fails.
	if got, _ := database.Authenticate("alice", "s3cret"); got == nil || got.ID != u.ID {
		t.Errorf("authenticate valid: got %v", got)
	}
	if got, _ := database.Authenticate("ALICE", "s3cret"); got == nil {
		t.Errorf("authenticate is not case-insensitive on username")
	}
	if got, _ := database.Authenticate("alice", "wrong"); got != nil {
		t.Errorf("authenticate wrong password: got %v, want nil", got)
	}
	if got, _ := database.Authenticate("ghost", "whatever"); got != nil {
		t.Errorf("authenticate unknown user: got %v, want nil", got)
	}

	// Password change invalidates the old password.
	if err := database.SetUserPassword(u.ID, "newpass"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if got, _ := database.Authenticate("alice", "s3cret"); got != nil {
		t.Errorf("old password still works after change")
	}
	if got, _ := database.Authenticate("alice", "newpass"); got == nil {
		t.Errorf("new password rejected after change")
	}

	if err := database.DeleteUser(u.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := database.CountUsers(); n != 0 {
		t.Errorf("count after delete = %d, want 0", n)
	}
}

func TestLastAdminCannotBeDemotedOrDeleted(t *testing.T) {
	database := newTestDB(t)

	admin, err := database.CreateUser("Admin", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := database.UpdateUserAccess(admin.ID, UserAccess{Role: RoleMember, ContentScope: ContentScopeAll}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: got %v, want ErrLastAdmin", err)
	}
	if err := database.DeleteUser(admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete last admin: got %v, want ErrLastAdmin", err)
	}

	other, err := database.CreateUser("Other", "pw", RoleAdmin)
	if err != nil {
		t.Fatalf("create other admin: %v", err)
	}
	if _, err := database.UpdateUserAccess(admin.ID, UserAccess{Role: RoleMember, ContentScope: ContentScopeAll}); err != nil {
		t.Fatalf("demote admin with replacement: %v", err)
	}
	if err := database.DeleteUser(other.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete new last admin: got %v, want ErrLastAdmin", err)
	}
}

func TestDeleteUserPreservesTrashedWorkWithoutAttribution(t *testing.T) {
	database := newTestDB(t)

	member, err := database.CreateUser("deleter", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO works (id, title, sort_title) VALUES ('w-trash', 'Trashed', 'Trashed')`); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	if err := SoftDeleteWork(database, "w-trash", member.ID); err != nil {
		t.Fatalf("soft delete work: %v", err)
	}
	if err := database.DeleteUser(member.ID); err != nil {
		t.Fatalf("delete attributed user: %v", err)
	}

	var deletedAtSet, attributionCleared bool
	if err := database.QueryRow(`
		SELECT deleted_at IS NOT NULL, deleted_by IS NULL
		FROM works WHERE id = 'w-trash'
	`).Scan(&deletedAtSet, &attributionCleared); err != nil {
		t.Fatalf("query preserved trash row: %v", err)
	}
	if !deletedAtSet || !attributionCleared {
		t.Fatalf("trash state = deleted:%v attribution-cleared:%v; want true, true", deletedAtSet, attributionCleared)
	}
}

func TestCreateUserValidation(t *testing.T) {
	database := newTestDB(t)
	if _, err := database.CreateUser("", "pw", RoleAdmin); !errors.Is(err, ErrInvalidUserInput) {
		t.Errorf("empty username error = %v; want ErrInvalidUserInput", err)
	}
	if _, err := database.CreateUser("bob", "", RoleAdmin); !errors.Is(err, ErrInvalidUserInput) {
		t.Errorf("empty password error = %v; want ErrInvalidUserInput", err)
	}
	if _, err := database.CreateUser("bob", "pw", "superadmin"); !errors.Is(err, ErrInvalidUserInput) {
		t.Errorf("invalid role error = %v; want ErrInvalidUserInput", err)
	}
	if _, err := database.CreateUser("bob", strings.Repeat("x", 73), RoleAdmin); !errors.Is(err, ErrInvalidUserInput) {
		t.Errorf("long password error = %v; want ErrInvalidUserInput", err)
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
