package db

import (
	"bytes"
	"testing"
)

func TestPageCountBelongsToAsset(t *testing.T) {
	database := newTestDB(t)
	hash := bytes.Repeat([]byte{1}, 32)
	mustExec(t, database, `INSERT INTO books (id, title, sort_title, metadata_rev) VALUES (1, 'Book', 'Book', 4)`)
	mustExec(t, database, `INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256, writeback_rev) VALUES
		(10, 1, 'book.epub', 'book.epub', '.epub', 'epub', ?, ?, 4),
		(11, 1, 'other.epub', 'other.epub', '.epub', 'epub', randomblob(32), randomblob(32), 4)`, hash, hash)
	store := func(identity []byte, pages int) bool {
		t.Helper()
		saved, err := StorePageCount(database.Write(t.Context()), 10, identity, pages)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	if store(bytes.Repeat([]byte{2}, 32), 100) {
		t.Fatal("stored a count for obsolete bytes")
	}
	if !store(hash, 123) || store(hash, 456) {
		t.Fatal("missing count must be filled exactly once")
	}
	snap, err := LoadMetadataWritebackSnapshot(database.Read(t.Context()), 10)
	if err != nil {
		t.Fatal(err)
	}
	other, err := LoadMetadataWritebackSnapshot(database.Read(t.Context()), 11)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Metadata.PageCount != 123 || other.Metadata.PageCount != 0 {
		t.Fatalf("per-asset snapshots: %+v; %+v", snap, other)
	}
	mustExec(t, database, `UPDATE assets SET page_count = NULL WHERE id = 10`)
	mustExec(t, database, `UPDATE books SET deleted_at = unixepoch() WHERE id = 1`)
	if store(hash, 7) {
		t.Fatal("stored a count for a trashed book")
	}
}
