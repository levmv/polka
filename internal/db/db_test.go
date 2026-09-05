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
		_, err := tx.Exec("INSERT INTO books (id, title, sort_title) VALUES ('w_commit', 'Committed', 'Committed')")
		return err
	})
	if err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	assertBookCount(t, database, "w_commit", 1)

	sentinel := errors.New("stop")
	err = database.Transact(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO books (id, title, sort_title) VALUES ('w_rollback', 'Rollback', 'Rollback')"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback transaction err = %v, want sentinel", err)
	}
	assertBookCount(t, database, "w_rollback", 0)
}

func assertBookCount(t *testing.T, database *DB, bookID string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow("SELECT count(*) FROM books WHERE id = ?", bookID).Scan(&got); err != nil {
		t.Fatalf("count book %s: %v", bookID, err)
	}
	if got != want {
		t.Fatalf("count book %s = %d, want %d", bookID, got, want)
	}
}
