package cli

import (
	"context"
	"fmt"
	"os"
)

func runLibrary(ctx context.Context, dataDir string, args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printLibraryUsage()
		if len(args) == 0 {
			return reportedErrorf("usage: polka library <authors|shelves|writeback> [args]")
		}
		return nil
	}

	sub, rest := args[0], args[1:]
	switch sub {
	case "authors", "author":
		return runLibraryAuthors(ctx, dataDir, rest)
	case "shelves", "shelf":
		return runLibraryShelves(dataDir, rest)
	case "writeback":
		return runLibraryWriteback(ctx, dataDir, rest)
	default:
		printLibraryUsage()
		return reportedErrorf("unknown library command: %s", sub)
	}
}

func printLibraryUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka library authors <command> [args]
  polka library shelves <command> [args]
  polka library writeback [--all|<work-id>...] [--dry-run] [--limit N] [--force]

Commands:
  authors   Manage authors (rename, merge)
  shelves   Manage shelves (list, create, books, add-book, remove-book)
  writeback Write SQLite metadata back into managed book files
`)
}
