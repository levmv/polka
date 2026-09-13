package db

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
)

func TestMetadataWritebackDirtyQuery(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, metadata_rev) VALUES (127, 'EPUB', 'EPUB', 2)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, metadata_rev) VALUES (165, 'PDF', 'PDF', 2)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, metadata_rev, deleted_at) VALUES (122, 'Deleted', 'Deleted', 2, 10)")
	mustExec(t, database, `
		INSERT INTO assets (id, book_id, storage_path, filename, extension, format, writeback_rev, writeback_error, original_sha256, current_sha256)
		VALUES
			(1, 127, 'A/EPUB/as_epub_dirty.epub', 'as_epub_dirty.epub', '.epub', 'epub', 1, NULL, randomblob(32), randomblob(32)),
			(2, 127, 'A/EPUB/as_kepub_dirty.kepub.epub', 'as_kepub_dirty.kepub.epub', '.kepub.epub', 'kepub', 1, NULL, randomblob(32), randomblob(32)),
			(3, 127, 'A/EPUB/as_epub_failed.epub', 'as_epub_failed.epub', '.epub', 'epub', 0, 'bad opf', randomblob(32), randomblob(32)),
			(4, 127, 'A/EPUB/as_epub_clean.epub', 'as_epub_clean.epub', '.epub', 'epub', 2, NULL, randomblob(32), randomblob(32)),
			(5, 165, 'A/PDF/as_pdf_dirty.pdf', 'as_pdf_dirty.pdf', '.pdf', 'pdf', 0, NULL, randomblob(32), randomblob(32)),
			(6, 122, 'A/Deleted/as_deleted_dirty.epub', 'as_deleted_dirty.epub', '.epub', 'epub', 0, NULL, randomblob(32), randomblob(32))
	`)

	counts, err := CountDirtyMetadataWritebackAssets(database.Read(t.Context()), FullVisibilityScope())
	if err != nil {
		t.Fatalf("CountDirtyMetadataWritebackAssets: %v", err)
	}
	if counts.Dirty != 3 || counts.Failed != 1 {
		t.Fatalf("counts = %+v; want dirty=3 failed=1", counts)
	}

	rows, err := ListDirtyMetadataWritebackAssets(database.Read(t.Context()), FullVisibilityScope(), 0)
	if err != nil {
		t.Fatalf("ListDirtyMetadataWritebackAssets: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d; want 3", len(rows))
	}
	got := map[int64]MetadataWritebackAssetRow{}
	for _, row := range rows {
		got[row.AssetID] = row
	}
	if got[1].MetadataRev != 2 || got[1].WritebackRev != 1 {
		t.Fatalf("dirty row = %+v; want rev 2/1", got[1])
	}
	if got[2].Format != format.FormatKEPUB || got[2].MetadataRev != 2 {
		t.Fatalf("kepub dirty row = %+v; want kepub rev 2", got[2])
	}
	if got[3].Error != "bad opf" {
		t.Fatalf("failed row = %+v; want error", got[3])
	}

	limited, err := ListDirtyMetadataWritebackAssets(database.Read(t.Context()), FullVisibilityScope(), 1)
	if err != nil {
		t.Fatalf("limited ListDirtyMetadataWritebackAssets: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited rows = %d; want 1", len(limited))
	}

	failed, err := ListFailedMetadataWritebackAssets(database.Read(t.Context()), FullVisibilityScope(), 0)
	if err != nil {
		t.Fatalf("ListFailedMetadataWritebackAssets: %v", err)
	}
	if len(failed) != 1 || failed[0].AssetID != 3 {
		t.Fatalf("failed rows = %+v; want as_epub_failed only", failed)
	}
	mustExec(t, database, "UPDATE assets SET updated_at = 100 WHERE id = 3")

	automatic, err := ListAutomaticMetadataWritebackAssets(database.Read(t.Context()), FullVisibilityScope(), 99, 10)
	if err != nil {
		t.Fatalf("ListAutomaticMetadataWritebackAssets fresh failure: %v", err)
	}
	if len(automatic) != 2 {
		t.Fatalf("automatic fresh-failure rows = %+v; want two non-failed dirty assets", automatic)
	}
	for _, row := range automatic {
		if row.AssetID == 3 {
			t.Fatalf("fresh failed asset selected automatically: %+v", automatic)
		}
	}
	automatic, err = ListAutomaticMetadataWritebackAssets(database.Read(t.Context()), FullVisibilityScope(), 100, 10)
	if err != nil {
		t.Fatalf("ListAutomaticMetadataWritebackAssets due failure: %v", err)
	}
	if len(automatic) != 3 {
		t.Fatalf("automatic due-failure rows = %+v; want all three dirty assets", automatic)
	}
	automatic, err = ListAutomaticMetadataWritebackAssets(database.Read(t.Context()), FullVisibilityScope(), 100, 1)
	if err != nil {
		t.Fatalf("ListAutomaticMetadataWritebackAssets bounded: %v", err)
	}
	if len(automatic) != 1 {
		t.Fatalf("bounded automatic rows = %d; want SQL limit 1", len(automatic))
	}
}

func TestMetadataWritebackSnapshotAndAttempt(t *testing.T) {
	database := newTestDB(t)
	oldHash := bytes.Repeat([]byte{1}, 32)
	newHash := bytes.Repeat([]byte{2}, 32)
	mustExec(t, database, `
		INSERT INTO books
			(id, title, sort_title, series, series_index, description, tags, publisher, published_date, language, identifiers, metadata_rev)
		VALUES
			(1, 'Snapshot Title', 'Title, Snapshot', 'Series', 2, 'Desc', 'tag one, tag two', 'Press', '2026', 'eng', 'isbn:9780306406157', 4)
	`)
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Jane Writer', 'Writer, Jane')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, role, author_order) VALUES (1, 1, 'aut', 0)")
	mustExec(t, database, `
		INSERT INTO assets
			(id, book_id, storage_path, filename, extension, format, current_sha256, current_size, writeback_rev, original_sha256)
		VALUES
			(1, 1, 'A/Book/as1.epub', 'as1.epub', '.epub', 'epub', ?, 123, 2, randomblob(32))
	`, oldHash)

	snap, err := LoadMetadataWritebackSnapshot(database.Read(t.Context()), 1)
	if err != nil {
		t.Fatalf("LoadMetadataWritebackSnapshot: %v", err)
	}
	if snap.MetadataRev != 4 || snap.Metadata.Title != "Snapshot Title" || snap.Metadata.SortTitle != "Title, Snapshot" {
		t.Fatalf("snapshot basics = %+v", snap)
	}
	if len(snap.Metadata.Authors) != 1 || snap.Metadata.Authors[0].Name != "Jane Writer" || snap.Metadata.Authors[0].SortName != "Writer, Jane" || snap.Metadata.Authors[0].Role != "aut" {
		t.Fatalf("snapshot authors = %+v", snap.Metadata.Authors)
	}
	if strings.Join(snap.Metadata.Tags, "|") != "tag one|tag two" {
		t.Fatalf("snapshot tags = %v", snap.Metadata.Tags)
	}

	row, err := GetMetadataWritebackAsset(database.Read(t.Context()), 1)
	if err != nil {
		t.Fatalf("GetMetadataWritebackAsset: %v", err)
	}
	if !bytes.Equal(row.CurrentSHA256, oldHash) || !row.CurrentSize.Valid || row.CurrentSize.Int64 != 123 {
		t.Fatalf("asset current identity = %q/%+v", row.CurrentSHA256, row.CurrentSize)
	}

	attempt := MetadataWritebackAttempt{
		AssetID:     1,
		MetadataRev: 4,
		StoragePath: "A/Book/as1.epub",
		TempPath:    "A/Book/.writeback-as1-rev4.tmp",
		SHA256:      newHash,
		Size:        456,
	}
	if err := UpsertMetadataWritebackAttempt(database.Write(t.Context()), attempt); err != nil {
		t.Fatalf("UpsertMetadataWritebackAttempt: %v", err)
	}
	if err := database.Transact(context.Background(), func(tx *Tx) error {
		return MarkMetadataWritebackSuccess(tx, 1, "A/Book/as1.epub", newHash, 456, 4)
	}); err != nil {
		t.Fatalf("MarkMetadataWritebackSuccess: %v", err)
	}

	var currentHash []byte
	var koHash string
	var currentSize, writebackRev int64
	var writebackError sql.NullString
	if err := database.Read(t.Context()).QueryRow(`
		SELECT current_sha256, current_size, COALESCE(koreader_hash, ''), writeback_rev, writeback_error
		FROM assets WHERE id = 1
	`).Scan(&currentHash, &currentSize, &koHash, &writebackRev, &writebackError); err != nil {
		t.Fatalf("query success asset: %v", err)
	}
	if !bytes.Equal(currentHash, newHash) || currentSize != 456 || koHash != "" || writebackRev != 4 || writebackError.Valid {
		t.Fatalf("success asset = %x/%d/%q/%d/%+v", currentHash, currentSize, koHash, writebackRev, writebackError)
	}
	var pending int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM metadata_writeback_attempts WHERE asset_id = 1").Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending attempts = %d; want 0", pending)
	}
}
