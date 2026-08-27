package cli

import (
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestShelfCLISharedManualShelf(t *testing.T) {
	dataDir := t.TempDir()
	initialized, err := ensureLibraryInitialized(dataDir)
	if err != nil {
		t.Fatalf("ensureLibraryInitialized: %v", err)
	}
	initialized.Close()

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	if _, err := database.CreateUser("admin", "pw", db.RoleAdmin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w_1', 'Title', 'Title')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a_1', 'Author', 'Author')")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w_1', 'a_1', 0)")
	database.Close()

	if err := runLibraryShelves(dataDir, []string{"create", "Favorites"}); err != nil {
		t.Fatalf("shelf create: %v", err)
	}

	database, err = db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}

	var shelfID string
	if err := database.QueryRow("SELECT id FROM shelves WHERE name = 'Favorites' AND visibility = 'shared'").Scan(&shelfID); err != nil {
		t.Fatalf("query shelf: %v", err)
	}
	database.Close()

	if err := runLibraryShelves(dataDir, []string{"add-book", shelfID, "w_1"}); err != nil {
		t.Fatalf("shelf add-book: %v", err)
	}
	if err := runLibraryShelves(dataDir, []string{"books", shelfID}); err != nil {
		t.Fatalf("shelf books: %v", err)
	}

	database, err = db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("final reopen db: %v", err)
	}
	defer database.Close()

	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM shelf_books WHERE shelf_id = ? AND work_id = 'w_1'", shelfID).Scan(&n); err != nil {
		t.Fatalf("count shelf books: %v", err)
	}
	if n != 1 {
		t.Fatalf("shelf_books count = %d, want 1", n)
	}
}
