package testfixture

import (
	"database/sql"
	"testing"
)

const userPasswordHash = "$2a$10$68J7MSd01UZ.WwXr7tgLseQGB3ibrjUUnD4LjkepvTj428rEmDOfi"

// SeedUser inserts an account with password "pw" and its default personal shelf.
// username is the stored, normalized login name.
func SeedUser(t testing.TB, exec func(string, ...any) (sql.Result, error), username, role string) int64 {
	t.Helper()
	result, err := exec("INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)", username, userPasswordHash, role)
	if err != nil {
		t.Fatalf("seed user %q: %v", username, err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec(`INSERT INTO shelves (name, kind, owner_id, visibility, position)
		VALUES ('Want to read', 'manual', ?, 'personal', 0)`, userID); err != nil {
		t.Fatalf("seed user shelf: %v", err)
	}
	return userID
}
