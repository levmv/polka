package db

import (
	"errors"
	"math"
	"testing"
)

func TestUserSettingsLifecycle(t *testing.T) {
	database := newTestDB(t)
	alice := mustUser(t, database, "alice", RoleMember)
	bob := mustUser(t, database, "bob", RoleMember)

	defaults := UserSettings{
		UserID: alice.ID, Theme: ThemeSystem, ShowContinueReading: true, ReaderFlow: ReaderFlowPaginated,
		ReaderStyle: ReaderStylePaper, ReaderColumnWidth: 760, ReaderLineHeight: 1.72,
	}
	settings, err := GetUserSettings(database.Read(t.Context()), alice.ID)
	if err != nil || *settings != defaults {
		t.Fatalf("defaults = %+v, %v; want %+v", settings, err, defaults)
	}

	want := defaults
	for _, tc := range []struct {
		name   string
		patch  UserSettingsPatch
		update func(*UserSettings)
	}{
		{"reader first", UserSettingsPatch{ReaderFlow: new(ReaderFlowScrolled), ReaderStyle: new(ReaderStyleCustom), ReaderFontSize: new(2), ReaderColumnWidth: new(820), ReaderLineHeight: new(1.9)},
			func(s *UserSettings) {
				s.ReaderFlow = ReaderFlowScrolled
				s.ReaderStyle = ReaderStyleCustom
				s.ReaderFontSize = 2
				s.ReaderColumnWidth = 820
				s.ReaderLineHeight = 1.9
			}},
		{"account", UserSettingsPatch{Theme: new(ThemeSepia), ShowContinueReading: new(true), TimeZone: new("UTC")},
			func(s *UserSettings) {
				s.Theme = ThemeSepia
				s.ShowContinueReading = true
				s.TimeZone = "UTC"
			}},
		{"explicit zero and false", UserSettingsPatch{ReaderFontSize: new(0), ShowContinueReading: new(false)},
			func(s *UserSettings) {
				s.ReaderFontSize = 0
				s.ShowContinueReading = false
			}},
		{"lower bounds", UserSettingsPatch{ReaderFontSize: new(-4), ReaderColumnWidth: new(560), ReaderLineHeight: new(1.2)},
			func(s *UserSettings) {
				s.ReaderFontSize = -4
				s.ReaderColumnWidth = 560
				s.ReaderLineHeight = 1.2
			}},
		{"upper bounds", UserSettingsPatch{ReaderFontSize: new(6), ReaderColumnWidth: new(920), ReaderLineHeight: new(2.2)},
			func(s *UserSettings) {
				s.ReaderFontSize = 6
				s.ReaderColumnWidth = 920
				s.ReaderLineHeight = 2.2
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, err = database.SaveUserSettings(t.Context(), alice.ID, tc.patch)
			if err != nil {
				t.Fatal(err)
			}
			tc.update(&want)
			if settings.UpdatedAt == 0 {
				t.Fatal("missing update time")
			}
			want.UpdatedAt = settings.UpdatedAt
			if *settings != want {
				t.Fatalf("settings = %+v; want %+v", settings, want)
			}
			stored, err := GetUserSettings(database.Read(t.Context()), alice.ID)
			if err != nil || *stored != want {
				t.Fatalf("stored = %+v, %v; want %+v", stored, err, want)
			}
		})
	}
	defaults.UserID = bob.ID
	settings, err = GetUserSettings(database.Read(t.Context()), bob.ID)
	if err != nil || *settings != defaults {
		t.Fatalf("other user's settings = %+v, %v", settings, err)
	}
}

func TestUserSettingsRejectInvalidPatch(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "reader", RoleMember)
	before, err := database.SaveUserSettings(t.Context(), user.ID, UserSettingsPatch{Theme: new(ThemeSepia)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		patch   UserSettingsPatch
		wantErr error
	}{
		{"theme", UserSettingsPatch{Theme: new("solarized")}, ErrInvalidTheme},
		{"flow", UserSettingsPatch{ReaderFlow: new("sideways")}, ErrInvalidUserSettings},
		{"empty flow", UserSettingsPatch{ReaderFlow: new("")}, ErrInvalidUserSettings},
		{"style", UserSettingsPatch{ReaderStyle: new("neon")}, ErrInvalidUserSettings},
		{"font low", UserSettingsPatch{ReaderFontSize: new(-5)}, ErrInvalidUserSettings},
		{"font high", UserSettingsPatch{ReaderFontSize: new(7)}, ErrInvalidUserSettings},
		{"width low", UserSettingsPatch{ReaderColumnWidth: new(559)}, ErrInvalidUserSettings},
		{"width high", UserSettingsPatch{ReaderColumnWidth: new(921)}, ErrInvalidUserSettings},
		{"line low", UserSettingsPatch{ReaderLineHeight: new(1.19)}, ErrInvalidUserSettings},
		{"line high", UserSettingsPatch{ReaderLineHeight: new(2.21)}, ErrInvalidUserSettings},
		{"line NaN", UserSettingsPatch{ReaderLineHeight: new(math.NaN())}, ErrInvalidUserSettings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.patch.ShowContinueReading = new(false)
			if _, err := database.SaveUserSettings(t.Context(), user.ID, tc.patch); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v; want %v", err, tc.wantErr)
			}
			after, err := GetUserSettings(database.Read(t.Context()), user.ID)
			if err != nil || *after != *before {
				t.Fatalf("invalid patch changed settings: %+v, %v", after, err)
			}
		})
	}
	if _, err := GetUserSettings(database.Read(t.Context()), 0); !errors.Is(err, ErrUserIDRequired) {
		t.Fatalf("missing user: %v", err)
	}
}

func TestUserSettingsConcurrentPatches(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "reader", RoleMember)
	patches := []UserSettingsPatch{
		{Theme: new(ThemeDark)}, {ShowContinueReading: new(false)}, {TimeZone: new("UTC")},
		{ReaderFlow: new(ReaderFlowScrolled)}, {ReaderStyle: new(ReaderStyleOriginal)},
		{ReaderFontSize: new(2)}, {ReaderColumnWidth: new(820)}, {ReaderLineHeight: new(1.9)},
	}
	start := make(chan struct{})
	done := make(chan error, len(patches))
	for _, patch := range patches {
		go func() {
			<-start
			_, err := database.SaveUserSettings(t.Context(), user.ID, patch)
			done <- err
		}()
	}
	close(start)
	for range patches {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	settings, err := GetUserSettings(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := UserSettings{UserID: user.ID, Theme: ThemeDark, ShowContinueReading: false, TimeZone: "UTC",
		ReaderFlow: ReaderFlowScrolled, ReaderStyle: ReaderStyleOriginal, ReaderFontSize: 2,
		ReaderColumnWidth: 820, ReaderLineHeight: 1.9, UpdatedAt: settings.UpdatedAt}
	if *settings != want {
		t.Fatalf("concurrent patches = %+v; want %+v", settings, want)
	}
}

func TestUserTimeZoneInitialization(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "reader", RoleMember)
	settings, err := GetUserSettings(database.Read(t.Context()), user.ID)
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
		{UserSettingsPatch{ReaderFontSize: new(2)}, "UTC"},
	} {
		settings, err = database.SaveUserSettings(t.Context(), user.ID, tc.patch)
		if err != nil || settings.TimeZone != tc.want {
			t.Fatalf("time zone = %+v, %v; want %s", settings, err, tc.want)
		}
	}
	for _, zone := range []string{"", "Local", "Mars/Olympus", "../UTC", "/etc/localtime"} {
		if _, err := database.SaveUserSettings(t.Context(), user.ID, UserSettingsPatch{TimeZone: &zone}); !errors.Is(err, ErrInvalidTimeZone) {
			t.Fatalf("time zone %q error = %v", zone, err)
		}
	}
}
