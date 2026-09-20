package importer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/levmv/polka/internal/format"
)

func HasBookExtension(name string) bool {
	return format.KnownBookExtension(format.BookExtension(name))
}

// FolderItem is one result of discovering import sources below a folder.
// Sources is non-empty for an importable file or Calibre-shaped group. Group
// remains true for a Calibre directory containing only one book file. An item
// with Err reports a local discovery failure; an item with neither Sources nor
// Err is a skipped leaf entry.
type FolderItem struct {
	Path    string
	Sources []Source
	Group   bool
	Err     error
}

// WalkFolder discovers book files and Calibre-shaped groups in lexical order.
// The root can itself be a Calibre group. Local discovery errors are passed to
// visit; root traversal errors, cancellation, and visit errors stop the walk.
func WalkFolder(ctx context.Context, rootPath string, visit func(FolderItem) error) error {
	err := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if walkErr != nil {
			if path == rootPath {
				return walkErr
			}
			if err := visit(FolderItem{Path: path, Err: walkErr}); err != nil {
				return err
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			sources, ok, err := CalibreBookSources(path)
			if err != nil {
				if visitErr := visit(FolderItem{Path: path, Err: err}); visitErr != nil {
					return visitErr
				}
				return filepath.SkipDir
			}
			if !ok {
				return nil
			}
			if err := visit(FolderItem{Path: path, Sources: sources, Group: true}); err != nil {
				return err
			}
			return filepath.SkipDir
		}

		if !entry.Type().IsRegular() || !HasBookExtension(entry.Name()) {
			return visit(FolderItem{Path: path})
		}
		return visit(FolderItem{
			Path:    path,
			Sources: []Source{{Path: path}},
		})
	})
	if err != nil {
		return fmt.Errorf("walk import folder: %w", err)
	}
	return nil
}

// CalibreBookSources recognizes a directory containing metadata.opf and at least
// one regular file with a known book extension. It returns those files as sources
// sharing the directory's sidecars.
func CalibreBookSources(dir string) ([]Source, bool, error) {
	if _, err := os.Stat(filepath.Join(dir, "metadata.opf")); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("stat metadata.opf: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, fmt.Errorf("read calibre dir: %w", err)
	}

	var sources []Source
	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() || !HasBookExtension(entry.Name()) {
			continue
		}
		sources = append(sources, Source{
			Path:       filepath.Join(dir, entry.Name()),
			SidecarDir: dir,
		})
	}
	if len(sources) == 0 {
		return nil, false, nil
	}
	return sources, true, nil
}
