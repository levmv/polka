package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
)

type reportedError struct {
	cause error
}

func (err *reportedError) Error() string {
	return err.cause.Error()
}

func (err *reportedError) Unwrap() error {
	return err.cause
}

func alreadyReported(err error) error {
	if err == nil {
		return nil
	}
	return &reportedError{cause: err}
}

func reportedErrorf(format string, args ...any) error {
	return alreadyReported(fmt.Errorf(format, args...))
}

// IsReportedFailure reports whether err needs no final generic diagnostic. It
// covers argument errors and command findings that were already printed, plus
// expected process cancellation after an interrupt.
func IsReportedFailure(err error) bool {
	_, reported := errors.AsType[*reportedError](err)
	return reported || errors.Is(err, ErrIssuesFound) || errors.Is(err, errImportItemsFailed) || errors.Is(err, context.Canceled)
}

// Run is the context-free convenience entry point used by tests and embedders.
func Run(args []string) error {
	return RunContext(context.Background(), args)
}

// RunContext parses the arguments and dispatches the subcommand under the
// caller-owned process lifetime.
func RunContext(ctx context.Context, args []string) error {
	var dataDir string
	var remainingArgs []string

	for i := 0; i < len(args); i++ {
		if args[i] == "--data" || args[i] == "-data" {
			if i+1 < len(args) {
				dataDir = args[i+1]
				i++
			} else {
				return errors.New("missing value for --data")
			}
		} else if strings.HasPrefix(args[i], "--data=") || strings.HasPrefix(args[i], "-data=") {
			parts := strings.SplitN(args[i], "=", 2)
			dataDir = parts[1]
		} else {
			remainingArgs = append(remainingArgs, args[i])
		}
	}

	if dataDir == "" {
		dataDir = os.Getenv("POLKA_DATA")
	}
	if dataDir == "" {
		dataDir = "./library"
	}

	if len(remainingArgs) == 0 {
		printUsage()
		return reportedErrorf("no subcommand provided")
	}

	subcommand := remainingArgs[0]
	subArgs := remainingArgs[1:]

	if subcommand == "-h" || subcommand == "--help" {
		printUsage()
		return nil
	}
	if subcommand == "help" {
		if len(subArgs) == 0 {
			printUsage()
			return nil
		}
		return runHelp(ctx, dataDir, subArgs)
	}

	return runSubcommand(ctx, dataDir, subcommand, subArgs)
}

func runHelp(ctx context.Context, dataDir string, args []string) error {
	subcommand := args[0]
	if isHelpArg(subcommand) {
		printUsage()
		return nil
	}

	helpArgs := []string{"-h"}
	if len(args) > 1 {
		helpArgs = append([]string{}, args[1:]...)
		helpArgs = append(helpArgs, "-h")
	}
	return runSubcommand(ctx, dataDir, subcommand, helpArgs)
}

func runSubcommand(ctx context.Context, dataDir, subcommand string, subArgs []string) error {
	if helpOutputRequested(subArgs) {
		defer printGlobalFlags()
	}
	switch subcommand {
	case "import":
		return runImport(ctx, dataDir, subArgs)
	case "import-file":
		return runImportFile(ctx, dataDir, subArgs)
	case "serve":
		return runServe(ctx, dataDir, subArgs)
	case "check":
		return runCheck(dataDir, subArgs)
	case "repair":
		return runRepair(ctx, dataDir, subArgs)
	case "import-folder":
		return runImportFolder(ctx, dataDir, subArgs)
	case "convert":
		return runConvert(ctx, dataDir, subArgs)
	case "meta":
		return runMeta(dataDir, subArgs)
	case "ingest":
		return runIngest(ctx, dataDir, subArgs)
	case "storage":
		return runStorage(ctx, dataDir, subArgs)
	case "library":
		return runLibrary(ctx, dataDir, subArgs)
	case "user":
		return runUser(dataDir, subArgs)
	case "token":
		return runToken(dataDir, subArgs)
	default:
		return fmt.Errorf("unknown subcommand: %s", subcommand)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `polka — a quiet personal book library

Usage:
  polka <command> [arguments]

File tools:
  meta            Inspect or edit metadata for one or more book files
  convert         Convert one book file to another format

Library:
  serve           Start the web server
  import          Import a file or folder
  ingest          Process the configured incoming folder once
  check           Check the library for consistency
  repair          Repair missing or moved files
  storage         Manage storage policy and maintenance
  library         Manage library contents for scripts and agents
  user            Manage accounts
  token           Manage device app-password tokens
`)
	printGlobalFlags()
	fmt.Fprintln(os.Stderr, `Use "polka <command> -h" or "polka help <command>" for more information about a command.`)
}

func printGlobalFlags() {
	fmt.Fprint(os.Stderr, `
Global flags:
  --data <dir>    Application data directory (or POLKA_DATA env; default ./library)

`)
}

func commandFlagSet(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

func parseCommandFlags(fs *flag.FlagSet, args []string) (bool, error) {
	if helpRequested(args) {
		fs.Usage()
		return true, nil
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, alreadyReported(err)
	}
	return false, nil
}

func formatCount(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func helpRequested(args []string) bool {
	return len(args) > 0 && isHelpArg(args[0])
}

func subcommandHelpRequested(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if isHelpArg(args[0]) {
		return true
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func helpOutputRequested(args []string) bool {
	return slices.ContainsFunc(args, isHelpArg)
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}
