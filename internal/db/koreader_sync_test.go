package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
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
	if err != nil || target.AssetID != 1 || target.BookID != 1 {
		t.Fatalf("ResolveKOReaderHash = %+v err=%v; want asset1/1", target, err)
	}
	if target, err := ResolveKOReaderHash(database.Read(t.Context()), "missing"); err != nil || target != (KOReaderHashTarget{}) {
		t.Fatalf("missing hash target=%+v err=%v; want empty", target, err)
	}

	if err := database.CacheAssetKOReaderHash(t.Context(), 2, pdfHash[:], "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("CacheAssetKOReaderHash second same-book asset: %v", err)
	}
	target, err = ResolveKOReaderHash(database.Read(t.Context()), "0123456789abcdef0123456789abcdef")
	if err != nil || target.AssetID != 0 || target.BookID != 1 {
		t.Fatalf("same-book hash target = %+v err=%v; want one book without an arbitrary asset", target, err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (2, 'Other', 'Other');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_sha256, current_sha256)
		VALUES (3, 2, 'other.epub', 'other.epub', '.epub', '0123456789abcdef0123456789abcdef', randomblob(32), randomblob(32));
		INSERT INTO koreader_hashes(asset_id, hash) VALUES (3, unhex('0123456789abcdef0123456789abcdef'));
	`)

	target, err = ResolveKOReaderHash(database.Read(t.Context()), "0123456789abcdef0123456789abcdef")
	if err != nil || target.AssetID != 0 || target.BookID != 0 {
		t.Fatalf("cross-book hash target = %+v err=%v; want ambiguous without arbitrary target", target, err)
	}
	mustExec(t, database, "UPDATE books SET deleted_at = unixepoch() WHERE id = 2")

	target, err = ResolveKOReaderHash(database.Read(t.Context()), "0123456789abcdef0123456789abcdef")
	if err != nil || target.AssetID != 0 || target.BookID != 1 {
		t.Fatalf("live hash target with trashed collision = %+v err=%v; want book 1 without an arbitrary asset", target, err)
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

func TestKOReaderExternalProgressRoundTripAndIsolation(t *testing.T) {
	database := newTestDB(t)
	alice := mustUser(t, database, "alice", RoleMember)
	bob := mustUser(t, database, "bob", RoleMember)
	const document = "11111111111111111111111111111111"
	save := func(address string, percentage float64, device string) *KOReaderState {
		t.Helper()
		saved, err := database.SaveKOReaderProgress(t.Context(), alice.ID,
			KOReaderProgress{DocumentHash: document, Position: address, Progress: percentage, DeviceName: "KOReader", DeviceID: device})
		if err != nil || saved == nil {
			t.Fatalf("save: %+v, %v", saved, err)
		}
		stored, err := GetExternalKOReaderState(database.Read(t.Context()), alice.ID, document)
		if err != nil || stored == nil || *stored != *saved {
			t.Fatalf("stored external position = %+v, %v; want %+v", stored, err, saved)
		}
		return saved
	}
	first := save("chapter-1", .25, "a")
	if first.Position != "chapter-1" || first.Progress != .25 || first.UpdatedAt == 0 {
		t.Fatalf("saved position = %+v", first)
	}
	if state, err := GetExternalKOReaderState(database.Read(t.Context()), bob.ID, document); err != nil || state != nil {
		t.Fatalf("another user's position = %+v, %v", state, err)
	}
	if state, err := GetExternalKOReaderState(database.Read(t.Context()), alice.ID, "other-document"); err != nil || state != nil {
		t.Fatalf("another document's position = %+v, %v", state, err)
	}
	second := save("chapter-3", .75, "b")
	mustExec(t, database, "UPDATE koreader_external_positions SET updated_at=100 WHERE user_id=?", alice.ID)
	second.UpdatedAt = 100
	replay := save("chapter-1", .27, "a") // Repagination is not further reading.
	if *replay != *second {
		t.Fatalf("sleeping reader undid another reader's progress: %+v", replay)
	}
	back := save("chapter-2", .5, "a")
	if back.Position != "chapter-2" || back.Progress != .5 || back.DeviceID != "a" {
		t.Fatalf("deliberate backward reading was lost: %+v", back)
	}
	if replay := save("chapter-3", .75, "b"); *replay != *back {
		t.Fatalf("the other sleeping reader restored its old position: %+v", replay)
	}
	mustExec(t, database, "DELETE FROM koreader_external_positions WHERE user_id=?", alice.ID)
	if restored := save("chapter-2", .5, "a"); restored.Position != "chapter-2" || restored.Progress != .5 {
		t.Fatalf("replay cache suppressed recreation of deleted state: %+v", restored)
	}
}

func TestKOReaderExternalPositionAdoption(t *testing.T) {
	for _, existing := range []string{"unopened", "opened", "finished", "positioned", "reset"} {
		t.Run(existing, func(t *testing.T) {
			database := newTestDB(t)
			user := mustUser(t, database, "reader", RoleMember)
			other := mustUser(t, database, "other", RoleMember)
			input := KOReaderProgress{DocumentHash: "11111111111111111111111111111111",
				Position: "chapter-1", Progress: .25, DeviceName: "KOReader", DeviceID: "reader-a"}
			if _, err := database.SaveKOReaderProgress(t.Context(), user.ID, input); err != nil {
				t.Fatal(err)
			}
			otherState, err := database.SaveKOReaderProgress(t.Context(), other.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			later := input
			later.Position, later.Progress, later.DeviceID = "chapter-3", .6, "reader b/c"
			if existing == "finished" {
				later.Progress = 1
			}
			external, err := database.SaveKOReaderProgress(t.Context(), user.ID, later)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, database, "UPDATE koreader_external_positions SET updated_at=100 WHERE user_id=?", user.ID)
			external.UpdatedAt = 100
			mustExec(t, database, `INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book');
                INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
                VALUES (1, 1, 'book.epub', 'book.epub', '.epub', randomblob(32), randomblob(32));
                INSERT INTO koreader_hashes(asset_id, hash) VALUES (1, unhex('11111111111111111111111111111111'));`)
			switch existing {
			case "opened":
				err = database.TouchReader(t.Context(), user.ID, 1, ReadingStatusSourceWebReader)
			case "positioned":
				_, _, err = database.SaveReaderState(t.Context(), user.ID, 1,
					testReaderWrite(.8, Locator{CFI: "epubcfi(/6/4)"}, 0), ReadingStatusSourceWebReader)
			case "reset":
				err = database.ResetReaderState(t.Context(), user.ID, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			// The older reader repeats its last upload after the book becomes known.
			// Adoption must preserve the latest external observation, or the library's position.
			got, err := database.SaveKOReaderProgress(t.Context(), user.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			after, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
			if err != nil || got == nil || *got != *after.KOReaderState() {
				t.Fatalf("adopted position = %+v, %v; response %+v", after, err, got)
			}
			remaining, err := GetExternalKOReaderState(database.Read(t.Context()), user.ID, input.DocumentHash)
			if err != nil {
				t.Fatal(err)
			}
			if before.Revision == 0 {
				if *got != *external || after.Revision != 1 || remaining != nil {
					t.Fatalf("external position was not transferred intact: %+v; remaining %+v", after, remaining)
				}
			} else if !reflect.DeepEqual(after, before) {
				t.Fatalf("external position replaced library state: %+v; want %+v", after, before)
			}
			wantStatus := ReadingStatusReading
			if existing == "finished" {
				wantStatus = ReadingStatusFinished
			} else if existing == "reset" {
				wantStatus = ReadingStatusUnread
			}
			if status, err := GetReadingStatus(database.Read(t.Context()), user.ID, 1); err != nil || status.Status != wantStatus {
				t.Fatalf("adopted reading status = %+v, %v; want %s", status, err, wantStatus)
			}
			if got, err := GetExternalKOReaderState(database.Read(t.Context()), other.ID, input.DocumentHash); err != nil || got == nil || *got != *otherState {
				t.Fatalf("adoption changed another user's position: %+v, %v", got, err)
			}
		})
	}
}

func TestKOReaderProgressValidation(t *testing.T) {
	database := newTestDB(t)

	user := mustUser(t, database, "alice", RoleMember)

	cases := []struct {
		name string
		p    KOReaderProgress
	}{
		{"missing document", KOReaderProgress{Position: "p", Progress: 0.1, DeviceName: "d"}},
		{"missing position", KOReaderProgress{DocumentHash: "doc", Progress: 0.1, DeviceName: "d"}},
		{"missing device", KOReaderProgress{DocumentHash: "doc", Position: "p", Progress: 0.1}},
		{"low percentage", KOReaderProgress{DocumentHash: "doc", Position: "p", Progress: -0.1, DeviceName: "d"}},
		{"high percentage", KOReaderProgress{DocumentHash: "doc", Position: "p", Progress: 1.1, DeviceName: "d"}},
		{"long document", KOReaderProgress{DocumentHash: strings.Repeat("x", 257), Position: "p", Progress: 0.1, DeviceName: "d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := database.SaveKOReaderProgress(context.Background(), user.ID, tc.p)
			if !errors.Is(err, ErrKOReaderInvalidInput) {
				t.Fatalf("SaveKOReaderProgress err = %v; want invalid input", err)
			}
		})
	}
}

func TestKOReaderProgressAndStatusCommitTogether(t *testing.T) {
	for _, origin := range []string{"new position", "external position", "external replay"} {
		t.Run(origin, func(t *testing.T) {
			database := newTestDB(t)
			user := mustUser(t, database, "kosync-atomic", RoleReader)
			input := KOReaderProgress{DocumentHash: "44444444444444444444444444444444",
				Position: "chapter-1", Progress: .2, DeviceName: "KOReader", DeviceID: "device-a"}
			var external *KOReaderState
			var err error
			if origin != "new position" {
				external, err = database.SaveKOReaderProgress(t.Context(), user.ID, input)
				if err != nil {
					t.Fatal(err)
				}
			}
			mustExec(t, database, `
                INSERT INTO books (id, title, sort_title) VALUES (144, 'Atomic', 'Atomic');
                INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
                VALUES (1, 144, 'atomic.epub', 'atomic.epub', '.epub', randomblob(32), randomblob(32));
                INSERT INTO koreader_hashes(asset_id, hash) VALUES (1, unhex('44444444444444444444444444444444'));
                CREATE TRIGGER reject_kosync_status BEFORE INSERT ON user_book_reading_events
                BEGIN SELECT RAISE(ABORT, 'status write rejected'); END;`)
			if origin != "external replay" {
				input.Position, input.Progress = "chapter-4", .4
			}
			if _, err := database.SaveKOReaderProgress(t.Context(), user.ID, input); err == nil {
				t.Fatal("atomic KOSync save succeeded with rejecting status trigger")
			}
			if state, err := GetReaderState(database.Read(t.Context()), user.ID, 1); err != nil || state.Revision != 0 {
				t.Fatalf("failed native save left a shared position: %+v, %v", state, err)
			}
			if got, err := GetExternalKOReaderState(database.Read(t.Context()), user.ID, input.DocumentHash); err != nil || !reflect.DeepEqual(got, external) {
				t.Fatalf("failed transfer lost the external position: %+v, %v; want %+v", got, err, external)
			}
			mustExec(t, database, "DROP TRIGGER reject_kosync_status")
			saved, err := database.SaveKOReaderProgress(t.Context(), user.ID, input)
			if err != nil || saved == nil || saved.Position != input.Position || saved.Progress != input.Progress {
				t.Fatalf("failed save became a cached replay: %+v, %v", saved, err)
			}
			state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
			if err != nil || state.Revision != 1 || state.KOReaderPosition != input.Position || state.Progress != input.Progress {
				t.Fatalf("retry did not persist the shared position: %+v, %v", state, err)
			}
			if remaining, err := GetExternalKOReaderState(database.Read(t.Context()), user.ID, input.DocumentHash); err != nil || remaining != nil {
				t.Fatalf("successful transfer left the external position: %+v, %v", remaining, err)
			}
		})
	}
}

func TestKOReaderDownloadsShareAssetPosition(t *testing.T) {
	const oldHash = "11111111111111111111111111111111"
	const newHash = "22222222222222222222222222222222"
	const convertedHash = "33333333333333333333333333333333"
	for _, download := range []struct{ name, hash string }{
		{"older original", oldHash}, {"new original", newHash}, {"converted", convertedHash},
	} {
		t.Run(download.name, func(t *testing.T) {
			database := newTestDB(t)
			user := mustUser(t, database, "reader", RoleMember)
			mustExec(t, database, `INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book');
                INSERT INTO assets (id, book_id, storage_path, filename, extension, format, koreader_hash, original_sha256, current_sha256)
                VALUES (1, 1, 'book.epub', 'book.epub', '.epub', 'epub', ?, randomblob(32), randomblob(32));`, newHash)
			mustExec(t, database, `INSERT INTO koreader_hashes(asset_id, hash, conversion)
                VALUES (1, unhex(?), ''), (1, unhex(?), ''), (1, unhex(?), 'kepub');`, oldHash, newHash, convertedHash)
			save := func(input KOReaderProgress) *ReaderState {
				t.Helper()
				response, err := database.SaveKOReaderProgress(t.Context(), user.ID, input)
				if err != nil {
					t.Fatal(err)
				}
				state, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
				if err != nil || response == nil || *response != *state.KOReaderState() {
					t.Fatalf("library position = %+v, %v; response %+v", state, err, response)
				}
				return state
			}
			native := KOReaderProgress{DocumentHash: download.hash, Position: "chapter-2", Progress: .4, DeviceName: "KOReader", DeviceID: "reader-a"}
			advanced := save(native)
			if advanced.Revision != 1 || advanced.Progress != .4 {
				t.Fatalf("reading downloaded copy = %+v", advanced)
			}
			web, _, err := database.SaveReaderState(t.Context(), user.ID, 1,
				testReaderWrite(.8, Locator{CFI: "epubcfi(/6/4)"}, advanced.Revision), ReadingStatusSourceWebReader)
			if err != nil {
				t.Fatal(err)
			}
			repeated := save(native)
			if !reflect.DeepEqual(repeated, web) {
				t.Fatalf("old copy's unchanged upload displaced web reading: %+v", repeated)
			}
			native.DocumentHash, native.Position, native.Progress = oldHash, "chapter-3", .6
			back := save(native)
			if back.Revision != web.Revision+1 || back.Progress != .6 || !back.Locator.IsZero() {
				t.Fatalf("backward reading in old copy = %+v", back)
			}
		})
	}
}

func TestKOReaderHashCollisionPreservesExistingAssetPosition(t *testing.T) {
	for _, bookID := range []int64{1, 2} {
		database := newTestDB(t)
		user := mustUser(t, database, "reader", RoleMember)
		const hash = "11111111111111111111111111111111"
		mustExec(t, database, `INSERT INTO books(id,title,sort_title) VALUES(1,'First','First'),(2,'Second','Second');
            INSERT INTO assets(id,book_id,storage_path,filename,extension,original_sha256,current_sha256)
            VALUES(1,1,'book.epub','book.epub','.epub',randomblob(32),randomblob(32));
            INSERT INTO koreader_hashes(asset_id, hash) VALUES(1,unhex('11111111111111111111111111111111'));`)
		input := KOReaderProgress{DocumentHash: hash, Position: "chapter-1", Progress: .2, DeviceName: "Reader", DeviceID: "a"}
		if _, err := database.SaveKOReaderProgress(t.Context(), user.ID, input); err != nil {
			t.Fatal(err)
		}
		first, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, database, `INSERT INTO assets(id,book_id,storage_path,filename,extension,original_sha256,current_sha256)
            VALUES(2,?,'other.epub','other.epub','.epub',randomblob(32),randomblob(32));
            INSERT INTO koreader_hashes(asset_id, hash) VALUES(2,unhex('11111111111111111111111111111111'));`, bookID)
		input.Position, input.Progress = "chapter-2", 1
		external, err := database.SaveKOReaderProgress(t.Context(), user.ID, input)
		if err != nil || external.Position != input.Position || external.Progress != 1 {
			t.Fatalf("ambiguous document = %+v, %v", external, err)
		}
		if got, err := GetExternalKOReaderState(database.Read(t.Context()), user.ID, hash); err != nil || got == nil || *got != *external {
			t.Fatalf("ambiguous document's external position = %+v, %v", got, err)
		}
		current, err := GetReaderState(database.Read(t.Context()), user.ID, 1)
		if err != nil || !reflect.DeepEqual(current, first) {
			t.Fatalf("hash collision changed existing library reading: %+v, %v", current, err)
		}
		other, err := GetReaderState(database.Read(t.Context()), user.ID, 2)
		if err != nil || other.Revision != 0 {
			t.Fatalf("hash collision picked the other asset: %+v, %v", other, err)
		}
		wantStatus := ReadingStatusReading
		if bookID == 1 {
			wantStatus = ReadingStatusFinished
		}
		for id, want := range map[int64]string{1: wantStatus, 2: ReadingStatusUnread} {
			status, err := GetReadingStatus(database.Read(t.Context()), user.ID, id)
			if err != nil || status.Status != want {
				t.Fatalf("book %d status after collision = %+v, %v; want %s", id, status, err, want)
			}
		}
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
		if err != nil || target.BookID != 1 {
			t.Fatalf("known hash %s: %+v %v", hash, target, err)
		}
	}
}
