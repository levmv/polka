package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
)

func TestCLIWriterLeaseCancelsAndReportsForcedTakeover(t *testing.T) {
	dataDir := t.TempDir()
	database, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	defer database.Close()

	first, err := db.AcquireWriterLease(context.Background(), database, "first", false)
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	guard := superviseCLIWriterLease(context.Background(), first, 5*time.Millisecond)

	forced, err := db.AcquireWriterLease(context.Background(), database, "forced", true)
	if err != nil {
		t.Fatalf("Acquire forced: %v", err)
	}
	defer forced.Release(context.Background())

	select {
	case <-guard.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("CLI lease context was not cancelled after takeover")
	}
	err = guard.finish(context.Cause(guard.Context()))
	if !errors.Is(err, db.ErrWriterLeaseHeld) || !strings.Contains(err.Error(), "writer lease lost") {
		t.Fatalf("finish error = %v; want reported writer lease loss", err)
	}
	if strings.Count(err.Error(), "writer lease lost") != 1 {
		t.Fatalf("finish error repeats lease loss: %v", err)
	}
}

func TestCLIWriterLeaseFinishReleasesAndPreservesCommandError(t *testing.T) {
	dataDir := t.TempDir()
	database, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	defer database.Close()

	first, err := db.AcquireWriterLease(context.Background(), database, "first", false)
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	guard := superviseCLIWriterLease(context.Background(), first, time.Hour)
	want := errors.New("command failed")
	if got := guard.finish(want); !errors.Is(got, want) {
		t.Fatalf("finish error = %v; want command error preserved", got)
	}

	second, err := db.AcquireWriterLease(context.Background(), database, "second", false)
	if err != nil {
		t.Fatalf("Acquire after finish: %v", err)
	}
	defer second.Release(context.Background())
}
