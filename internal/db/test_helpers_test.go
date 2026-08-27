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

func bookIDs(books []BookSummaryRow) []string {
	ids := make([]string, 0, len(books))
	for _, book := range books {
		ids = append(ids, book.ID)
	}
	return ids
}
