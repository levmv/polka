package appsettings

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestGetSet(t *testing.T) {
	database, err := db.InitPath(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	if got, ok, err := Get(database, "missing"); err != nil || ok || got != "" {
		t.Fatalf("Get missing = %q, %v, %v; want empty, false, nil", got, ok, err)
	}
	if err := Set(database, "example", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Set(database, "example", "updated"); err != nil {
		t.Fatalf("Set update: %v", err)
	}
	got, ok, err := Get(database, "example")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got != "updated" {
		t.Fatalf("Get = %q, %v; want updated, true", got, ok)
	}
}

func TestBoolSettings(t *testing.T) {
	database, err := db.InitPath(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	got, err := GetBool(database, "missing", true)
	if err != nil {
		t.Fatalf("GetBool default: %v", err)
	}
	if !got {
		t.Fatalf("GetBool missing = false; want default true")
	}
	if err := SetBool(database, "flag", false); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	got, err = GetBool(database, "flag", true)
	if err != nil {
		t.Fatalf("GetBool: %v", err)
	}
	if got {
		t.Fatalf("GetBool flag = true; want false")
	}
	if err := Set(database, "flag", "bad"); err != nil {
		t.Fatalf("Set bad: %v", err)
	}
	if _, err := GetBool(database, "flag", true); err == nil || !strings.Contains(err.Error(), `invalid flag value "bad"`) {
		t.Fatalf("GetBool bad error = %v; want invalid flag value", err)
	}
}
