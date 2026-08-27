package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/format"
)

func TestMetadataWritebackDirtyQuery(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title, metadata_rev) VALUES ('w_epub', 'EPUB', 'EPUB', 2)")
	database.Exec("INSERT INTO works (id, title, sort_title, metadata_rev) VALUES ('w_pdf', 'PDF', 'PDF', 2)")
	database.Exec("INSERT INTO works (id, title, sort_title, metadata_rev, deleted_at) VALUES ('w_deleted', 'Deleted', 'Deleted', 2, 10)")
	database.Exec(`
		INSERT INTO assets (id, work_id, storage_path, filename, extension, format, writeback_rev, writeback_error)
		VALUES
			('as_epub_dirty', 'w_epub', 'A/EPUB/as_epub_dirty.epub', 'as_epub_dirty.epub', '.epub', 'epub', 1, NULL),
			('as_kepub_dirty', 'w_epub', 'A/EPUB/as_kepub_dirty.kepub.epub', 'as_kepub_dirty.kepub.epub', '.kepub.epub', 'kepub', 1, NULL),
			('as_epub_failed', 'w_epub', 'A/EPUB/as_epub_failed.epub', 'as_epub_failed.epub', '.epub', 'epub', 0, 'bad opf'),
			('as_epub_clean', 'w_epub', 'A/EPUB/as_epub_clean.epub', 'as_epub_clean.epub', '.epub', 'epub', 2, NULL),
			('as_pdf_dirty', 'w_pdf', 'A/PDF/as_pdf_dirty.pdf', 'as_pdf_dirty.pdf', '.pdf', 'pdf', 0, NULL),
			('as_deleted_dirty', 'w_deleted', 'A/Deleted/as_deleted_dirty.epub', 'as_deleted_dirty.epub', '.epub', 'epub', 0, NULL)
	`)

	counts, err := CountDirtyMetadataWritebackAssets(database, FullVisibilityScope())
	if err != nil {
		t.Fatalf("CountDirtyMetadataWritebackAssets: %v", err)
	}
	if counts.Dirty != 3 || counts.Failed != 1 {
		t.Fatalf("counts = %+v; want dirty=3 failed=1", counts)
	}

	rows, err := ListDirtyMetadataWritebackAssets(database, FullVisibilityScope(), 0)
	if err != nil {
		t.Fatalf("ListDirtyMetadataWritebackAssets: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d; want 3", len(rows))
	}
	got := map[string]MetadataWritebackAssetRow{}
	for _, row := range rows {
		got[row.AssetID] = row
	}
	if got["as_epub_dirty"].MetadataRev != 2 || got["as_epub_dirty"].WritebackRev != 1 {
		t.Fatalf("dirty row = %+v; want rev 2/1", got["as_epub_dirty"])
	}
	if got["as_epub_dirty"].CurrentSHA256 != "" || got["as_epub_dirty"].CurrentSize.Valid {
		t.Fatalf("dirty row current identity = %q/%+v; want empty/null", got["as_epub_dirty"].CurrentSHA256, got["as_epub_dirty"].CurrentSize)
	}
	if got["as_kepub_dirty"].Format != format.FormatKEPUB || got["as_kepub_dirty"].MetadataRev != 2 {
		t.Fatalf("kepub dirty row = %+v; want kepub rev 2", got["as_kepub_dirty"])
	}
	if got["as_epub_failed"].Error != "bad opf" {
		t.Fatalf("failed row = %+v; want error", got["as_epub_failed"])
	}

	limited, err := ListDirtyMetadataWritebackAssets(database, FullVisibilityScope(), 1)
	if err != nil {
		t.Fatalf("limited ListDirtyMetadataWritebackAssets: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited rows = %d; want 1", len(limited))
	}

	failed, err := ListFailedMetadataWritebackAssets(database, FullVisibilityScope(), 0)
	if err != nil {
		t.Fatalf("ListFailedMetadataWritebackAssets: %v", err)
	}
	if len(failed) != 1 || failed[0].AssetID != "as_epub_failed" {
		t.Fatalf("failed rows = %+v; want as_epub_failed only", failed)
	}
	if _, err := database.Exec("UPDATE assets SET updated_at = 100 WHERE id = 'as_epub_failed'"); err != nil {
		t.Fatalf("set failure timestamp: %v", err)
	}
	automatic, err := ListAutomaticMetadataWritebackAssets(database, FullVisibilityScope(), 99, 10)
	if err != nil {
		t.Fatalf("ListAutomaticMetadataWritebackAssets fresh failure: %v", err)
	}
	if len(automatic) != 2 {
		t.Fatalf("automatic fresh-failure rows = %+v; want two non-failed dirty assets", automatic)
	}
	for _, row := range automatic {
		if row.AssetID == "as_epub_failed" {
			t.Fatalf("fresh failed asset selected automatically: %+v", automatic)
		}
	}
	automatic, err = ListAutomaticMetadataWritebackAssets(database, FullVisibilityScope(), 100, 10)
	if err != nil {
		t.Fatalf("ListAutomaticMetadataWritebackAssets due failure: %v", err)
	}
	if len(automatic) != 3 {
		t.Fatalf("automatic due-failure rows = %+v; want all three dirty assets", automatic)
	}
	automatic, err = ListAutomaticMetadataWritebackAssets(database, FullVisibilityScope(), 100, 1)
	if err != nil {
		t.Fatalf("ListAutomaticMetadataWritebackAssets bounded: %v", err)
	}
	if len(automatic) != 1 {
		t.Fatalf("bounded automatic rows = %d; want SQL limit 1", len(automatic))
	}
}

func TestMetadataWritebackSnapshotAndAttempt(t *testing.T) {
	database := newTestDB(t)

	database.Exec(`
		INSERT INTO works
			(id, title, sort_title, series, series_index, description, tags, publisher, published_date, language, identifiers, metadata_rev)
		VALUES
			('w1', 'Snapshot Title', 'Title, Snapshot', 'Series', 2, 'Desc', 'tag one, tag two', 'Press', '2026', 'eng', 'isbn:9780306406157', 4)
	`)
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('au1', 'Jane Writer', 'Writer, Jane')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, role, author_order) VALUES ('w1', 'au1', 'aut', 0)")
	database.Exec(`
		INSERT INTO assets
			(id, work_id, storage_path, filename, extension, format, current_sha256, current_size, writeback_rev)
		VALUES
			('as1', 'w1', 'A/Book/as1.epub', 'as1.epub', '.epub', 'epub', 'oldhash', 123, 2)
	`)

	snap, err := LoadMetadataWritebackSnapshot(database, "w1")
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

	row, err := GetMetadataWritebackAsset(database, "as1")
	if err != nil {
		t.Fatalf("GetMetadataWritebackAsset: %v", err)
	}
	if row.CurrentSHA256 != "oldhash" || !row.CurrentSize.Valid || row.CurrentSize.Int64 != 123 {
		t.Fatalf("asset current identity = %q/%+v", row.CurrentSHA256, row.CurrentSize)
	}

	attempt := MetadataWritebackAttempt{
		AssetID:      "as1",
		MetadataRev:  4,
		StoragePath:  "A/Book/as1.epub",
		TempPath:     "A/Book/.writeback-as1-rev4.tmp",
		SHA256:       "newhash",
		Size:         456,
		KOReaderHash: "kohash",
	}
	if err := UpsertMetadataWritebackAttempt(database, attempt); err != nil {
		t.Fatalf("UpsertMetadataWritebackAttempt: %v", err)
	}
	if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		return MarkMetadataWritebackSuccess(tx, "as1", "A/Book/as1.epub", "newhash", 456, "kohash", 4)
	}); err != nil {
		t.Fatalf("MarkMetadataWritebackSuccess: %v", err)
	}

	var currentHash, koHash string
	var currentSize, writebackRev int64
	var writebackError sql.NullString
	if err := database.QueryRow(`
		SELECT current_sha256, current_size, koreader_hash, writeback_rev, writeback_error
		FROM assets WHERE id = 'as1'
	`).Scan(&currentHash, &currentSize, &koHash, &writebackRev, &writebackError); err != nil {
		t.Fatalf("query success asset: %v", err)
	}
	if currentHash != "newhash" || currentSize != 456 || koHash != "kohash" || writebackRev != 4 || writebackError.Valid {
		t.Fatalf("success asset = %q/%d/%q/%d/%+v", currentHash, currentSize, koHash, writebackRev, writebackError)
	}
	var pending int
	if err := database.QueryRow("SELECT COUNT(*) FROM metadata_writeback_attempts WHERE asset_id = 'as1'").Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending attempts = %d; want 0", pending)
	}
}

func TestBumpMetadataRev(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title, metadata_rev) VALUES ('w1', 'One', 'One', 3)")
	database.Exec("INSERT INTO works (id, title, sort_title, metadata_rev) VALUES ('w2', 'Two', 'Two', 7)")

	if err := BumpMetadataRev(database, []string{"w1", "w1", "", "w2"}); err != nil {
		t.Fatalf("BumpMetadataRev: %v", err)
	}

	var rev1, rev2 int
	if err := database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w1'").Scan(&rev1); err != nil {
		t.Fatalf("query w1 rev: %v", err)
	}
	if err := database.QueryRow("SELECT metadata_rev FROM works WHERE id = 'w2'").Scan(&rev2); err != nil {
		t.Fatalf("query w2 rev: %v", err)
	}
	if rev1 != 4 || rev2 != 8 {
		t.Fatalf("metadata revs = %d/%d; want 4/8", rev1, rev2)
	}
}
