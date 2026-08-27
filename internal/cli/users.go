package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/levmv/polka/internal/db"
)

// runUser implements `polka user <add|list|passwd|remove>` for managing
// accounts from the shell — the headless counterpart to the web first-run setup
// and the admin UI. Passwords are read from the terminal without echo (or from
// stdin when piped, for scripting); they never go through a flag, which would
// leak them into shell history and the process list.
func runUser(dataDir string, args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printUserUsage()
		if len(args) == 0 {
			return reportedErrorf("usage: polka user <add|list|passwd|remove> [args]")
		}
		return nil
	}

	sub, rest := args[0], args[1:]
	if subcommandHelpRequested(rest) {
		printUserSubcommandUsage(sub)
		return nil
	}

	var run func(*db.DB, []string) error
	switch sub {
	case "add":
		return runUserAdd(dataDir, rest)
	case "list":
		run = userList
	case "passwd":
		run = userPasswd
	case "remove", "rm":
		run = userRemove
	default:
		printUserUsage()
		return reportedErrorf("unknown user subcommand: %s", sub)
	}

	database, err := openDatabase(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	return run(database, rest)
}

func printUserUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka user add [--admin|--member] <username>
  polka user list
  polka user passwd <username>
  polka user remove <username>
`)
}

func printUserSubcommandUsage(sub string) {
	switch sub {
	case "add":
		fmt.Fprintln(os.Stderr, "Usage: polka user add [--admin|--member] <username>")
	case "list":
		fmt.Fprintln(os.Stderr, "Usage: polka user list")
	case "passwd":
		fmt.Fprintln(os.Stderr, "Usage: polka user passwd <username>")
	case "remove", "rm":
		fmt.Fprintln(os.Stderr, "Usage: polka user remove <username>")
	default:
		printUserUsage()
	}
}

type userAddRequest struct {
	username string
	role     string
}

func runUserAdd(dataDir string, args []string) error {
	req, err := parseUserAddArgs(args)
	if err != nil {
		return err
	}
	password, err := readNewPassword()
	if err != nil {
		return err
	}

	database, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	return createUser(database, req, password)
}

func parseUserAddArgs(args []string) (userAddRequest, error) {
	// Split positionals from flags so the username may appear before or after
	// --admin/--member (stdlib flag otherwise stops parsing at the first
	// positional).
	var flags, positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}

	fs := commandFlagSet("user add", "polka user add [--admin|--member] <username>")
	admin := fs.Bool("admin", false, "create the account with the admin role")
	member := fs.Bool("member", false, "create the account with the member role")
	if help, err := parseCommandFlags(fs, flags); help || err != nil {
		return userAddRequest{}, err
	}
	if len(positional) != 1 {
		fs.Usage()
		return userAddRequest{}, reportedErrorf("usage: polka user add <username> [--admin|--member]")
	}
	if *admin && *member {
		return userAddRequest{}, errors.New("--admin and --member are mutually exclusive")
	}
	role := db.RoleReader
	if *member {
		role = db.RoleMember
	}
	if *admin {
		role = db.RoleAdmin
	}
	return userAddRequest{username: positional[0], role: role}, nil
}

func createUser(database *db.DB, req userAddRequest, password string) error {
	u, err := database.CreateUser(req.username, password, req.role)
	if err != nil {
		return err
	}
	fmt.Printf("Created %s account %q\n", u.Role, u.Username)
	return nil
}

func userList(database *db.DB, args []string) error {
	if len(args) != 0 {
		printUserSubcommandUsage("list")
		return errors.New("usage: polka user list")
	}
	users, err := database.ListUsers()
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Println("No users yet.")
		return nil
	}
	for _, u := range users {
		fmt.Printf("%-20s %-6s created %s\n", u.Username, u.Role, time.Unix(u.CreatedAt, 0).Format("2006-01-02"))
	}
	return nil
}

func userPasswd(database *db.DB, args []string) error {
	if len(args) != 1 {
		printUserSubcommandUsage("passwd")
		return errors.New("usage: polka user passwd <username>")
	}
	u, err := database.GetUserByUsername(args[0])
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("no user named %q", args[0])
	}
	password, err := readNewPassword()
	if err != nil {
		return err
	}
	if err := database.SetUserPassword(u.ID, password); err != nil {
		return err
	}
	fmt.Printf("Updated password for %q\n", u.Username)
	return nil
}

func userRemove(database *db.DB, args []string) error {
	if len(args) != 1 {
		printUserSubcommandUsage("remove")
		return errors.New("usage: polka user remove <username>")
	}
	u, err := database.GetUserByUsername(args[0])
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("no user named %q", args[0])
	}

	if err := database.DeleteUser(u.ID); err != nil {
		if errors.Is(err, db.ErrLastAdmin) {
			return errors.New("refusing to remove the only admin account")
		}
		return err
	}
	fmt.Printf("Removed user %q\n", u.Username)
	return nil
}

// readNewPassword reads a password twice (no echo on a terminal) and checks the
// two entries match. When stdin is piped it reads a single line, so a password
// can be supplied non-interactively for scripting.
func readNewPassword() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	fmt.Print("Password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Print("Confirm password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
