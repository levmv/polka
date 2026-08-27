package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/appsettings"
)

const (
	booksRootKey     = "books_root"
	defaultBooksRoot = "books"
)

var (
	ErrLayoutMissing = errors.New("books folder is unavailable")
	// ErrRootEmpty guards the classic dropped-mount shadow write: a books root
	// that exists but looks empty while the catalog already has books.
	ErrRootEmpty = errors.New("books folder appears empty for an existing catalog")
)

// Root resolves managed-library paths. App data (SQLite, sessions, covers,
// caches, upload temp files) lives under dataDir; the books Root holds only the
// book files, and the root *is* the books tree — bucket directories sit directly
// under it, beside a hidden .staging area.
type Root struct {
	Path string
}

func NewRoot(path string) Root {
	return Root{Path: filepath.Clean(path)}
}

func (r Root) Abs(relPath string) string {
	return filepath.Join(r.Path, relPath)
}

// Resolve asserts that a managed relative path is still confined to the root
// before filesystem IO. Polka normally minted the path itself: canonical book
// paths must already be safe when rendered, and storage primitives must return
// safe temporary paths. These checks are therefore defence-in-depth for a path
// read back from SQLite or otherwise round-tripped, not a substitute for fixing
// the code that produced an invalid path. Abs is for fixed package-owned paths
// and for values a storage primitive has just returned without a persistence
// round-trip.
func (r Root) Resolve(relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", errors.New("storage path is empty")
	}
	if strings.ContainsRune(relPath, '\x00') {
		return "", fmt.Errorf("storage path contains NUL byte: %q", relPath)
	}
	if strings.Contains(relPath, "\\") {
		return "", fmt.Errorf("storage path contains backslash: %q", relPath)
	}
	if strings.Contains(relPath, ":") {
		return "", fmt.Errorf("storage path contains colon: %q", relPath)
	}
	if filepath.IsAbs(relPath) || path.IsAbs(relPath) {
		return "", fmt.Errorf("storage path must be relative: %q", relPath)
	}

	parts := strings.SplitSeq(relPath, "/")
	for part := range parts {
		if part == "" {
			return "", fmt.Errorf("storage path contains an empty segment: %q", relPath)
		}
		if part == "." || part == ".." {
			return "", fmt.Errorf("storage path contains invalid segment %q: %q", part, relPath)
		}
	}

	rootAbs, err := filepath.Abs(r.Path)
	if err != nil {
		return "", fmt.Errorf("resolve books folder: %w", err)
	}
	return filepath.Join(rootAbs, filepath.FromSlash(relPath)), nil
}

// BooksDir returns the directory that holds book files. The root is the books
// tree, so this is the root itself; the accessor is kept for callers that want
// to name the intent.
func (r Root) BooksDir() string {
	return r.Path
}

// WalkBooks walks managed book content without entering the root-level staging
// area. The callback otherwise has filepath.Walk's exact contract, including
// absolute paths and error delivery; callers retain their own error policy.
func WalkBooks(root Root, visit filepath.WalkFunc) error {
	stagingDir := root.StagingDir()
	return filepath.Walk(root.BooksDir(), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && path == stagingDir {
			return filepath.SkipDir
		}
		return visit(path, info, err)
	})
}

// EnsureLayout creates the books root directory. It is a lifecycle operation for
// first-run bootstrap/`storage root set`, not a guard to call casually before
// every runtime mutation: doing so can silently create a fresh layout in an
// empty mountpoint when a NAS/external drive is unavailable. Bucket directories
// and the .staging area are created lazily on first write.
func EnsureLayout(root Root) error {
	if err := os.MkdirAll(root.Path, 0o755); err != nil {
		return fmt.Errorf("create books folder %s: %w", root.Path, err)
	}
	return nil
}

// RequireLayout verifies that the books root is present as a directory without
// creating anything. The root is the books tree, so it has no required inner
// marker. Emptiness is a catalog-sensitive write concern owned by
// RequireWritableRoot; a fresh empty library must still pass this check.
func RequireLayout(root Root) error {
	info, err := os.Stat(root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: root not found: %s", ErrLayoutMissing, root.Path)
		}
		return fmt.Errorf("stat books folder %s: %w", root.Path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: root is not a directory: %s", ErrLayoutMissing, root.Path)
	}
	return nil
}

// RequireWritableRoot verifies the books root is safe to write into. It must be
// present (RequireLayout), and — to block the classic dropped-mount shadow write
// — must not look empty when the catalog already has books. A brand-new catalog
// (catalogHasBooks == false) may write into a fresh empty root.
func RequireWritableRoot(root Root, catalogHasBooks bool) error {
	if err := RequireLayout(root); err != nil {
		return err
	}
	if catalogHasBooks {
		empty, err := RootLooksEmpty(root)
		if err != nil {
			return err
		}
		if empty {
			return fmt.Errorf("%w: %s (check the mount, or run `polka check` / `polka repair`)", ErrRootEmpty, root.Path)
		}
	}
	return nil
}

// RootLooksEmpty reports whether the books root exists but contains no visible
// bucket entries. Hidden names (the .staging area, other dotfiles) are ignored,
// so an otherwise-empty root reads as empty even if staging exists.
func RootLooksEmpty(root Root) (bool, error) {
	f, err := os.Open(root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("open books folder %s: %w", root.Path, err)
	}
	defer f.Close()
	for {
		names, err := f.Readdirnames(64)
		for _, name := range names {
			if !strings.HasPrefix(name, ".") {
				return false, nil
			}
		}
		if err == io.EOF {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("read books folder %s: %w", root.Path, err)
		}
	}
}

// resolveDataDir returns the absolute, cleaned data directory.
func resolveDataDir(dataDir string) (string, error) {
	if dataDir == "" {
		return "", errors.New("data directory is required")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	return filepath.Clean(abs), nil
}

// ResolveRoot resolves a configured books-folder value against dataDir. Empty
// values use the default books subdirectory. Absolute values allow keeping the
// book files on a separate disk. The books folder may not resolve to the data
// dir itself: bucket directories would sprawl beside library.db, and the data
// dir's covers/ingest/tmp entries would make the root read as permanently
// non-empty for the write guard.
func ResolveRoot(dataDir, configured string) (Root, error) {
	dataAbs, err := resolveDataDir(dataDir)
	if err != nil {
		return Root{}, err
	}
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = defaultBooksRoot
	}

	var path string
	if filepath.IsAbs(configured) {
		path = configured
	} else {
		path = filepath.Join(dataAbs, configured)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Root{}, fmt.Errorf("resolve books folder: %w", err)
	}
	if filepath.Clean(abs) == dataAbs {
		return Root{}, fmt.Errorf("books folder must not be the data directory itself: %s", dataAbs)
	}
	return NewRoot(abs), nil
}

// OpenRoot reads the configured books folder from app_settings. Missing rows
// fall back to the default books subdirectory.
func OpenRoot(q appsettings.Queryer, dataDir string) (Root, error) {
	configured, ok, err := appsettings.Get(q, booksRootKey)
	if err != nil {
		return Root{}, fmt.Errorf("load books folder: %w", err)
	}
	if !ok {
		return ResolveRoot(dataDir, defaultBooksRoot)
	}
	return ResolveRoot(dataDir, configured)
}

// RootConfigured reports whether a books-folder row has been written. Bootstrap
// uses it as the "already initialized" marker: a configured library preserves
// its settings, and serve/import only create the default books folder when no
// row exists.
func RootConfigured(q appsettings.Queryer) (bool, error) {
	_, ok, err := appsettings.Get(q, booksRootKey)
	if err != nil {
		return false, fmt.Errorf("load books folder: %w", err)
	}
	return ok, nil
}

// SaveRoot stores the configured books folder and returns its resolved form.
func SaveRoot(exec appsettings.Execer, dataDir, configured string) (Root, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = defaultBooksRoot
	}
	root, err := ResolveRoot(dataDir, configured)
	if err != nil {
		return Root{}, err
	}
	if err := appsettings.Set(exec, booksRootKey, filepath.Clean(configured)); err != nil {
		return Root{}, fmt.Errorf("save books folder: %w", err)
	}
	return root, nil
}
