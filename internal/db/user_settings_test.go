package db

import (
	"errors"
	"testing"
)

func TestUserSettingsLifecycle(t *testing.T) {
	database := newTestDB(t)

	alice, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser("bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	settings, err := database.GetUserSettings(alice.ID)
	if err != nil {
		t.Fatalf("GetUserSettings default: %v", err)
	}
	if settings.Theme != ThemeSystem || settings.HideContinueReading || settings.UpdatedAt != 0 {
		t.Fatalf("default user settings = %+v", settings)
	}

	settings, err = database.SaveUserSettings(alice.ID, UserSettings{
		Theme:               ThemeSepia,
		HideContinueReading: true,
	})
	if err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}
	if settings.Theme != ThemeSepia || !settings.HideContinueReading || settings.UpdatedAt == 0 {
		t.Fatalf("saved user settings = %+v", settings)
	}

	bobSettings, err := database.GetUserSettings(bob.ID)
	if err != nil {
		t.Fatalf("GetUserSettings bob: %v", err)
	}
	if bobSettings.Theme != ThemeSystem || bobSettings.HideContinueReading {
		t.Fatalf("user settings leaked across users: %+v", bobSettings)
	}

	if _, err := database.SaveUserSettings(alice.ID, UserSettings{Theme: "solarized"}); !errors.Is(err, ErrInvalidTheme) {
		t.Fatalf("invalid theme err = %v, want ErrInvalidTheme", err)
	}
	if _, err := database.GetUserSettings(""); !errors.Is(err, ErrUserIDRequired) {
		t.Fatalf("missing user err = %v, want ErrUserIDRequired", err)
	}
}
