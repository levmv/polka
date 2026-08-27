package storage

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPlace(t *testing.T) {
	root := testRoot(t)
	dataDir := root.Path

	content := []byte("hello world")
	relPath := "books/A/Author/book.txt"

	// Success case
	err := Place(root, relPath, bytes.NewReader(content), func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	absPath := filepath.Join(dataDir, relPath)
	got, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch")
	}

	dir := filepath.Dir(absPath)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("tmp file leaked: %s", e.Name())
		}
	}

	// Failure case during commit
	relPath2 := "books/B/Author/book2.txt"
	err = Place(root, relPath2, bytes.NewReader(content), func() error {
		return errors.New("db commit failed")
	})
	if err == nil {
		t.Fatalf("expected error")
	}

	absPath2 := filepath.Join(dataDir, relPath2)
	if _, err := os.Stat(absPath2); !os.IsNotExist(err) {
		t.Errorf("file should not exist at final path")
	}

	dir2 := filepath.Dir(absPath2)
	entries2, _ := os.ReadDir(dir2)
	for _, e := range entries2 {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("tmp file leaked on error: %s", e.Name())
		}
	}
}

func TestStageFinalizesFromRootStaging(t *testing.T) {
	root := testRoot(t)
	relPath := "books/A/Author/Book - Author [a_stage].epub"

	staged, err := Stage(root, "[a_stage].epub", bytes.NewReader([]byte("staged book bytes")))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if filepath.Dir(staged.tmpPath) != root.StagingDir() {
		t.Fatalf("staged file dir = %q; want %q", filepath.Dir(staged.tmpPath), root.StagingDir())
	}
	if !strings.Contains(filepath.Base(staged.tmpPath), "[a_stage]") {
		t.Fatalf("staged file %q does not include asset id", staged.tmpPath)
	}

	if err := staged.Finalize(root, relPath); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(got) != "staged book bytes" {
		t.Fatalf("final file content = %q", got)
	}
	if _, err := os.Stat(staged.tmpPath); !os.IsNotExist(err) {
		t.Fatalf("staged file still exists after finalize: %v", err)
	}
}

func TestStagedFinalizeRefusesExistingDestination(t *testing.T) {
	root := testRoot(t)
	relPath := "books/A/Author/Book [a_stage].epub"

	staged, err := Stage(root, "[a_stage].epub", bytes.NewReader([]byte("staged book bytes")))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(root.Abs(relPath)), 0o755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	if err := os.WriteFile(root.Abs(relPath), []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	if err := staged.Finalize(root, relPath); err == nil {
		t.Fatalf("Finalize succeeded; want destination exists error")
	}
	if got, err := os.ReadFile(root.Abs(relPath)); err != nil || string(got) != "existing" {
		t.Fatalf("destination content = %q, %v; want unchanged", got, err)
	}
	staged.Cleanup()
}

func TestReplaceWithStagedOverwritesDestination(t *testing.T) {
	root := testRoot(t)
	relPath := "books/A/Author/Book [a_stage].epub"
	tempRel := "books/A/Author/.writeback-a_stage-rev1-test.tmp"
	if err := os.MkdirAll(filepath.Dir(root.Abs(relPath)), 0o755); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	if err := os.WriteFile(root.Abs(relPath), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(root.Abs(tempRel), []byte("new"), 0o644); err != nil {
		t.Fatalf("write staged: %v", err)
	}

	if err := ReplaceWithStaged(root, tempRel, relPath); err != nil {
		t.Fatalf("ReplaceWithStaged: %v", err)
	}
	got, err := os.ReadFile(root.Abs(relPath))
	if err != nil {
		t.Fatalf("read replaced file: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("replaced file = %q; want new", got)
	}
	if _, err := os.Stat(root.Abs(tempRel)); !os.IsNotExist(err) {
		t.Fatalf("staged temp still exists: %v", err)
	}
}

func TestWriteAdjacentTempWith(t *testing.T) {
	root := testRoot(t)
	finalRel := "books/A/Author/Book [a_write].epub"

	tempRel, err := WriteAdjacentTempWith(root, finalRel, "a_write-rev1", func(w io.Writer) error {
		_, err := w.Write([]byte("streamed bytes"))
		return err
	})
	if err != nil {
		t.Fatalf("WriteAdjacentTempWith: %v", err)
	}
	if filepath.Dir(tempRel) != filepath.Dir(finalRel) {
		t.Fatalf("tempRel = %q; want adjacent to %q", tempRel, finalRel)
	}
	got, err := os.ReadFile(root.Abs(tempRel))
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if string(got) != "streamed bytes" {
		t.Fatalf("temp content = %q", got)
	}
}

func TestWriteAdjacentTempWithCleansUpAfterWriteError(t *testing.T) {
	root := testRoot(t)
	finalRel := "books/A/Author/Book [a_write].epub"

	if _, err := WriteAdjacentTempWith(root, finalRel, "a_write-rev1", func(io.Writer) error {
		return errors.New("stream failed")
	}); err == nil {
		t.Fatalf("WriteAdjacentTempWith succeeded; want error")
	}
	entries, err := os.ReadDir(filepath.Dir(root.Abs(finalRel)))
	if err != nil {
		t.Fatalf("read destination dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("destination entries after error = %v; want none", entries)
	}
}

func TestStageCleansUpAfterCopyError(t *testing.T) {
	root := testRoot(t)

	if _, err := Stage(root, "[a_broken].epub", errorReader{}); err == nil {
		t.Fatalf("Stage succeeded; want copy error")
	}

	entries, err := os.ReadDir(root.StagingDir())
	if err != nil {
		t.Fatalf("read staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging entries after copy error = %v; want none", entries)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestMove(t *testing.T) {
	root := testRoot(t)
	dataDir := root.Path

	oldRel := "books/A/Asimov/Foundation.epub"
	newRel := "books/Б/Bulgakov/Master.epub"

	oldAbs := filepath.Join(dataDir, oldRel)
	os.MkdirAll(filepath.Dir(oldAbs), 0o755)
	os.WriteFile(oldAbs, []byte("data"), 0o644)

	// Keep an extra file in A to ensure we don't prune books/A
	extraFile := filepath.Join(dataDir, "books/A/Another.txt")
	os.WriteFile(extraFile, []byte("extra"), 0o644)

	err := Move(root, oldRel, newRel)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, newRel)); os.IsNotExist(err) {
		t.Errorf("new file does not exist")
	}

	if _, err := os.Stat(oldAbs); !os.IsNotExist(err) {
		t.Errorf("old file still exists")
	}

	if _, err := os.Stat(filepath.Join(dataDir, "books/A/Asimov")); !os.IsNotExist(err) {
		t.Errorf("Asimov dir should be pruned")
	}

	if _, err := os.Stat(filepath.Join(dataDir, "books/A")); os.IsNotExist(err) {
		t.Errorf("books/A should NOT be pruned because it has Another.txt")
	}
}

func TestMoveRefusesExistingDestination(t *testing.T) {
	root := testRoot(t)

	oldRel := "books/A/Author/old.epub"
	newRel := "books/A/Author/new.epub"
	if err := os.MkdirAll(filepath.Dir(root.Abs(oldRel)), 0o755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	if err := os.WriteFile(root.Abs(oldRel), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(root.Abs(newRel), []byte("new"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
	}

	if err := Move(root, oldRel, newRel); err == nil {
		t.Fatalf("Move succeeded; want destination exists error")
	}
	if got, err := os.ReadFile(root.Abs(newRel)); err != nil || string(got) != "new" {
		t.Fatalf("destination content = %q, %v; want unchanged", got, err)
	}
	if got, err := os.ReadFile(root.Abs(oldRel)); err != nil || string(got) != "old" {
		t.Fatalf("source content = %q, %v; want unchanged", got, err)
	}
}

func TestRemoveDoesNotRequireLayout(t *testing.T) {
	root := NewRoot(t.TempDir())

	if err := Remove(root, "books/A/Author/missing.epub"); err != nil {
		t.Fatalf("Remove missing file without layout: %v", err)
	}
	if _, err := os.Stat(root.Abs("books")); !os.IsNotExist(err) {
		t.Fatalf("Remove created books dir in missing layout; stat err=%v", err)
	}
}

func testRoot(t *testing.T) Root {
	t.Helper()
	root := NewRoot(t.TempDir())
	if err := EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	return root
}

func TestMoveFileFallsBackOnEXDEV(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.epub")
	dst := filepath.Join(dir, "nested", "dest.epub")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := os.WriteFile(src, []byte("cross-device bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	calls := 0
	err := moveFileWithRename(src, dst, func(oldPath, newPath string) error {
		calls++
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EXDEV}
	})
	if err != nil {
		t.Fatalf("moveFileWithRename: %v", err)
	}
	if calls != 1 {
		t.Fatalf("rename calls = %d; want 1", calls)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "cross-device bytes" {
		t.Fatalf("destination content = %q", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists after fallback move: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatalf("read destination dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temp file leaked after fallback move: %s", e.Name())
		}
	}
}

func TestMoveFileDoesNotFallbackOnOtherRenameError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.epub")
	dst := filepath.Join(dir, "dest.epub")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	renameErr := errors.New("rename denied")

	err := moveFileWithRename(src, dst, func(string, string) error {
		return renameErr
	})
	if !errors.Is(err, renameErr) {
		t.Fatalf("moveFileWithRename error = %v; want %v", err, renameErr)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("destination exists after non-EXDEV error: %v", err)
	}
	got, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(got) != "source" {
		t.Fatalf("source content = %q", got)
	}
}

func TestPruneEmptyParentsRemovesWholeChain(t *testing.T) {
	root := testRoot(t)
	leaf := filepath.Join("A", "Author", "empty")
	if err := os.MkdirAll(root.Abs(leaf), 0o755); err != nil {
		t.Fatalf("mkdir empty chain: %v", err)
	}
	if err := os.MkdirAll(root.StagingDir(), 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}

	PruneEmptyParents(root, leaf)

	if _, err := os.Stat(root.Abs("A")); !os.IsNotExist(err) {
		t.Fatalf("empty chain remains: %v", err)
	}
	if _, err := os.Stat(root.Path); err != nil {
		t.Fatalf("managed root was removed: %v", err)
	}
	if _, err := os.Stat(root.StagingDir()); err != nil {
		t.Fatalf("staging directory was removed: %v", err)
	}
}
