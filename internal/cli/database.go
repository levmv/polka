package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/levmv/polka/internal/bootstrap"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/fsprofile"
	"github.com/levmv/polka/internal/storage"
)

func openDatabase(dataDir string) (*db.DB, error) {
	return bootstrap.OpenExisting(dataDir)
}

func openDatabaseReadOnly(dataDir string) (*db.DB, error) {
	return bootstrap.OpenExistingReadOnly(dataDir)
}

func ensureLibraryInitialized(dataDir string) (*db.DB, error) {
	return bootstrap.EnsureLibrary(dataDir)
}

func ensureLibraryWithoutBooksRoot(dataDir string) (*db.DB, error) {
	return bootstrap.EnsureLibraryWithoutBooksRoot(dataDir)
}

func noteStorageFilesystem(root storage.Root) {
	info := fsprofile.Detect(root.Path)
	if info.IsNetwork() {
		fmt.Fprintf(os.Stderr, "Note: the books folder is on network filesystem %q: %s\n", info.TypeOrUnknown(), root.Path)
	}
}

func requireStorageLayout(dataDir string, root storage.Root) (storage.Root, error) {
	if err := storage.RequireLayout(root); err == nil {
		return root, nil
	} else if errors.Is(err, storage.ErrLayoutMissing) {
		// A missing books folder is far more often a dropped mount than a truly
		// new library, so lead with checking the path. Brand-new libraries are
		// created by first-run entry points such as `serve`, `import`, or
		// `storage root set`.
		return storage.Root{}, fmt.Errorf("books folder not found at %s; check that the drive or mount is available. For a brand-new library, run `polka serve --data %s` or `polka import <path> --data %s` first: %w", root.Path, dataDir, dataDir, err)
	} else {
		return storage.Root{}, err
	}
}
