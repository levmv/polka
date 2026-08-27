package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/levmv/polka/internal/db"
)

// runToken implements `polka token <add|list|revoke>` for managing per-device
// app-password tokens from the shell — the headless counterpart to the Settings
// UI. A token lets OPDS readers and KOReader sync authenticate without the
// account's real password, and can be revoked per device.
func runToken(dataDir string, args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printTokenUsage()
		if len(args) == 0 {
			return reportedErrorf("usage: polka token <add|list|revoke> <username> [name]")
		}
		return nil
	}

	sub, rest := args[0], args[1:]
	if subcommandHelpRequested(rest) {
		printTokenSubcommandUsage(sub)
		return nil
	}

	var run func(*db.DB, []string) error
	switch sub {
	case "add":
		run = tokenAdd
	case "list":
		run = tokenList
	case "revoke", "rm", "remove":
		run = tokenRevoke
	default:
		printTokenUsage()
		return reportedErrorf("unknown token subcommand: %s", sub)
	}

	database, err := openDatabase(dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	return run(database, rest)
}

func printTokenUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  polka token add [--base-url <url>] <username> <name>
  polka token list <username>
  polka token revoke <username> <name>
`)
}

func printTokenSubcommandUsage(sub string) {
	switch sub {
	case "add":
		fmt.Fprintln(os.Stderr, "Usage: polka token add [--base-url <url>] <username> <name>")
	case "list":
		fmt.Fprintln(os.Stderr, "Usage: polka token list <username>")
	case "revoke", "rm", "remove":
		fmt.Fprintln(os.Stderr, "Usage: polka token revoke <username> <name>")
	default:
		printTokenUsage()
	}
}

func tokenAdd(database *db.DB, args []string) error {
	flags, positional, err := splitTokenAddArgs(args)
	if err != nil {
		return err
	}
	fs := commandFlagSet("token add", "polka token add [--base-url <url>] <username> <name>")
	baseURL := fs.String("base-url", os.Getenv("POLKA_BASE_URL"), "public polka base URL, e.g. https://books.example")
	if help, err := parseCommandFlags(fs, flags); help || err != nil {
		return err
	}
	if len(positional) != 2 {
		fs.Usage()
		return reportedErrorf("usage: polka token add [--base-url <url>] <username> <name>")
	}
	user, err := resolveUser(database, positional[0])
	if err != nil {
		return err
	}

	token, err := database.CreateAppToken(user.ID, positional[1])
	if err != nil {
		if errors.Is(err, db.ErrTokenNameExists) {
			return fmt.Errorf("%q already has a token named %q", user.Username, positional[1])
		}
		return err
	}

	opdsURL, koSyncURL, err := tokenServiceURLs(*baseURL, token)
	if err != nil {
		return err
	}
	printCreatedToken(os.Stdout, positional[1], user.Username, token, opdsURL, koSyncURL)
	return nil
}

func splitTokenAddArgs(args []string) (flags, positional []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--base-url" || a == "-base-url" {
			if i+1 >= len(args) {
				return nil, nil, errors.New("missing value for --base-url")
			}
			flags = append(flags, a, args[i+1])
			i++
			continue
		}
		if strings.HasPrefix(a, "--base-url=") || strings.HasPrefix(a, "-base-url=") {
			flags = append(flags, a)
			continue
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			continue
		}
		positional = append(positional, a)
	}
	return flags, positional, nil
}

func tokenServiceURLs(baseURL, token string) (string, string, error) {
	koSyncPath := "/kosync/" + url.PathEscape(token)
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "/opds", koSyncPath, nil
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return "", "", fmt.Errorf("parse --base-url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", "", fmt.Errorf("--base-url must be an absolute http(s) URL")
	}
	u.RawQuery = ""
	u.Fragment = ""
	base := strings.TrimRight(u.String(), "/")
	return base + "/opds", base + koSyncPath, nil
}

func printCreatedToken(w io.Writer, name, username, token, opdsURL, koSyncURL string) {
	fmt.Fprintf(w, "Created token %q for %q.\n\n", name, username)
	fmt.Fprintf(w, "    %s\n\n", token)
	fmt.Fprintln(w, "This is shown once. Store it now; it cannot be retrieved later,")
	fmt.Fprintln(w, "only revoked.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "OPDS:")
	fmt.Fprintf(w, "    URL:      %s\n", opdsURL)
	fmt.Fprintf(w, "    Username: %s\n", username)
	fmt.Fprintln(w, "    Password: this token")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "KOReader sync:")
	fmt.Fprintf(w, "    Server:   %s\n", koSyncURL)
}

func tokenList(database *db.DB, args []string) error {
	if len(args) != 1 {
		printTokenSubcommandUsage("list")
		return errors.New("usage: polka token list <username>")
	}
	user, err := resolveUser(database, args[0])
	if err != nil {
		return err
	}

	tokens, err := database.ListAppTokens(user.ID)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Printf("%q has no tokens.\n", user.Username)
		return nil
	}
	for _, t := range tokens {
		used := "never"
		if t.LastUsedAt.Valid {
			used = time.Unix(t.LastUsedAt.Int64, 0).Format("2006-01-02")
		}
		fmt.Printf("%-24s created %s  last used %s\n",
			t.Name, time.Unix(t.CreatedAt, 0).Format("2006-01-02"), used)
	}
	return nil
}

func tokenRevoke(database *db.DB, args []string) error {
	if len(args) != 2 {
		printTokenSubcommandUsage("revoke")
		return errors.New("usage: polka token revoke <username> <name>")
	}
	user, err := resolveUser(database, args[0])
	if err != nil {
		return err
	}

	if err := database.RevokeAppToken(user.ID, args[1]); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%q has no token named %q", user.Username, args[1])
		}
		return err
	}
	fmt.Printf("Revoked token %q for %q.\n", args[1], user.Username)
	return nil
}

func resolveUser(database *db.DB, username string) (*db.User, error) {
	u, err := database.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("no user named %q", username)
	}
	return u, nil
}
