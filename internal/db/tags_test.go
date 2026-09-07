package db

import (
	"slices"
	"testing"
)

func TestListTags(t *testing.T) {
	database := newTestDB(t)

	must := func(query string) {
		mustExec(t, database, query)

	}
	must("INSERT INTO books (id, title, sort_title, tags) VALUES (1, 'T1', 'T1', ' Fantasy, classics, Fantasy ')")
	must("INSERT INTO books (id, title, sort_title, tags) VALUES (2, 'T2', 'T2', 'science fiction, CLASSICS')")
	must("INSERT INTO books (id, title, sort_title, tags) VALUES (3, 'T3', 'T3', '')")
	must(`INSERT INTO books (id, title, sort_title, tags) VALUES (4, 'T4', 'T4', '100% real, under_score, path\name')`)
	must("INSERT INTO books (id, title, sort_title, tags) VALUES (5, 'T5', 'T5', 'Классика')")
	must("INSERT INTO books (id, title, sort_title, tags, deleted_at) VALUES (122, 'Deleted', 'Deleted', 'archived', 1)")

	must(`INSERT INTO books (id, title, sort_title, tags) VALUES (6, 'T6', 'T6', 'İstanbul, Kelvin')`)

	wantAll := []string{"100% real", "classics", "Fantasy", "İstanbul", "Kelvin", `path\name`, "science fiction", "under_score", "Классика"}
	for _, tt := range []struct {
		name, q string
		limit   int
		want    []string
	}{
		{"all", "", 0, wantAll},
		{"ASCII case and whitespace", " SCI ", 20, []string{"science fiction"}},
		{"percent", "%", 20, []string{"100% real"}},
		{"underscore", "_", 20, []string{"under_score"}},
		{"backslash", `\`, 20, []string{`path\name`}},
		{"Cyrillic case", "КЛАСС", 20, []string{"Классика"}},
		{"dotted I", "İstanbul", 20, []string{"İstanbul"}},
		{"ASCII to dotted I", "istanbul", 20, []string{"İstanbul"}},
		{"Kelvin sign", "Kelvin", 20, []string{"Kelvin"}},
		{"ASCII to Kelvin sign", "kelvin", 20, []string{"Kelvin"}},
		{"missing", "absent", 20, nil},
		{"limited", "", 2, wantAll[:2]},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ListTags(database.Read(t.Context()), FullVisibilityScope(), tt.q, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("ListTags = %v; want %v", got, tt.want)
			}
		})
	}

	must("UPDATE books SET tags = 'newtag' WHERE id = 2")
	updated, err := ListTags(database.Read(t.Context()), FullVisibilityScope(), "new", 20)
	if err != nil {
		t.Fatalf("ListTags updated: %v", err)
	}
	if !slices.Equal(updated, []string{"newtag"}) {
		t.Fatalf("ListTags after update = %v; want [newtag]", updated)
	}
}

func TestListTagsVisibility(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, `
		INSERT INTO users (id, username, password_hash, role, content_scope) VALUES
			(1, 'reader', 'unused', 'reader', 'shelves'),
			(2, 'curator', 'unused', 'admin', 'all');
		INSERT INTO books (id, title, sort_title, tags, deleted_at) VALUES
			(1, 'Manual book', 'Manual book', 'Fantasy, Shared', NULL),
			(2, 'Query book', 'Query book', 'Science fiction, Shared, Классика, İstanbul', NULL),
			(3, 'Query overlap', 'Query overlap', 'Adventure, Shared', NULL),
			(4, 'Hidden book', 'Hidden book', 'Hİdden, Классика тайная', NULL),
			(5, 'Query trashed', 'Query trashed', 'Archived', 1);
		INSERT INTO search (rowid, title, tags) SELECT  id, title, tags FROM books;
		INSERT INTO shelves (id, name, kind, owner_id, visibility, query, query_match) VALUES
			(1, 'Manual', 'manual', 2, 'shared', NULL, NULL),
			(2, 'Query', 'query', 2, 'shared', 'title:Query', ?),
			(3, 'Curator personal', 'manual', 2, 'personal', NULL, NULL),
			(4, 'Own manual', 'manual', 1, 'personal', NULL, NULL),
			(5, 'Own query', 'query', 1, 'personal', 'title:Hidden', ?),
			(6, 'Empty query', 'query', 2, 'shared', '', '');
		INSERT INTO shelf_books (shelf_id, book_id) VALUES
			(1, 1), (1, 3), (1, 5),
			(3, 4), (4, 4);
		INSERT INTO user_scope_shelves (user_id, shelf_id) VALUES (2, 3);
	`, ParseQuery("title:Query"), ParseQuery("title:Hidden"))

	manual := []string{"Adventure", "Fantasy", "Shared"}
	query := []string{"Adventure", "İstanbul", "Science fiction", "Shared", "Классика"}
	mixed := []string{"Adventure", "Fantasy", "İstanbul", "Science fiction", "Shared", "Классика"}
	for _, tt := range []struct {
		name    string
		shelves []int64
		q       string
		limit   int
		want    []string
	}{
		{"no grants", nil, "", 0, nil},
		{"manual", []int64{1}, "", 0, manual},
		{"query", []int64{2}, "", 0, query},
		{"mixed overlapping", []int64{1, 2, 4, 5}, "", 0, mixed},
		{"ASCII filter", []int64{1, 2}, "ISTAN", 20, []string{"İstanbul"}},
		{"Cyrillic filter", []int64{1, 2}, "КЛАСС", 20, []string{"Классика"}},
		{"hidden match", []int64{1, 2}, "Hidden", 20, nil},
		{"limit after sorting", []int64{1, 2}, "", 2, mixed[:2]},
		{"own manual", []int64{4}, "", 0, nil},
		{"own query", []int64{5}, "", 0, nil},
		{"curator personal", []int64{3}, "", 0, []string{"Hİdden", "Классика тайная"}},
		{"empty query", []int64{6}, "", 0, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Seed even ineligible grants to exercise the read-side access boundary.
			mustExec(t, database, `DELETE FROM user_scope_shelves WHERE user_id = 1`)

			for _, shelf := range tt.shelves {
				mustExec(t, database, `INSERT INTO user_scope_shelves (user_id, shelf_id) VALUES (1, ?)`, shelf)

			}
			scope := VisibilityScope{UserID: 1, ContentScope: ContentScopeShelves}
			got, err := ListTags(database.Read(t.Context()), scope, tt.q, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("ListTags = %v; want %v", got, tt.want)
			}
		})
	}
}
