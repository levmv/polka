package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArgumentDiagnostics(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		wantError    string
		wantReported bool
	}{
		{name: "missing command", wantError: "no subcommand provided", wantReported: true},
		{name: "invalid flag", args: []string{"convert", "--bogus"}, wantError: "flag provided but not defined: -bogus", wantReported: true},
		{name: "missing flag value", args: []string{"meta", "set", "book.epub", "--title"}, wantError: "flag needs an argument: -title", wantReported: true},
		{name: "missing field flags", args: []string{"meta", "set", "book.epub"}, wantError: "meta set requires at least one field flag"},
		{name: "unknown library command", args: []string{"library", "bogus"}, wantError: "unknown library command: bogus"},
		{name: "missing template command", args: []string{"storage", "template"}, wantError: "missing storage template command"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := captureStderr(t, func() error { return Run(tc.args) })
			if err == nil {
				t.Fatal("Run returned nil error")
			}
			if err.Error() != tc.wantError {
				t.Fatalf("Run error = %q; want %q", err, tc.wantError)
			}
			if reported := IsReportedFailure(err); reported != tc.wantReported {
				t.Fatalf("IsReportedFailure(%q) = %v; want %v", err, reported, tc.wantReported)
			}
			wantPrinted := 0
			if tc.wantReported {
				wantPrinted = 1
			}
			if count := strings.Count(stderr, tc.wantError); count != wantPrinted {
				t.Fatalf("stderr contains %q %d times; want %d:\n%s", tc.wantError, count, wantPrinted, stderr)
			}
		})
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
			stderr, err := captureStderr(t, func() error { return Run(fullArgs) })
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
