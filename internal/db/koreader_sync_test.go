package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAssetKOReaderHash(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T', 'T')")
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset1', 'w1', 'book.epub', 'book.epub', '.epub'), ('asset2', 'w1', 'book.pdf', 'book.pdf', '.pdf')")

	if err := SetAssetKOReaderHash(database, "asset1", "abc123"); err != nil {
		t.Fatalf("SetAssetKOReaderHash: %v", err)
	}
	target, err := ResolveKOReaderHash(database, "abc123")
	if err != nil || target.Ambiguous || target.AssetID != "asset1" || target.WorkID != "w1" {
		t.Fatalf("ResolveKOReaderHash = %+v err=%v; want asset1/w1", target, err)
	}
	if target, err := ResolveKOReaderHash(database, "missing"); err != nil || target != (KOReaderHashTarget{}) {
		t.Fatalf("missing hash target=%+v err=%v; want empty", target, err)
	}

	if err := SetAssetKOReaderHash(database, "asset2", "abc123"); err != nil {
		t.Fatalf("SetAssetKOReaderHash second same-work asset: %v", err)
	}
	target, err = ResolveKOReaderHash(database, "abc123")
	if err != nil || target.Ambiguous || target.AssetID != "asset1" || target.WorkID != "w1" {
		t.Fatalf("same-work hash target = %+v err=%v; want one unambiguous work", target, err)
	}

	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('w2', 'Other', 'Other');
		INSERT INTO assets (id, work_id, storage_path, filename, extension, koreader_hash)
		VALUES ('asset3', 'w2', 'other.epub', 'other.epub', '.epub', 'abc123')
	`); err != nil {
		t.Fatalf("seed cross-work hash collision: %v", err)
	}
	target, err = ResolveKOReaderHash(database, "abc123")
	if err != nil || !target.Ambiguous || target.AssetID != "" || target.WorkID != "" {
		t.Fatalf("cross-work hash target = %+v err=%v; want ambiguous without arbitrary target", target, err)
	}

	if _, err := database.Exec("UPDATE works SET deleted_at = unixepoch() WHERE id = 'w2'"); err != nil {
		t.Fatalf("trash colliding work: %v", err)
	}
	target, err = ResolveKOReaderHash(database, "abc123")
	if err != nil || target.Ambiguous || target.AssetID != "asset1" || target.WorkID != "w1" {
		t.Fatalf("live hash target with trashed collision = %+v err=%v; want asset1/w1", target, err)
	}
	if _, err := database.Exec("UPDATE works SET deleted_at = unixepoch() WHERE id = 'w1'"); err != nil {
		t.Fatalf("trash last live work: %v", err)
	}
	if target, err := ResolveKOReaderHash(database, "abc123"); err != nil || target != (KOReaderHashTarget{}) {
		t.Fatalf("trashed-only hash target = %+v err=%v; want empty", target, err)
	}
}

func TestKOReaderAmbiguousHashSavesProviderStateWithoutAdvancingAWork(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES
			('w1', 'First', 'First'),
			('w2', 'Second', 'Second');
		INSERT INTO assets (id, work_id, storage_path, filename, extension, koreader_hash) VALUES
			('asset1', 'w1', 'first.epub', 'first.epub', '.epub', 'shared-hash'),
			('asset2', 'w2', 'second.epub', 'second.epub', '.epub', 'shared-hash')
	`); err != nil {
		t.Fatalf("seed hash collision: %v", err)
	}

	saved, change, err := database.SaveKOReaderProgressAndAdvanceStatus(context.Background(), user.ID, KOReaderProgress{
		DocumentHash: "shared-hash",
		Progress:     "chapter-4",
		Percentage:   0.6,
		Device:       "KOReader",
		DeviceID:     "device-a",
	})
	if err != nil || saved == nil || saved.Progress != "chapter-4" || change.Changed || change.State.WorkID != "" {
		t.Fatalf("ambiguous hash save = saved:%+v change:%+v err:%v", saved, change, err)
	}
	for _, workID := range []string{"w1", "w2"} {
		state, err := GetReadingStatus(database, user.ID, workID)
		if err != nil || state.Status != ReadingStatusUnread {
			t.Fatalf("ambiguous hash advanced %s: %+v, err %v", workID, state, err)
		}
	}
}

func TestKOReaderProgressRoundTripAndIsolation(t *testing.T) {
	database := newTestDB(t)

	alice, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}
	bob, err := database.CreateUser("bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser bob: %v", err)
	}

	saved, _, err := database.SaveKOReaderProgressAndAdvanceStatus(context.Background(), alice.ID, KOReaderProgress{
		DocumentHash: "doc1",
		Progress:     "/body/DocFragment[1]",
		Percentage:   0.25,
		Device:       "KOReader",
		DeviceID:     "device-a",
	})
	if err != nil {
		t.Fatalf("SaveKOReaderProgressAndAdvanceStatus: %v", err)
	}
	if saved.UserID != alice.ID || saved.DocumentHash != "doc1" || saved.Percentage != 0.25 || saved.UpdatedAt == 0 {
		t.Fatalf("saved progress = %+v", saved)
	}

	if _, err := database.GetKOReaderProgress(bob.ID, "doc1"); !errors.Is(err, ErrKOReaderProgressNotFound) {
		t.Fatalf("bob progress err = %v; want not found", err)
	}

	updated, _, err := database.SaveKOReaderProgressAndAdvanceStatus(context.Background(), alice.ID, KOReaderProgress{
		DocumentHash: "doc1",
		Progress:     "/body/DocFragment[2]",
		Percentage:   0.75,
		Device:       "KOReader",
		DeviceID:     "device-b",
	})
	if err != nil {
		t.Fatalf("SaveKOReaderProgressAndAdvanceStatus update: %v", err)
	}
	if updated.Progress != "/body/DocFragment[2]" || updated.Percentage != 0.75 || updated.DeviceID != "device-b" {
		t.Fatalf("updated progress = %+v", updated)
	}
}

func TestKOReaderProgressValidation(t *testing.T) {
	database := newTestDB(t)

	user, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cases := []struct {
		name string
		p    KOReaderProgress
	}{
		{"missing document", KOReaderProgress{Progress: "p", Percentage: 0.1, Device: "d"}},
		{"missing progress", KOReaderProgress{DocumentHash: "doc", Percentage: 0.1, Device: "d"}},
		{"missing device", KOReaderProgress{DocumentHash: "doc", Progress: "p", Percentage: 0.1}},
		{"low percentage", KOReaderProgress{DocumentHash: "doc", Progress: "p", Percentage: -0.1, Device: "d"}},
		{"high percentage", KOReaderProgress{DocumentHash: "doc", Progress: "p", Percentage: 1.1, Device: "d"}},
		{"long document", KOReaderProgress{DocumentHash: strings.Repeat("x", 257), Progress: "p", Percentage: 0.1, Device: "d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := database.SaveKOReaderProgressAndAdvanceStatus(context.Background(), user.ID, tc.p)
			if !errors.Is(err, ErrKOReaderInvalidInput) {
				t.Fatalf("SaveKOReaderProgressAndAdvanceStatus err = %v; want invalid input", err)
			}
		})
	}
}

func TestKOReaderProgressAndStatusCommitTogether(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("kosync-atomic", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('w_kosync_atomic', 'Atomic', 'Atomic');
		INSERT INTO assets (id, work_id, storage_path, filename, extension, koreader_hash)
		VALUES ('a_kosync_atomic', 'w_kosync_atomic', 'atomic.epub', 'atomic.epub', '.epub', 'atomic-hash');
		CREATE TRIGGER reject_kosync_status
		BEFORE INSERT ON user_work_reading_events
		BEGIN
			SELECT RAISE(ABORT, 'status write rejected');
		END;
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err = database.SaveKOReaderProgressAndAdvanceStatus(context.Background(), user.ID, KOReaderProgress{
		DocumentHash: "atomic-hash",
		Progress:     "chapter-4",
		Percentage:   0.4,
		Device:       "KOReader",
		DeviceID:     "device-a",
	})
	if err == nil {
		t.Fatal("atomic KOSync save succeeded with rejecting status trigger")
	}
	if _, err := database.GetKOReaderProgress(user.ID, "atomic-hash"); !errors.Is(err, ErrKOReaderProgressNotFound) {
		t.Fatalf("KOSync position survived rolled-back status write: %v", err)
	}
}
