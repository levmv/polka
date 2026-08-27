package web

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestStopIngesterWaitsForServiceExit(t *testing.T) {
	canceled := make(chan struct{})
	done := make(chan struct{})
	stopped := make(chan struct{})
	s := &Server{
		ingestCancel: func() { close(canceled) },
		ingestDone:   done,
	}

	go func() {
		s.stopIngester()
		close(stopped)
	}()
	<-canceled
	select {
	case <-stopped:
		t.Fatal("stopIngester returned before the service exited")
	default:
	}

	close(done)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stopIngester did not return after the service exited")
	}
}

func TestOpenServeBooksRootCreatesDefaultWhenUnconfigured(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()

	want := filepath.Join(dataDir, "books")
	if _, err := os.Stat(want); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition stat default books root = %v; want not exist", err)
	}

	root, err := openServeBooksRoot(database, dataDir)
	if err != nil {
		t.Fatalf("openServeBooksRoot: %v", err)
	}
	if root.Path != want {
		t.Fatalf("root.Path = %q; want %q", root.Path, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("default books root stat = %v/%v; want directory", info, err)
	}
	configured, err := storage.RootConfigured(database.DB)
	if err != nil {
		t.Fatalf("RootConfigured: %v", err)
	}
	if !configured {
		t.Fatal("RootConfigured = false; want persisted default books root")
	}
}

func TestOpenServeBooksRootDoesNotCreateConfiguredMissingRoot(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()

	configuredRoot, err := storage.SaveRoot(database.DB, dataDir, "configured-books")
	if err != nil {
		t.Fatalf("SaveRoot: %v", err)
	}
	if _, err := os.Stat(configuredRoot.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition stat configured books root = %v; want not exist", err)
	}

	root, err := openServeBooksRoot(database, dataDir)
	if err != nil {
		t.Fatalf("openServeBooksRoot: %v", err)
	}
	if root.Path != configuredRoot.Path {
		t.Fatalf("root.Path = %q; want %q", root.Path, configuredRoot.Path)
	}
	if _, err := os.Stat(configuredRoot.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured missing root was created; stat err = %v", err)
	}
}
