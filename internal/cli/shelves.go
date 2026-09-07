package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/db"
)

func runLibraryShelves(ctx context.Context, dataDir string, args []string) error {
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

	var run func(context.Context, *db.DB, []string) error
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
	return run(ctx, database, rest)
}

func printShelfUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka library shelves list
  polka library shelves create [--query <search>] <name>
  polka library shelves remove <shelf-id>
  polka library shelves books [--limit N] <shelf-id>
  polka library shelves add-book <shelf-id> <book-id>
  polka library shelves remove-book <shelf-id> <book-id>
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
		fmt.Fprintln(os.Stderr, "Usage: polka library shelves add-book <shelf-id> <book-id>")
	case "remove-book", "rm-book":
		fmt.Fprintln(os.Stderr, "Usage: polka library shelves remove-book <shelf-id> <book-id>")
	default:
		printShelfUsage()
	}
}

func shelfList(ctx context.Context, database *db.DB, args []string) error {
	if len(args) != 0 {
		printShelfSubcommandUsage("list")
		return errors.New("usage: polka library shelves list")
	}
	shelves, err := db.ListShelves(database.Read(ctx), 0)
	if err != nil {
		return err
	}
	if len(shelves) == 0 {
		fmt.Println("No shelves yet.")
		return nil
	}
	for _, s := range shelves {
		if s.Kind == db.ShelfQuery {
			fmt.Printf("%-8d %-6s %s  q=%q\n", s.ID, s.Kind, s.Name, s.Query)
		} else {
			fmt.Printf("%-8d %-6s %s\n", s.ID, s.Kind, s.Name)
		}
	}
	return nil
}

func shelfCreate(ctx context.Context, database *db.DB, args []string) error {
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
	ownerID, err := defaultShelfOwner(database.Read(ctx))
	if err != nil {
		return err
	}
	shelf, err := database.CreateShelf(ctx, ownerID, db.ShelfShared, name, kind, *query)
	if err != nil {
		return err
	}
	fmt.Printf("Created %s shelf %q (%d)\n", shelf.Kind, shelf.Name, shelf.ID)
	return nil
}

func defaultShelfOwner(queryer db.Queryer) (int64, error) {
	users, err := db.ListUsers(queryer)
	if err != nil {
		return 0, err
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
	return 0, errors.New("cannot create a shelf before creating a user")
}

func shelfRemove(ctx context.Context, database *db.DB, args []string) error {
	if len(args) != 1 {
		printShelfSubcommandUsage("remove")
		return errors.New("usage: polka library shelves remove <shelf-id>")
	}
	shelfID, err := parseID(args[0], "shelf")
	if err != nil {
		return err
	}
	if err := database.DeleteShelf(ctx, shelfID, 0); err != nil {
		return err
	}
	fmt.Printf("Removed shelf %s\n", args[0])
	return nil
}

func shelfBooks(ctx context.Context, database *db.DB, args []string) error {
	fs := commandFlagSet("library shelves books", "polka library shelves books [--limit N] <shelf-id>")
	limit := fs.Int("limit", 50, "maximum books to print")
	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		fs.Usage()
		return reportedErrorf("usage: polka library shelves books [--limit N] <shelf-id>")
	}

	shelfID, err := parseID(fs.Args()[0], "shelf")
	if err != nil {
		return err
	}
	shelf, err := db.GetShelf(database.Read(ctx), shelfID, 0)
	if err != nil {
		return err
	}
	var books []db.BookSummaryRow
	if shelf.Kind == db.ShelfQuery {
		// status: is viewer-relative. This command has no signed-in viewer, so
		// evaluate a saved query shelf for the account whose shelf it is.
		books, err = db.ListBooks(database.Read(ctx), db.FullVisibilityScope(), shelf.OwnerID, shelf.Query, db.SortRelevance, *limit, 0)
	} else {
		books, err = db.ListBooksInManualShelf(database.Read(ctx), db.FullVisibilityScope(), shelf.ID, db.SortAdded, *limit, 0)
	}
	if err != nil {
		return err
	}
	if len(books) == 0 {
		fmt.Println("No books on this shelf.")
		return nil
	}
	bookIDs := make([]int64, 0, len(books))
	for _, b := range books {
		bookIDs = append(bookIDs, b.ID)
	}
	authorsByBook, err := db.AuthorsByBookIDs(database.Read(ctx), bookIDs)
	if err != nil {
		return err
	}
	for _, b := range books {
		authors := authorsByBook[b.ID]
		names := make([]string, 0, len(authors))
		for _, author := range authors {
			names = append(names, author.Name)
		}
		fmt.Printf("%-18d %s - %s\n", b.ID, b.Title, strings.Join(names, " & "))
	}
	return nil
}

func shelfAddBook(ctx context.Context, database *db.DB, args []string) error {
	if len(args) != 2 {
		printShelfSubcommandUsage("add-book")
		return errors.New("usage: polka library shelves add-book <shelf-id> <book-id>")
	}
	shelfID, err := parseID(args[0], "shelf")
	if err != nil {
		return err
	}
	bookID, err := parseID(args[1], "book")
	if err != nil {
		return err
	}
	if err := database.AddBookToShelf(ctx, shelfID, 0, bookID); err != nil {
		return err
	}
	fmt.Printf("Added %s to shelf %s\n", args[1], args[0])
	return nil
}

func shelfRemoveBook(ctx context.Context, database *db.DB, args []string) error {
	if len(args) != 2 {
		printShelfSubcommandUsage("remove-book")
		return errors.New("usage: polka library shelves remove-book <shelf-id> <book-id>")
	}
	shelfID, err := parseID(args[0], "shelf")
	if err != nil {
		return err
	}
	bookID, err := parseID(args[1], "book")
	if err != nil {
		return err
	}
	if err := database.RemoveBookFromShelf(ctx, shelfID, 0, bookID); err != nil {
		return err
	}
	fmt.Printf("Removed %s from shelf %s\n", args[1], args[0])
	return nil
}

func parseID(raw, entity string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s ID %q", entity, raw)
	}
	return id, nil
}
