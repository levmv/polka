package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	return captureOutput(t, &os.Stdout, fn)
}

func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	return captureOutput(t, &os.Stderr, fn)
}

// A temporary file captures large output without a pipe reader goroutine.
// Tests that replace process-wide streams must run sequentially.
func captureOutput(t *testing.T, stream **os.File, fn func() error) (string, error) {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatalf("create output file: %v", err)
	}
	defer f.Close()

	original := *stream
	*stream = f
	defer func() { *stream = original }()

	runErr := fn()
	output, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	return string(output), runErr
}

func initDefaultTestLibrary(t testing.TB, dataDir string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("create test data directory: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("init test database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close test database: %v", err)
	}

	root, err := storage.ResolveRoot(dataDir, "")
	if err != nil {
		t.Fatalf("resolve test storage root: %v", err)
	}
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("create test storage layout: %v", err)
	}
}
