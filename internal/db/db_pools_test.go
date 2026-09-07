package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/polka/internal/fsprofile"
)

func TestWriterWaitsWithoutBlockingReaders(t *testing.T) {
	database := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := database.Write(t.Context()).Exec(
		"INSERT INTO books (id, title, sort_title) VALUES (1, 'Before', 'Before')"); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE books SET title = 'After' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	// Exercise both standalone writes and transactions while the writer is held.
	writes := make(chan error, 2)
	go func() {
		_, err := database.Write(ctx).Exec("INSERT INTO app_settings (key, value) VALUES ('queued', 'saved')")
		writes <- err
	}()
	go func() {
		writes <- database.Transact(ctx, func(tx *Tx) error {
			var title string
			if err := tx.QueryRow("SELECT title FROM books WHERE id = 1").Scan(&title); err != nil {
				return err
			}
			if title != "After" {
				return errors.New("queued transaction did not see the committed edit")
			}
			_, err := tx.Exec("INSERT INTO app_settings (key, value) VALUES ('transaction', 'saved')")
			return err
		})
	}()
	waitForWriterQueue(t, ctx, database, 2)
	select {
	case err := <-writes:
		t.Fatalf("write finished while another transaction held the writer: %v", err)
	default:
	}
	var title string
	if err := database.Read(ctx).QueryRow("SELECT title FROM books WHERE id = 1").Scan(&title); err != nil || title != "Before" {
		t.Fatalf("reader during write = %q, %v; want Before", title, err)
	}
	if err := tx.QueryRow("SELECT title FROM books WHERE id = 1").Scan(&title); err != nil || title != "After" {
		t.Fatalf("transaction's own read = %q, %v; want After", title, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-writes:
			if err != nil {
				t.Fatalf("queued write: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var count int
	if err := database.Read(ctx).QueryRow("SELECT count(*) FROM app_settings WHERE value = 'saved'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("saved queued writes = %d, %v; want 2", count, err)
	}
}

func TestCanceledWriterWaitDoesNotWrite(t *testing.T) {
	database := newTestDB(t)
	tx, err := database.BeginWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, transact := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		before := database.writer.Stats().WaitCount
		done := make(chan error, 1)
		go func() {
			if transact {
				done <- database.Transact(ctx, func(tx *Tx) error {
					_, err := tx.Exec("INSERT INTO app_settings (key, value) VALUES ('canceled-tx', 'bad')")
					return err
				})
			} else {
				_, err := database.Write(ctx).Exec("INSERT INTO app_settings (key, value) VALUES ('canceled-exec', 'bad')")
				done <- err
			}
		}()
		waitCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		waitForWriterQueue(t, waitCtx, database, before+1)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel writer wait (transaction %v): %v", transact, err)
			}
		case <-waitCtx.Done():
			t.Fatal(waitCtx.Err())
		}
		stop()
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.Read(t.Context()).QueryRow("SELECT count(*) FROM app_settings WHERE value = 'bad'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("canceled writes persisted = %d, %v; want 0", count, err)
	}
}

func TestWriterWaitDeadlineDoesNotBecomeTransactionDeadline(t *testing.T) {
	database := newTestDB(t)
	database.writerWaitTimeout = 20 * time.Millisecond
	tx, err := database.BeginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO app_settings (key, value) VALUES ('held', 'saved')"); err != nil {
		t.Fatal(err)
	}
	for _, transact := range []bool{false, true} {
		if transact {
			err = database.Transact(t.Context(), func(tx *Tx) error {
				t.Error("expired waiter started a transaction")
				return nil
			})
		} else {
			_, err = database.Write(t.Context()).Exec("INSERT INTO app_settings (key, value) VALUES ('expired', 'bad')")
		}
		if !errors.Is(err, ErrWriterTimeout) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait timeout (transaction %v): %v", transact, err)
		}
	}
	// The owner outlived both waiters. Its original context still permits SQL
	// and commit; the queue deadline must never roll back a running import.
	if _, err := tx.Exec("UPDATE app_settings SET value = 'committed' WHERE key = 'held'"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Write(t.Context()).Exec("INSERT INTO app_settings (key, value) VALUES ('next', 'saved')"); err != nil {
		t.Fatalf("writer was not released after commit: %v", err)
	}
	var count int
	if err := database.Read(t.Context()).QueryRow("SELECT count(*) FROM app_settings WHERE key = 'expired'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired write persisted = %d, %v", count, err)
	}
}

func TestCanceledTransactionReleasesWriterBeforeCallerReturns(t *testing.T) {
	database := newTestDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tx, err := database.BeginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO app_settings (key, value) VALUES ('canceled', 'bad')"); err != nil {
		t.Fatal(err)
	}
	cancel()
	// Do not explicitly roll back yet: cancellation must return the reserved
	// connection even while the caller is still doing non-SQL work.
	waitCtx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if _, err := database.Write(waitCtx).Exec("INSERT INTO app_settings (key, value) VALUES ('next', 'saved')"); err != nil {
		t.Fatalf("canceled transaction held the writer: %v", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("canceled transaction committed")
	}
	var count int
	if err := database.Read(t.Context()).QueryRow("SELECT count(*) FROM app_settings WHERE key = 'canceled'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("canceled transaction persisted = %d, %v", count, err)
	}
}

func waitForWriterQueue(t *testing.T, ctx context.Context, database *DB, count int64) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for database.writer.Stats().WaitCount < count {
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for queued writers: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestReaderConnectionsRejectWrites(t *testing.T) {
	database := newTestDB(t)
	// Reserve the initial connection so QueryRow must open another reader.
	conn, err := database.reader.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var key string
	if err := conn.QueryRowContext(t.Context(), "INSERT INTO app_settings (key, value) VALUES ('misrouted', 'bad') RETURNING key").Scan(&key); err == nil {
		t.Fatal("initial reader allowed a write")
	}
	if err := database.Read(t.Context()).QueryRow("INSERT INTO app_settings (key, value) VALUES ('public-query', 'bad') RETURNING key").Scan(&key); err == nil {
		t.Fatal("QueryRow allowed a write on a new reader")
	}
}

func TestSQLiteOpenModes(t *testing.T) {
	for _, test := range []struct {
		name, journal string
		synchronous   int
		info          fsprofile.Info
	}{
		{name: "local", journal: "wal", synchronous: 1},
		{name: "network", journal: "delete", synchronous: 2, info: fsprofile.Info{Kind: fsprofile.KindNetwork, Type: "nfs4"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Exercise URI escaping through an actual open, not an expected DSN.
			path := filepath.Join(t.TempDir(), "library ?#%.db")
			writer, err := initPath(path, test.info, false)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("database was not created at the requested path: %v", err)
			}
			var synchronous int
			if err := writer.writer.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != test.synchronous {
				t.Fatalf("writer synchronous = %d, %v; want %d", synchronous, err, test.synchronous)
			}
			if _, err := writer.Write(t.Context()).Exec(
				"INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book')"); err != nil {
				t.Fatal(err)
			}
			reader, err := initPath(path, test.info, true)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			assertCount(t, reader, 1, "SELECT count(*) FROM books WHERE id = 1")
			var journal string
			if err := reader.Read(t.Context()).QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != test.journal {
				t.Fatalf("read-only journal = %q, %v; want %s", journal, err, test.journal)
			}
			if _, err := reader.Write(t.Context()).Exec(
				"DELETE FROM books"); !errors.Is(err, ErrReadOnly) {
				t.Fatalf("read-only Exec: %v", err)
			}
			if _, err := reader.BeginWrite(context.Background()); !errors.Is(err, ErrReadOnly) {
				t.Fatalf("read-only BeginWrite: %v", err)
			}
			assertCount(t, writer, 1, "SELECT count(*) FROM books WHERE id = 1")
		})
	}
}
