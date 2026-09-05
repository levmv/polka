package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func readingActivityFixture(t *testing.T, zone string) (*DB, int64) {
	t.Helper()
	database := newTestDB(t)
	user, err := database.CreateUser("reader", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveUserSettings(user.ID, UserSettingsPatch{TimeZone: &zone}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO works (id, title, sort_title) VALUES ('w1', 'Book', 'Book')`,
		`INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('a1', 'w1', 'one.epub', 'one.epub', '.epub'), ('a2', 'w1', 'two.pdf', 'two.pdf', '.pdf')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return database, user.ID
}

func activityID(n int) string { return fmt.Sprintf("%032x", n) }

func startActivity(t *testing.T, database *DB, user int64, asset string, n int, now time.Time) ReadingActivityResult {
	t.Helper()
	result, err := database.StartWebReadingSession(context.Background(), user, asset, activityID(n), 0, now)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func checkpointActivity(t *testing.T, database *DB, user int64, asset string, n int, origin time.Time, elapsed, action time.Duration, finished bool) ReadingActivityResult {
	t.Helper()
	result, err := database.CheckpointWebReadingSession(context.Background(), user, asset, activityID(n), ReadingActivityCheckpoint{
		ElapsedMS: elapsed.Milliseconds(), LastActivityMS: action.Milliseconds(), Finished: finished,
	}, origin.Add(elapsed))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readingTimeByDay(t *testing.T, database *DB, user int64) map[string]int64 {
	t.Helper()
	rows, err := database.Query(`SELECT d.day, SUM(d.active_ms) FROM reading_session_days d
		JOIN reading_sessions s ON s.id = d.session_id WHERE s.user_id = ? GROUP BY d.day`, user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]int64{}
	for rows.Next() {
		var day string
		var ms int64
		if err := rows.Scan(&day, &ms); err != nil {
			t.Fatal(err)
		}
		result[day] = ms
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReadingActivityRetriesAndTakeover(t *testing.T) {
	database, user := readingActivityFixture(t, "UTC")
	start := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	startActivity(t, database, user, "a1", 1, start)
	for i, elapsed := range []time.Duration{60 * time.Second, 60 * time.Second, 30 * time.Second} {
		// Reports arrive in this order while server time keeps moving forward.
		got, err := database.CheckpointWebReadingSession(context.Background(), user, "a1", activityID(1),
			ReadingActivityCheckpoint{ElapsedMS: elapsed.Milliseconds()}, start.Add(time.Minute+time.Duration(i)*time.Second))
		if err != nil || got.CountedMS != 60_000 || !got.Active {
			t.Fatalf("retry/reorder = %+v, %v", got, err)
		}
	}
	second := start.Add(90 * time.Second)
	startActivity(t, database, user, "a2", 2, second)
	got := checkpointActivity(t, database, user, "a1", 1, start, 120*time.Second, 0, true)
	if got.Active || got.CountedMS != 90_000 {
		t.Fatalf("late final = %+v", got)
	}
	if startActivity(t, database, user, "a1", 1, start.Add(130*time.Second)).Active {
		t.Fatal("retry reclaimed retired session")
	}
	checkpointActivity(t, database, user, "a2", 2, second, 60*time.Second, 0, false)
	if days := readingTimeByDay(t, database, user); days["2026-09-05"] != 150_000 {
		t.Fatalf("overlapping readers = %v", days)
	}
	var positions, statuses int
	if err := database.QueryRow(`SELECT (SELECT COUNT(*) FROM user_asset_state), (SELECT COUNT(*) FROM user_work_reading_events)`).Scan(&positions, &statuses); err != nil {
		t.Fatal(err)
	}
	if positions != 0 || statuses != 0 {
		t.Fatalf("activity changed progress/status: %d/%d", positions, statuses)
	}
}

func TestReadingActivityIdleAndResume(t *testing.T) {
	database, user := readingActivityFixture(t, "UTC")
	start := time.Now()
	startActivity(t, database, user, "a1", 1, start)
	checkpointActivity(t, database, user, "a1", 1, start, time.Minute, 0, false)
	got := checkpointActivity(t, database, user, "a1", 1, start, time.Hour, time.Hour, false)
	if got.Active || got.CountedMS != ReadingIdleLimit.Milliseconds() {
		t.Fatalf("idle gap counted: %+v", got)
	}
	got = checkpointActivity(t, database, user, "a1", 1, start, 2*time.Hour, 2*time.Hour, false)
	if got.Active || got.CountedMS != ReadingIdleLimit.Milliseconds() {
		t.Fatalf("retired interval revived: %+v", got)
	}
	startActivity(t, database, user, "a1", 2, start.Add(2*time.Hour))
	got = checkpointActivity(t, database, user, "a1", 2, start.Add(2*time.Hour), time.Minute, 0, false)
	if !got.Active || got.CountedMS != 60_000 {
		t.Fatalf("resume = %+v", got)
	}
}

func TestReadingActivityActionBeforeIdleDeadline(t *testing.T) {
	database, user := readingActivityFixture(t, "UTC")
	start := time.Now()
	startActivity(t, database, user, "a1", 1, start)
	got := checkpointActivity(t, database, user, "a1", 1, start, 310*time.Second, 290*time.Second, false)
	if !got.Active || got.CountedMS != 310_000 {
		t.Fatalf("action before deadline was lost in transit: %+v", got)
	}
}

func TestReadingActivityShortPauseAndLateCheckpoints(t *testing.T) {
	database, user := readingActivityFixture(t, "UTC")
	ctx := context.Background()
	start := time.Date(2026, 9, 5, 23, 59, 0, 0, time.UTC)
	startActivity(t, database, user, "a1", 1, start)
	checkpointActivity(t, database, user, "a1", 1, start, 30*time.Second, 0, false)
	resume := start.Add(90 * time.Second)
	got, err := database.StartWebReadingSession(ctx, user, "a1", activityID(1), 1, resume)
	if err != nil || !got.Active || got.CountedMS != 30_000 {
		t.Fatalf("resume = %+v, %v", got, err)
	}
	checkpoint := ReadingActivityCheckpoint{Segment: 1, ElapsedMS: 30_000}
	for range 2 {
		got, err = database.CheckpointWebReadingSession(ctx, user, "a1", activityID(1), checkpoint, resume.Add(30*time.Second))
		if err != nil || !got.Active || got.CountedMS != 60_000 {
			t.Fatalf("resumed checkpoint = %+v, %v", got, err)
		}
	}
	// The hidden minute crosses midnight but contributes to neither day.
	days := readingTimeByDay(t, database, user)
	if len(days) != 2 || days["2026-09-05"] != 30_000 || days["2026-09-06"] != 30_000 {
		t.Fatalf("hidden time counted or attributed to the wrong day: %v", days)
	}
	got, err = database.CheckpointWebReadingSession(ctx, user, "a1", activityID(1), ReadingActivityCheckpoint{
		ElapsedMS: 45_000, Finished: true,
	}, resume.Add(time.Minute))
	if err != nil || got.Active || got.CountedMS != 60_000 {
		t.Fatalf("old segment's late final = %+v, %v", got, err)
	}
	if got := startActivity(t, database, user, "a1", 1, resume.Add(time.Minute)); got.Active {
		t.Fatal("old segment reclaimed current accounting")
	}
	got, err = database.StartWebReadingSession(ctx, user, "a1", activityID(1), 1, resume.Add(time.Minute))
	if err != nil || !got.Active || got.CountedMS != 60_000 {
		t.Fatalf("resume retry reset or closed the session: %+v, %v", got, err)
	}
	var sessions int
	if err := database.QueryRow("SELECT COUNT(*) FROM reading_sessions").Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("short pause created history rows: %d, %v", sessions, err)
	}
	got, err = database.StartWebReadingSession(ctx, user, "a1", activityID(1), 2, resume.Add(time.Hour))
	if err != nil || got.Active || got.CountedMS != 60_000 {
		t.Fatalf("long pause revived old session: %+v, %v", got, err)
	}
}

func TestReadingActivityMidnightAndTimeZoneChange(t *testing.T) {
	database, user := readingActivityFixture(t, "Asia/Tokyo")
	start := time.Date(2026, 9, 5, 14, 59, 30, 0, time.UTC)
	startActivity(t, database, user, "a1", 1, start)
	checkpointActivity(t, database, user, "a1", 1, start, time.Minute, 0, true)
	days := readingTimeByDay(t, database, user)
	if days["2026-09-05"] != 30_000 || days["2026-09-06"] != 30_000 {
		t.Fatalf("midnight split = %v", days)
	}
	if _, err := database.SaveUserSettings(user, UserSettingsPatch{TimeZone: new("UTC")}); err != nil {
		t.Fatal(err)
	}
	second := start.Add(time.Minute)
	startActivity(t, database, user, "a1", 2, second)
	checkpointActivity(t, database, user, "a1", 2, second, time.Minute, 0, true)
	days = readingTimeByDay(t, database, user)
	if days["2026-09-05"] != 90_000 || days["2026-09-06"] != 30_000 {
		t.Fatalf("past day moved after zone change: %v", days)
	}
}

func TestReadingActivityMidnightOffsetChanges(t *testing.T) {
	for _, tc := range []struct {
		zone, utcStart, beforeDay, afterDay string
	}{
		// Spring-forward skips midnight.
		{"America/Sao_Paulo", "2018-11-04T02:59:30Z", "2018-11-03", "2018-11-04"},
		// The midnight hour occurs twice.
		{"America/Havana", "2026-11-01T03:59:30Z", "2026-10-31", "2026-11-01"},
		// A dateline change skips an entire calendar date.
		{"Pacific/Apia", "2011-12-30T09:59:30Z", "2011-12-29", "2011-12-31"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			database, user := readingActivityFixture(t, tc.zone)
			start, err := time.Parse(time.RFC3339, tc.utcStart)
			if err != nil {
				t.Fatal(err)
			}
			startActivity(t, database, user, "a1", 1, start)
			checkpointActivity(t, database, user, "a1", 1, start, time.Minute, 0, true)
			days := readingTimeByDay(t, database, user)
			if len(days) != 2 || days[tc.beforeDay] != 30_000 || days[tc.afterDay] != 30_000 {
				t.Fatalf("midnight with offset change = %v", days)
			}
		})
	}
}

func TestReadingActivityDayLengthsAcrossDST(t *testing.T) {
	for _, tc := range []struct {
		date  string
		hours int
	}{{"2026-03-08", 23}, {"2026-11-01", 25}} {
		t.Run(tc.date, func(t *testing.T) {
			database, user := readingActivityFixture(t, "America/New_York")
			zone, err := time.LoadLocation("America/New_York")
			if err != nil {
				t.Fatal(err)
			}
			start, err := time.ParseInLocation("2006-01-02", tc.date, zone)
			if err != nil {
				t.Fatal(err)
			}
			startActivity(t, database, user, "a1", 1, start)
			elapsed := time.Duration(tc.hours+1) * time.Hour
			// Keep reading through the day and into the next one, reporting
			// actions within the idle limit instead of bypassing accounting.
			for offset := 4 * time.Minute; offset <= elapsed; offset += 4 * time.Minute {
				checkpointActivity(t, database, user, "a1", 1, start, offset, offset, offset == elapsed)
			}
			days := readingTimeByDay(t, database, user)
			if len(days) != 2 || days[tc.date] != int64(tc.hours)*3_600_000 || days[start.Add(elapsed).Format("2006-01-02")] != 3_600_000 {
				t.Fatalf("DST split = %v", days)
			}
		})
	}
}

func TestReadingActivityIdentityAndElapsedValidation(t *testing.T) {
	database, user := readingActivityFixture(t, "UTC")
	start := time.Now()
	startActivity(t, database, user, "a1", 1, start)
	if _, err := database.StartWebReadingSession(context.Background(), user, "a2", activityID(1), 0, start); !errors.Is(err, ErrInvalidReaderInput) {
		t.Fatalf("identity rebound to another asset: %v", err)
	}
	for _, cp := range []ReadingActivityCheckpoint{{ElapsedMS: -1}, {ElapsedMS: 1, LastActivityMS: 2}, {LastActivityMS: -1}} {
		if _, err := database.CheckpointWebReadingSession(context.Background(), user, "a1", activityID(1), cp, start); !errors.Is(err, ErrInvalidReaderInput) {
			t.Fatalf("invalid checkpoint accepted: %+v, %v", cp, err)
		}
	}
	got, err := database.CheckpointWebReadingSession(context.Background(), user, "a1", activityID(1), ReadingActivityCheckpoint{ElapsedMS: 1 << 62, LastActivityMS: 1 << 62}, start.Add(time.Second))
	if err != nil || got.CountedMS != 1000 {
		t.Fatalf("client future time = %+v, %v", got, err)
	}
}
