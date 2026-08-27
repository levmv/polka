package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/levmv/polka/internal/db"
)

func runLibraryShelves(dataDir string, args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printShelfUsage()
		if len(args) == 0 {
			return reportedErrorf("usage: polka library shelves <list|create|remove|books|add-book|remove-book> [args]")
		}
		return nil
	}

	sub, rest := args[0], args[1:]
	if subcommandHelpRequested(rest) {
		printShelfSubcommandUsage(sub)
		return nil
	}

	var run func(*db.DB, []string) error
	switch sub {
	case "list":
		run = shelfList
	case "create":
		run = shelfCreate
	case "remove", "rm":
		run = shelfRemove
	case "books":
		run = shelfBooks
	case "add-book":
		run = shelfAddBook
	case "remove-book", "rm-book":
		run = shelfRemoveBook
	default:
		printShelfUsage()
		return reportedErrorf("unknown shelf subcommand: %s", sub)
	}

	database, err := openDatabase(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	return run(database, rest)
}

func printShelfUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka library shelves list
  polka library shelves create [--query <search>] <name>
  polka library shelves remove <shelf-id>
  polka library shelves books [--limit N] <shelf-id>
  polka library shelves add-book <shelf-id> <work-id>
  polka library shelves remove-book <shelf-id> <work-id>
`)
}

func printShelfSubcommandUsage(sub string) {
	switch sub {
	case "list":
		fmt.Fprintln(os.Stderr, "Usage: polka library shelves list")
	case "create":
		fmt.Fprintln(os.Stderr, "Usage: polka library shelves create [--query <search>] <name>")
	case "remove", "rm":
		fmt.Fprintln(os.Stderr, "Usage: polka library shelves remove <shelf-id>")
	case "books":
		fmt.Fprintln(os.Stderr, "Usage: polka library shelves books [--limit N] <shelf-id>")
	case "add-book":
		fmt.Fprintln(os.Stderr, "Usage: polka library shelves add-book <shelf-id> <work-id>")
	case "remove-book", "rm-book":
		fmt.Fprintln(os.Stderr, "Usage: polka library shelves remove-book <shelf-id> <work-id>")
	default:
		printShelfUsage()
	}
}

func shelfList(database *db.DB, args []string) error {
	if len(args) != 0 {
		printShelfSubcommandUsage("list")
		return errors.New("usage: polka library shelves list")
	}
	shelves, err := database.ListShelves("")
	if err != nil {
		return err
	}
	if len(shelves) == 0 {
		fmt.Println("No shelves yet.")
		return nil
	}
	for _, s := range shelves {
		if s.Kind == db.ShelfQuery {
			fmt.Printf("%-18s %-6s %s  q=%q\n", s.ID, s.Kind, s.Name, s.Query)
		} else {
			fmt.Printf("%-18s %-6s %s\n", s.ID, s.Kind, s.Name)
		}
	}
	return nil
}

func shelfCreate(database *db.DB, args []string) error {
	fs := commandFlagSet("library shelves create", "polka library shelves create [--query <search>] <name>")
	query := fs.String("query", "", "create a query shelf from this search string")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if name == "" {
		fs.Usage()
		return reportedErrorf("usage: polka library shelves create [--query <search>] <name>")
	}

	kind := db.ShelfManual
	if strings.TrimSpace(*query) != "" {
		kind = db.ShelfQuery
	}
	ownerID, err := defaultShelfOwner(database)
	if err != nil {
		return err
	}
	shelf, err := database.CreateShelf(ownerID, db.ShelfShared, name, kind, *query)
	if err != nil {
		return err
	}
	fmt.Printf("Created %s shelf %q (%s)\n", shelf.Kind, shelf.Name, shelf.ID)
	return nil
}

func defaultShelfOwner(database *db.DB) (string, error) {
	users, err := database.ListUsers()
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Role == db.RoleAdmin {
			return u.ID, nil
		}
	}
	for _, u := range users {
		if u.Role == db.RoleMember {
			return u.ID, nil
		}
	}
	if len(users) > 0 {
		return users[0].ID, nil
	}
	return "", errors.New("cannot create a shelf before creating a user")
}

func shelfRemove(database *db.DB, args []string) error {
	if len(args) != 1 {
		printShelfSubcommandUsage("remove")
		return errors.New("usage: polka library shelves remove <shelf-id>")
	}
	if err := database.DeleteShelf(args[0], ""); err != nil {
		return err
	}
	fmt.Printf("Removed shelf %s\n", args[0])
	return nil
}

func shelfBooks(database *db.DB, args []string) error {
	fs := commandFlagSet("library shelves books", "polka library shelves books [--limit N] <shelf-id>")
	limit := fs.Int("limit", 50, "maximum books to print")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		fs.Usage()
		return reportedErrorf("usage: polka library shelves books [--limit N] <shelf-id>")
	}

	shelf, err := database.GetShelf(fs.Args()[0], "")
	if err != nil {
		return err
	}
	var books []db.BookSummaryRow
	if shelf.Kind == db.ShelfQuery {
		// status: is viewer-relative. This command has no signed-in viewer, so
		// evaluate a saved query shelf for the account whose shelf it is.
		books, err = db.ListBooks(database, db.FullVisibilityScope(), shelf.OwnerID, shelf.Query, db.SortRelevance, *limit, 0)
	} else {
		books, err = db.ListBooksInManualShelf(database, db.FullVisibilityScope(), shelf.ID, db.SortAdded, *limit, 0)
	}
	if err != nil {
		return err
	}
	if len(books) == 0 {
		fmt.Println("No books on this shelf.")
		return nil
	}
	workIDs := make([]string, 0, len(books))
	for _, b := range books {
		workIDs = append(workIDs, b.ID)
	}
	authorsByWork, err := db.AuthorsByWorkIDs(database, workIDs)
	if err != nil {
		return err
	}
	for _, b := range books {
		authors := authorsByWork[b.ID]
		names := make([]string, 0, len(authors))
		for _, author := range authors {
			names = append(names, author.Name)
		}
		fmt.Printf("%-18s %s - %s\n", b.ID, b.Title, strings.Join(names, " & "))
	}
	return nil
}

func shelfAddBook(database *db.DB, args []string) error {
	if len(args) != 2 {
		printShelfSubcommandUsage("add-book")
		return errors.New("usage: polka library shelves add-book <shelf-id> <work-id>")
	}
	if err := database.AddBookToShelf(args[0], "", args[1]); err != nil {
		return err
	}
	fmt.Printf("Added %s to shelf %s\n", args[1], args[0])
	return nil
}

func shelfRemoveBook(database *db.DB, args []string) error {
	if len(args) != 2 {
		printShelfSubcommandUsage("remove-book")
		return errors.New("usage: polka library shelves remove-book <shelf-id> <work-id>")
	}
	if err := database.RemoveBookFromShelf(args[0], "", args[1]); err != nil {
		return err
	}
	fmt.Printf("Removed %s from shelf %s\n", args[1], args[0])
	return nil
}
