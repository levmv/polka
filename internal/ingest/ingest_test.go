package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/pdfcover"
	"github.com/levmv/polka/internal/storage"
)

func TestServiceWaitsForStableFileThenImports(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	storageDir := filepath.Join(base, "storage")
	ingestDir := filepath.Join(base, "ingest")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root := storage.NewRoot(storageDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("storage.EnsureLayout: %v", err)
	}

	service := NewService(database, root, ingestDir, Options{StableScans: 2})
	src := filepath.Join(ingestDir, "incoming.epub")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir ingest: %v", err)
	}
	if err := os.WriteFile(src, []byte("opaque epub bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	first, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("first ScanOnce: %v", err)
	}
	if first.Deferred != 1 || first.Imported != 0 {
		t.Fatalf("first summary = %+v; want one deferred", first)
	}

	second, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("second ScanOnce: %v", err)
	}
	if second.Imported != 1 || second.Deferred != 0 || second.Failed != 0 {
		t.Fatalf("second summary = %+v; want one import", second)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source was not left in place after import: %v", err)
	}

	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets LIMIT 1").Scan(&storagePath); err != nil {
		t.Fatalf("query storage_path: %v", err)
	}
	if _, err := os.Stat(root.Abs(storagePath)); err != nil {
		t.Fatalf("managed file missing: %v", err)
	}

	third, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("third ScanOnce: %v", err)
	}
	if third.Imported != 0 || third.Duplicates != 0 || third.Failed != 0 || third.Deferred != 0 {
		t.Fatalf("third summary = %+v; want no repeated work", third)
	}
	status, err := service.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Pending != 0 {
		t.Fatalf("Pending = %d; want processed source hidden from queue", status.Pending)
	}
}

func TestServiceScanWaitHonorsContext(t *testing.T) {
	base := t.TempDir()
	database, err := db.InitPath(filepath.Join(base, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	service := NewService(database, storage.NewRoot(base), filepath.Join(base, "ingest"), Options{})
	release, err := service.scanQueue.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire scan slot: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := service.ScanOnce(ctx, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ScanOnce error = %v; want context deadline while scan slot is busy", err)
	}
}

func TestServiceDeletesSourceAfterImportWhenConfigured(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	storageDir := filepath.Join(base, "storage")
	ingestDir := filepath.Join(base, "ingest")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root := storage.NewRoot(storageDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("storage.EnsureLayout: %v", err)
	}

	service := NewService(database, root, ingestDir, Options{
		StableScans:   1,
		DeleteSources: true,
	})
	src := filepath.Join(ingestDir, "incoming.epub")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir ingest: %v", err)
	}
	if err := os.WriteFile(src, []byte("opaque epub bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	summary, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	if summary.Imported != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v; want one import", summary)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source exists after import with delete enabled; err=%v", err)
	}
	var workID string
	if err := database.QueryRow("SELECT id FROM works LIMIT 1").Scan(&workID); err != nil {
		t.Fatalf("query imported work: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET deleted_at = unixepoch() WHERE id = ?", workID); err != nil {
		t.Fatalf("trash imported work: %v", err)
	}

	duplicateSrc := filepath.Join(ingestDir, "duplicate.epub")
	if err := os.WriteFile(duplicateSrc, []byte("opaque epub bytes"), 0o644); err != nil {
		t.Fatalf("rewrite duplicate source: %v", err)
	}
	duplicate, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("duplicate ScanOnce: %v", err)
	}
	if duplicate.Duplicates != 1 || duplicate.Trashed != 1 || duplicate.Failed != 0 {
		t.Fatalf("duplicate summary = %+v; want one duplicate in Trash", duplicate)
	}
	if _, err := os.Stat(duplicateSrc); !os.IsNotExist(err) {
		t.Fatalf("duplicate source exists after import with delete enabled; err=%v", err)
	}
	var stillTrashed bool
	if err := database.QueryRow("SELECT deleted_at IS NOT NULL FROM works WHERE id = ?", workID).Scan(&stillTrashed); err != nil {
		t.Fatalf("query duplicate work: %v", err)
	}
	if !stillTrashed {
		t.Fatal("recurring ingest restored a plain duplicate from Trash")
	}
}

func TestServiceLeavesUnsupportedFilesInPlaceWithError(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root := storage.NewRoot(filepath.Join(base, "storage"))
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("storage.EnsureLayout: %v", err)
	}
	ingestDir := filepath.Join(base, "ingest")
	service := NewService(database, root, ingestDir, Options{StableScans: 1})
	src := filepath.Join(ingestDir, "notes.xyz")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir ingest: %v", err)
	}
	if err := os.WriteFile(src, []byte("not a book"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	summary, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	if summary.Failed != 1 {
		t.Fatalf("summary = %+v; want one failed", summary)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source was not left in place after failure: %v", err)
	}
	status, err := service.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Pending != 0 {
		t.Fatalf("Pending = %d; want processed failure hidden from queue", status.Pending)
	}
	if !strings.Contains(status.LastError, "unsupported book format") {
		t.Fatalf("LastError = %q; want unsupported format note", status.LastError)
	}
}

func TestServiceContinuesAfterUnreadableIngestEntry(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	storageDir := filepath.Join(base, "storage")
	ingestDir := filepath.Join(base, "ingest")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root := storage.NewRoot(storageDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("storage.EnsureLayout: %v", err)
	}

	service := NewService(database, root, ingestDir, Options{StableScans: 1})
	good := filepath.Join(ingestDir, "incoming.epub")
	if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
		t.Fatalf("mkdir ingest: %v", err)
	}
	if err := os.WriteFile(good, []byte("opaque epub bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	blocked := filepath.Join(ingestDir, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatalf("mkdir blocked: %v", err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Skipf("chmod blocked dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(blocked, 0o755)
	})
	if _, err := os.ReadDir(blocked); err == nil {
		t.Skip("filesystem permissions do not make blocked dir unreadable")
	}

	summary, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	if summary.Imported != 1 || summary.Failed != 1 {
		t.Fatalf("summary = %+v; want one import and one scan failure", summary)
	}

	status, err := service.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(status.LastError, "blocked") {
		t.Fatalf("LastError = %q; want blocked entry note", status.LastError)
	}
	if status.Pending != 0 {
		t.Fatalf("Pending = %d; want processed good source hidden and blocked entry skipped", status.Pending)
	}
}

func TestServiceDeletesCalibreDirectoryAfterImportWhenConfigured(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	storageDir := filepath.Join(base, "storage")
	ingestDir := filepath.Join(base, "ingest")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root := storage.NewRoot(storageDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("storage.EnsureLayout: %v", err)
	}

	bookDir := filepath.Join(ingestDir, "Calibre Author", "Calibre Book (1)")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Calibre Book</dc:title>
    <dc:creator opf:file-as="Author, Calibre">Calibre Author</dc:creator>
  </metadata>
</package>`
	if err := os.WriteFile(filepath.Join(bookDir, "metadata.opf"), []byte(opf), 0o644); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "Calibre Book.epub"), []byte("epub bytes"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}

	service := NewService(database, root, ingestDir, Options{
		StableScans:   1,
		DeleteSources: true,
	})
	summary, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	if summary.Imported != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v; want one imported group", summary)
	}
	if _, err := os.Stat(bookDir); !os.IsNotExist(err) {
		t.Fatalf("calibre source dir exists after import with delete enabled; err=%v", err)
	}
}

func TestServiceRequiresLayoutBeforeImport(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	storageDir := filepath.Join(base, "storage")
	ingestDir := filepath.Join(base, "ingest")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root := storage.NewRoot(storageDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("storage.EnsureLayout: %v", err)
	}
	// Simulate a dropped mount: the whole books root disappears.
	if err := os.RemoveAll(root.Path); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	service := NewService(database, root, ingestDir, Options{StableScans: 1})
	src := filepath.Join(ingestDir, "incoming.epub")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir ingest: %v", err)
	}
	if err := os.WriteFile(src, []byte("opaque epub bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if _, err := service.ScanOnce(context.Background(), false); !errors.Is(err, storage.ErrLayoutMissing) {
		t.Fatalf("ScanOnce error = %v; want ErrLayoutMissing", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source was not left in place after storage error: %v", err)
	}
	if _, err := os.Stat(root.Path); !os.IsNotExist(err) {
		t.Fatalf("ingest recreated the books root despite missing layout; stat err=%v", err)
	}
}

func TestServiceImportsCalibreDirectoryAsGroup(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	storageDir := filepath.Join(base, "storage")
	ingestDir := filepath.Join(base, "ingest")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root := storage.NewRoot(storageDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("storage.EnsureLayout: %v", err)
	}

	bookDir := filepath.Join(ingestDir, "Calibre Author", "Calibre Book (1)")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Calibre Book</dc:title>
    <dc:creator opf:file-as="Author, Calibre">Calibre Author</dc:creator>
  </metadata>
</package>`
	if err := os.WriteFile(filepath.Join(bookDir, "metadata.opf"), []byte(opf), 0o644); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "Calibre Book.epub"), []byte("epub bytes"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "Calibre Book.pdf"), []byte("%PDF-1.4\n%%EOF"), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	service := NewService(database, root, ingestDir, Options{StableScans: 1})
	summary, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	if summary.Imported != 1 || summary.Failed != 0 || summary.Deferred != 0 {
		t.Fatalf("summary = %+v; want one imported group", summary)
	}

	var works, assets int
	if err := database.QueryRow("SELECT COUNT(*) FROM works").Scan(&works); err != nil {
		t.Fatalf("count works: %v", err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if works != 1 || assets != 2 {
		t.Fatalf("works/assets = %d/%d; want 1/2", works, assets)
	}
	var title, author string
	if err := database.QueryRow(`
		SELECT w.title, a.name
		FROM works w
		JOIN work_authors wa ON wa.work_id = w.id
		JOIN authors a ON a.id = wa.author_id
		LIMIT 1
	`).Scan(&title, &author); err != nil {
		t.Fatalf("query metadata: %v", err)
	}
	if title != "Calibre Book" || author != "Calibre Author" {
		t.Fatalf("metadata = title %q author %q; want calibre sidecar values", title, author)
	}
	if _, err := os.Stat(bookDir); err != nil {
		t.Fatalf("source calibre dir was not left in place: %v", err)
	}

	second, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("second ScanOnce: %v", err)
	}
	if second.Imported != 0 || second.Duplicates != 0 || second.Failed != 0 || second.Deferred != 0 {
		t.Fatalf("second summary = %+v; want no repeated group work", second)
	}

	var workID string
	if err := database.QueryRow("SELECT id FROM works LIMIT 1").Scan(&workID); err != nil {
		t.Fatalf("query work ID: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET deleted_at = unixepoch() WHERE id = ?", workID); err != nil {
		t.Fatalf("trash work: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "Calibre Book.fb2"), []byte("<FictionBook/>"), 0o644); err != nil {
		t.Fatalf("write added format: %v", err)
	}
	restored, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("restoring ScanOnce: %v", err)
	}
	if restored.Imported != 1 || restored.Restored != 1 || restored.Trashed != 0 || restored.Failed != 0 {
		t.Fatalf("restoring summary = %+v; want one imported and restored group", restored)
	}
	var workTrashed bool
	if err := database.QueryRow("SELECT deleted_at IS NOT NULL FROM works WHERE id = ?", workID).Scan(&workTrashed); err != nil {
		t.Fatalf("query restored work: %v", err)
	}
	if workTrashed {
		t.Fatal("work stayed in Trash after ingest added a format")
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM assets WHERE work_id = ?", workID).Scan(&assets); err != nil {
		t.Fatalf("count restored assets: %v", err)
	}
	if assets != 3 {
		t.Fatalf("restored asset count = %d; want 3", assets)
	}
	status, err := service.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Pending != 0 {
		t.Fatalf("Pending = %d; want processed group hidden from queue", status.Pending)
	}
}

func TestServiceRecoversImportPanicAndLeavesFileInPlace(t *testing.T) {
	base := t.TempDir()
	ingestDir := filepath.Join(base, "ingest")
	database, err := db.InitPath(filepath.Join(base, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root := storage.NewRoot(filepath.Join(base, "storage"))
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("storage.EnsureLayout: %v", err)
	}
	service := NewService(database, root, ingestDir, Options{StableScans: 1})
	service.importFile = func(context.Context, *db.DB, storage.Root, string, *pdfcover.Renderer, importer.Options) (importer.Result, error) {
		panic("boom")
	}
	src := filepath.Join(ingestDir, "panic.epub")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir ingest: %v", err)
	}
	if err := os.WriteFile(src, []byte("panic epub bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	summary, err := service.ScanOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	if summary.Failed != 1 {
		t.Fatalf("summary = %+v; want one failed", summary)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source was not left in place after panic: %v", err)
	}
	status, err := service.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Pending != 0 {
		t.Fatalf("Pending = %d; want processed panic hidden from queue", status.Pending)
	}
	if !strings.Contains(status.LastError, "panic while importing") {
		t.Fatalf("LastError = %q; want panic note", status.LastError)
	}
}

func TestStatusForPathDoesNotCreateMissingDir(t *testing.T) {
	// A missing incoming folder (e.g. a dropped NAS mount) must be reported as
	// unreachable, not silently MkdirAll'd into a shadow directory.
	missing := filepath.Join(t.TempDir(), "not-there")
	status, err := StatusForPath(missing)
	if err != nil {
		t.Fatalf("StatusForPath: %v", err)
	}
	if status.Reachable {
		t.Fatalf("Reachable = true; want false for a missing folder")
	}
	if status.Pending != 0 {
		t.Fatalf("Pending = %d; want 0 for a missing folder", status.Pending)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("status read created the folder: %v", statErr)
	}

	if err := os.MkdirAll(missing, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	status, err = StatusForPath(missing)
	if err != nil {
		t.Fatalf("StatusForPath after create: %v", err)
	}
	if !status.Reachable {
		t.Fatalf("Reachable = false; want true once the folder exists")
	}
}
