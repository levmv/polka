package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/storage"
)

func runIngest(ctx context.Context, dataDir string, args []string) error {
	if helpRequested(args) {
		printIngestUsage()
		return nil
	}
	if len(args) != 0 {
		printIngestUsage()
		return reportedErrorf("usage: polka ingest")
	}

	database, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()

	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		return err
	}
	root, err = requireStorageLayout(dataDir, root)
	if err != nil {
		return err
	}

	service, err := ingest.NewServiceFromSettings(database, dataDir, root, ingest.Options{
		StableScans: 1,
	})
	if err != nil {
		return err
	}
	summary, err := service.ScanOnce(ctx, true)
	if err != nil {
		return err
	}
	status, err := service.Status()
	if err != nil {
		return err
	}

	fmt.Printf("Incoming folder: %s\n", status.Path)
	fmt.Printf("Imported:   %d\n", summary.Imported)
	fmt.Printf("Duplicates: %d\n", summary.Duplicates)
	fmt.Printf("In Trash:   %d\n", summary.Trashed)
	fmt.Printf("Restored:   %d\n", summary.Restored)
	fmt.Printf("Failed:     %d\n", summary.Failed)
	if summary.Failed > 0 {
		fmt.Println("Failed files were left in place.")
		return errors.New("some ingest files failed")
	}
	return nil
}

func printIngestUsage() {
	fmt.Fprintln(os.Stderr, "Usage: polka ingest")
}
