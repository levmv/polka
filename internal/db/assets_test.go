package db

import (
	"context"
	"strconv"
	"testing"
)

func TestEnsureReadablePrimaryAsset(t *testing.T) {
	type assetSpec struct {
		id        int64
		canRead   int
		isPrimary int
		createdAt int
	}
	tests := []struct {
		name   string
		assets []assetSpec
		want   int64
	}{
		{
			name: "keeps current readable primary",
			assets: []assetSpec{
				{id: 1, canRead: 1, isPrimary: 1, createdAt: 20},
				{id: 2, canRead: 1, createdAt: 10},
			},
			want: 1,
		},
		{
			name: "replaces unreadable primary",
			assets: []assetSpec{
				{id: 1, isPrimary: 1, createdAt: 10},
				{id: 2, canRead: 1, createdAt: 20},
			},
			want: 2,
		},
		{
			name: "keeps unreadable primary without readable candidate",
			assets: []assetSpec{
				{id: 1, isPrimary: 1, createdAt: 20},
				{id: 2, createdAt: 10},
			},
			want: 1,
		},
		{
			name: "fills missing primary with readable candidate",
			assets: []assetSpec{
				{id: 1, createdAt: 10},
				{id: 2, canRead: 1, createdAt: 20},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book')")

			for _, asset := range tt.assets {
				mustExec(t, database, `
					INSERT INTO assets (id, book_id, storage_path, filename, extension, can_read, is_primary, created_at, original_sha256, current_sha256)
					VALUES (?, 1, ?, ?, '.book', ?, ?, ?, randomblob(32), randomblob(32))
				`, asset.id, strconv.FormatInt(asset.id, 10)+".book", strconv.FormatInt(asset.id, 10)+".book", asset.canRead, asset.isPrimary, asset.createdAt)

			}

			if err := database.Transact(context.Background(), func(tx *Tx) error {
				return EnsureReadablePrimaryAsset(tx, 1)
			}); err != nil {
				t.Fatalf("EnsureReadablePrimaryAsset: %v", err)
			}

			var got int64
			if err := database.Read(t.Context()).QueryRow("SELECT id FROM assets WHERE book_id = 1 AND is_primary = 1").Scan(&got); err != nil {
				t.Fatalf("query primary: %v", err)
			}
			if got != tt.want {
				t.Fatalf("primary asset = %d, want %d", got, tt.want)
			}
		})
	}
}
