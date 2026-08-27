package cli

import (
	"reflect"
	"testing"
)

func TestTokenServiceURLs(t *testing.T) {
	tests := []struct {
		name       string
		baseURL    string
		token      string
		wantOPDS   string
		wantKOSync string
		wantErr    bool
	}{
		{
			name:       "relative paths without base URL",
			token:      "abc123",
			wantOPDS:   "/opds",
			wantKOSync: "/kosync/abc123",
		},
		{
			name:       "absolute public URL",
			baseURL:    "https://books.example/",
			token:      "abc123",
			wantOPDS:   "https://books.example/opds",
			wantKOSync: "https://books.example/kosync/abc123",
		},
		{
			name:       "reverse proxy prefix",
			baseURL:    "https://example.test/polka/",
			token:      "a/b c",
			wantOPDS:   "https://example.test/polka/opds",
			wantKOSync: "https://example.test/polka/kosync/a%2Fb%20c",
		},
		{
			name:    "reject relative base",
			baseURL: "books.example",
			token:   "abc123",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOPDS, gotKOSync, err := tokenServiceURLs(tt.baseURL, tt.token)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("tokenServiceURLs returned nil error")
				}
				return
			}
			if err != nil {
				t.Fatalf("tokenServiceURLs: %v", err)
			}
			if gotOPDS != tt.wantOPDS || gotKOSync != tt.wantKOSync {
				t.Fatalf("URLs = %q / %q; want %q / %q", gotOPDS, gotKOSync, tt.wantOPDS, tt.wantKOSync)
			}
		})
	}
}

func TestSplitTokenAddArgs(t *testing.T) {
	flags, positional, err := splitTokenAddArgs([]string{
		"alice",
		"--base-url",
		"https://books.example",
		"KOReader",
	})
	if err != nil {
		t.Fatalf("splitTokenAddArgs: %v", err)
	}
	if !reflect.DeepEqual(flags, []string{"--base-url", "https://books.example"}) {
		t.Fatalf("flags = %v", flags)
	}
	if !reflect.DeepEqual(positional, []string{"alice", "KOReader"}) {
		t.Fatalf("positional = %v", positional)
	}

	if _, _, err := splitTokenAddArgs([]string{"alice", "--base-url"}); err == nil {
		t.Fatalf("missing --base-url value returned nil error")
	}
}
