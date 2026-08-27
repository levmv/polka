package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

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
