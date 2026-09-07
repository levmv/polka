package db

import (
	"context"
	"testing"
)

func TestEnsureReadablePrimaryAsset(t *testing.T) {
	type assetSpec struct {
		id        string
		canRead   int
		isPrimary int
		createdAt int
	}
	tests := []struct {
		name   string
		assets []assetSpec
		want   string
	}{
		{
			name: "keeps current readable primary",
			assets: []assetSpec{
				{id: "current", canRead: 1, isPrimary: 1, createdAt: 20},
				{id: "older", canRead: 1, createdAt: 10},
			},
			want: "current",
		},
		{
			name: "replaces unreadable primary",
			assets: []assetSpec{
				{id: "unreadable", isPrimary: 1, createdAt: 10},
				{id: "readable", canRead: 1, createdAt: 20},
			},
			want: "readable",
		},
		{
			name: "keeps unreadable primary without readable candidate",
			assets: []assetSpec{
				{id: "current", isPrimary: 1, createdAt: 20},
				{id: "older", createdAt: 10},
			},
			want: "current",
		},
		{
			name: "fills missing primary with readable candidate",
			assets: []assetSpec{
				{id: "unreadable", createdAt: 10},
				{id: "readable", canRead: 1, createdAt: 20},
			},
			want: "readable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book')")

			for _, asset := range tt.assets {
				mustExec(t, database, `
					INSERT INTO assets (id, book_id, storage_path, filename, extension, can_read, is_primary, created_at)
					VALUES (?, 1, ?, ?, '.book', ?, ?, ?)
				`, asset.id, asset.id+".book", asset.id+".book", asset.canRead, asset.isPrimary, asset.createdAt)

			}

			if err := database.Transact(context.Background(), func(tx *Tx) error {
				return EnsureReadablePrimaryAsset(tx, 1)
			}); err != nil {
				t.Fatalf("EnsureReadablePrimaryAsset: %v", err)
			}

			var got string
			if err := database.Read(t.Context()).QueryRow("SELECT id FROM assets WHERE book_id = 1 AND is_primary = 1").Scan(&got); err != nil {
				t.Fatalf("query primary: %v", err)
			}
			if got != tt.want {
				t.Fatalf("primary asset = %q, want %q", got, tt.want)
			}
		})
	}
}
