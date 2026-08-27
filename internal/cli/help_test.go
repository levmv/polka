package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintedArgumentDiagnosticsAreMarkedReported(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantError  string
		wantStderr string
	}{
		{name: "missing command", wantError: "no subcommand provided", wantStderr: "Usage:\n  polka <command> [arguments]"},
		{name: "missing arguments", args: []string{"convert"}, wantError: "usage: polka convert --to <format> [--force] <source> <output>", wantStderr: "Usage: polka convert --to <format> [--force] <source> <output>"},
		{name: "invalid flag", args: []string{"convert", "--bogus"}, wantError: "flag provided but not defined: -bogus", wantStderr: "flag provided but not defined: -bogus"},
		{name: "unknown nested command", args: []string{"storage", "unknown"}, wantError: "unknown storage command: unknown", wantStderr: "Usage:\n  polka storage root <command> [args]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := runCapturingStderr(func() error { return Run(tc.args) })
			if err == nil {
				t.Fatal("Run returned nil error")
			}
			if err.Error() != tc.wantError {
				t.Fatalf("Run error = %q; want %q", err, tc.wantError)
			}
			if !IsReportedFailure(err) {
				t.Fatalf("Run error %q was not marked as already reported", err)
			}
			if count := strings.Count(stderr, tc.wantStderr); count != 1 {
				t.Fatalf("stderr contains %q %d times; want once:\n%s", tc.wantStderr, count, stderr)
			}
		})
	}
}

func TestReportedErrorPreservesCause(t *testing.T) {
	cause := errors.New("argument failure")
	err := alreadyReported(cause)
	if err.Error() != cause.Error() {
		t.Fatalf("reported error = %q; want %q", err, cause)
	}
	if !errors.Is(err, cause) {
		t.Fatal("reported error does not preserve its cause")
	}
	if !IsReportedFailure(err) {
		t.Fatal("reported error was not recognized by the process boundary")
	}
}

func TestUnprintedArgumentErrorStillNeedsDiagnostic(t *testing.T) {
	err := shelfList(nil, []string{"unexpected"})
	if err == nil {
		t.Fatal("shelfList returned nil error")
	}
	if IsReportedFailure(err) {
		t.Fatalf("unprinted error %q was marked as already reported", err)
	}
}

func TestHelpIsSelfContainedAndDoesNotRequireLibrary(t *testing.T) {
	cases := [][]string{
		{"-h"},
		{"--help"},
		{"help"},
		{"help", "serve"},
		{"help", "user", "add"},
		{"help", "storage", "template"},
		{"help", "library", "shelves", "create"},
		{"help", "library", "authors", "rename"},
		{"serve", "-h"},
		{"serve", "help"},
		{"import", "-h"},
		{"import", "help"},
		{"import-file", "-h"},
		{"import-file", "help"},
		{"import-folder", "-h"},
		{"convert", "-h"},
		{"meta", "-h"},
		{"meta", "set", "-h"},
		{"ingest", "-h"},
		{"check", "-h"},
		{"repair", "-h"},
		{"storage", "-h"},
		{"storage", "root", "-h"},
		{"storage", "root", "set", "-h"},
		{"storage", "template", "-h"},
		{"storage", "template", "preview", "-h"},
		{"storage", "template", "apply", "help"},
		{"library", "-h"},
		{"library", "authors", "-h"},
		{"library", "authors", "rename", "-h"},
		{"library", "authors", "merge", "help"},
		{"library", "shelves", "-h"},
		{"library", "shelves", "create", "-h"},
		{"library", "shelves", "books", "--help"},
		{"library", "shelves", "add-book", "help"},
		{"library", "shelves", "remove-book", "-h"},
		{"library", "shelves", "remove", "help"},
		{"library", "writeback", "-h"},
		{"user", "-h"},
		{"user", "add", "-h"},
		{"user", "list", "--help"},
		{"user", "passwd", "help"},
		{"user", "remove", "-h"},
		{"token", "-h"},
		{"token", "add", "-h"},
		{"token", "list", "--help"},
		{"token", "revoke", "help"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "missing-library")
			fullArgs := append([]string{"--data", dataDir}, args...)
			stderr, err := runCapturingStderr(func() error { return Run(fullArgs) })
			if err != nil {
				t.Fatalf("Run(%q) returned error: %v", strings.Join(fullArgs, " "), err)
			}
			if !strings.Contains(stderr, "Global flags:\n  --data <dir>") {
				t.Fatalf("Run(%q) help omitted global --data flag:\n%s", strings.Join(fullArgs, " "), stderr)
			}
			if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
				t.Fatalf("help path touched data dir %s: %v", dataDir, err)
			}
		})
	}
}

func runCapturingStderr(fn func() error) (string, error) {
	oldStdout, oldStderr := os.Stdout, os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return "", err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return "", err
	}

	stdoutDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdoutR)
		stdoutR.Close()
		close(stdoutDone)
	}()
	stderrDone := make(chan string, 1)
	go func() {
		var output strings.Builder
		_, _ = io.Copy(&output, stderrR)
		stderrR.Close()
		stderrDone <- output.String()
	}()

	os.Stdout, os.Stderr = stdoutW, stderrW
	runErr := fn()
	stdoutW.Close()
	stderrW.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	<-stdoutDone
	stderr := <-stderrDone
	return stderr, runErr
}
