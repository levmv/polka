package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

func TestAssetKOReaderHash(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T', 'T')")
	epubHash := sha256.Sum256([]byte("EPUB"))
	pdfHash := sha256.Sum256([]byte("PDF"))
	mustExec(t, database, `INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (1, 1, 'book.epub', 'book.epub', '.epub', ?, ?), (2, 1, 'book.pdf', 'book.pdf', '.pdf', ?, ?)`, epubHash[:], epubHash[:], pdfHash[:], pdfHash[:])

	if err := database.CacheAssetKOReaderHash(t.Context(), 1, epubHash[:], "abc123"); err != nil {
		t.Fatalf("CacheAssetKOReaderHash: %v", err)
	}
	target, err := ResolveKOReaderHash(database.Read(t.Context()), "abc123")
	if err != nil || target.Ambiguous || target.AssetID != 1 || target.BookID != 1 {
		t.Fatalf("ResolveKOReaderHash = %+v err=%v; want asset1/1", target, err)
	}
	if target, err := ResolveKOReaderHash(database.Read(t.Context()), "missing"); err != nil || target != (KOReaderHashTarget{}) {
		t.Fatalf("missing hash target=%+v err=%v; want empty", target, err)
	}

	if err := database.CacheAssetKOReaderHash(t.Context(), 2, pdfHash[:], "abc123"); err != nil {
		t.Fatalf("CacheAssetKOReaderHash second same-book asset: %v", err)
	}
	target, err = ResolveKOReaderHash(database.Read(t.Context()), "abc123")
	if err != nil || target.Ambiguous || target.AssetID != 1 || target.BookID != 1 {
		t.Fatalf("same-book hash target = %+v err=%v; want one unambiguous book", target, err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (2, 'Other', 'Other');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_sha256, current_sha256)
		VALUES (3, 2, 'other.epub', 'other.epub', '.epub', 'abc123', randomblob(32), randomblob(32))
	`)

	target, err = ResolveKOReaderHash(database.Read(t.Context()), "abc123")
	if err != nil || !target.Ambiguous || target.AssetID != 0 || target.BookID != 0 {
		t.Fatalf("cross-book hash target = %+v err=%v; want ambiguous without arbitrary target", target, err)
	}
	mustExec(t, database, "UPDATE books SET deleted_at = unixepoch() WHERE id = 2")

	target, err = ResolveKOReaderHash(database.Read(t.Context()), "abc123")
	if err != nil || target.Ambiguous || target.AssetID != 1 || target.BookID != 1 {
		t.Fatalf("live hash target with trashed collision = %+v err=%v; want asset1/1", target, err)
	}
	mustExec(t, database, "UPDATE books SET deleted_at = unixepoch() WHERE id = 1")

	if target, err := ResolveKOReaderHash(database.Read(t.Context()), "abc123"); err != nil || target != (KOReaderHashTarget{}) {
		t.Fatalf("trashed-only hash target = %+v err=%v; want empty", target, err)
	}
}

func TestAssetKOReaderHashUsesCurrentBytes(t *testing.T) {
	oldHash := sha256.Sum256([]byte("downloaded bytes"))
	currentHash := sha256.Sum256([]byte("replacement bytes"))
	for _, tt := range []struct {
		name     string
		observed []byte
		cached   string
		want     string
	}{
		{"restored during download", oldHash[:], "", ""},
		{"rewritten during download", oldHash[:], "writeback-hash", "writeback-hash"},
		{"current download", currentHash[:], "", "download-hash"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book')")
			mustExec(t, database, `INSERT INTO assets
				(id, book_id, storage_path, filename, extension, original_sha256, current_sha256, koreader_hash)
				VALUES (1, 1, 'book.epub', 'book.epub', '.epub', ?, ?, NULLIF(?, ''))`, oldHash[:], currentHash[:], tt.cached)
			if err := database.CacheAssetKOReaderHash(t.Context(), 1, tt.observed, "download-hash"); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := database.Read(t.Context()).QueryRow("SELECT COALESCE(koreader_hash, '') FROM assets WHERE id = 1").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("cached hash = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestKOReaderAmbiguousHashSavesProviderStateWithoutAdvancingABook(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser(t.Context(), "alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES
			(1, 'First', 'First'),
			(2, 'Second', 'Second');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_sha256, current_sha256) VALUES
			(1, 1, 'first.epub', 'first.epub', '.epub', 'shared-hash', randomblob(32), randomblob(32)),
			(2, 2, 'second.epub', 'second.epub', '.epub', 'shared-hash', randomblob(32), randomblob(32))
	`)

	saved, change, err := database.SaveKOReaderProgressAndAdvanceStatus(context.Background(), user.ID, KOReaderProgress{
		DocumentHash: "shared-hash",
		Progress:     "chapter-4",
		Percentage:   0.6,
		Device:       "KOReader",
		DeviceID:     "device-a",
	})
	if err != nil || saved == nil || saved.Progress != "chapter-4" || change.Changed || change.State.BookID != 0 {
		t.Fatalf("ambiguous hash save = saved:%+v change:%+v err:%v", saved, change, err)
	}
	for _, bookID := range []int64{1, 2} {
		state, err := GetReadingStatus(database.Read(t.Context()), user.ID, bookID)
		if err != nil || state.Status != ReadingStatusUnread {
			t.Fatalf("ambiguous hash advanced %d: %+v, err %v", bookID, state, err)
		}
	}
}

func TestKOReaderProgressRoundTripAndIsolation(t *testing.T) {
	database := newTestDB(t)

	alice, err := database.CreateUser(t.Context(), "alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}
	bob, err := database.CreateUser(t.Context(), "bob", "pw", RoleMember)
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

	if _, err := GetKOReaderProgress(database.Read(t.Context()), bob.ID, "doc1"); !errors.Is(err, ErrKOReaderProgressNotFound) {
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

	user, err := database.CreateUser(t.Context(), "alice", "pw", RoleMember)
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
	user, err := database.CreateUser(t.Context(), "kosync-atomic", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (144, 'Atomic', 'Atomic');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_sha256, current_sha256)
		VALUES (1, 144, 'atomic.epub', 'atomic.epub', '.epub', 'atomic-hash', randomblob(32), randomblob(32));
		CREATE TRIGGER reject_kosync_status
		BEFORE INSERT ON user_book_reading_events
		BEGIN
			SELECT RAISE(ABORT, 'status write rejected');
		END;
	`)

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
	if _, err := GetKOReaderProgress(database.Read(t.Context()), user.ID, "atomic-hash"); !errors.Is(err, ErrKOReaderProgressNotFound) {
		t.Fatalf("KOSync position survived rolled-back status write: %v", err)
	}
}
