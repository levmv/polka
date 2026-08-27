package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestOpenRootDefaultsToBooksSubdir(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root, err := OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}

	want, err := filepath.Abs(filepath.Join(dataDir, "books"))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if root.Path != want {
		t.Fatalf("root.Path = %q; want %q", root.Path, want)
	}
}

func TestSaveRootResolvesRelativeToDataDir(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	root, err := SaveRoot(database.DB, dataDir, "managed")
	if err != nil {
		t.Fatalf("SaveRoot: %v", err)
	}

	want, err := filepath.Abs(filepath.Join(dataDir, "managed"))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if root.Path != want {
		t.Fatalf("saved root.Path = %q; want %q", root.Path, want)
	}

	loaded, err := OpenRoot(database.DB, dataDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	if loaded.Path != want {
		t.Fatalf("loaded root.Path = %q; want %q", loaded.Path, want)
	}
}

func TestResolveRootRejectsDataDir(t *testing.T) {
	dataDir := t.TempDir()
	// Both the literal "." and the absolute data dir resolve to the data dir
	// itself, which must be refused: bucket dirs would sprawl beside library.db
	// and covers/ingest/tmp would keep the root permanently non-empty.
	for _, configured := range []string{".", dataDir} {
		if root, err := ResolveRoot(dataDir, configured); err == nil {
			t.Fatalf("ResolveRoot(%q) = %q, nil; want error", configured, root.Path)
		}
	}
	// A subdirectory is fine.
	if _, err := ResolveRoot(dataDir, "books"); err != nil {
		t.Fatalf("ResolveRoot(books): %v", err)
	}
}

func TestRootResolve(t *testing.T) {
	root := NewRoot(t.TempDir())

	got, err := root.Resolve("books/A/Book [asset_1].epub")
	if err != nil {
		t.Fatalf("Resolve valid path: %v", err)
	}
	want := filepath.Join(root.Path, "books", "A", "Book [asset_1].epub")
	if got != want {
		t.Fatalf("Resolve valid path = %q; want %q", got, want)
	}

	validStaging, err := root.Resolve(".staging/.tmp-deadbeef-[asset_1].epub")
	if err != nil {
		t.Fatalf("Resolve staging path: %v", err)
	}
	if filepath.Dir(validStaging) != root.StagingDir() {
		t.Fatalf("Resolve staging dir = %q; want %q", filepath.Dir(validStaging), root.StagingDir())
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "blank", path: "   "},
		{name: "absolute", path: "/tmp/book.epub"},
		{name: "parent", path: "../book.epub"},
		{name: "parent segment", path: "books/A/../book.epub"},
		{name: "dot segment", path: "books/./book.epub"},
		{name: "empty segment", path: "books//book.epub"},
		{name: "backslash", path: `books\A\book.epub`},
		{name: "colon", path: "books/A:bad/book.epub"},
		{name: "nul", path: "books/A/book\x00.epub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := root.Resolve(tt.path); err == nil {
				t.Fatalf("Resolve(%q) = %q, nil; want error", tt.path, got)
			}
		})
	}
}

func TestRequireLayout(t *testing.T) {
	t.Run("missing root", func(t *testing.T) {
		root := NewRoot(filepath.Join(t.TempDir(), "nope"))
		if err := RequireLayout(root); !errors.Is(err, ErrLayoutMissing) {
			t.Fatalf("RequireLayout error = %v; want ErrLayoutMissing", err)
		}
	})

	t.Run("existing empty dir is present", func(t *testing.T) {
		// The root is the books tree; a fresh empty library still passes present.
		root := NewRoot(t.TempDir())
		if err := RequireLayout(root); err != nil {
			t.Fatalf("RequireLayout: %v", err)
		}
	})

	t.Run("root is a file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "books")
		if err := os.WriteFile(p, []byte("not a directory"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		root := NewRoot(p)
		if err := RequireLayout(root); !errors.Is(err, ErrLayoutMissing) {
			t.Fatalf("RequireLayout error = %v; want ErrLayoutMissing", err)
		}
	})
}

func TestRequireWritableRoot(t *testing.T) {
	t.Run("fresh empty library may write", func(t *testing.T) {
		root := NewRoot(t.TempDir())
		if err := RequireWritableRoot(root, false); err != nil {
			t.Fatalf("RequireWritableRoot(empty catalog): %v", err)
		}
	})

	t.Run("empty root with existing catalog is blocked", func(t *testing.T) {
		root := NewRoot(t.TempDir())
		// A leftover .staging must not make the root look non-empty.
		if err := os.MkdirAll(root.StagingDir(), 0o755); err != nil {
			t.Fatalf("mkdir staging: %v", err)
		}
		if err := RequireWritableRoot(root, true); !errors.Is(err, ErrRootEmpty) {
			t.Fatalf("RequireWritableRoot error = %v; want ErrRootEmpty", err)
		}
	})

	t.Run("populated root with existing catalog may write", func(t *testing.T) {
		root := NewRoot(t.TempDir())
		if err := os.MkdirAll(root.Abs("A"), 0o755); err != nil {
			t.Fatalf("mkdir bucket: %v", err)
		}
		if err := RequireWritableRoot(root, true); err != nil {
			t.Fatalf("RequireWritableRoot(populated): %v", err)
		}
	})

	t.Run("missing root is blocked", func(t *testing.T) {
		root := NewRoot(filepath.Join(t.TempDir(), "nope"))
		if err := RequireWritableRoot(root, false); !errors.Is(err, ErrLayoutMissing) {
			t.Fatalf("RequireWritableRoot error = %v; want ErrLayoutMissing", err)
		}
	})
}

func TestWalkBooksSkipsRootStaging(t *testing.T) {
	root := NewRoot(t.TempDir())
	book := root.Abs(filepath.Join("A", "Author", "Book.epub"))
	staged := root.Abs(filepath.Join(".staging", "Book.epub"))
	for _, path := range []string{book, staged} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("book"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	var files []string
	if err := WalkBooks(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("WalkBooks: %v", err)
	}
	if len(files) != 1 || files[0] != book {
		t.Fatalf("walked files = %v; want [%s]", files, book)
	}
}
