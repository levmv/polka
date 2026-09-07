package db

import (
	"context"
	"testing"
)

func TestListOPDSPublicationsReturnsOnlyLiveBooksWithAssets(t *testing.T) {
	database := newTestDB(t)

	mustExec := func(query string, args ...any) {
		t.Helper()
		mustExec(t, database, query, args...)

	}

	mustExec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Author One', 'Author One')")
	mustExec("INSERT INTO books (id, title, sort_title, description, tags, publisher, published_date, language, identifiers, updated_at) VALUES (1, 'B Title', 'B Title', 'Desc', 'one, two', 'Press', '2024-05-01', 'en', 'isbn:978-0-306-40615-7', 10)")
	mustExec("INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 'a1', 0)")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset1', 1, 'b.epub', 'b.epub', '.epub')")
	mustExec("INSERT INTO books (id, title, sort_title, updated_at) VALUES (2, 'The A Book', 'A Book, The', 11)")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset2', 2, 'a.epub', 'a.epub', '.epub')")
	mustExec("INSERT INTO books (id, title, sort_title) VALUES (158, 'No Asset', 'No Asset')")
	mustExec("INSERT INTO books (id, title, sort_title, deleted_at) VALUES (122, 'Deleted', 'Deleted', 20)")
	mustExec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_deleted', 122, 'd.epub', 'd.epub', '.epub')")

	rows, err := ListOPDSPublications(database.Read(t.Context()), FullVisibilityScope(), 10, 0)
	if err != nil {
		t.Fatalf("ListOPDSPublications: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].ID != 2 || rows[0].Title != "The A Book" {
		t.Fatalf("first row = %+v, want 2/The A Book sorted by sort_title", rows[0])
	}
	got := rows[1]
	if got.ID != 1 || got.Title != "B Title" {
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

	count, err := CountOPDSPublications(database.Read(t.Context()), FullVisibilityScope())
	if err != nil {
		t.Fatalf("CountOPDSPublications: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountOPDSPublications = %d; want 2", count)
	}
}

func TestListRecentOPDSPublicationsIsNewestFirstWithinOneSecond(t *testing.T) {
	database := newTestDB(t)

	// A fast import can assign the same second-resolution added_at to both books.
	// The later insertion has the larger ID.
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, added_at) VALUES
			(101, 'Earlier', 'Earlier', 100),
			(102, 'Later', 'Later', 100);
		INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES
			('a_earlier', 101, 'earlier.epub', 'earlier.epub', '.epub'),
			('a_later', 102, 'later.epub', 'later.epub', '.epub');
	`)

	first, err := ListRecentOPDSPublications(database.Read(t.Context()), FullVisibilityScope(), 1, 0)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	second, err := ListRecentOPDSPublications(database.Read(t.Context()), FullVisibilityScope(), 1, 1)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(first) != 1 || first[0].ID != 102 {
		t.Fatalf("first page = %+v, want later book", first)
	}
	if len(second) != 1 || second[0].ID != 101 {
		t.Fatalf("second page = %+v, want earlier book", second)
	}
}

func TestSearchOPDSPublicationsSupportsPerUserStatusFilters(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES
			(1, 'Alpha Needle', 'Alpha Needle'),
			(2, 'Beta Needle', 'Beta Needle');
		INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES
			('a1', 1, 'a.epub', 'a.epub', '.epub'),
			('a2', 2, 'b.epub', 'b.epub', '.epub');
		INSERT INTO search (rowid, title) VALUES
			(1, 'Alpha Needle'), (2, 'Beta Needle');
	`)

	if _, err := database.SetReadingStatus(context.Background(), user.ID, 2, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("set status: %v", err)
	}

	rows, err := SearchOPDSPublications(database.Read(t.Context()), FullVisibilityScope(), user.ID, "status:finished needle", 10, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != 2 {
		t.Fatalf("status OPDS rows = %+v, err %v; want 2", rows, err)
	}
	count, err := CountSearchOPDSPublications(database.Read(t.Context()), FullVisibilityScope(), user.ID, "status:unread")
	if err != nil || count != 1 {
		t.Fatalf("unread OPDS count = %d, err %v; want 1", count, err)
	}
}

func TestManualShelfOPDSPublicationsRespectContentScope(t *testing.T) {
	database := newTestDB(t)

	reader, err := database.CreateUser(t.Context(), "reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	for _, statement := range []string{
		"INSERT INTO books (id, title, sort_title) VALUES (11, 'Allowed', 'Allowed')",
		"INSERT INTO books (id, title, sort_title) VALUES (13, 'Outside', 'Outside')",
		"INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_allowed', 11, 'allowed.epub', 'allowed.epub', '.epub')",
		"INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES ('asset_outside', 13, 'outside.epub', 'outside.epub', '.epub')",
	} {
		mustExec(t, database, statement)

	}

	accessShelf, err := database.CreateShelf(t.Context(), reader.ID, ShelfShared, "Allowed library", ShelfManual, "")
	if err != nil {
		t.Fatalf("create access shelf: %v", err)
	}
	deviceShelf, err := database.CreateShelf(t.Context(), reader.ID, ShelfPersonal, "Device", ShelfManual, "")
	if err != nil {
		t.Fatalf("create device shelf: %v", err)
	}
	if err := database.AddBookToShelf(t.Context(), accessShelf.ID, reader.ID, 11); err != nil {
		t.Fatalf("add allowed book to access shelf: %v", err)
	}
	for _, bookID := range []int64{11, 13} {
		if err := database.AddBookToShelf(t.Context(), deviceShelf.ID, reader.ID, bookID); err != nil {
			t.Fatalf("add %d to device shelf: %v", bookID, err)
		}
	}
	if _, err := database.UpdateUserAccess(t.Context(), reader.ID, UserAccess{
		Role:         RoleReader,
		ContentScope: ContentScopeShelves,
		ShelfIDs:     []string{accessShelf.ID},
	}); err != nil {
		t.Fatalf("scope reader: %v", err)
	}
	scope, err := VisibilityScopeForUser(database.Read(t.Context()), reader.ID)
	if err != nil {
		t.Fatalf("reader scope: %v", err)
	}

	rows, err := ListManualShelfOPDSPublications(database.Read(t.Context()), scope, deviceShelf.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListManualShelfOPDSPublications: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 11 {
		t.Fatalf("scoped shelf rows = %+v, want only allowed", rows)
	}
	count, err := CountManualShelfOPDSPublications(database.Read(t.Context()), scope, deviceShelf.ID)
	if err != nil {
		t.Fatalf("CountManualShelfOPDSPublications: %v", err)
	}
	if count != 1 {
		t.Fatalf("scoped shelf count = %d, want 1", count)
	}
}
