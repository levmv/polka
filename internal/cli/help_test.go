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
		{name: "invalid flag", args: []string{"convert", "--bogus"}, wantError: "flag provided but not defined: -bogus", wantStderr: "flag provided but not defined: -bogus"},
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

func TestOrdinaryErrorStillNeedsDiagnostic(t *testing.T) {
	err := errors.New("not printed")
	if IsReportedFailure(err) {
		t.Fatalf("ordinary error %q was marked as already reported", err)
	}
}

func TestHelpIsSelfContainedAndDoesNotRequireLibrary(t *testing.T) {
	cases := [][]string{
		{"--help"},
		{"help", "import"},
		{"help", "user", "add"},
		{"storage", "template", "apply", "help"},
		{"library", "shelves", "books", "--help"},
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
	oldStderr := os.Stderr
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return "", err
	}

	stderrDone := make(chan string, 1)
	go func() {
		var output strings.Builder
		_, _ = io.Copy(&output, stderrR)
		stderrR.Close()
		stderrDone <- output.String()
	}()

	os.Stderr = stderrW
	runErr := fn()
	stderrW.Close()
	os.Stderr = oldStderr
	stderr := <-stderrDone
	return stderr, runErr
}
