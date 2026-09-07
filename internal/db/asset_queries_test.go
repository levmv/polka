package db

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/levmv/polka/internal/format"
)

func TestAssetsByBookIDsOrdersPrimaryFirst(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T1', 'T1')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (1, 1, 'books/a_pdf.pdf', 'a_pdf.pdf', '.pdf', 'pdf', 0, 1, randomblob(32), randomblob(32))")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (2, 1, 'books/a_epub.epub', 'a_epub.epub', '.epub', 'epub', 1, 1, randomblob(32), randomblob(32))")

	assets, err := AssetsByBookIDs(database.Read(t.Context()), []int64{1})
	if err != nil {
		t.Fatalf("AssetsByBookIDs: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("assets len = %d; want 2", len(assets))
	}
	if assets[0].ID != 2 || assets[0].Format != format.FormatEPUB || !assets[0].IsPrimary || !assets[0].CanRead {
		t.Fatalf("first asset = %+v; want primary EPUB first", assets[0])
	}
	if assets[1].ID != 1 || assets[1].Format != format.FormatPDF || assets[1].IsPrimary {
		t.Fatalf("second asset = %+v; want non-primary PDF second", assets[1])
	}
}

func TestPrimaryAssetForBook(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'T1', 'T1')")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (2, 'T2', 'T2')")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, original_sha256, current_sha256) VALUES (1, 1, 'books/a_pdf.pdf', 'a_pdf.pdf', '.pdf', 'pdf', 0, 1, randomblob(32), randomblob(32))")
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, can_read, current_sha256, original_sha256) VALUES (2, 1, 'books/a_epub.epub', 'a_epub.epub', '.epub', 'epub', 1, 1, X'0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', randomblob(32))")

	asset, err := PrimaryAssetForBook(database.Read(t.Context()), FullVisibilityScope(), 1)
	if err != nil {
		t.Fatalf("PrimaryAssetForBook: %v", err)
	}
	if asset.ID != 2 || asset.BookID != 1 || asset.Title != "T1" || asset.Format != format.FormatEPUB || !asset.CanRead {
		t.Fatalf("primary asset = %+v; want a_epub for 1", asset)
	}
	if hex.EncodeToString(asset.CurrentSHA256) != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("primary asset current SHA-256 = %q", asset.CurrentSHA256)
	}

	if _, err := PrimaryAssetForBook(database.Read(t.Context()), FullVisibilityScope(), 2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing primary err = %v, want sql.ErrNoRows", err)
	}
}
