package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestTokenAddAndList(t *testing.T) {
	database, err := db.InitPath(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	user, err := database.CreateUser(t.Context(), "reader:name", "pw", db.RoleMember)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name    string
		baseURL string
		wantURL string
		wantErr bool
	}{
		{"relative paths", "", "", false},
		{"public URL", "https://books.example/", "https://books.example", false},
		{"proxy prefix", "https://books.example/polka/?query=1#fragment", "https://books.example/polka", false},
		{"relative base", "books.example", "", true},
		{"wrong scheme", "ftp://books.example", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output, err := captureStdout(t, func() error {
				return tokenAdd(t.Context(), database, []string{"--base-url", tt.baseURL, user.Username, tt.name})
			})
			defer database.RevokeAppToken(t.Context(), user.ID, tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("token add error = %v, wantErr=%v", err, tt.wantErr)
			}
			tokens, err := db.ListAppTokens(database.Read(t.Context()), user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantErr {
				if len(tokens) != 0 {
					t.Fatal("invalid base URL created a credential")
				}
				return
			}
			if len(tokens) != 1 {
				t.Fatalf("created tokens = %+v", tokens)
			}
			for _, want := range []string{
				"Username: polka",
				tt.wantURL + "/opds",
				tt.wantURL + "/kosync/" + tokens[0].Token,
			} {
				if !strings.Contains(output, want) {
					t.Fatalf("setup output missing %q: %s", want, output)
				}
			}
			listed, err := captureStdout(t, func() error { return tokenList(t.Context(), database, []string{user.Username}) })
			if err != nil || !strings.Contains(listed, tokens[0].Token) {
				t.Fatalf("token list = %q, err=%v", listed, err)
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
