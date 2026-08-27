package db

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/levmv/polka/internal/format"
)

func TestAssetsByWorkIDsOrdersPrimaryFirst(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T1', 'T1')")
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension, format, is_primary, can_read) VALUES ('a_pdf', 'w1', 'books/a_pdf.pdf', 'a_pdf.pdf', '.pdf', 'pdf', 0, 1)")
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension, format, is_primary, can_read) VALUES ('a_epub', 'w1', 'books/a_epub.epub', 'a_epub.epub', '.epub', 'epub', 1, 1)")

	assets, err := AssetsByWorkIDs(database, []string{"w1"})
	if err != nil {
		t.Fatalf("AssetsByWorkIDs: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("assets len = %d; want 2", len(assets))
	}
	if assets[0].ID != "a_epub" || assets[0].Format != format.FormatEPUB || !assets[0].IsPrimary || !assets[0].CanRead {
		t.Fatalf("first asset = %+v; want primary EPUB first", assets[0])
	}
	if assets[1].ID != "a_pdf" || assets[1].Format != format.FormatPDF || assets[1].IsPrimary {
		t.Fatalf("second asset = %+v; want non-primary PDF second", assets[1])
	}
}

func TestPrimaryAssetForWork(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'T1', 'T1')")
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w2', 'T2', 'T2')")
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension, format, is_primary, can_read) VALUES ('a_pdf', 'w1', 'books/a_pdf.pdf', 'a_pdf.pdf', '.pdf', 'pdf', 0, 1)")
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension, format, is_primary, can_read) VALUES ('a_epub', 'w1', 'books/a_epub.epub', 'a_epub.epub', '.epub', 'epub', 1, 1)")

	asset, err := PrimaryAssetForWork(database, FullVisibilityScope(), "w1")
	if err != nil {
		t.Fatalf("PrimaryAssetForWork: %v", err)
	}
	if asset.ID != "a_epub" || asset.WorkID != "w1" || asset.Title != "T1" || asset.Format != format.FormatEPUB || !asset.IsPrimary || !asset.CanRead {
		t.Fatalf("primary asset = %+v; want a_epub for w1", asset)
	}

	if _, err := PrimaryAssetForWork(database, FullVisibilityScope(), "w2"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing primary err = %v, want sql.ErrNoRows", err)
	}
}
