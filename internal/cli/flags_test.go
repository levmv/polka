package cli

import (
	"flag"
	"io"
	"slices"
	"testing"
)

func TestCommandFlagsPreserveValuesAndPositionals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		wantTitle string
		wantForce bool
		wantFiles []string
		wantError bool
	}{
		{name: "flags first", args: []string{"--title", "New title", "--force", "book.epub"}, wantTitle: "New title", wantForce: true, wantFiles: []string{"book.epub"}},
		{name: "interspersed", args: []string{"one.epub", "--title", "New title", "two.epub", "--force"}, wantTitle: "New title", wantForce: true, wantFiles: []string{"one.epub", "two.epub"}},
		{name: "equals and boolean", args: []string{"book.epub", "-title=New title", "--force=false"}, wantTitle: "New title", wantFiles: []string{"book.epub"}},
		{name: "empty value", args: []string{"book.epub", "--title", ""}, wantFiles: []string{"book.epub"}},
		{name: "flag as value", args: []string{"book.epub", "--title", "--help"}, wantTitle: "--help", wantFiles: []string{"book.epub"}},
		{name: "terminator", args: []string{"one.epub", "--force", "--", "--title", "--help"}, wantForce: true, wantFiles: []string{"one.epub", "--title", "--help"}},
		{name: "literal dash", args: []string{"-", "--force"}, wantForce: true, wantFiles: []string{"-"}},
		{name: "missing value after file", args: []string{"book.epub", "--title"}, wantError: true},
		{name: "unknown flag after file", args: []string{"book.epub", "--unknown"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			title := fs.String("title", "", "book title")
			force := fs.Bool("force", false, "overwrite")
			help, err := parseCommandFlags(fs, tc.args)
			if help || (err != nil) != tc.wantError {
				t.Fatalf("parse = help %v, error %v; want error %v", help, err, tc.wantError)
			}
			if tc.wantError {
				return
			}
			if *title != tc.wantTitle || *force != tc.wantForce || !slices.Equal(fs.Args(), tc.wantFiles) {
				t.Fatalf("parsed title %q, force %v, files %q; want %q, %v, %q", *title, *force, fs.Args(), tc.wantTitle, tc.wantForce, tc.wantFiles)
			}
		})
	}
}
