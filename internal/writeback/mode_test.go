package writeback

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestWritebackMode(t *testing.T) {
	database, err := db.InitPath(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	if mode, err := OpenMode(database.DB); err != nil || mode != ModeManual {
		t.Fatalf("OpenMode default = %q, %v; want manual", mode, err)
	}

	for _, want := range []Mode{ModeOff, ModeManual, ModeAuto} {
		if err := SaveMode(database.DB, want); err != nil {
			t.Fatalf("SaveMode(%q): %v", want, err)
		}
		if got, err := OpenMode(database.DB); err != nil || got != want {
			t.Fatalf("OpenMode after save = %q, %v; want %q", got, err, want)
		}
	}

	if err := SaveMode(database.DB, "surprise"); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("SaveMode(surprise) error = %v; want ErrInvalidMode", err)
	}
	if got, _ := OpenMode(database.DB); got != ModeAuto {
		t.Fatalf("mode after rejected save = %q; want the prior auto", got)
	}
}
