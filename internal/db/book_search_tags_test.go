package db

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
)

func TestQuotedTagsMatchWholeValues(t *testing.T) {
	database := newTestDB(t)
	longTag := strings.Repeat("x", 40000)
	works := []struct{ id, tags string }{
		{"w1", " История , ИСТОРИЯ "},
		{"w2", "История искусства"},
		{"w3", "История, искусства"},
		{"w4", `C++, sci-"fi", lang:ru / 2026, ☆`},
		{"w5", "C, sci-fi"},
		{"w6", "café"},
		{"w7", "cafe"},
		{"w8", longTag + "A"},
		{"w9", longTag + "B"},
	}
	for _, work := range works {
		if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec("INSERT INTO works (id, title, sort_title, tags) VALUES (?, ?, ?, ?)", work.id, "Needle "+work.id, work.id, work.tags); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES (?, ?, ?, ?, '.epub')", "a"+work.id, work.id, work.id+".epub", work.id+".epub"); err != nil {
				return err
			}
			return UpdateSearchIndex(tx, work.id)
		}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name, query string
		want        []string
	}{
		{"whole tag and case", `tag:"ИСТОРИЯ"`, []string{"w1", "w3"}},
		{"compound tag not adjacent tags", `tag:"История искусства"`, []string{"w2"}},
		{"two whole tags", `tag:"История" tag:"искусства"`, []string{"w3"}},
		{"unquoted prefix stays textual", "tag:Ист", []string{"w1", "w2", "w3"}},
		{"unfinished quote stays textual", `tag:"Ист`, []string{"w1", "w2", "w3"}},
		{"completed unquoted term stays textual", "tag:История title:Needle", []string{"w1", "w2", "w3"}},
		{"mixed exact and word search", `tag:"История" needle`, []string{"w1", "w3"}},
		{"punctuation survives", `tag:"C++"`, []string{"w4"}},
		{"punctuation distinguishes names", `tag:"C"`, []string{"w5"}},
		{"literal quotes", QueryTerm("tag", `sci-"fi"`), []string{"w4"}},
		{"literal query syntax", `tag:"lang:ru / 2026"`, []string{"w4"}},
		{"symbol-only tag", `tag:"☆"`, []string{"w4"}},
		{"accents are part of exact name", `tag:"CAFÉ"`, []string{"w6"}},
		{"unaccented exact name", `tag:"cafe"`, []string{"w7"}},
		{"long tag not truncated", QueryTerm("tag", longTag+"a"), []string{"w8"}},
		{"long tag suffix differs", QueryTerm("tag", longTag+"b"), []string{"w9"}},
		{"keys hidden from free search", tagSearchKey("История"), nil},
		{"keys hidden from quoted free search", `"` + tagSearchKey("История") + `"`, nil},
		{"missing tag", `tag:"never present"`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertTagSearchResults(t, database, FullVisibilityScope(), 0, tt.query, tt.want)
		})
	}
}

func TestExactTagShelfTracksMetadataAndAccess(t *testing.T) {
	database := newTestDB(t)
	for _, work := range []struct{ id, tags string }{{"w1", "История"}, {"w2", "История искусства"}} {
		if _, err := database.Exec("INSERT INTO works (id, title, sort_title, tags) VALUES (?, ?, ?, ?)", work.id, work.id, work.id, work.tags); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES (?, ?, ?, ?, '.epub')", "a"+work.id, work.id, work.id+".epub", work.id+".epub"); err != nil {
			t.Fatal(err)
		}
		setSearchTags(t, database, work.id, work.tags)
	}
	user, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := database.CreateShelf(user.ID, ShelfShared, "History", ShelfQuery, `tag:"История"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatal(err)
	}
	scope, err := database.VisibilityScopeForUser(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want []string) {
		t.Helper()
		assertTagSearchResults(t, database, FullVisibilityScope(), user.ID, shelf.Query, want)
		books, err := ListBooks(database, scope, user.ID, "", SortTitle, 50, 0)
		if err != nil || !slices.Equal(bookIDs(books), want) {
			t.Fatalf("scoped catalog = %v, %v; want %v", bookIDs(books), err, want)
		}
		assertTagSearchResults(t, database, scope, user.ID, shelf.Query, want)
		for _, id := range []string{"w1", "w2"} {
			allowed, err := CanAccessWork(database, scope, id)
			if err != nil || allowed != slices.Contains(want, id) {
				t.Fatalf("access %s = %v, %v; want %v", id, allowed, err, slices.Contains(want, id))
			}
		}
	}
	check([]string{"w1"})
	setSearchTags(t, database, "w1", "История искусства")
	check(nil)
	setSearchTags(t, database, "w2", "История искусства, история")
	check([]string{"w2"})
	setSearchTags(t, database, "w2", "")
	check(nil)
}

func setSearchTags(t *testing.T, database *DB, workID, tags string) {
	t.Helper()
	if err := database.Transact(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE works SET tags = ? WHERE id = ?", tags, workID); err != nil {
			return err
		}
		return UpdateSearchIndex(tx, workID)
	}); err != nil {
		t.Fatal(err)
	}
}

func assertTagSearchResults(t *testing.T, database *DB, scope VisibilityScope, userID int64, query string, want []string) {
	t.Helper()
	books, err := ListBooks(database, scope, userID, query, SortTitle, 50, 0)
	if err != nil || !slices.Equal(bookIDs(books), want) {
		t.Fatalf("search = %v, %v; want %v", bookIDs(books), err, want)
	}
	count, err := CountSearchOPDSPublications(database, scope, userID, query)
	if err != nil || count != len(want) {
		t.Fatalf("count = %d, %v; want %d", count, err, len(want))
	}
	publications, err := SearchOPDSPublications(database, scope, userID, query, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(publications))
	for _, publication := range publications {
		ids = append(ids, publication.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, want) {
		t.Fatalf("OPDS = %v; want %v", ids, want)
	}
	if len(want) > 0 {
		sequence, err := BookSequenceInList(database, scope, userID, want[0], query, SortTitle, 50, 50)
		if err != nil || sequence.Total != len(want) || len(sequence.Items) != len(want) {
			t.Fatalf("sequence = %+v, %v; want %v", sequence, err, want)
		}
		for i, item := range sequence.Items {
			if item.ID != want[i] {
				t.Fatalf("sequence item %d = %s; want %s", i, item.ID, want[i])
			}
			page, err := ListBooks(database, scope, userID, query, SortTitle, 1, i)
			if err != nil || len(page) != 1 || page[0].ID != want[i] {
				t.Fatalf("page %d = %v, %v; want %s", i, bookIDs(page), err, want[i])
			}
		}
	}
}
