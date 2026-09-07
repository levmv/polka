package db

import (
	"context"
	"errors"
	"testing"
)

func TestWriterLeaseBlocksFreshForeignOwner(t *testing.T) {
	database := newTestDB(t)

	ctx := context.Background()
	first, err := AcquireWriterLease(ctx, database, "first", false)
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	defer first.Release(ctx)

	_, err = AcquireWriterLease(ctx, database, "second", false)
	if !errors.Is(err, ErrWriterLeaseHeld) {
		t.Fatalf("Acquire second err = %v; want ErrWriterLeaseHeld", err)
	}
}

func TestWriterLeaseAllowsStaleOrForcedClaim(t *testing.T) {
	database := newTestDB(t)

	ctx := context.Background()
	mustExec(t, database, "INSERT INTO writer_leases (name, owner, updated_at) VALUES (?, ?, ?)", storageWriterLeaseName, "old", 100)

	stale, err := AcquireWriterLease(ctx, database, "stale-claim", false)
	if err != nil {
		t.Fatalf("Acquire stale: %v", err)
	}
	if stale.Owner() != "stale-claim" {
		t.Fatalf("stale owner = %q", stale.Owner())
	}

	forced, err := AcquireWriterLease(ctx, database, "forced", true)
	if err != nil {
		t.Fatalf("Acquire forced: %v", err)
	}
	if forced.Owner() != "forced" {
		t.Fatalf("forced owner = %q", forced.Owner())
	}

	if err := stale.Release(ctx); err != nil {
		t.Fatalf("release stale owner: %v", err)
	}
	var owner string
	if err := database.Read(ctx).QueryRow("SELECT owner FROM writer_leases WHERE name = ?", storageWriterLeaseName).Scan(&owner); err != nil {
		t.Fatalf("query forced lease: %v", err)
	}
	if owner != "forced" {
		t.Fatalf("owner after releasing old owner = %q; want forced", owner)
	}
	if err := forced.Release(ctx); err != nil {
		t.Fatalf("release forced owner: %v", err)
	}
	var count int
	if err := database.Read(ctx).QueryRow("SELECT COUNT(*) FROM writer_leases WHERE name = ?", storageWriterLeaseName).Scan(&count); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if count != 0 {
		t.Fatalf("leases = %d; want 0", count)
	}
}
