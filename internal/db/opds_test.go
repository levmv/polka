package db

import (
	"context"
	"testing"
)

func TestListOPDSPublicationsReturnsOnlyLiveWorksWithAssets(t *testing.T) {
	database := newTestDB(t)

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	mustExec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Author One', 'Author One')")
	mustExec("INSERT INTO works (id, title, sort_title, description, tags, publisher, published_date, language, identifiers, updated_at) VALUES ('w1', 'B Title', 'B Title', 'Desc', 'one, two', 'Press', '2024-05-01', 'en', 'isbn:978-0-306-40615-7', 10)")
	mustExec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a1', 0)")
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset1', 'w1', 'b.epub', 'b.epub', '.epub')")
	mustExec("INSERT INTO works (id, title, sort_title, updated_at) VALUES ('w2', 'The A Book', 'A Book, The', 11)")
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset2', 'w2', 'a.epub', 'a.epub', '.epub')")
	mustExec("INSERT INTO works (id, title, sort_title) VALUES ('w_no_asset', 'No Asset', 'No Asset')")
	mustExec("INSERT INTO works (id, title, sort_title, deleted_at) VALUES ('w_deleted', 'Deleted', 'Deleted', 20)")
	mustExec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_deleted', 'w_deleted', 'd.epub', 'd.epub', '.epub')")

	rows, err := ListOPDSPublications(database, FullVisibilityScope(), 10, 0)
	if err != nil {
		t.Fatalf("ListOPDSPublications: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].ID != "w2" || rows[0].Title != "The A Book" {
		t.Fatalf("first row = %+v, want w2/The A Book sorted by sort_title", rows[0])
	}
	got := rows[1]
	if got.ID != "w1" || got.Title != "B Title" {
		t.Fatalf("unexpected row: %+v", got)
	}
	if !got.Description.Valid || got.Description.String != "Desc" {
		t.Fatalf("description = %+v, want Desc", got.Description)
	}
	if !got.Tags.Valid || got.Tags.String != "one, two" {
		t.Fatalf("tags = %+v, want one, two", got.Tags)
	}
	if !got.Publisher.Valid || got.Publisher.String != "Press" {
		t.Fatalf("publisher = %+v, want Press", got.Publisher)
	}
	if !got.PublishedDate.Valid || got.PublishedDate.String != "2024-05-01" {
		t.Fatalf("published date = %+v, want 2024-05-01", got.PublishedDate)
	}
	if !got.Language.Valid || got.Language.String != "en" {
		t.Fatalf("language = %+v, want en", got.Language)
	}
	if !got.Identifiers.Valid || got.Identifiers.String != "isbn:978-0-306-40615-7" {
		t.Fatalf("identifiers = %+v, want isbn", got.Identifiers)
	}
	if got.UpdatedAt != 10 {
		t.Fatalf("updated_at = %d, want 10", got.UpdatedAt)
	}

	count, err := CountOPDSPublications(database, FullVisibilityScope())
	if err != nil {
		t.Fatalf("CountOPDSPublications: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountOPDSPublications = %d; want 2", count)
	}
}

func TestListRecentOPDSPublicationsIsNewestFirstWithinOneSecond(t *testing.T) {
	database := newTestDB(t)

	// Production work IDs are time-sortable. These two rows model a fast import
	// where SQLite's second-resolution added_at value is identical but the later
	// work has the lexicographically larger ID.
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title, added_at) VALUES
			('w_01EARLIER', 'Earlier', 'Earlier', 100),
			('w_01LATER', 'Later', 'Later', 100);
		INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES
			('a_earlier', 'w_01EARLIER', 'earlier.epub', 'earlier.epub', '.epub'),
			('a_later', 'w_01LATER', 'later.epub', 'later.epub', '.epub');
	`); err != nil {
		t.Fatalf("seed same-second works: %v", err)
	}

	first, err := ListRecentOPDSPublications(database, FullVisibilityScope(), 1, 0)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	second, err := ListRecentOPDSPublications(database, FullVisibilityScope(), 1, 1)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(first) != 1 || first[0].ID != "w_01LATER" {
		t.Fatalf("first page = %+v, want later work", first)
	}
	if len(second) != 1 || second[0].ID != "w_01EARLIER" {
		t.Fatalf("second page = %+v, want earlier work", second)
	}
}

func TestSearchOPDSPublicationsSupportsPerUserStatusFilters(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES
			('w1', 'Alpha Needle', 'Alpha Needle'),
			('w2', 'Beta Needle', 'Beta Needle');
		INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES
			('a1', 'w1', 'a.epub', 'a.epub', '.epub'),
			('a2', 'w2', 'b.epub', 'b.epub', '.epub');
		INSERT INTO search (work_id, title) VALUES
			('w1', 'Alpha Needle'), ('w2', 'Beta Needle');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), user.ID, "w2", ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("set status: %v", err)
	}

	rows, err := SearchOPDSPublications(database, FullVisibilityScope(), user.ID, "status:finished needle", 10, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != "w2" {
		t.Fatalf("status OPDS rows = %+v, err %v; want w2", rows, err)
	}
	count, err := CountSearchOPDSPublications(database, FullVisibilityScope(), user.ID, "status:unread")
	if err != nil || count != 1 {
		t.Fatalf("unread OPDS count = %d, err %v; want 1", count, err)
	}
}

func TestManualShelfOPDSPublicationsRespectContentScope(t *testing.T) {
	database := newTestDB(t)

	reader, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	for _, statement := range []string{
		"INSERT INTO works (id, title, sort_title) VALUES ('allowed', 'Allowed', 'Allowed')",
		"INSERT INTO works (id, title, sort_title) VALUES ('outside', 'Outside', 'Outside')",
		"INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_allowed', 'allowed', 'allowed.epub', 'allowed.epub', '.epub')",
		"INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES ('asset_outside', 'outside', 'outside.epub', 'outside.epub', '.epub')",
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("fixture %q: %v", statement, err)
		}
	}

	accessShelf, err := database.CreateShelf(reader.ID, ShelfShared, "Allowed library", ShelfManual, "")
	if err != nil {
		t.Fatalf("create access shelf: %v", err)
	}
	deviceShelf, err := database.CreateShelf(reader.ID, ShelfPersonal, "Device", ShelfManual, "")
	if err != nil {
		t.Fatalf("create device shelf: %v", err)
	}
	if err := database.AddBookToShelf(accessShelf.ID, reader.ID, "allowed"); err != nil {
		t.Fatalf("add allowed book to access shelf: %v", err)
	}
	for _, workID := range []string{"allowed", "outside"} {
		if err := database.AddBookToShelf(deviceShelf.ID, reader.ID, workID); err != nil {
			t.Fatalf("add %s to device shelf: %v", workID, err)
		}
	}
	if _, err := database.UpdateUserAccess(reader.ID, UserAccess{
		Role:         RoleReader,
		ContentScope: ContentScopeShelves,
		ShelfIDs:     []string{accessShelf.ID},
	}); err != nil {
		t.Fatalf("scope reader: %v", err)
	}
	scope, err := database.VisibilityScopeForUser(reader.ID)
	if err != nil {
		t.Fatalf("reader scope: %v", err)
	}

	rows, err := ListManualShelfOPDSPublications(database, scope, deviceShelf.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListManualShelfOPDSPublications: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "allowed" {
		t.Fatalf("scoped shelf rows = %+v, want only allowed", rows)
	}
	count, err := CountManualShelfOPDSPublications(database, scope, deviceShelf.ID)
	if err != nil {
		t.Fatalf("CountManualShelfOPDSPublications: %v", err)
	}
	if count != 1 {
		t.Fatalf("scoped shelf count = %d, want 1", count)
	}
}
