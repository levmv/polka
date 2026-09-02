package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestRepairReconciliation(t *testing.T) {
	dataDir := t.TempDir()

	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	epubPath := filepath.Join(dataDir, "test.epub")
	if err := os.WriteFile(epubPath, []byte("dummy epub content"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}

	if err := runImport(context.Background(), dataDir, []string{epubPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	var assetID, currentPath string
	if err := database.QueryRow("SELECT id, storage_path FROM assets LIMIT 1").Scan(&assetID, &currentPath); err != nil {
		t.Fatalf("query asset: %v", err)
	}
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}

	absCurrent := root.Abs(currentPath)
	if _, err := os.Stat(absCurrent); err != nil {
		t.Fatalf("expected file at %s: %v", absCurrent, err)
	}

	// Put the filesystem and DB into a deliberately inconsistent state:
	// a) move file to a wrong path but keep asset_id in name
	wrongRelPath := "books/wrong_folder/some_file_[" + assetID + "].epub"
	wrongAbsPath := root.Abs(wrongRelPath)
	os.MkdirAll(filepath.Dir(wrongAbsPath), 0o755)
	if err := os.Rename(absCurrent, wrongAbsPath); err != nil {
		t.Fatalf("rename to wrong path: %v", err)
	}
	// prune the old dir manually for the test
	os.RemoveAll(filepath.Dir(absCurrent))

	// b) make DB storage_path stale (something else)
	staleRelPath := "books/stale/path.epub"
	if _, err := database.Exec("UPDATE assets SET storage_path = ? WHERE id = ?", staleRelPath, assetID); err != nil {
		t.Fatalf("update storage_path: %v", err)
	}

	// runCheck's reporting of this same state is covered by the
	// check/repair pairs below; here we only assert the reconciliation.
	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair: %v", err)
	}

	// Retain only the durable fields that repair owns.
	type assetSnapshot struct {
		StoragePath, Filename, OriginalFilename string
		OriginalSHA256, CurrentSHA256           string
		OriginalSize, CurrentSize               int64
		Format                                  string
		CanRead                                 bool
	}
	loadAsset := func() assetSnapshot {
		t.Helper()
		var snapshot assetSnapshot
		if err := database.QueryRow(`
			SELECT storage_path, filename, original_filename,
			       original_sha256, current_sha256, original_size, current_size,
			       format, can_read
			FROM assets WHERE id = ?
		`, assetID).Scan(
			&snapshot.StoragePath, &snapshot.Filename, &snapshot.OriginalFilename,
			&snapshot.OriginalSHA256, &snapshot.CurrentSHA256,
			&snapshot.OriginalSize, &snapshot.CurrentSize,
			&snapshot.Format, &snapshot.CanRead,
		); err != nil {
			t.Fatalf("query repaired asset: %v", err)
		}
		return snapshot
	}
	first := loadAsset()
	newPath := first.StoragePath

	if newPath == staleRelPath {
		t.Fatalf("repair did not update storage_path in DB")
	}

	absNewPath := root.Abs(newPath)
	firstBytes, err := os.ReadFile(absNewPath)
	if err != nil {
		t.Fatalf("read file at canonical path %s: %v", absNewPath, err)
	}
	if string(firstBytes) != "dummy epub content" {
		t.Fatalf("repaired file = %q; want original bytes", firstBytes)
	}

	// The converged state is a fixed point, not merely a second crash-free run.
	out, err := captureStdout(t, func() error {
		return runRepair(context.Background(), dataDir, nil)
	})
	if err != nil {
		t.Fatalf("second runRepair: %v", err)
	}
	for _, label := range []string{
		"Relocated files:", "Hash recoveries:", "Fixed database paths:",
		"Size backfills:", "Original sizes:", "Hash backfills:",
		"Formats:", "Reader capabilities:",
	} {
		if expected := fmt.Sprintf("  %-32s 0\n", label); !strings.Contains(out, expected) {
			t.Fatalf("second repair output = %q; want %q", out, expected)
		}
	}
	if second := loadAsset(); second != first {
		t.Fatalf("second repair changed asset state: before=%+v after=%+v", first, second)
	}
	if secondBytes, err := os.ReadFile(absNewPath); err != nil {
		t.Fatalf("read canonical file after second repair: %v", err)
	} else if !bytes.Equal(secondBytes, firstBytes) {
		t.Fatalf("second repair changed managed bytes: before=%q after=%q", firstBytes, secondBytes)
	}
	if _, err := os.Stat(wrongAbsPath); !os.IsNotExist(err) {
		t.Fatalf("wrong-path source exists after second repair: %v", err)
	}
}

func TestRepairFinalizesCompletedWritebackAttempt(t *testing.T) {
	dataDir, database, root, assetID, storagePath := setupImportedRepairEPUB(t, "Old Final", "Writer One")
	defer database.Close()

	newPath := filepath.Join(dataDir, "new-final.epub")
	writeEPUB(t, newPath, "New Final", "Writer Two", "Two, Writer")
	newBytes, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read new epub: %v", err)
	}
	finalAbs := root.Abs(storagePath)
	if err := os.WriteFile(finalAbs, newBytes, 0o644); err != nil {
		t.Fatalf("replace final bytes: %v", err)
	}
	newHash, newSize, err := fileSHA256AndSize(finalAbs)
	if err != nil {
		t.Fatalf("hash replaced final: %v", err)
	}
	tempRel := storage.WritebackTempRelPath(storagePath, assetID+"-rev2")
	insertWritebackAttempt(t, database, assetID, storagePath, tempRel, newHash, newSize, "ko-new", 2)

	if err := runCheck(dataDir, nil); !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck before repair = %v; want ErrIssuesFound", err)
	}
	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair: %v", err)
	}
	assertWritebackAttemptCleared(t, database, assetID)
	assertAssetWritebackState(t, database, assetID, newHash, newSize, "ko-new", 2)
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after repair: %v", err)
	}
}

func TestRepairAppliesPendingWritebackTemp(t *testing.T) {
	dataDir, database, root, assetID, storagePath := setupImportedRepairEPUB(t, "Old Temp", "Writer One")
	defer database.Close()

	newPath := filepath.Join(dataDir, "new-temp.epub")
	writeEPUB(t, newPath, "New Temp", "Writer Two", "Two, Writer")
	newBytes, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read new epub: %v", err)
	}
	tempRel, err := storage.WriteAdjacentTemp(root, storagePath, assetID+"-rev2", newBytes)
	if err != nil {
		t.Fatalf("write adjacent temp: %v", err)
	}
	tempAbs := root.Abs(tempRel)
	newHash, newSize, err := fileSHA256AndSize(tempAbs)
	if err != nil {
		t.Fatalf("hash temp: %v", err)
	}
	insertWritebackAttempt(t, database, assetID, storagePath, tempRel, newHash, newSize, "ko-temp", 2)

	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair: %v", err)
	}
	if _, err := os.Stat(tempAbs); !os.IsNotExist(err) {
		t.Fatalf("temp after repair stat err = %v; want not exist", err)
	}
	finalHash, finalSize, err := fileSHA256AndSize(root.Abs(storagePath))
	if err != nil {
		t.Fatalf("hash final: %v", err)
	}
	if finalHash != newHash || finalSize != newSize {
		t.Fatalf("final hash/size = %s/%d; want %s/%d", finalHash, finalSize, newHash, newSize)
	}
	assertWritebackAttemptCleared(t, database, assetID)
	assertAssetWritebackState(t, database, assetID, newHash, newSize, "ko-temp", 2)
}

func TestRepairRemovesOrphanWritebackTemp(t *testing.T) {
	dataDir, database, root, _, storagePath := setupImportedRepairEPUB(t, "Old Orphan", "Writer One")
	defer database.Close()

	orphanRel := storage.WritebackTempRelPath(storagePath, "orphan")
	orphanAbs := root.Abs(orphanRel)
	if err := os.MkdirAll(filepath.Dir(orphanAbs), 0o755); err != nil {
		t.Fatalf("mkdir orphan temp dir: %v", err)
	}
	if err := os.WriteFile(orphanAbs, []byte("orphan temp"), 0o644); err != nil {
		t.Fatalf("write orphan temp: %v", err)
	}

	if err := runCheck(dataDir, nil); !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck before repair = %v; want ErrIssuesFound", err)
	}
	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair: %v", err)
	}
	if _, err := os.Stat(orphanAbs); !os.IsNotExist(err) {
		t.Fatalf("orphan temp after repair stat err = %v; want not exist", err)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after repair: %v", err)
	}
}

func setupImportedRepairEPUB(t *testing.T, title, author string) (string, *db.DB, storage.Root, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()
	srcPath := filepath.Join(dataDir, "source.epub")
	writeEPUB(t, srcPath, title, author, author)
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		database.Close()
		t.Fatalf("OpenRoot: %v", err)
	}
	var assetID, storagePath, workID string
	if err := database.QueryRow("SELECT id, storage_path, work_id FROM assets LIMIT 1").Scan(&assetID, &storagePath, &workID); err != nil {
		database.Close()
		t.Fatalf("query asset: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET metadata_rev = 2 WHERE id = ?", workID); err != nil {
		database.Close()
		t.Fatalf("update metadata_rev: %v", err)
	}
	return dataDir, database, root, assetID, storagePath
}

func insertWritebackAttempt(t *testing.T, database *db.DB, assetID, storagePath, tempRel, hash string, size int64, koHash string, rev int64) {
	t.Helper()
	if err := db.UpsertMetadataWritebackAttempt(database, db.MetadataWritebackAttempt{
		AssetID:      assetID,
		MetadataRev:  rev,
		StoragePath:  storagePath,
		TempPath:     tempRel,
		SHA256:       hash,
		Size:         size,
		KOReaderHash: koHash,
	}); err != nil {
		t.Fatalf("insert writeback attempt: %v", err)
	}
}

func assertWritebackAttemptCleared(t *testing.T, database *db.DB, assetID string) {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM metadata_writeback_attempts WHERE asset_id = ?", assetID).Scan(&count); err != nil {
		t.Fatalf("query writeback attempt count: %v", err)
	}
	if count != 0 {
		t.Fatalf("metadata_writeback_attempts count = %d; want 0", count)
	}
}

func assertAssetWritebackState(t *testing.T, database *db.DB, assetID, hash string, size int64, koHash string, rev int64) {
	t.Helper()
	var gotHash, gotKO string
	var gotSize, gotRev int64
	var writebackError string
	if err := database.QueryRow(`
		SELECT current_sha256, current_size, COALESCE(koreader_hash, ''), writeback_rev, COALESCE(writeback_error, '')
		FROM assets
		WHERE id = ?
	`, assetID).Scan(&gotHash, &gotSize, &gotKO, &gotRev, &writebackError); err != nil {
		t.Fatalf("query asset writeback state: %v", err)
	}
	if gotHash != hash || gotSize != size || gotKO != koHash || gotRev != rev || writebackError != "" {
		t.Fatalf("asset writeback state = hash:%q size:%d ko:%q rev:%d err:%q; want hash:%q size:%d ko:%q rev:%d no err", gotHash, gotSize, gotKO, gotRev, writebackError, hash, size, koHash, rev)
	}
}

func TestCheckReportsInvalidStoragePath(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcPath := filepath.Join(dataDir, "invalid-path.epub")
	if err := os.WriteFile(srcPath, []byte("invalid path epub"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	if _, err := database.Exec("UPDATE assets SET storage_path = '../outside.epub'"); err != nil {
		t.Fatalf("corrupt storage_path: %v", err)
	}

	if err := runCheck(dataDir, nil); !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck with invalid storage_path = %v; want ErrIssuesFound", err)
	}
}

func TestCheckReportsUnavailableStorage(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()
	srcPath := filepath.Join(dataDir, "unavailable.epub")
	if err := os.WriteFile(srcPath, []byte("unavailable storage epub"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	// Simulate a dropped mount: the whole books root disappears.
	if err := os.RemoveAll(root.Path); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runCheck(dataDir, nil)
	})
	if !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck with unavailable storage = %v; want ErrIssuesFound", err)
	}
	if !strings.Contains(out, "Storage unavailable:") {
		t.Fatalf("check output = %q; want storage unavailable section", out)
	}
	if strings.Contains(out, "Missing files") {
		t.Fatalf("check output = %q; storage unavailable should not degrade into missing files", out)
	}
	if _, err := os.Stat(root.Abs("books")); !os.IsNotExist(err) {
		t.Fatalf("runCheck recreated books dir despite unavailable storage; stat err=%v", err)
	}
}

func TestRepairRefusesUnavailableStorage(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()
	srcPath := filepath.Join(dataDir, "repair-unavailable.epub")
	if err := os.WriteFile(srcPath, []byte("repair unavailable epub"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	// Simulate a dropped mount: the whole books root disappears.
	if err := os.RemoveAll(root.Path); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	if err := runRepair(context.Background(), dataDir, nil); !errors.Is(err, storage.ErrLayoutMissing) {
		t.Fatalf("runRepair with unavailable storage = %v; want ErrLayoutMissing", err)
	}
	if _, err := os.Stat(root.Abs("books")); !os.IsNotExist(err) {
		t.Fatalf("runRepair recreated books dir despite unavailable storage; stat err=%v", err)
	}
}

func TestCheckReportsRootStagingFiles(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()
	srcPath := filepath.Join(dataDir, "staging-check.epub")
	if err := os.WriteFile(srcPath, []byte("staging check epub"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	stagedPath := filepath.Join(root.StagingDir(), ".tmp-deadbeef-[a_orphan].epub")
	if err := os.MkdirAll(filepath.Dir(stagedPath), 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(stagedPath, []byte("staged bytes"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runCheck(dataDir, nil)
	})
	if !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck with staged file = %v; want ErrIssuesFound", err)
	}
	if !strings.Contains(out, "Staged files (1):") || !strings.Contains(out, ".staging") {
		t.Fatalf("check output = %q; want staged files section", out)
	}
}

func TestCheckCollectsIOErrorsAndContinues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadable file test is Unix-specific")
	}
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()
	first := filepath.Join(dataDir, "io-first.epub")
	second := filepath.Join(dataDir, "io-second.epub")
	writeEPUB(t, first, "IO First", "Ada Writer", "Writer, Ada")
	writeEPUB(t, second, "IO Second", "Ada Writer", "Writer, Ada")
	if err := runImport(context.Background(), dataDir, []string{first}); err != nil {
		t.Fatalf("import first: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{second}); err != nil {
		t.Fatalf("import second: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}

	rows, err := database.Query("SELECT storage_path FROM assets ORDER BY id")
	if err != nil {
		t.Fatalf("query assets: %v", err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan asset path: %v", err)
		}
		paths = append(paths, p)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("asset paths = %v; want 2", paths)
	}
	unreadableAbs := root.Abs(paths[0])
	missingAbs := root.Abs(paths[1])
	if err := os.Chmod(unreadableAbs, 0o000); err != nil {
		t.Fatalf("chmod unreadable: %v", err)
	}
	defer os.Chmod(unreadableAbs, 0o644)
	if err := os.Remove(missingAbs); err != nil {
		t.Fatalf("remove missing target: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runCheck(dataDir, []string{"--deep"})
	})
	if !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck = %v; want ErrIssuesFound", err)
	}
	if !strings.Contains(out, "I/O errors (1):") {
		t.Fatalf("check output = %q; want I/O errors section", out)
	}
	if !strings.Contains(out, "Missing files (1):") {
		t.Fatalf("check output = %q; want missing files section too", out)
	}
}

func TestRepairRecoversCommittedStagedAsset(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcBytes := []byte("recoverable staged epub")
	srcPath := filepath.Join(dataDir, "staged.epub")
	if err := os.WriteFile(srcPath, srcBytes, 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets LIMIT 1").Scan(&storagePath); err != nil {
		t.Fatalf("query asset: %v", err)
	}
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}

	finalAbs := root.Abs(storagePath)
	stagedRel := filepath.Join(filepath.Dir(storagePath), ".tmp-deadbeef-"+filepath.Base(storagePath))
	stagedAbs := root.Abs(stagedRel)
	if err := os.WriteFile(stagedAbs, srcBytes, 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	if err := os.Remove(finalAbs); err != nil {
		t.Fatalf("remove final file: %v", err)
	}

	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair: %v", err)
	}
	if got, err := os.ReadFile(finalAbs); err != nil {
		t.Fatalf("read repaired final file: %v", err)
	} else if string(got) != string(srcBytes) {
		t.Fatalf("repaired final file = %q; want %q", got, srcBytes)
	}
	if _, err := os.Stat(stagedAbs); !os.IsNotExist(err) {
		t.Fatalf("staged file still exists after repair: %v", err)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after repair: %v", err)
	}
}

func TestCheckAndRepairCoverOriginals(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcPath := filepath.Join(dataDir, "cover-repair.epub")
	writeEPUB(t, srcPath, "Cover Repair", "Ada Writer", "Writer, Ada")
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	// Covers (and their staging) live in the app data dir, not the books root.
	dataRoot := storage.NewRoot(dataDir)

	var workID string
	if err := database.QueryRow("SELECT id FROM works LIMIT 1").Scan(&workID); err != nil {
		t.Fatalf("query work: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET cover_version = 1 WHERE id = ?", workID); err != nil {
		t.Fatalf("set cover_version: %v", err)
	}
	stagedCover := filepath.Join(dataRoot.StagingDir(), ".tmp-deadbeef-"+workID+"-cover")
	if err := os.MkdirAll(filepath.Dir(stagedCover), 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(stagedCover, []byte("cover bytes"), 0o644); err != nil {
		t.Fatalf("write staged cover: %v", err)
	}
	stagedBookWithCoverInName := filepath.Join(root.StagingDir(), ".tmp-deadbeef-Hard-cover-edition.epub")
	if err := os.MkdirAll(filepath.Dir(stagedBookWithCoverInName), 0o755); err != nil {
		t.Fatalf("mkdir books staging: %v", err)
	}
	if err := os.WriteFile(stagedBookWithCoverInName, []byte("staged book bytes"), 0o644); err != nil {
		t.Fatalf("write staged book with cover in name: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runCheck(dataDir, nil)
	})
	if !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck missing staged cover = %v; want ErrIssuesFound", err)
	}
	if !strings.Contains(out, "Missing cover originals (1):") || !strings.Contains(out, "Staged files (2):") {
		t.Fatalf("check output = %q; want missing cover and staged sections", out)
	}
	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair staged cover: %v", err)
	}
	coverAbs := dataRoot.Abs(covers.OriginalPath(workID))
	if got, err := os.ReadFile(coverAbs); err != nil {
		t.Fatalf("read restored cover: %v", err)
	} else if string(got) != "cover bytes" {
		t.Fatalf("restored cover = %q; want staged bytes", got)
	}
	if _, err := os.Stat(stagedCover); !os.IsNotExist(err) {
		t.Fatalf("staged cover still exists: %v", err)
	}
	if _, err := os.Stat(stagedBookWithCoverInName); err != nil {
		t.Fatalf("staged book with cover in name was removed: %v", err)
	}
	if err := os.Remove(stagedBookWithCoverInName); err != nil {
		t.Fatalf("remove staged book with cover in name: %v", err)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after cover restore: %v", err)
	}

	if err := os.Remove(coverAbs); err != nil {
		t.Fatalf("remove restored cover: %v", err)
	}
	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair missing cover: %v", err)
	}
	var coverVersion int
	if err := database.QueryRow("SELECT cover_version FROM works WHERE id = ?", workID).Scan(&coverVersion); err != nil {
		t.Fatalf("query cover_version: %v", err)
	}
	if coverVersion != 0 {
		t.Fatalf("cover_version = %d; want cleared", coverVersion)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after cover clear: %v", err)
	}

	if err := os.WriteFile(coverAbs, []byte("orphan cover"), 0o644); err != nil {
		t.Fatalf("write orphan cover: %v", err)
	}
	out, err = captureStdout(t, func() error {
		return runCheck(dataDir, nil)
	})
	if !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck orphan cover = %v; want ErrIssuesFound", err)
	}
	if !strings.Contains(out, "Orphan cover originals (1):") {
		t.Fatalf("check output = %q; want orphan cover section", out)
	}
	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair orphan cover: %v", err)
	}
	if _, err := os.Stat(coverAbs); !os.IsNotExist(err) {
		t.Fatalf("orphan cover still exists: %v", err)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after orphan cover repair: %v", err)
	}
}

func TestRepairReextractsMissingCoverFromPrimaryAsset(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcPath := filepath.Join(dataDir, "embedded-cover.epub")
	writeMetaEPUBWithCover(t, srcPath, metaTinyPNG)
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	var workID string
	var initialCoverVersion int
	var initialMetadataRev int64
	if err := database.QueryRow("SELECT id, cover_version, metadata_rev FROM works LIMIT 1").Scan(&workID, &initialCoverVersion, &initialMetadataRev); err != nil {
		t.Fatalf("query work cover: %v", err)
	}
	if initialCoverVersion <= 0 {
		t.Fatalf("initial cover_version = %d; want imported cover", initialCoverVersion)
	}

	dataRoot := storage.NewRoot(dataDir)
	coverAbs := dataRoot.Abs(covers.OriginalPath(workID))
	if got, err := os.ReadFile(coverAbs); err != nil {
		t.Fatalf("read imported cover: %v", err)
	} else if !bytes.Equal(got, metaTinyPNG) {
		t.Fatalf("imported cover = %d bytes; want embedded PNG", len(got))
	}
	if err := os.Remove(coverAbs); err != nil {
		t.Fatalf("remove imported cover: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runCheck(dataDir, nil)
	})
	if !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck missing cover = %v; want ErrIssuesFound", err)
	}
	if !strings.Contains(out, "Missing cover originals (1):") {
		t.Fatalf("check output = %q; want missing cover section", out)
	}

	out, err = captureStdout(t, func() error {
		return runRepair(context.Background(), dataDir, nil)
	})
	if err != nil {
		t.Fatalf("runRepair: %v", err)
	}
	if !strings.Contains(out, fmt.Sprintf("%-32s 1", "Covers extracted:")) {
		t.Fatalf("repair output = %q; want extracted cover count", out)
	}
	if got, err := os.ReadFile(coverAbs); err != nil {
		t.Fatalf("read re-extracted cover: %v", err)
	} else if !bytes.Equal(got, metaTinyPNG) {
		t.Fatalf("re-extracted cover = %d bytes; want embedded PNG", len(got))
	}
	var coverVersion int
	var metadataRev int64
	if err := database.QueryRow("SELECT cover_version, metadata_rev FROM works WHERE id = ?", workID).Scan(&coverVersion, &metadataRev); err != nil {
		t.Fatalf("query repaired cover_version: %v", err)
	}
	if coverVersion <= initialCoverVersion {
		t.Fatalf("cover_version = %d; want > %d", coverVersion, initialCoverVersion)
	}
	if metadataRev <= initialMetadataRev {
		t.Fatalf("metadata_rev = %d; want > %d", metadataRev, initialMetadataRev)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after cover re-extract: %v", err)
	}

	if _, err := database.Exec("UPDATE works SET manual_overrides = ? WHERE id = ?", bookmeta.MarshalOverrides(map[string]bool{"cover": true, "title": true}), workID); err != nil {
		t.Fatalf("set cover override: %v", err)
	}
	if err := os.Remove(coverAbs); err != nil {
		t.Fatalf("remove re-extracted cover: %v", err)
	}
	out, err = captureStdout(t, func() error {
		return runRepair(context.Background(), dataDir, nil)
	})
	if err != nil {
		t.Fatalf("runRepair fallback: %v", err)
	}
	if !strings.Contains(out, fmt.Sprintf("%-32s 1", "Covers fallback:")) {
		t.Fatalf("repair fallback output = %q; want fallback cover count", out)
	}
	if got, err := os.ReadFile(coverAbs); err != nil {
		t.Fatalf("read fallback cover: %v", err)
	} else if !bytes.Equal(got, metaTinyPNG) {
		t.Fatalf("fallback cover = %d bytes; want embedded PNG", len(got))
	}
	var rawOverrides string
	var fallbackMetadataRev int64
	if err := database.QueryRow("SELECT manual_overrides, metadata_rev FROM works WHERE id = ?", workID).Scan(&rawOverrides, &fallbackMetadataRev); err != nil {
		t.Fatalf("query fallback overrides: %v", err)
	}
	if fallbackMetadataRev <= metadataRev {
		t.Fatalf("fallback metadata_rev = %d; want > %d", fallbackMetadataRev, metadataRev)
	}
	overrides := bookmeta.ParseOverrides(rawOverrides)
	if overrides["cover"] {
		t.Fatalf("manual_overrides = %q; cover override should be cleared after fallback recovery", rawOverrides)
	}
	if !overrides["title"] {
		t.Fatalf("manual_overrides = %q; unrelated title override should remain", rawOverrides)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after fallback cover re-extract: %v", err)
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
	}()

	runErr := fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close stdout pipe reader: %v", err)
	}
	return string(out), runErr
}

func TestRepairRecoversRootStagedAsset(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcBytes := []byte("recoverable root staged epub")
	srcPath := filepath.Join(dataDir, "root-staged.epub")
	if err := os.WriteFile(srcPath, srcBytes, 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	var assetID, storagePath string
	if err := database.QueryRow("SELECT id, storage_path FROM assets LIMIT 1").Scan(&assetID, &storagePath); err != nil {
		t.Fatalf("query asset: %v", err)
	}

	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	finalAbs := root.Abs(storagePath)
	stagedAbs := filepath.Join(root.StagingDir(), ".tmp-deadbeef-["+assetID+"].epub")
	if err := os.MkdirAll(filepath.Dir(stagedAbs), 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(stagedAbs, srcBytes, 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	if err := os.Remove(finalAbs); err != nil {
		t.Fatalf("remove final file: %v", err)
	}

	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair: %v", err)
	}
	if got, err := os.ReadFile(finalAbs); err != nil {
		t.Fatalf("read repaired final file: %v", err)
	} else if string(got) != string(srcBytes) {
		t.Fatalf("repaired final file = %q; want %q", got, srcBytes)
	}
	if _, err := os.Stat(stagedAbs); !os.IsNotExist(err) {
		t.Fatalf("staged file still exists after repair: %v", err)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after root-staged repair: %v", err)
	}
}

func TestRepairRecoversTaglessOrphanByHash(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcBytes := []byte("tagless orphan epub")
	srcPath := filepath.Join(dataDir, "tagless.epub")
	if err := os.WriteFile(srcPath, srcBytes, 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}

	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets LIMIT 1").Scan(&storagePath); err != nil {
		t.Fatalf("query asset: %v", err)
	}
	finalAbs := root.Abs(storagePath)
	orphanRel := "books/orphaned-without-id.epub"
	orphanAbs := root.Abs(orphanRel)
	if err := os.MkdirAll(filepath.Dir(orphanAbs), 0o755); err != nil {
		t.Fatalf("mkdir orphan dir: %v", err)
	}
	if err := os.Rename(finalAbs, orphanAbs); err != nil {
		t.Fatalf("rename to tagless orphan: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runCheck(dataDir, nil)
	})
	if !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck tagless orphan = %v; want ErrIssuesFound", err)
	}
	if !strings.Contains(out, "Missing files (1):") || !strings.Contains(out, "Orphan files (1):") {
		t.Fatalf("check output = %q; want missing and orphan sections", out)
	}

	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair: %v", err)
	}
	if got, err := os.ReadFile(finalAbs); err != nil {
		t.Fatalf("read repaired final file: %v", err)
	} else if string(got) != string(srcBytes) {
		t.Fatalf("repaired final file = %q; want %q", got, srcBytes)
	}
	if _, err := os.Stat(orphanAbs); !os.IsNotExist(err) {
		t.Fatalf("tagless orphan still exists after repair: %v", err)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after hash recovery: %v", err)
	}
}

func TestDuplicateImportRestoresMissingManagedFile(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcPath := filepath.Join(dataDir, "duplicate-restore.epub")
	writeEPUB(t, srcPath, "Duplicate Restore", "Ada Writer", "Writer, Ada")
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("initial import: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets LIMIT 1").Scan(&storagePath); err != nil {
		t.Fatalf("query storage path: %v", err)
	}
	finalAbs := root.Abs(storagePath)
	if err := os.Remove(finalAbs); err != nil {
		t.Fatalf("remove managed file: %v", err)
	}
	if err := runCheck(dataDir, nil); !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck after remove = %v; want ErrIssuesFound", err)
	}

	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("duplicate import: %v", err)
	}
	if _, err := os.Stat(finalAbs); err != nil {
		t.Fatalf("managed file was not restored: %v", err)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after duplicate restore: %v", err)
	}
}

func TestCheckUsesEscapedDatabaseURI(t *testing.T) {
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "library ? # uri")
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcPath := filepath.Join(parent, "uri-source.epub")
	if err := os.WriteFile(srcPath, []byte("uri path epub"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck with URI-significant data dir: %v", err)
	}
}

func TestCheckAndRepairCurrentHashes(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	srcPath := filepath.Join(dataDir, "hashed.epub")
	if err := os.WriteFile(srcPath, []byte("hash me"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{srcPath}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	var assetID, storagePath, importedHash string
	var importedSize int64
	if err := database.QueryRow("SELECT id, storage_path, current_sha256, current_size FROM assets LIMIT 1").Scan(&assetID, &storagePath, &importedHash, &importedSize); err != nil {
		t.Fatalf("query asset: %v", err)
	}
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	absPath := root.Abs(storagePath)

	if _, err := database.Exec("UPDATE assets SET current_sha256 = NULL, current_size = NULL WHERE id = ?", assetID); err != nil {
		t.Fatalf("clear current hash/size: %v", err)
	}
	if err := runCheck(dataDir, nil); !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck missing current hash = %v; want ErrIssuesFound", err)
	}
	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair backfill hash: %v", err)
	}

	wantHash, wantSize, err := fileSHA256AndSize(absPath)
	if err != nil {
		t.Fatalf("hash final file: %v", err)
	}
	var backfilledHash string
	var backfilledSize int64
	if err := database.QueryRow("SELECT current_sha256, current_size FROM assets WHERE id = ?", assetID).Scan(&backfilledHash, &backfilledSize); err != nil {
		t.Fatalf("query backfilled hash/size: %v", err)
	}
	if backfilledHash != wantHash || backfilledHash != importedHash {
		t.Fatalf("backfilled hash = %q; want %q", backfilledHash, wantHash)
	}
	if backfilledSize != wantSize || backfilledSize != importedSize {
		t.Fatalf("backfilled size = %d; want %d", backfilledSize, wantSize)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after backfill: %v", err)
	}

	if err := os.WriteFile(absPath, []byte("changed on disk"), 0o644); err != nil {
		t.Fatalf("mutate final file: %v", err)
	}
	if err := runCheck(dataDir, nil); !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck hash mismatch = %v; want ErrIssuesFound", err)
	}
	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair with mismatch: %v", err)
	}

	var afterMismatchRepair string
	var sizeAfterMismatchRepair int64
	if err := database.QueryRow("SELECT current_sha256, current_size FROM assets WHERE id = ?", assetID).Scan(&afterMismatchRepair, &sizeAfterMismatchRepair); err != nil {
		t.Fatalf("query hash/size after mismatch repair: %v", err)
	}
	if afterMismatchRepair != backfilledHash {
		t.Fatalf("repair rewrote mismatched hash: got %q, want unchanged %q", afterMismatchRepair, backfilledHash)
	}
	if sizeAfterMismatchRepair != backfilledSize {
		t.Fatalf("repair rewrote mismatched size: got %d, want unchanged %d", sizeAfterMismatchRepair, backfilledSize)
	}
}

func TestCheckAndRepairReaderCapability(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	fb2Path := filepath.Join(dataDir, "reader-capability.fb2")
	fb2 := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>Reader Capability</book-title>
      <author><first-name>Ada</first-name><last-name>Lovelace</last-name></author>
    </title-info>
  </description>
  <body><section><p>Hello.</p></section></body>
</FictionBook>`)
	if err := os.WriteFile(fb2Path, fb2, 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{fb2Path}); err != nil {
		t.Fatalf("runImport: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	var assetID string
	var formatKey string
	var canRead int
	if err := database.QueryRow("SELECT id, format, can_read FROM assets LIMIT 1").Scan(&assetID, &formatKey, &canRead); err != nil {
		t.Fatalf("query asset format/can_read: %v", err)
	}
	if formatKey != "fb2" {
		t.Fatalf("imported format = %q; want fb2", formatKey)
	}
	if canRead != 1 {
		t.Fatalf("imported can_read = %d; want 1", canRead)
	}
	if _, err := database.Exec("UPDATE assets SET format = 'unknown', can_read = 0 WHERE id = ?", assetID); err != nil {
		t.Fatalf("make format/can_read stale: %v", err)
	}

	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("fast runCheck with stale can_read = %v; want no deep-only issue", err)
	}
	out, err := captureStdout(t, func() error {
		return runCheck(dataDir, []string{"--deep"})
	})
	if !errors.Is(err, ErrIssuesFound) {
		t.Fatalf("runCheck --deep stale can_read = %v; want ErrIssuesFound", err)
	}
	if !strings.Contains(out, "Format mismatches (1):") || !strings.Contains(out, "Reader capability mismatches (1):") || !strings.Contains(out, "detected true (FB2)") {
		t.Fatalf("check output = %q; want format and reader capability mismatch", out)
	}

	if err := runRepair(context.Background(), dataDir, nil); err != nil {
		t.Fatalf("runRepair: %v", err)
	}
	if err := database.QueryRow("SELECT format, can_read FROM assets WHERE id = ?", assetID).Scan(&formatKey, &canRead); err != nil {
		t.Fatalf("query repaired format/can_read: %v", err)
	}
	if formatKey != "fb2" {
		t.Fatalf("repaired format = %q; want fb2", formatKey)
	}
	if canRead != 1 {
		t.Fatalf("repaired can_read = %d; want 1", canRead)
	}
	if err := runCheck(dataDir, nil); err != nil {
		t.Fatalf("runCheck after can_read repair: %v", err)
	}
}

func TestBracketTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"managed filename", "Title [as_1].epub", []string{"[as_1]"}},
		{"no brackets", "plain.epub", nil},
		{"two tags", "A [x] B [y].fb2", []string{"[x]", "[y]"}},
		{"unclosed", "Title [as_1.epub", nil},
		{"empty tag", "x [].epub", []string{"[]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bracketTags(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bracketTags(%q) = %v; want %v", tt.in, got, tt.want)
			}
		})
	}
}
