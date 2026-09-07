package db

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestQuotedTagsMatchWholeValues(t *testing.T) {
	database := newTestDB(t)
	longTag := strings.Repeat("x", 40000)
	books := []struct {
		id   int64
		tags string
	}{
		{1, " История , ИСТОРИЯ "},
		{2, "История искусства"},
		{3, "История, искусства"},
		{4, `C++, sci-"fi", lang:ru / 2026, ☆`},
		{5, "C, sci-fi"},
		{6, "café"},
		{7, "cafe"},
		{8, longTag + "A"},
		{9, longTag + "B"},
	}
	for _, book := range books {
		if err := database.Transact(context.Background(), func(tx *Tx) error {
			if _, err := tx.Exec("INSERT INTO books (id, title, sort_title, tags) VALUES (?, ?, ?, ?)", book.id, fmt.Sprintf("Needle %d", book.id), book.id, book.tags); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES (?, ?, ?, ?, '.epub')", fmt.Sprintf("a%d", book.id), book.id, fmt.Sprintf("%d.epub", book.id), fmt.Sprintf("%d.epub", book.id)); err != nil {
				return err
			}
			return UpdateSearchIndex(tx, book.id)
		}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name, query string
		want        []int64
	}{
		{"whole tag and case", `tag:"ИСТОРИЯ"`, []int64{1, 3}},
		{"compound tag not adjacent tags", `tag:"История искусства"`, []int64{2}},
		{"two whole tags", `tag:"История" tag:"искусства"`, []int64{3}},
		{"unquoted prefix stays textual", "tag:Ист", []int64{1, 2, 3}},
		{"unfinished quote stays textual", `tag:"Ист`, []int64{1, 2, 3}},
		{"completed unquoted term stays textual", "tag:История title:Needle", []int64{1, 2, 3}},
		{"mixed exact and word search", `tag:"История" needle`, []int64{1, 3}},
		{"punctuation survives", `tag:"C++"`, []int64{4}},
		{"punctuation distinguishes names", `tag:"C"`, []int64{5}},
		{"literal quotes", QueryTerm("tag", `sci-"fi"`), []int64{4}},
		{"literal query syntax", `tag:"lang:ru / 2026"`, []int64{4}},
		{"symbol-only tag", `tag:"☆"`, []int64{4}},
		{"accents are part of exact name", `tag:"CAFÉ"`, []int64{6}},
		{"unaccented exact name", `tag:"cafe"`, []int64{7}},
		{"long tag not truncated", QueryTerm("tag", longTag+"a"), []int64{8}},
		{"long tag suffix differs", QueryTerm("tag", longTag+"b"), []int64{9}},
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
	for _, book := range []struct {
		id   int64
		tags string
	}{{1, "История"}, {2, "История искусства"}} {
		mustExec(t, database, "INSERT INTO books (id, title, sort_title, tags) VALUES (?, ?, ?, ?)", book.id, book.id, book.id, book.tags)
		mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES (?, ?, ?, ?, '.epub')", fmt.Sprintf("a%d", book.id), book.id, fmt.Sprintf("%d.epub", book.id), fmt.Sprintf("%d.epub", book.id))

		setSearchTags(t, database, book.id, book.tags)
	}
	user, err := database.CreateUser(t.Context(), "reader", "pw", RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfShared, "History", ShelfQuery, `tag:"История"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), user.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatal(err)
	}
	scope, err := VisibilityScopeForUser(database.Read(t.Context()), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want []int64) {
		t.Helper()
		assertTagSearchResults(t, database, FullVisibilityScope(), user.ID, shelf.Query, want)
		books, err := ListBooks(database.Read(t.Context()), scope, user.ID, "", SortTitle, 50, 0)
		if err != nil || !slices.Equal(bookIDs(books), want) {
			t.Fatalf("scoped catalog = %v, %v; want %v", bookIDs(books), err, want)
		}
		assertTagSearchResults(t, database, scope, user.ID, shelf.Query, want)
		for _, id := range []int64{1, 2} {
			allowed, err := CanAccessBook(database.Read(t.Context()), scope, id)
			if err != nil || allowed != slices.Contains(want, id) {
				t.Fatalf("access %d = %v, %v; want %v", id, allowed, err, slices.Contains(want, id))
			}
		}
	}
	check([]int64{1})
	setSearchTags(t, database, 1, "История искусства")
	check(nil)
	setSearchTags(t, database, 2, "История искусства, история")
	check([]int64{2})
	setSearchTags(t, database, 2, "")
	check(nil)
}

func setSearchTags(t *testing.T, database *DB, bookID int64, tags string) {
	t.Helper()
	if err := database.Transact(context.Background(), func(tx *Tx) error {
		if _, err := tx.Exec("UPDATE books SET tags = ? WHERE id = ?", tags, bookID); err != nil {
			return err
		}
		return UpdateSearchIndex(tx, bookID)
	}); err != nil {
		t.Fatal(err)
	}
}

func assertTagSearchResults(t *testing.T, database *DB, scope VisibilityScope, userID int64, query string, want []int64) {
	t.Helper()
	books, err := ListBooks(database.Read(t.Context()), scope, userID, query, SortTitle, 50, 0)
	if err != nil || !slices.Equal(bookIDs(books), want) {
		t.Fatalf("search = %v, %v; want %v", bookIDs(books), err, want)
	}
}
