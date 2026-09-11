package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	storageWriterLeaseName    = "storage_writer"
	DefaultWriterLeaseTTL     = 30 * time.Second
	writerLeaseReleaseTimeout = 5 * time.Second
)

var ErrWriterLeaseHeld = errors.New("writer lease held by another process")

type WriterLease struct {
	db    *DB
	owner string
}

type WriterLeaseHeldError struct {
	Owner string
}

func (e WriterLeaseHeldError) Error() string {
	owner := strings.TrimSpace(e.Owner)
	if owner == "" {
		owner = "unknown owner"
	}
	return fmt.Sprintf("managed files are held by %s", owner)
}

func (e WriterLeaseHeldError) Unwrap() error {
	return ErrWriterLeaseHeld
}

func NewWriterLeaseOwner(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "polka"
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s:%s:%d:%s", label, host, os.Getpid(), leaseRandHex(6))
}

func AcquireWriterLease(ctx context.Context, database *DB, owner string, force bool) (*WriterLease, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = NewWriterLeaseOwner("polka")
	}
	tx, err := database.BeginWrite(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin writer lease: %w", err)
	}
	defer tx.Rollback()
	// Waiting for the writer must not age a newly acquired lease.
	now := time.Now().Unix()
	cutoff := now - int64(DefaultWriterLeaseTTL.Seconds())

	var currentOwner string
	var updatedAt int64
	err = tx.QueryRow("SELECT owner, updated_at FROM writer_leases WHERE name = ?", storageWriterLeaseName).Scan(&currentOwner, &updatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec("INSERT INTO writer_leases (name, owner, updated_at) VALUES (?, ?, ?)", storageWriterLeaseName, owner, now); err != nil {
			return nil, fmt.Errorf("create writer lease: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("read writer lease: %w", err)
	case currentOwner == owner || updatedAt <= cutoff || force:
		if _, err := tx.Exec("UPDATE writer_leases SET owner = ?, updated_at = ? WHERE name = ?", owner, now, storageWriterLeaseName); err != nil {
			return nil, fmt.Errorf("claim writer lease: %w", err)
		}
	default:
		return nil, WriterLeaseHeldError{Owner: currentOwner}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit writer lease: %w", err)
	}
	return &WriterLease{db: database, owner: owner}, nil
}

func (l *WriterLease) Owner() string {
	if l == nil {
		return ""
	}
	return l.owner
}

func (l *WriterLease) Renew(ctx context.Context) error {
	if l == nil {
		return nil
	}
	res, err := l.db.Write(ctx).Exec("UPDATE writer_leases SET updated_at = unixepoch() WHERE name = ? AND owner = ?", storageWriterLeaseName, l.owner)
	if err != nil {
		return fmt.Errorf("renew writer lease: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrWriterLeaseHeld
	}
	return nil
}

func (l *WriterLease) Release(ctx context.Context) error {
	if l == nil {
		return nil
	}
	// Release is also used during shutdown with cancellation detached.
	ctx, cancel := context.WithTimeout(ctx, writerLeaseReleaseTimeout)
	defer cancel()
	if _, err := l.db.Write(ctx).Exec("DELETE FROM writer_leases WHERE name = ? AND owner = ?", storageWriterLeaseName, l.owner); err != nil {
		return fmt.Errorf("release writer lease: %w", err)
	}
	return nil
}

// RunHeartbeat renews the lease until ctx is canceled or renewal fails.
// It returns nil on cancellation.
func (l *WriterLease) RunHeartbeat(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := l.Renew(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

func leaseRandHex(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}
