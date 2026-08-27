package db

import (
	"testing"

	"github.com/levmv/polka/internal/fsprofile"
)

func TestDSN(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/media/dev/books/library.db", "file:/media/dev/books/library.db"},
		{"rel/lib.db", "file:rel/lib.db"},
		{"/has space/a?b#c.db", "file:/has%20space/a%3Fb%23c.db"},
	}
	for _, tt := range tests {
		if got := DSN(tt.path); got != tt.want {
			t.Errorf("DSN(%q) = %q; want %q", tt.path, got, tt.want)
		}
	}
}

func TestAppendDSNQuery(t *testing.T) {
	got := appendDSNQuery("file:/tmp/library.db", sqliteOptions)
	want := "file:/tmp/library.db?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	if got != want {
		t.Fatalf("appendDSNQuery without query = %q; want %q", got, want)
	}

	got = appendDSNQuery("file:/tmp/library.db?mode=ro", sqliteOptions)
	want = "file:/tmp/library.db?mode=ro&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	if got != want {
		t.Fatalf("appendDSNQuery with query = %q; want %q", got, want)
	}
}

func TestSQLiteOptionsForNetworkFS(t *testing.T) {
	info := fsprofile.Info{Kind: fsprofile.KindNetwork, Type: "nfs4"}

	got := appendDSNQuery("file:/tmp/library.db", sqliteOptionsFor(info, false))
	want := "file:/tmp/library.db?_pragma=foreign_keys(1)&_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)&_pragma=busy_timeout(15000)&_txlock=immediate"
	if got != want {
		t.Fatalf("network sqlite options = %q; want %q", got, want)
	}

	got = appendDSNQuery(appendDSNQuery("file:/tmp/library.db", "mode=ro"), sqliteOptionsFor(info, true))
	want = "file:/tmp/library.db?mode=ro&_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_txlock=immediate"
	if got != want {
		t.Fatalf("network readonly sqlite options = %q; want %q", got, want)
	}
}
