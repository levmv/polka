package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/storage"
)

func TestRecoverableCoverIndex(t *testing.T) {
	root := storage.NewRoot(t.TempDir())
	staging := root.StagingDir()
	covers := root.Abs("covers")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.MkdirAll(covers, 0o755); err != nil {
		t.Fatalf("mkdir covers: %v", err)
	}

	staged := filepath.Join(staging, ".tmp-1111-b_staged-cover")
	placed := filepath.Join(covers, ".tmp-2222-b_placed")
	adjacent := filepath.Join(covers, ".writeback-b_adjacent-cover-3333.tmp")
	shadowed := filepath.Join(covers, ".tmp-4444-b_staged")
	unrelated := filepath.Join(covers, "notes-b_placed")
	for _, path := range []string{staged, placed, adjacent, shadowed, unrelated} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(path), err)
		}
	}

	idx := newRecoverableCoverIndex(context.Background(), root)
	for _, tc := range []struct {
		bookID string
		want   string
	}{
		{bookID: "b_staged", want: staged},
		{bookID: "b_placed", want: placed},
		{bookID: "b_adjacent", want: adjacent},
		{bookID: "b_missing", want: ""},
	} {
		if got := idx.find(tc.bookID); got != tc.want {
			t.Errorf("find(%q) = %q; want %q", tc.bookID, got, tc.want)
		}
	}
}
