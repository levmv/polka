package db

import (
	"path/filepath"
	"testing"
)

func newTestDB(t testing.TB) *DB {
	t.Helper()
	database, err := InitPath(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatalf("init test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func mustExec(t testing.TB, database *DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Write(t.Context()).Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func bookIDs(books []BookSummaryRow) []int64 {
	ids := make([]int64, 0, len(books))
	for _, book := range books {
		ids = append(ids, book.ID)
	}
	return ids
}
