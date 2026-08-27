// Package storage owns low-level filesystem mutations inside a polka managed
// storage root.
//
// It also owns the canonical relative path policy for managed book assets.
// Callers still commit database state at the right boundary and pass relative
// paths here for atomic-ish file placement or moves, preserving the
// SQLite-as-truth invariant.
package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

type StagedFile struct {
	tmpPath string
}

func (r Root) StagingDir() string {
	return r.Abs(".staging")
}

func StagingRelPath(label string) string {
	return path.Join(".staging", ".relayout-"+randHex(8)+"-"+filepath.Base(label))
}

func WritebackTempRelPath(finalRelPath, label string) string {
	dir := path.Dir(finalRelPath)
	base := ".writeback-" + safeTempLabel(label) + "-" + randHex(8) + ".tmp"
	if dir == "." {
		return base
	}
	return path.Join(dir, base)
}

// IsWritebackTempFileName recognizes the adjacent-temp naming family used by
// check and repair to distinguish orphan candidates from managed content.
func IsWritebackTempFileName(name string) bool {
	return strings.HasPrefix(name, ".writeback-") && strings.HasSuffix(name, ".tmp")
}

func WriteAdjacentTemp(root Root, finalRelPath, label string, data []byte) (string, error) {
	return WriteAdjacentTempWith(root, finalRelPath, label, func(w io.Writer) error {
		if n, err := w.Write(data); err != nil {
			return err
		} else if n != len(data) {
			return io.ErrShortWrite
		}
		return nil
	})
}

// WriteAdjacentTempWith writes a complete, fsynced temp beside finalRelPath and
// returns ownership of it to the caller. Adjacency lets ReplaceWithStaged use an
// atomic same-directory rename; the recognizable name lets recovery find an
// orphan if the caller's durable workflow is interrupted. The caller owns its
// DB-before-replace ordering and removal when it abandons a successful temp.
func WriteAdjacentTempWith(root Root, finalRelPath, label string, write func(io.Writer) error) (string, error) {
	relPath := WritebackTempRelPath(finalRelPath, label)
	fullPath, err := root.Resolve(relPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir adjacent temp: %w", err)
	}
	f, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create adjacent temp: %w", err)
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(fullPath)
		}
	}()
	if err := write(f); err != nil {
		return "", fmt.Errorf("write adjacent temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("sync adjacent temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close adjacent temp: %w", err)
	}
	cleanup = false
	return relPath, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func safeTempLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "asset"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Stage copies one source into the root-level staging area before the caller
// opens its database transaction. Label should include stable identity such as
// asset_id, so repair can recover a committed DB row if final rename fails.
func Stage(root Root, label string, src io.Reader) (StagedFile, error) {
	if label == "" {
		return StagedFile{}, fmt.Errorf("empty staging label")
	}
	stagingDir := root.StagingDir()
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return StagedFile{}, fmt.Errorf("mkdir staging: %w", err)
	}

	tmpPath := filepath.Join(stagingDir, ".tmp-"+randHex(8)+"-"+filepath.Base(label))
	f, err := os.Create(tmpPath)
	if err != nil {
		return StagedFile{}, fmt.Errorf("create staged file: %w", err)
	}
	staged := StagedFile{tmpPath: tmpPath}
	closed := false
	cleanup := true
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if cleanup {
			staged.Cleanup()
		}
	}()
	if _, err := io.Copy(f, src); err != nil {
		return StagedFile{}, fmt.Errorf("copy staged file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return StagedFile{}, fmt.Errorf("sync staged file: %w", err)
	}
	if err := f.Close(); err != nil {
		closed = true
		return StagedFile{}, fmt.Errorf("close staged file: %w", err)
	}
	closed = true

	cleanup = false
	return staged, nil
}

func (s StagedFile) Finalize(root Root, relPath string) error {
	dstPath, err := root.Resolve(relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("destination exists: %s", relPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat destination: %w", err)
	}
	if err := moveFile(s.tmpPath, dstPath); err != nil {
		return fmt.Errorf("move staged file: %w", err)
	}
	return nil
}

func (s StagedFile) Cleanup() {
	if s.tmpPath != "" {
		_ = os.Remove(s.tmpPath)
	}
}

// Place stages to a temp file in the destination directory, flush/fsyncs, calls
// the optional commitDB function, and then moves the temp file to relPath. The
// normal path is an atomic os.Rename; a cross-device rename falls back to copying
// into a destination temp file before the final rename. The temp file name
// includes the final base name, including asset_id for book assets, so a crash
// after DB commit but before final rename is recoverable by `polka repair`. No
// partial/half-written file ever appears at the final path. Temporary sources
// are removed in normal operation; after a successful cross-device fallback, a
// failed best-effort source cleanup may leave a complete orphan for check/repair.
// Known accepted gap: the file is fsynced but the parent directory is not, so a
// power loss right after rename can lose the directory entry — fold parent-dir
// sync into a future durability hardening pass rather than fixing it piecemeal
// here.
func Place(root Root, relPath string, src io.Reader, commitDB func() error) error {
	dstPath, err := root.Resolve(relPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dstPath)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	tmpPath := stagedPath(dir, relPath)
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}

	committed := false
	defer func() {
		f.Close()
		if !committed {
			os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(f, src); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync staged file: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close staged file: %w", err)
	}

	if commitDB != nil {
		if err := commitDB(); err != nil {
			return fmt.Errorf("commit db: %w", err)
		}
		committed = true
	}

	if err := moveFile(tmpPath, dstPath); err != nil {
		return fmt.Errorf("place staged file: %w", err)
	}

	return nil
}

func stagedPath(dir, relPath string) string {
	return filepath.Join(dir, ".tmp-"+randHex(8)+"-"+filepath.Base(relPath))
}

func moveFile(src, dst string) error {
	return moveFileWithRename(src, dst, os.Rename)
}

// ReplaceWithStaged atomically moves a complete staged file over an existing
// managed file. Write-back uses this after recording a pending attempt, so a
// crash during replacement can be reconciled by repair.
func ReplaceWithStaged(root Root, stagedRelPath, relPath string) error {
	stagedPath, err := root.Resolve(stagedRelPath)
	if err != nil {
		return err
	}
	dstPath, err := root.Resolve(relPath)
	if err != nil {
		return err
	}
	if filepath.Dir(stagedPath) != filepath.Dir(dstPath) {
		return fmt.Errorf("staged file must be in destination directory: %s", stagedRelPath)
	}
	if err := os.Rename(stagedPath, dstPath); err != nil {
		return fmt.Errorf("replace staged file: %w", err)
	}
	return nil
}

// moveFileWithRename moves within the managed root, falling back to copy when
// the kernel refuses the rename as cross-device.
//
// The fallback is not defensive padding: every managed move stays inside one
// books root, but "one root" does not imply one filesystem. Union mounts —
// mergerfs, unionfs, an Unraid user share — present several disks as a single
// tree and return EXDEV for a rename that would cross branches, which is exactly
// the storage layout a home NAS running polka tends to have. Without this,
// import and relayout would fail on those setups for no reason the user could
// act on. Do not remove it on the grounds that the paths share a root.
func moveFileWithRename(src, dst string, rename func(string, string) error) error {
	if err := rename(src, dst); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return err
		}
		if err := copyFileToDestination(src, dst); err != nil {
			return err
		}
		// Destination is complete. Returning an error on source cleanup failure can
		// strand callers on an already-existing destination, so leave the stale
		// source as an orphan for check/repair instead.
		_ = os.Remove(src)
	}
	return nil
}

func copyFileToDestination(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("source is a directory: %s", src)
	}

	tmpPath := stagedPath(filepath.Dir(dst), dst)
	out, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create destination temp: %w", err)
	}
	closed := false
	cleanup := true
	defer func() {
		if !closed {
			_ = out.Close()
		}
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync destination temp: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination temp: %w", err)
	}
	closed = true

	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("rename destination temp: %w", err)
	}
	cleanup = false
	return nil
}

// Move creates parent dirs of newRel, moves oldRel to newRel, then prunes
// now-empty parent directories (author dir, bucket dir) but never the root itself.
// Same-device moves use atomic os.Rename; cross-device moves use the compact
// copy-to-destination-temp fallback in moveFile.
func Move(root Root, oldRel, newRel string) error {
	if oldRel == newRel {
		return nil
	}

	oldAbs, err := root.Resolve(oldRel)
	if err != nil {
		return err
	}
	newAbs, err := root.Resolve(newRel)
	if err != nil {
		return err
	}
	newDir := filepath.Dir(newAbs)

	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	if _, err := os.Stat(newAbs); err == nil {
		return fmt.Errorf("destination exists: %s", newRel)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat destination: %w", err)
	}

	if err := moveFile(oldAbs, newAbs); err != nil {
		return err
	}

	PruneEmptyParents(root, filepath.Dir(oldRel))

	return nil
}

// Remove unlinks the file at relPath and prunes any now-empty author/bucket
// directories left behind, mirroring Move's cleanup. It is idempotent: an
// already-missing file is not an error, so a purge that re-runs after a partial
// failure still succeeds. Callers must have removed the DB rows first (DB is the
// source of truth); this only reconciles the disk.
func Remove(root Root, relPath string) error {
	abs, err := root.Resolve(relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	PruneEmptyParents(root, filepath.Dir(relPath))
	return nil
}

// PruneEmptyParents removes relDir and then its ancestors while each is empty,
// stopping before the managed root. Cleanup is best-effort: the file mutation
// that made a directory empty has already succeeded, and a concurrent entry or
// cleanup error can safely remain for check/repair to report later.
func PruneEmptyParents(root Root, relDir string) {
	relDir = filepath.Clean(relDir)
	for {
		// The root is the books tree itself, so stop before removing it. Empty
		// bucket/author directories and .staging may be pruned; writers recreate
		// their directories lazily.
		if relDir == "." || relDir == "/" || relDir == "" {
			break
		}

		absDir := root.Abs(relDir)
		f, err := os.Open(absDir)
		if err != nil {
			break
		}
		_, err = f.Readdirnames(1)
		f.Close()

		if err == io.EOF {
			if err := os.Remove(absDir); err != nil {
				break
			}
			relDir = filepath.Dir(relDir)
		} else {
			break
		}
	}
}
