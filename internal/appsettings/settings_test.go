package appsettings

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestBoolSettings(t *testing.T) {
	database, err := db.InitPath(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	got, err := GetBool(database.Read(t.Context()), "missing", true)
	if err != nil {
		t.Fatalf("GetBool default: %v", err)
	}
	if !got {
		t.Fatalf("GetBool missing = false; want default true")
	}
	if err := SetBool(database.Write(t.Context()), "flag", false); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	got, err = GetBool(database.Read(t.Context()), "flag", true)
	if err != nil {
		t.Fatalf("GetBool: %v", err)
	}
	if got {
		t.Fatalf("GetBool flag = true; want false")
	}
	if err := Set(database.Write(t.Context()), "flag", "bad"); err != nil {
		t.Fatalf("Set bad: %v", err)
	}
	if _, err := GetBool(database.Read(t.Context()), "flag", true); err == nil || !strings.Contains(err.Error(), `invalid flag value "bad"`) {
		t.Fatalf("GetBool bad error = %v; want invalid flag value", err)
	}
}
