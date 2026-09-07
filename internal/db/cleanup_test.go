package db

import "testing"

func TestCleanupCategories(t *testing.T) {
	database := newTestDB(t)

	// Seed authors
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Unknown Author', 'Unknown Author')")
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'Real Author', 'Real Author')")

	// Seed books
	// Book 1: Missing cover, but has tags, desc, real author
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, tags, description, cover_version) VALUES (1, 'B1', 'B1', 't1', 'd1', 0)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (1, 'a2', 0)")

	// Book 2: Unknown author, but has cover, tags, desc
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, tags, description, cover_version) VALUES (2, 'B2', 'B2', 't2', 'd2', 1)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (2, 'a1', 0)")

	// Book 3: No tags
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, tags, description, cover_version) VALUES (3, 'B3', 'B3', NULL, 'd3', 1)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (3, 'a2', 0)")

	// Book 4: No description
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, tags, description, cover_version) VALUES (4, 'B4', 'B4', 't4', NULL, 1)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (4, 'a2', 0)")

	// Book 5: Perfect book (should not be in any)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, tags, description, cover_version) VALUES (5, 'B5', 'B5', 't5', 'd5', 1)")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (5, 'a2', 0)")
	mustExec(t, database, "INSERT INTO search (rowid, title, authors, tags, description) VALUES (1, 'B1', 'Real Author', 't1', 'd1')")
	mustExec(t, database, "INSERT INTO search (rowid, title, authors, tags, description) VALUES (2, 'B2', 'Unknown Author', 't2', 'd2')")
	mustExec(t, database, "INSERT INTO search (rowid, title, authors, tags, description) VALUES (3, 'B3', 'Real Author', '', 'd3')")
	mustExec(t, database, "INSERT INTO search (rowid, title, authors, tags, description) VALUES (4, 'B4', 'Real Author', 't4', '')")
	mustExec(t, database, "INSERT INTO search (rowid, title, authors, tags, description) VALUES (5, 'B5', 'Real Author', 't5', 'd5')")

	counts, err := GetCleanupCounts(database.Read(t.Context()), FullVisibilityScope())
	if err != nil {
		t.Fatalf("GetCleanupCounts failed: %v", err)
	}
	if counts.MissingCover != 1 || counts.UnknownAuthor != 1 || counts.NoTags != 1 || counts.NoDescription != 1 {
		t.Errorf("Unexpected counts: %+v", counts)
	}

	filterTests := []struct {
		query string
		want  int64
	}{
		{"no:cover", 1},
		{"no:author", 2},
		{"no:tags", 3},
		{"no:description", 4},
		{"no:cover B1", 1},
	}
	for _, tt := range filterTests {
		t.Run("search "+tt.query, func(t *testing.T) {
			books, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), 0, tt.query, SortRelevance, 10, 0)
			if err != nil {
				t.Fatalf("ListBooks(%q) failed: %v", tt.query, err)
			}
			if len(books) != 1 || books[0].ID != tt.want {
				t.Fatalf("ListBooks(%q) ids = %v; want [%d]", tt.query, bookIDs(books), tt.want)
			}
		})
	}
}
