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

	settings, err = database.SaveUserSettings(alice.ID, UserSettingsPatch{
		Theme:               new(ThemeSepia),
		HideContinueReading: new(true),
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

	if _, err := database.SaveUserSettings(alice.ID, UserSettingsPatch{Theme: new("solarized")}); !errors.Is(err, ErrInvalidTheme) {
		t.Fatalf("invalid theme err = %v, want ErrInvalidTheme", err)
	}
	if _, err := database.GetUserSettings(0); !errors.Is(err, ErrUserIDRequired) {
		t.Fatalf("missing user err = %v, want ErrUserIDRequired", err)
	}
}

func TestUserTimeZoneInitialization(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("reader", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := database.GetUserSettings(user.ID)
	if err != nil || settings.TimeZone != "" {
		t.Fatalf("unset time zone: %+v, %v", settings, err)
	}
	for _, tc := range []struct {
		patch UserSettingsPatch
		want  string
	}{
		{UserSettingsPatch{TimeZone: new("Europe/Berlin"), InitializeTimeZone: true}, "Europe/Berlin"},
		{UserSettingsPatch{TimeZone: new("Asia/Tokyo"), InitializeTimeZone: true}, "Europe/Berlin"},
		{UserSettingsPatch{TimeZone: new("UTC")}, "UTC"},
		{UserSettingsPatch{TimeZone: new("Asia/Tokyo"), InitializeTimeZone: true}, "UTC"},
		{UserSettingsPatch{Theme: new(ThemeDark)}, "UTC"},
	} {
		settings, err = database.SaveUserSettings(user.ID, tc.patch)
		if err != nil || settings.TimeZone != tc.want {
			t.Fatalf("time zone = %+v, %v; want %s", settings, err, tc.want)
		}
	}
	for _, zone := range []string{"", "Local", "Mars/Olympus", "../UTC", "/etc/localtime"} {
		if _, err := database.SaveUserSettings(user.ID, UserSettingsPatch{TimeZone: &zone}); !errors.Is(err, ErrInvalidTimeZone) {
			t.Fatalf("time zone %q error = %v", zone, err)
		}
	}
}
