package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/levmv/polka/internal/relayout"
	"github.com/levmv/polka/internal/storage"
)

func runLibraryAuthors(ctx context.Context, dataDir string, args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printLibraryAuthorsUsage()
		if len(args) == 0 {
			return reportedErrorf("usage: polka library authors <rename|merge> [args]")
		}
		return nil
	}

	sub, rest := args[0], args[1:]
	if subcommandHelpRequested(rest) {
		printLibraryAuthorSubcommandUsage(sub)
		return nil
	}

	switch sub {
	case "rename":
		return renameAuthor(ctx, dataDir, rest, "library authors rename", "polka library authors rename [--force] <old name> <new name>")
	case "merge":
		return renameAuthor(ctx, dataDir, rest, "library authors merge", "polka library authors merge [--force] <old name> <new name>")
	default:
		printLibraryAuthorsUsage()
		return reportedErrorf("unknown library authors command: %s", sub)
	}
}

func printLibraryAuthorsUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka library authors rename [--force] <old name> <new name>
  polka library authors merge [--force] <old name> <new name>
`)
}

func printLibraryAuthorSubcommandUsage(sub string) {
	switch sub {
	case "rename":
		fmt.Fprintln(os.Stderr, "Usage: polka library authors rename [--force] <old name> <new name>")
	case "merge":
		fmt.Fprintln(os.Stderr, "Usage: polka library authors merge [--force] <old name> <new name>")
	default:
		printLibraryAuthorsUsage()
	}
}

// renameAuthor renames an author in place, or merges into an existing author of
// that name, then rebuilds the search index and relayouts the files of every
// affected work. The primary author is part of the canonical path, so this moves
// files in bulk.
func renameAuthor(parent context.Context, dataDir string, args []string, name, usage string) (retErr error) {
	fs := commandFlagSet(name, usage)
	force := fs.Bool("force", false, "override a fresh writer lease from another polka process")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		fs.Usage()
		return reportedErrorf("usage: %s", usage)
	}
	oldName, newName := fs.Args()[0], fs.Args()[1]

	database, err := openDatabase(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		return err
	}
	lease, err := acquireCLIWriterLease(parent, database, strings.ReplaceAll(name, " ", "-"), *force)
	if err != nil {
		return err
	}
	defer func() { retErr = lease.finish(retErr) }()
	ctx := lease.Context()

	res, err := relayout.RenameAuthor(ctx, database, root, oldName, newName)
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %v\n", warn)
	}

	fmt.Printf("Renamed %q → %q: %s affected, %s relocated\n",
		oldName, newName,
		formatCount(res.Affected, "work", "works"),
		formatCount(res.Moved, "file", "files"),
	)
	return nil
}
