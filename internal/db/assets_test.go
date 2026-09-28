package db

import (
	"strconv"
	"testing"
)

func TestEnsurePreferredPrimaryAsset(t *testing.T) {
	type assetSpec struct {
		id        int64
		format    string
		isPrimary int
		createdAt int
	}
	tests := []struct {
		name   string
		assets []assetSpec
		want   int64
	}{
		{
			name: "EPUB replaces a readable PDF primary and outranks older MOBI",
			assets: []assetSpec{
				{id: 1, format: "pdf", isPrimary: 1, createdAt: 10},
				{id: 2, format: "mobi", createdAt: 20},
				{id: 3, format: "epub", createdAt: 30},
			},
			want: 3,
		},
		{
			name: "keeps EPUB when a PDF is added",
			assets: []assetSpec{
				{id: 1, format: "epub", isPrimary: 1, createdAt: 10},
				{id: 2, format: "pdf", createdAt: 20},
			},
			want: 1,
		},
		{
			name: "EPUB preferred over KEPUB",
			assets: []assetSpec{
				{id: 1, format: "kepub", isPrimary: 1},
				{id: 2, format: "epub"},
			},
			want: 2,
		},
		{
			name: "FB2 preferred over Kindle formats",
			assets: []assetSpec{
				{id: 1, format: "azw3", isPrimary: 1},
				{id: 2, format: "fb2"},
			},
			want: 2,
		},
		{
			name: "AZW3 preferred over legacy MOBI",
			assets: []assetSpec{
				{id: 1, format: "mobi", isPrimary: 1},
				{id: 2, format: "azw3"},
			},
			want: 2,
		},
		{
			name: "ZIP comics preferred over RAR and PDF",
			assets: []assetSpec{
				{id: 1, format: "pdf", isPrimary: 1},
				{id: 2, format: "cbr"},
				{id: 3, format: "cbz"},
			},
			want: 3,
		},
		{
			name: "keeps current primary among equal formats",
			assets: []assetSpec{
				{id: 1, format: "epub", isPrimary: 1, createdAt: 20},
				{id: 2, format: "epub", createdAt: 10},
			},
			want: 1,
		},
		{
			name: "MOBI aliases have equal preference",
			assets: []assetSpec{
				{id: 1, format: "mobi", createdAt: 10},
				{id: 2, format: "prc", createdAt: 20},
				{id: 3, format: "azw", isPrimary: 1, createdAt: 30},
			},
			want: 3,
		},
		{
			name: "replaces unsupported primary",
			assets: []assetSpec{
				{id: 1, format: "docx", isPrimary: 1, createdAt: 10},
				{id: 2, format: "epub", createdAt: 20},
			},
			want: 2,
		},
		{
			name: "keeps primary when no format has a reader",
			assets: []assetSpec{
				{id: 1, format: "txt", isPrimary: 1, createdAt: 20},
				{id: 2, format: "docx", createdAt: 10},
			},
			want: 1,
		},
		{
			name: "fills missing primary by preference then age",
			assets: []assetSpec{
				{id: 1, format: "pdf", createdAt: 10},
				{id: 2, format: "epub", createdAt: 30},
				{id: 3, format: "epub", createdAt: 20},
			},
			want: 3,
		},
		{
			name: "asset ID breaks timestamp ties",
			assets: []assetSpec{
				{id: 2, format: "epub", createdAt: 10},
				{id: 1, format: "epub", createdAt: 10},
			},
			want: 1,
		},
		{
			name: "empty book",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book')")
			for _, asset := range tt.assets {
				mustExec(t, database, `
					INSERT INTO assets (id, book_id, storage_path, filename, extension, format, is_primary, created_at, original_hash, current_hash)
					VALUES (?, 1, ?, ?, '.book', ?, ?, ?, randomblob(16), randomblob(16))
				`, asset.id, strconv.FormatInt(asset.id, 10)+".book", strconv.FormatInt(asset.id, 10)+".book", asset.format, asset.isPrimary, asset.createdAt)
			}

			for range 2 {
				if err := database.Transact(t.Context(), func(tx *Tx) error {
					return EnsurePreferredPrimaryAsset(tx, 1)
				}); err != nil {
					t.Fatalf("EnsurePreferredPrimaryAsset: %v", err)
				}
				var got int64
				if err := database.Read(t.Context()).QueryRow("SELECT COALESCE(MAX(id), 0) FROM assets WHERE book_id = 1 AND is_primary = 1").Scan(&got); err != nil {
					t.Fatalf("query primary: %v", err)
				}
				if got != tt.want {
					t.Fatalf("primary asset = %d, want %d", got, tt.want)
				}
			}
		})
	}
}
