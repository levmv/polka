package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitPathErrorNamesDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}

	_, err := InitPath(path)
	if err == nil {
		t.Fatal("InitPath succeeded for corrupt database")
	}
	if !strings.HasPrefix(err.Error(), "library database "+path+": ") {
		t.Fatalf("InitPath error = %q; want database path context", err)
	}
}

func TestTransactCommitsAndRollsBack(t *testing.T) {
	database := newTestDB(t)

	err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w_commit', 'Committed', 'Committed')")
		return err
	})
	if err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	assertWorkCount(t, database, "w_commit", 1)

	sentinel := errors.New("stop")
	err = database.Transact(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w_rollback', 'Rollback', 'Rollback')"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback transaction err = %v, want sentinel", err)
	}
	assertWorkCount(t, database, "w_rollback", 0)
}

func assertWorkCount(t *testing.T, database *DB, workID string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow("SELECT count(*) FROM works WHERE id = ?", workID).Scan(&got); err != nil {
		t.Fatalf("count work %s: %v", workID, err)
	}
	if got != want {
		t.Fatalf("count work %s = %d, want %d", workID, got, want)
	}
}

func TestLargeLibraryIndexesExist(t *testing.T) {
	database := newTestDB(t)

	want := map[string]string{
		"idx_works_live_author_sort": "primary_author_sort",
		"idx_works_live_added":       "added_at",
		"idx_works_live_title":       "sort_title",
		"idx_works_live_pubdate":     "published_date",
		"idx_work_authors_author_id": "author_id",
	}
	for name, needle := range want {
		t.Run(name, func(t *testing.T) {
			var sql string
			err := database.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&sql)
			if err != nil {
				t.Fatalf("index %s missing: %v", name, err)
			}
			if !strings.Contains(sql, needle) {
				t.Fatalf("index %s SQL = %q; want it to mention %q", name, sql, needle)
			}
		})
	}
}
