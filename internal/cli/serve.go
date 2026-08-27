package cli

import (
	"context"
	"os"

	"github.com/levmv/polka/internal/web"
)

func runServe(ctx context.Context, dataDir string, args []string) error {
	fs := commandFlagSet("serve", "polka serve [--addr <addr>] [--admin-user <name> --admin-password <password>]")
	addr := fs.String("addr", "127.0.0.1:8080", "address to bind to")
	adminUser := fs.String("admin-user", "", "bootstrap an initial admin with this username (or POLKA_ADMIN_USER)")
	adminPassword := fs.String("admin-password", "", "bootstrap password for --admin-user (or POLKA_ADMIN_PASSWORD)")

	if help, err := parseCommandFlags(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return reportedErrorf("usage: polka serve [--addr <addr>] [--admin-user <name> --admin-password <password>]")
	}

	au := *adminUser
	if au == "" {
		au = os.Getenv("POLKA_ADMIN_USER")
	}
	ap := *adminPassword
	if ap == "" {
		ap = os.Getenv("POLKA_ADMIN_PASSWORD")
	}

	// Auth is mandatory. The bootstrap creds (if given) seed the first account;
	// otherwise the first-run setup page does, so serve always starts.
	return web.Serve(ctx, web.Config{
		DataDir:       dataDir,
		Addr:          *addr,
		AdminUser:     au,
		AdminPassword: ap,
	})
}
