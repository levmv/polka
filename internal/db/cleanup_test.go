package db

import "testing"

func TestCleanupCategories(t *testing.T) {
	database := newTestDB(t)

	// Seed authors
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Unknown Author', 'Unknown Author')")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a2', 'Real Author', 'Real Author')")

	// Seed works
	// Book 1: Missing cover, but has tags, desc, real author
	database.Exec("INSERT INTO works (id, title, sort_title, tags, description, cover_version) VALUES ('w1', 'B1', 'B1', 't1', 'd1', 0)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w1', 'a2', 0)")

	// Book 2: Unknown author, but has cover, tags, desc
	database.Exec("INSERT INTO works (id, title, sort_title, tags, description, cover_version) VALUES ('w2', 'B2', 'B2', 't2', 'd2', 1)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w2', 'a1', 0)")

	// Book 3: No tags
	database.Exec("INSERT INTO works (id, title, sort_title, tags, description, cover_version) VALUES ('w3', 'B3', 'B3', NULL, 'd3', 1)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w3', 'a2', 0)")

	// Book 4: No description
	database.Exec("INSERT INTO works (id, title, sort_title, tags, description, cover_version) VALUES ('w4', 'B4', 'B4', 't4', NULL, 1)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w4', 'a2', 0)")

	// Book 5: Perfect book (should not be in any)
	database.Exec("INSERT INTO works (id, title, sort_title, tags, description, cover_version) VALUES ('w5', 'B5', 'B5', 't5', 'd5', 1)")
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES ('w5', 'a2', 0)")
	database.Exec("INSERT INTO search (work_id, title, authors, tags, description) VALUES ('w1', 'B1', 'Real Author', 't1', 'd1')")
	database.Exec("INSERT INTO search (work_id, title, authors, tags, description) VALUES ('w2', 'B2', 'Unknown Author', 't2', 'd2')")
	database.Exec("INSERT INTO search (work_id, title, authors, tags, description) VALUES ('w3', 'B3', 'Real Author', '', 'd3')")
	database.Exec("INSERT INTO search (work_id, title, authors, tags, description) VALUES ('w4', 'B4', 'Real Author', 't4', '')")
	database.Exec("INSERT INTO search (work_id, title, authors, tags, description) VALUES ('w5', 'B5', 'Real Author', 't5', 'd5')")

	counts, err := GetCleanupCounts(database, FullVisibilityScope())
	if err != nil {
		t.Fatalf("GetCleanupCounts failed: %v", err)
	}
	if counts.MissingCover != 1 || counts.UnknownAuthor != 1 || counts.NoTags != 1 || counts.NoDescription != 1 {
		t.Errorf("Unexpected counts: %+v", counts)
	}

	filterTests := []struct {
		query string
		want  string
	}{
		{"no:cover", "w1"},
		{"no:author", "w2"},
		{"no:tags", "w3"},
		{"no:description", "w4"},
		{"no:cover B1", "w1"},
	}
	for _, tt := range filterTests {
		t.Run("search "+tt.query, func(t *testing.T) {
			books, err := ListBooks(database, FullVisibilityScope(), "", tt.query, SortRelevance, 10, 0)
			if err != nil {
				t.Fatalf("ListBooks(%q) failed: %v", tt.query, err)
			}
			if len(books) != 1 || books[0].ID != tt.want {
				t.Fatalf("ListBooks(%q) ids = %v; want [%s]", tt.query, bookIDs(books), tt.want)
			}
		})
	}
}
