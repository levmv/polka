package db

import (
	"context"
	"database/sql"
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
			if _, err := database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w1', 'Book', 'Book')"); err != nil {
				t.Fatalf("insert work: %v", err)
			}
			for _, asset := range tt.assets {
				if _, err := database.Exec(`
					INSERT INTO assets (id, work_id, storage_path, filename, extension, can_read, is_primary, created_at)
					VALUES (?, 'w1', ?, ?, '.book', ?, ?, ?)
				`, asset.id, asset.id+".book", asset.id+".book", asset.canRead, asset.isPrimary, asset.createdAt); err != nil {
					t.Fatalf("insert asset %s: %v", asset.id, err)
				}
			}

			if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
				return EnsureReadablePrimaryAsset(tx, "w1")
			}); err != nil {
				t.Fatalf("EnsureReadablePrimaryAsset: %v", err)
			}

			var got string
			if err := database.QueryRow("SELECT id FROM assets WHERE work_id = 'w1' AND is_primary = 1").Scan(&got); err != nil {
				t.Fatalf("query primary: %v", err)
			}
			if got != tt.want {
				t.Fatalf("primary asset = %q, want %q", got, tt.want)
			}
		})
	}
}
