package writeback

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/storage"
)

func TestRunWritesDirtyEPUBAndUpdatesAssetIdentity(t *testing.T) {
	database, root, assetID, relPath := setupWritebackEPUB(t, "Old Title", "Old Author")
	defer database.Close()

	if _, err := database.Exec(`
		UPDATE works
		SET title = 'New Title', sort_title = 'Title, New', metadata_rev = 1, updated_at = 1800000000
		WHERE id = 'w1'
	`); err != nil {
		t.Fatalf("update work metadata: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE authors SET name = 'Jane Writer', sort_name = 'Writer, Jane' WHERE id = 'au1'
	`); err != nil {
		t.Fatalf("update author: %v", err)
	}

	summary, err := Run(context.Background(), database, root, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Written != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v; want one written", summary)
	}

	rewritten, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read rewritten epub: %v", err)
	}
	meta, err := format.ExtractEPUBMetadata(bytes.NewReader(rewritten), int64(len(rewritten)))
	if err != nil {
		t.Fatalf("ExtractEPUBMetadata: %v", err)
	}
	if meta.Title != "New Title" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Jane Writer" {
		t.Fatalf("rewritten metadata = %+v", meta)
	}

	var currentHash, koHash string
	var currentSize, writebackRev int64
	var writebackError sql.NullString
	if err := database.QueryRow(`
		SELECT current_sha256, current_size, koreader_hash, writeback_rev, writeback_error
		FROM assets WHERE id = ?
	`, assetID).Scan(&currentHash, &currentSize, &koHash, &writebackRev, &writebackError); err != nil {
		t.Fatalf("query asset identity: %v", err)
	}
	if currentHash != sha256HexForTest(rewritten) || currentSize != int64(len(rewritten)) || koHash == "" || writebackRev != 1 || writebackError.Valid {
		t.Fatalf("asset identity = hash:%q size:%d ko:%q rev:%d err:%+v", currentHash, currentSize, koHash, writebackRev, writebackError)
	}
	assertNoPendingAttempts(t, database, assetID)

	rerun, err := Run(context.Background(), database, root, Options{All: true})
	if err != nil {
		t.Fatalf("Run --all second pass: %v", err)
	}
	if rerun.Written != 0 || rerun.Unchanged != 1 || rerun.Failed != 0 {
		t.Fatalf("second pass summary = %+v; want one unchanged", rerun)
	}
}

func TestRunWritesDirtyEPUBCover(t *testing.T) {
	database, root, assetID, relPath := setupWritebackEPUB(t, "Cover Title", "Cover Author")
	defer database.Close()
	dataRoot := storage.NewRoot(filepath.Dir(root.Path))
	embeddedCover := testWritebackPNG(t, color.NRGBA{R: 20, G: 40, B: 60, A: 255})
	storedCover := testWritebackPNG(t, color.NRGBA{R: 210, G: 120, B: 30, A: 255})
	src := testWritebackEPUBBytesWithCover(t, "Cover Title", "Cover Author", embeddedCover)
	if err := os.WriteFile(root.Abs(relPath), src, 0o644); err != nil {
		t.Fatalf("replace source epub: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE assets
		SET current_sha256 = ?, current_size = ?, original_sha256 = ?, original_size = ?
		WHERE id = ?
	`, sha256HexForTest(src), len(src), sha256HexForTest(src), len(src), assetID); err != nil {
		t.Fatalf("update asset identity: %v", err)
	}
	coverPath := dataRoot.Abs(covers.OriginalPath("w1"))
	if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
		t.Fatalf("mkdir cover dir: %v", err)
	}
	if err := os.WriteFile(coverPath, storedCover, 0o644); err != nil {
		t.Fatalf("write stored cover: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE works
		SET cover_version = 1, metadata_rev = 1, updated_at = 1800000000
		WHERE id = 'w1'
	`); err != nil {
		t.Fatalf("mark cover dirty: %v", err)
	}

	summary, err := Run(context.Background(), database, root, Options{CoverRoot: dataRoot})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Written != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v; want one written", summary)
	}

	rewritten, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read rewritten epub: %v", err)
	}
	cover, ext, err := format.ExtractCover(bytes.NewReader(rewritten), int64(len(rewritten)), format.FormatEPUB)
	if err != nil {
		t.Fatalf("ExtractCover: %v", err)
	}
	if ext != ".png" || !bytes.Equal(cover, storedCover) {
		t.Fatalf("rewritten cover = ext %q, %d bytes; want stored PNG", ext, len(cover))
	}

	var writebackRev int64
	if err := database.QueryRow("SELECT writeback_rev FROM assets WHERE id = ?", assetID).Scan(&writebackRev); err != nil {
		t.Fatalf("query writeback rev: %v", err)
	}
	if writebackRev != 1 {
		t.Fatalf("writeback_rev = %d; want 1", writebackRev)
	}
	assertNoPendingAttempts(t, database, assetID)
}

func TestRunWritesDirtyKEPUBContainer(t *testing.T) {
	database, root, _, relPath := setupWritebackEPUB(t, "Old Title", "Old Author")
	defer database.Close()

	if _, err := database.Exec("UPDATE assets SET format = 'kepub', extension = '.kepub.epub' WHERE id = 'as_writeback'"); err != nil {
		t.Fatalf("mark asset as kepub: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE works
		SET title = 'New KEPUB Title', metadata_rev = 1
		WHERE id = 'w1'
	`); err != nil {
		t.Fatalf("update work metadata: %v", err)
	}

	summary, err := Run(context.Background(), database, root, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Written != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v; want one written", summary)
	}

	rewritten, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read rewritten kepub: %v", err)
	}
	meta, err := format.ExtractMetadata(bytes.NewReader(rewritten), int64(len(rewritten)), format.FormatKEPUB)
	if err != nil {
		t.Fatalf("ExtractMetadata KEPUB: %v", err)
	}
	if meta == nil || meta.Title != "New KEPUB Title" {
		t.Fatalf("rewritten kepub metadata = %+v", meta)
	}
	assertNoPendingAttempts(t, database, "as_writeback")
}

func TestRunWritesDirtyFB2(t *testing.T) {
	database, root, assetID, relPath := setupWritebackFB2(t, "Old FB2 Title", "Old FB2 Author")
	defer database.Close()

	if _, err := database.Exec(`
		UPDATE works
		SET title = 'New FB2 Title', cover_version = 1, metadata_rev = 1, updated_at = 1800000000
		WHERE id = 'w1'
	`); err != nil {
		t.Fatalf("update work metadata: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE authors SET name = 'Jane FB2 Writer', sort_name = 'Writer, Jane FB2' WHERE id = 'au1'
	`); err != nil {
		t.Fatalf("update author: %v", err)
	}

	summary, err := Run(context.Background(), database, root, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Written != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v; want one written", summary)
	}

	rewritten, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read rewritten fb2: %v", err)
	}
	meta, err := format.ExtractMetadata(bytes.NewReader(rewritten), int64(len(rewritten)), format.FormatFB2)
	if err != nil {
		t.Fatalf("ExtractMetadata FB2: %v", err)
	}
	if meta == nil || meta.Title != "New FB2 Title" || len(meta.Authors) != 1 || meta.Authors[0].Name != "Jane FB2 Writer" {
		t.Fatalf("rewritten fb2 metadata = %+v", meta)
	}

	var writebackRev int64
	if err := database.QueryRow("SELECT writeback_rev FROM assets WHERE id = ?", assetID).Scan(&writebackRev); err != nil {
		t.Fatalf("query writeback rev: %v", err)
	}
	if writebackRev != 1 {
		t.Fatalf("writeback_rev = %d; want 1", writebackRev)
	}
	assertNoPendingAttempts(t, database, assetID)
}

func TestRunFailedOnlyPlansFailedDirtyAssets(t *testing.T) {
	database, err := db.InitPath(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatalf("InitPath: %v", err)
	}
	defer database.Close()
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title, metadata_rev) VALUES ('w1', 'Book', 'Book', 2);
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, writeback_rev, writeback_error)
		VALUES
			('clean_dirty', 'w1', 'Book/clean.epub', 'clean.epub', '.epub', 'epub', 1, NULL),
			('failed_dirty', 'w1', 'Book/failed.epub', 'failed.epub', '.epub', 'epub', 1, 'bad opf');
	`); err != nil {
		t.Fatalf("seed failed-only rows: %v", err)
	}

	summary, err := Run(context.Background(), database, storage.Root{}, Options{DryRun: true, FailedOnly: true})
	if err != nil {
		t.Fatalf("Run failed-only dry-run: %v", err)
	}
	if summary.WouldWrite != 1 || len(summary.Results) != 1 || summary.Results[0].AssetID != "failed_dirty" {
		t.Fatalf("failed-only summary = %+v; want only failed_dirty", summary)
	}
}

func TestServiceRunOnceWritesOnlyInAutoMode(t *testing.T) {
	database, root, assetID, relPath := setupWritebackEPUB(t, "Old Title", "Old Author")
	defer database.Close()

	if _, err := database.Exec("UPDATE works SET title = 'Auto Title', metadata_rev = 1 WHERE id = 'w1'"); err != nil {
		t.Fatalf("mark work dirty: %v", err)
	}
	svc := NewService(database, root, ServiceOptions{BatchLimit: 1})
	svc.now = func() time.Time { return time.Unix(200, 0) }

	manual, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce manual: %v", err)
	}
	if manual.Written != 0 || manual.Planned != 0 {
		t.Fatalf("manual summary = %+v; want no work", manual)
	}
	var writebackRev int64
	if err := database.QueryRow("SELECT writeback_rev FROM assets WHERE id = ?", assetID).Scan(&writebackRev); err != nil {
		t.Fatalf("query manual writeback rev: %v", err)
	}
	if writebackRev != 0 {
		t.Fatalf("manual writeback_rev = %d; want 0", writebackRev)
	}

	if err := SaveMode(database.DB, ModeAuto); err != nil {
		t.Fatalf("SaveMode auto: %v", err)
	}
	auto, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce auto: %v", err)
	}
	if auto.Written != 1 || auto.Failed != 0 {
		t.Fatalf("auto summary = %+v; want one written", auto)
	}
	rewritten, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read rewritten epub: %v", err)
	}
	meta, err := format.ExtractMetadata(bytes.NewReader(rewritten), int64(len(rewritten)), format.FormatEPUB)
	if err != nil {
		t.Fatalf("ExtractMetadata EPUB: %v", err)
	}
	if meta == nil || meta.Title != "Auto Title" {
		t.Fatalf("auto metadata = %+v; want Auto Title", meta)
	}
}

func TestServiceFailureRetryUsesDurableTimestamp(t *testing.T) {
	database, root, _, relPath := setupWritebackEPUB(t, "Old Title", "Old Author")
	defer database.Close()
	if _, err := database.Exec("UPDATE works SET title = 'New Title', metadata_rev = 1 WHERE id = 'w1'"); err != nil {
		t.Fatalf("mark work dirty: %v", err)
	}
	driftBytes := testWritebackEPUBBytes(t, "Drift Title", "Drift Author")
	if err := os.WriteFile(root.Abs(relPath), driftBytes, 0o644); err != nil {
		t.Fatalf("write drift file: %v", err)
	}
	if err := SaveMode(database.DB, ModeAuto); err != nil {
		t.Fatalf("SaveMode auto: %v", err)
	}

	now := time.Now()
	svc := NewService(database, root, ServiceOptions{BatchLimit: 1})
	svc.now = func() time.Time { return now }
	first, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if first.Planned != 1 || first.Failed != 1 {
		t.Fatalf("first summary = %+v; want one failed attempt", first)
	}

	second, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if second.Planned != 0 || second.Failed != 0 {
		t.Fatalf("fresh failure was selected again: %+v", second)
	}

	now = now.Add(6 * time.Minute)
	third, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("third RunOnce: %v", err)
	}
	if third.Planned != 1 || third.Failed != 1 {
		t.Fatalf("due failure summary = %+v; want one retried failure", third)
	}
}

func TestRunRefusesCurrentFileDrift(t *testing.T) {
	database, root, assetID, relPath := setupWritebackEPUB(t, "Old Title", "Old Author")
	defer database.Close()

	if _, err := database.Exec("UPDATE works SET title = 'New Title', metadata_rev = 1 WHERE id = 'w1'"); err != nil {
		t.Fatalf("update work metadata: %v", err)
	}
	driftBytes := testWritebackEPUBBytes(t, "Drift Title", "Drift Author")
	if err := os.WriteFile(root.Abs(relPath), driftBytes, 0o644); err != nil {
		t.Fatalf("write drift file: %v", err)
	}

	summary, err := Run(context.Background(), database, root, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Failed != 1 || !strings.Contains(summary.Results[0].Error, "current file drift") {
		t.Fatalf("summary = %+v; want drift failure", summary)
	}

	got, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read drift file: %v", err)
	}
	if !bytes.Equal(got, driftBytes) {
		t.Fatalf("drift file was overwritten")
	}
	var writebackRev int64
	var writebackError string
	if err := database.QueryRow("SELECT writeback_rev, COALESCE(writeback_error, '') FROM assets WHERE id = ?", assetID).Scan(&writebackRev, &writebackError); err != nil {
		t.Fatalf("query writeback error: %v", err)
	}
	if writebackRev != 0 || !strings.Contains(writebackError, "current file drift") {
		t.Fatalf("writeback state = rev:%d err:%q", writebackRev, writebackError)
	}
	assertNoPendingAttempts(t, database, assetID)
}

func TestRunReturnsSecondaryWritebackStateFailure(t *testing.T) {
	database, root, assetID, relPath := setupWritebackEPUB(t, "Old Title", "Old Author")
	defer database.Close()

	if _, err := database.Exec("UPDATE works SET title = 'New Title', metadata_rev = 1 WHERE id = 'w1'"); err != nil {
		t.Fatalf("update work metadata: %v", err)
	}
	driftBytes := testWritebackEPUBBytes(t, "Drift Title", "Drift Author")
	if err := os.WriteFile(root.Abs(relPath), driftBytes, 0o644); err != nil {
		t.Fatalf("write drift file: %v", err)
	}
	if _, err := database.Exec(`
		CREATE TRIGGER reject_writeback_error
		BEFORE UPDATE OF writeback_error ON assets
		WHEN NEW.writeback_error IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'blocked writeback_error update');
		END
	`); err != nil {
		t.Fatalf("create writeback error trigger: %v", err)
	}

	summary, err := Run(context.Background(), database, root, Options{})
	if err == nil {
		t.Fatal("Run returned nil error when failure state could not be persisted")
	}
	if summary.Failed != 1 || len(summary.Results) != 1 {
		t.Fatalf("summary = %+v; want one preserved failed result", summary)
	}
	for label, text := range map[string]string{
		"run error":    err.Error(),
		"result error": summary.Results[0].Error,
	} {
		if !strings.Contains(text, "current file drift") ||
			!strings.Contains(text, "record metadata write-back failure") ||
			!strings.Contains(text, "blocked writeback_error update") {
			t.Fatalf("%s = %q; want primary and secondary causes", label, text)
		}
	}

	var writebackError string
	if err := database.QueryRow("SELECT COALESCE(writeback_error, '') FROM assets WHERE id = ?", assetID).Scan(&writebackError); err != nil {
		t.Fatalf("query writeback error: %v", err)
	}
	if writebackError != "" {
		t.Fatalf("writeback_error = %q; trigger should have blocked persistence", writebackError)
	}
}

func TestRunRefusesOversizedInputBeforeRendering(t *testing.T) {
	database, root, assetID, relPath := setupWritebackEPUB(t, "Old Title", "Old Author")
	defer database.Close()

	oversized := maxMetadataWritebackInputBytes + 1
	f, err := os.OpenFile(root.Abs(relPath), os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open sparse source: %v", err)
	}
	if err := f.Truncate(oversized); err != nil {
		f.Close()
		t.Fatalf("truncate sparse source: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close sparse source: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE assets
		SET current_sha256 = '', current_size = ?
		WHERE id = ?
	`, oversized, assetID); err != nil {
		t.Fatalf("update asset identity: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET title = 'New Title', metadata_rev = 1 WHERE id = 'w1'"); err != nil {
		t.Fatalf("update work metadata: %v", err)
	}

	summary, err := Run(context.Background(), database, root, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Failed != 1 || !strings.Contains(summary.Results[0].Error, ErrInputTooLarge.Error()) {
		t.Fatalf("summary = %+v; want oversized failure", summary)
	}

	var writebackRev int64
	var writebackError string
	if err := database.QueryRow("SELECT writeback_rev, COALESCE(writeback_error, '') FROM assets WHERE id = ?", assetID).Scan(&writebackRev, &writebackError); err != nil {
		t.Fatalf("query writeback error: %v", err)
	}
	if writebackRev != 0 || !strings.Contains(writebackError, ErrInputTooLarge.Error()) {
		t.Fatalf("writeback state = rev:%d err:%q", writebackRev, writebackError)
	}
	assertNoPendingAttempts(t, database, assetID)
}

func TestRunDryRunDoesNotTouchFiles(t *testing.T) {
	database, root, assetID, relPath := setupWritebackEPUB(t, "Old Title", "Old Author")
	defer database.Close()
	before, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET title = 'New Title', metadata_rev = 1 WHERE id = 'w1'"); err != nil {
		t.Fatalf("update work metadata: %v", err)
	}

	summary, err := Run(context.Background(), database, root, Options{DryRun: true})
	if err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	if summary.WouldWrite != 1 {
		t.Fatalf("dry-run summary = %+v; want one would_write", summary)
	}
	after, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("dry-run changed file")
	}
	var writebackRev int64
	if err := database.QueryRow("SELECT writeback_rev FROM assets WHERE id = ?", assetID).Scan(&writebackRev); err != nil {
		t.Fatalf("query rev: %v", err)
	}
	if writebackRev != 0 {
		t.Fatalf("writeback_rev = %d; want 0", writebackRev)
	}
}

func setupWritebackEPUB(t *testing.T, title, author string) (*db.DB, storage.Root, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("InitPath: %v", err)
	}
	root := storage.NewRoot(filepath.Join(dataDir, "books"))
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	assetID := "as_writeback"
	relPath := "A/Author/Book [as_writeback].epub"
	src := testWritebackEPUBBytes(t, title, author)
	if err := os.MkdirAll(filepath.Dir(root.Abs(relPath)), 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	if err := os.WriteFile(root.Abs(relPath), src, 0o644); err != nil {
		t.Fatalf("write source epub: %v", err)
	}

	if _, err := database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', ?, ?)", title, title); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	if _, err := database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('au1', ?, ?)", author, author); err != nil {
		t.Fatalf("insert author: %v", err)
	}
	if _, err := database.Exec("INSERT INTO work_authors (work_id, author_id, role, author_order) VALUES ('w1', 'au1', 'aut', 0)"); err != nil {
		t.Fatalf("insert work author: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO assets
			(id, work_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256, original_size, current_size)
		VALUES
			(?, 'w1', ?, 'Book.epub', '.epub', 'epub', 1, 1, ?, ?, ?, ?)
	`, assetID, relPath, sha256HexForTest(src), sha256HexForTest(src), len(src), len(src)); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	return database, root, assetID, relPath
}

func setupWritebackFB2(t *testing.T, title, author string) (*db.DB, storage.Root, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("InitPath: %v", err)
	}
	root := storage.NewRoot(filepath.Join(dataDir, "books"))
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	assetID := "as_writeback_fb2"
	relPath := "A/Author/Book [as_writeback_fb2].fb2"
	src := testWritebackFB2Bytes(t, title, author)
	if err := os.MkdirAll(filepath.Dir(root.Abs(relPath)), 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	if err := os.WriteFile(root.Abs(relPath), src, 0o644); err != nil {
		t.Fatalf("write source fb2: %v", err)
	}

	if _, err := database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', ?, ?)", title, title); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	if _, err := database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('au1', ?, ?)", author, author); err != nil {
		t.Fatalf("insert author: %v", err)
	}
	if _, err := database.Exec("INSERT INTO work_authors (work_id, author_id, role, author_order) VALUES ('w1', 'au1', 'aut', 0)"); err != nil {
		t.Fatalf("insert work author: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO assets
			(id, work_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256, original_size, current_size)
		VALUES
			(?, 'w1', ?, 'Book.fb2', '.fb2', 'fb2', 1, 1, ?, ?, ?, ?)
	`, assetID, relPath, sha256HexForTest(src), sha256HexForTest(src), len(src), len(src)); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	return database, root, assetID, relPath
}

func testWritebackEPUBBytes(t *testing.T, title, creator string) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	mimetype, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := mimetype.Write([]byte("application/epub+zip")); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}
	container, err := zw.Create("META-INF/container.xml")
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if _, err := container.Write([]byte(`<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)); err != nil {
		t.Fatalf("write container: %v", err)
	}
	opf, err := zw.Create("OEBPS/content.opf")
	if err != nil {
		t.Fatalf("create opf: %v", err)
	}
	esc := func(s string) string {
		var b bytes.Buffer
		if err := xml.EscapeText(&b, []byte(s)); err != nil {
			t.Fatalf("escape: %v", err)
		}
		return b.String()
	}
	if _, err := opf.Write([]byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf"><dc:title>` + esc(title) + `</dc:title><dc:creator opf:role="aut">` + esc(creator) + `</dc:creator></metadata></package>`)); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return buf.Bytes()
}

func testWritebackFB2Bytes(t *testing.T, title, creator string) []byte {
	t.Helper()
	esc := func(s string) string {
		var b bytes.Buffer
		if err := xml.EscapeText(&b, []byte(s)); err != nil {
			t.Fatalf("escape: %v", err)
		}
		return b.String()
	}
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
<description><title-info><author><first-name>` + esc(creator) + `</first-name></author><book-title>` + esc(title) + `</book-title><lang>en</lang></title-info></description>
<body><section><p>Hello.</p></section></body>
</FictionBook>`)
}

func testWritebackEPUBBytesWithCover(t *testing.T, title, creator string, cover []byte) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	mimetype, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := mimetype.Write([]byte("application/epub+zip")); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}
	container, err := zw.Create("META-INF/container.xml")
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if _, err := container.Write([]byte(`<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)); err != nil {
		t.Fatalf("write container: %v", err)
	}
	opf, err := zw.Create("OEBPS/content.opf")
	if err != nil {
		t.Fatalf("create opf: %v", err)
	}
	esc := func(s string) string {
		var b bytes.Buffer
		if err := xml.EscapeText(&b, []byte(s)); err != nil {
			t.Fatalf("escape: %v", err)
		}
		return b.String()
	}
	if _, err := opf.Write([]byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf"><dc:title>` + esc(title) + `</dc:title><dc:creator opf:role="aut">` + esc(creator) + `</dc:creator><meta name="cover" content="cover-image"/></metadata><manifest><item id="cover-image" href="images/cover.png" media-type="image/png" properties="cover-image"/></manifest></package>`)); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	coverEntry, err := zw.Create("OEBPS/images/cover.png")
	if err != nil {
		t.Fatalf("create cover: %v", err)
	}
	if _, err := coverEntry.Write(cover); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return buf.Bytes()
}

func testWritebackPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func assertNoPendingAttempts(t *testing.T, database *db.DB, assetID string) {
	t.Helper()
	var pending int
	if err := database.QueryRow("SELECT COUNT(*) FROM metadata_writeback_attempts WHERE asset_id = ?", assetID).Scan(&pending); err != nil {
		t.Fatalf("count pending attempts: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending attempts = %d; want 0", pending)
	}
}

func sha256HexForTest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
