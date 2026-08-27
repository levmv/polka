package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestLibraryWritebackDryRun(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("InitPath: %v", err)
	}
	if _, err := database.Exec("INSERT INTO works (id, title, sort_title, metadata_rev) VALUES ('w1', 'Book', 'Book', 1)"); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, writeback_rev)
		VALUES ('as1', 'w1', 'A/Book/as1.epub', 'as1.epub', '.epub', 'epub', 0)
	`); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	database.Close()

	out, err := captureStdout(t, func() error {
		return runLibraryWriteback(context.Background(), dataDir, []string{"--dry-run"})
	})
	if err != nil {
		t.Fatalf("runLibraryWriteback dry-run: %v", err)
	}
	if !strings.Contains(out, "Would write metadata for 1 asset.") {
		t.Fatalf("dry-run output = %q", out)
	}
}
