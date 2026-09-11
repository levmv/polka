package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/levmv/polka/internal/version"
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

// reportedErrorf suppresses main's diagnostic; callers must first print the error
// or usage that fully explains it.
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
		if args[i] == "--" {
			remainingArgs = append(remainingArgs, args[i:]...)
			break
		}
		if args[i] == "--data" || args[i] == "-data" {
			if i+1 < len(args) {
				dataDir = args[i+1]
				i++
			} else {
				return errors.New("missing value for --data")
			}
		} else if strings.HasPrefix(args[i], "--data=") || strings.HasPrefix(args[i], "-data=") {
			_, dataDir, _ = strings.Cut(args[i], "=")
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
		fmt.Fprintln(os.Stderr, "Error: no subcommand provided")
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

	helpArgs := append(slices.Clone(args[1:]), "-h")
	return runSubcommand(ctx, dataDir, subcommand, helpArgs)
}

func runSubcommand(ctx context.Context, dataDir, subcommand string, subArgs []string) error {
	if helpOutputRequested(subArgs) {
		defer printGlobalFlags()
	}
	switch subcommand {
	case "import":
		return runImport(ctx, dataDir, subArgs)
	case "serve":
		return runServe(ctx, dataDir, subArgs)
	case "check":
		return runCheck(ctx, dataDir, subArgs)
	case "repair":
		return runRepair(ctx, dataDir, subArgs)
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
		return runUser(ctx, dataDir, subArgs)
	case "token":
		return runToken(ctx, dataDir, subArgs)
	default:
		return fmt.Errorf("unknown subcommand: %s", subcommand)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `polka %s — a quiet personal book library

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
`, version.Version)
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
	if err := fs.Parse(interspersedArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, alreadyReported(err)
	}
	return false, nil
}

// interspersedArgs lets flags follow positional arguments. The FlagSet owns
// each flag's syntax and value type; keep its values attached and leave error
// reporting to flag.Parse. An explicit -- protects all remaining arguments.
func interspersedArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := fs.Lookup(name)
		if f == nil || hasValue {
			continue
		}
		if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 == len(args) {
			// Do not let a preceding filename fill in a missing flag value.
			return flags
		}
		i++
		flags = append(flags, args[i])
	}
	return append(append(flags, "--"), positional...)
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

// Bare "help" is an alias only in the first argument; later it may be a
// positional value. The flag forms -h and --help may also follow positionals.
func subcommandHelpRequested(args []string) bool {
	if helpRequested(args) {
		return true
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func helpOutputRequested(args []string) bool {
	if end := slices.Index(args, "--"); end >= 0 {
		args = args[:end]
	}
	return slices.ContainsFunc(args, isHelpArg)
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}
