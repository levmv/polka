package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAssetKOReaderHash(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T', 'T')")
	epubHash := sha256.Sum256([]byte("EPUB"))
	pdfHash := sha256.Sum256([]byte("PDF"))
	mustExec(t, database, `INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (1, 1, 'book.epub', 'book.epub', '.epub', ?, ?), (2, 1, 'book.pdf', 'book.pdf', '.pdf', ?, ?)`, epubHash[:], epubHash[:], pdfHash[:], pdfHash[:])

	if err := database.CacheAssetKOReaderHash(t.Context(), 1, epubHash[:], "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("CacheAssetKOReaderHash: %v", err)
	}
	target, err := ResolveKOReaderHash(database.Read(t.Context()), "0123456789abcdef0123456789abcdef")
	if err != nil || target.Ambiguous || target.AssetID != 1 || target.BookID != 1 {
		t.Fatalf("ResolveKOReaderHash = %+v err=%v; want asset1/1", target, err)
	}
	if target, err := ResolveKOReaderHash(database.Read(t.Context()), "missing"); err != nil || target != (KOReaderHashTarget{}) {
		t.Fatalf("missing hash target=%+v err=%v; want empty", target, err)
	}

	if err := database.CacheAssetKOReaderHash(t.Context(), 2, pdfHash[:], "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("CacheAssetKOReaderHash second same-book asset: %v", err)
	}
	target, err = ResolveKOReaderHash(database.Read(t.Context()), "0123456789abcdef0123456789abcdef")
	if err != nil || target.Ambiguous || target.AssetID != 1 || target.BookID != 1 {
		t.Fatalf("same-book hash target = %+v err=%v; want one unambiguous book", target, err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (2, 'Other', 'Other');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_sha256, current_sha256)
		VALUES (3, 2, 'other.epub', 'other.epub', '.epub', '0123456789abcdef0123456789abcdef', randomblob(32), randomblob(32));
		INSERT INTO koreader_hashes VALUES (3, unhex('0123456789abcdef0123456789abcdef'));
	`)

	target, err = ResolveKOReaderHash(database.Read(t.Context()), "0123456789abcdef0123456789abcdef")
	if err != nil || !target.Ambiguous || target.AssetID != 0 || target.BookID != 0 {
		t.Fatalf("cross-book hash target = %+v err=%v; want ambiguous without arbitrary target", target, err)
	}
	mustExec(t, database, "UPDATE books SET deleted_at = unixepoch() WHERE id = 2")

	target, err = ResolveKOReaderHash(database.Read(t.Context()), "0123456789abcdef0123456789abcdef")
	if err != nil || target.Ambiguous || target.AssetID != 1 || target.BookID != 1 {
		t.Fatalf("live hash target with trashed collision = %+v err=%v; want asset1/1", target, err)
	}
	mustExec(t, database, "UPDATE books SET deleted_at = unixepoch() WHERE id = 1")

	if target, err := ResolveKOReaderHash(database.Read(t.Context()), "0123456789abcdef0123456789abcdef"); err != nil || target != (KOReaderHashTarget{}) {
		t.Fatalf("trashed-only hash target = %+v err=%v; want empty", target, err)
	}
}

func TestAssetKOReaderHashOptionalWrite(t *testing.T) {
	database := newTestDB(t)
	hash := sha256.Sum256([]byte("EPUB"))
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book')")
	mustExec(t, database, `INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
		VALUES (1, 1, 'book.epub', 'book.epub', '.epub', ?, ?)`, hash[:], hash[:])
	cache := func(ctx context.Context) error {
		return database.CacheAssetKOReaderHash(ctx, 1, hash[:], "11111111111111111111111111111111")
	}
	tx, err := database.BeginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := cache(ctx); err != nil || ctx.Err() != nil {
		t.Fatalf("optional cache waited for the busy writer: %v, %v", err, ctx.Err())
	}
	cancel()
	if err := cache(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation was hidden: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	mustExec(t, database, `CREATE TRIGGER reject_hash BEFORE INSERT ON koreader_hashes
		BEGIN SELECT RAISE(ABORT, 'hash bookkeeping failed'); END`)
	if err := cache(t.Context()); err == nil || !strings.Contains(err.Error(), "hash bookkeeping failed") {
		t.Fatalf("database error was hidden: %v", err)
	}
	var cached string
	if err := database.Read(t.Context()).QueryRow("SELECT coalesce(koreader_hash, '') FROM assets WHERE id = 1").Scan(&cached); err != nil || cached != "" {
		t.Fatalf("failed bookkeeping left a partial cache: %q, %v", cached, err)
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
		{"rewritten during download", oldHash[:], "22222222222222222222222222222222", "22222222222222222222222222222222"},
		{"current download", currentHash[:], "", "11111111111111111111111111111111"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book')")
			mustExec(t, database, `INSERT INTO assets
				(id, book_id, storage_path, filename, extension, original_sha256, current_sha256, koreader_hash)
				VALUES (1, 1, 'book.epub', 'book.epub', '.epub', ?, ?, NULLIF(?, ''))`, oldHash[:], currentHash[:], tt.cached)
			if err := database.CacheAssetKOReaderHash(t.Context(), 1, tt.observed, "11111111111111111111111111111111"); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := database.Read(t.Context()).QueryRow("SELECT COALESCE(koreader_hash, '') FROM assets WHERE id = 1").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("cached hash = %q; want %q", got, tt.want)
			}
			var known bool
			if err := database.Read(t.Context()).QueryRow(`SELECT EXISTS (
                SELECT 1 FROM koreader_hashes WHERE asset_id=1 AND hash=unhex('11111111111111111111111111111111')
            )`).Scan(&known); err != nil {
				t.Fatal(err)
			}
			if !known {
				t.Fatal("lost the downloaded copy's hash during a concurrent replacement")
			}
		})
	}
}

func TestKOReaderAmbiguousHashSavesProviderStateWithoutAdvancingABook(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "alice", RoleMember)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES
			(1, 'First', 'First'),
			(2, 'Second', 'Second');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_sha256, current_sha256) VALUES
			(1, 1, 'first.epub', 'first.epub', '.epub', '33333333333333333333333333333333', randomblob(32), randomblob(32)),
			(2, 2, 'second.epub', 'second.epub', '.epub', '33333333333333333333333333333333', randomblob(32), randomblob(32));
		INSERT INTO koreader_hashes SELECT id, unhex(koreader_hash) FROM assets;
	`)

	saved, change, err := database.SaveKOReaderProgressAndAdvanceStatus(context.Background(), user.ID, KOReaderProgress{
		DocumentHash: "33333333333333333333333333333333",
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

	alice := mustUser(t, database, "alice", RoleMember)
	bob := mustUser(t, database, "bob", RoleMember)

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

	user := mustUser(t, database, "alice", RoleMember)

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
	user := mustUser(t, database, "kosync-atomic", RoleReader)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (144, 'Atomic', 'Atomic');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_sha256, current_sha256)
		VALUES (1, 144, 'atomic.epub', 'atomic.epub', '.epub', '44444444444444444444444444444444', randomblob(32), randomblob(32));
		INSERT INTO koreader_hashes SELECT id, unhex(koreader_hash) FROM assets;
		CREATE TRIGGER reject_kosync_status
		BEFORE INSERT ON user_book_reading_events
		BEGIN
			SELECT RAISE(ABORT, 'status write rejected');
		END;
	`)

	_, _, err := database.SaveKOReaderProgressAndAdvanceStatus(context.Background(), user.ID, KOReaderProgress{
		DocumentHash: "44444444444444444444444444444444",
		Progress:     "chapter-4",
		Percentage:   0.4,
		Device:       "KOReader",
		DeviceID:     "device-a",
	})
	if err == nil {
		t.Fatal("atomic KOSync save succeeded with rejecting status trigger")
	}
	if _, err := GetKOReaderProgress(database.Read(t.Context()), user.ID, "44444444444444444444444444444444"); !errors.Is(err, ErrKOReaderProgressNotFound) {
		t.Fatalf("KOSync position survived rolled-back status write: %v", err)
	}
}

func TestKOReaderHashesRecordDownloadsAndSurviveFileChanges(t *testing.T) {
	database := newTestDB(t)
	oldHash, newHash := strings.Repeat("aa", 16), strings.Repeat("bb", 16)
	original := sha256.Sum256([]byte("original book"))
	mustExec(t, database, `INSERT INTO books(id,title,sort_title) VALUES(1,'First','First')`)
	mustExec(t, database, `INSERT INTO assets(id,book_id,storage_path,filename,extension,original_sha256,current_sha256)
        VALUES(1,1,'first.epub','first.epub','.epub',?,?)`, original[:], original[:])
	assertHashes := func(wantCache string, wantCount int) {
		t.Helper()
		var cache string
		var count int
		if err := database.Read(t.Context()).QueryRow(`
            SELECT COALESCE(koreader_hash, ''), (SELECT count(*) FROM koreader_hashes WHERE asset_id=1)
            FROM assets WHERE id=1`).Scan(&cache, &count); err != nil {
			t.Fatal(err)
		}
		if cache != wantCache || count != wantCount {
			t.Fatalf("cache=%q, known hashes=%d; want %q, %d", cache, count, wantCache, wantCount)
		}
	}
	assertHashes("", 0)
	if err := database.CacheAssetKOReaderHash(t.Context(), 1, original[:], oldHash); err != nil {
		t.Fatal(err)
	}
	assertHashes(oldHash, 1)
	// Metadata-only intermediate versions never accumulate in the hash table.
	var rewritten [32]byte
	for _, content := range []string{"updated metadata", "another metadata edit"} {
		rewritten = sha256.Sum256([]byte(content))
		if err := database.Transact(t.Context(), func(tx *Tx) error {
			return MarkMetadataWritebackSuccess(tx, 1, "first.epub", rewritten[:], 100, 1)
		}); err != nil {
			t.Fatal(err)
		}
		assertHashes("", 1)
	}
	// A later download records the current version. Repeated downloads and an
	// unchanged write-back preserve the cache and do not duplicate associations.
	for range 2 {
		if err := database.CacheAssetKOReaderHash(t.Context(), 1, rewritten[:], newHash); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		return MarkMetadataWritebackSuccess(tx, 1, "first.epub", rewritten[:], 100, 2)
	}); err != nil {
		t.Fatal(err)
	}
	assertHashes(newHash, 2)
	if err := RecordAssetRestore(database.Write(t.Context()), 1, rewritten[:], 100); err != nil {
		t.Fatal(err)
	}
	assertHashes(newHash, 2)
	if err := RecordAssetRestore(database.Write(t.Context()), 1, original[:], 10); err != nil {
		t.Fatal(err)
	}
	assertHashes("", 2)
	if err := database.CacheAssetKOReaderHash(t.Context(), 1, original[:], oldHash); err != nil {
		t.Fatal(err)
	}
	assertHashes(oldHash, 2)
	// Both previously downloaded copies still identify the book after restore.
	for _, hash := range []string{oldHash, newHash} {
		target, err := ResolveKOReaderHash(database.Read(t.Context()), hash)
		if err != nil || target.BookID != 1 || target.Ambiguous {
			t.Fatalf("known hash %s: %+v %v", hash, target, err)
		}
	}
}
