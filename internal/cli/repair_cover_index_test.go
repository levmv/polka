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

	staged := filepath.Join(staging, ".tmp-1111-w_staged-cover")
	placed := filepath.Join(covers, ".tmp-2222-w_placed")
	adjacent := filepath.Join(covers, ".writeback-w_adjacent-cover-3333.tmp")
	shadowed := filepath.Join(covers, ".tmp-4444-w_staged")
	unrelated := filepath.Join(covers, "notes-w_placed")
	for _, path := range []string{staged, placed, adjacent, shadowed, unrelated} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(path), err)
		}
	}

	idx := newRecoverableCoverIndex(context.Background(), root)
	for _, tc := range []struct {
		workID string
		want   string
	}{
		{workID: "w_staged", want: staged},
		{workID: "w_placed", want: placed},
		{workID: "w_adjacent", want: adjacent},
		{workID: "w_missing", want: ""},
	} {
		if got := idx.find(tc.workID); got != tc.want {
			t.Errorf("find(%q) = %q; want %q", tc.workID, got, tc.want)
		}
	}
}
