package db

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
)

func TestTagHierarchySearchAndVisibility(t *testing.T) {
	database := newTestDB(t)
	for i, values := range [][2]string{
		{"Fiction.Science Fiction.Space Opera", "Topics.Space"},
		{"Fiction.Fantasy", "Fiction.Science Fiction"},
		{"Fiction", ""},
		{"fiction.science fiction, FICTION.SCIENCE FICTION.Space Opera", "Topics.Space"},
		{"Fictional", ""},
		{"Fiction.Science Fiction.Trash", ""},
	} {
		seedHierarchyBook(t, database, int64(i+1), values[0], values[1])
	}
	mustExec(t, database, "UPDATE books SET deleted_at = 1 WHERE id = 6")
	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{`genre:"fiction"`, []int64{1, 2, 3, 4}},
		{`genre:="Fiction"`, []int64{3}},
		{`genre:"Fiction.Science Fiction"`, []int64{1, 4}},
		{`genre:="Fiction.Science Fiction"`, []int64{4}},
		{`genre:"Fiction" tag:"Topics"`, []int64{1, 4}},
		{`genre:"Fiction" title:"2"`, []int64{2}},
		{`genre:="Fiction.Science Fiction" genre:"Fiction.Science Fiction.Space Opera"`, []int64{4}},
		{`tag:"Fiction"`, []int64{2}},
	} {
		assertTagSearchResults(t, database, FullVisibilityScope(), 0, tc.query, tc.want)
	}
	values, err := TagsByBookIDs(database.Read(t.Context()), []int64{1, 4})
	if err != nil || !slices.Equal(values[1].Genres, []string{"Fiction.Science Fiction.Space Opera"}) || !slices.Equal(values[4].Genres, []string{"Fiction.Science Fiction", "Fiction.Science Fiction.Space Opera"}) {
		t.Fatalf("direct memberships and canonical paths = %+v, %v", values, err)
	}
	owner := mustUser(t, database, "owner", RoleMember)
	reader := mustUser(t, database, "reader", RoleReader)
	shelf, err := database.CreateShelf(t.Context(), owner.ID, ShelfShared, "Science fiction", ShelfQuery, `genre:"Fiction.Science Fiction"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), reader.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []int64{shelf.ID}}); err != nil {
		t.Fatal(err)
	}
	scope := VisibilityScope{UserID: reader.ID, ContentScope: ContentScopeShelves}
	assertTagSearchResults(t, database, scope, reader.ID, `genre:"Fiction"`, []int64{1, 4})
	for _, id := range []int64{1, 2, 3, 4, 5, 6} {
		allowed, err := CanAccessBook(database.Read(t.Context()), scope, id)
		if err != nil || allowed != (id == 1 || id == 4) {
			t.Fatalf("book %d access = %v, %v", id, allowed, err)
		}
	}
}

func TestTagHierarchyBranchEdits(t *testing.T) {
	database := newTestDB(t)
	seedHierarchyBook(t, database, 1, "Fiction.Sci-Fi.Space Opera, Fiction.Fantasy, Other", "Fiction.Sci-Fi.Space Opera")
	seedHierarchyBook(t, database, 2, "Books.Sci-Fi.Space Opera, Fiction.Sci-Fi.Space Opera, Books.Sci-Fi.Hard, Books", "")
	seedHierarchyBook(t, database, 3, "Fiction.Sci-Fi", "")
	mustExec(t, database, "UPDATE books SET deleted_at = 1 WHERE id = 3")
	owner := mustUser(t, database, "owner", RoleMember)
	reader := mustUser(t, database, "reader", RoleReader)
	shelf, err := database.CreateShelf(t.Context(), owner.ID, ShelfShared, "Opera", ShelfQuery, `genre:"Fiction" genre:="Fiction.Sci-Fi.Space Opera"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUserAccess(t.Context(), reader.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []int64{shelf.ID}}); err != nil {
		t.Fatal(err)
	}
	mutate := func(name, target string, wantErr error) {
		t.Helper()
		err := database.Transact(t.Context(), func(tx *Tx) error {
			var id int64
			if err := tx.QueryRow("SELECT id FROM tags WHERE kind = 'genre' AND name = ?", name).Scan(&id); err != nil {
				return err
			}
			var ids []int64
			var err error
			if target == "" {
				ids, err = DeleteTag(tx, id)
			} else {
				ids, _, err = RenameOrMergeTag(tx, id, target)
			}
			if err != nil {
				return err
			}
			for _, bookID := range ids {
				if err := UpdateSearchIndex(tx, bookID); err != nil {
					return err
				}
			}
			return nil
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("move %q to %q: %v; want %v", name, target, err, wantErr)
		}
	}
	check := func(wants ...string) {
		t.Helper()
		values, err := TagsByBookIDs(database.Read(t.Context()), []int64{1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range wants {
			if got := bookmeta.FormatTagList(values[int64(i+1)].Genres); got != want {
				t.Fatalf("book %d genres = %q; want %q", i+1, got, want)
			}
		}
		if !slices.Equal(values[1].Tags, []string{"Fiction.Sci-Fi.Space Opera"}) {
			t.Fatalf("other kind changed: %v", values[1].Tags)
		}
	}
	mutate("Fiction", "books", nil)
	check("Books.Sci-Fi.Space Opera, Books.Fantasy, Other", "Books.Sci-Fi.Space Opera, Books.Sci-Fi.Hard, Books", "Books.Sci-Fi")
	saved, err := GetShelf(database.Read(t.Context()), shelf.ID, owner.ID)
	if err != nil || saved.Query != `genre:"Books" genre:="Books.Sci-Fi.Space Opera"` {
		t.Fatalf("saved branch and direct selectors = %+v, %v", saved, err)
	}
	mutate("Books", "Books.Fantasy.Nested", ErrTagCycle)
	mutate("Books", ".NET", ErrTagPath)
	mutate("Books", strings.Repeat("Level.", bookmeta.MaxTagDepth-1)+"Leaf", ErrTagPath)
	check("Books.Sci-Fi.Space Opera, Books.Fantasy, Other", "Books.Sci-Fi.Space Opera, Books.Sci-Fi.Hard, Books", "Books.Sci-Fi")
	mutate("Books.Sci-Fi", "Books", nil)
	check("Books.Space Opera, Books.Fantasy, Other", "Books.Space Opera, Books.Hard, Books", "Books")
	mutate("Books", "books", nil)
	check("books.Space Opera, books.Fantasy, Other", "books.Space Opera, books.Hard, books", "books")
	assertTagSearchResults(t, database, VisibilityScope{UserID: reader.ID, ContentScope: ContentScopeShelves}, reader.ID, "", []int64{1, 2})
	mutate("books", "", nil)
	check("Other", "", "")
	mutate("Other", ".NET", nil)
	check(".NET", "", "")
	var count int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM tags WHERE kind = 'genre'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphan branch nodes remain: %d, %v", count, err)
	}
}

func TestTagHierarchyDictionary(t *testing.T) {
	database := newTestDB(t)
	for i, tags := range []string{
		"A.Left.Deep, A.Left, Flat",
		"A.Right, A, Flat",
		"A.Left.Deep",
		"A.Hidden.Child, Flat.Hidden, Hidden",
		"A.Trash.Child, Flat.Trash, Trash-only",
	} {
		seedHierarchyBook(t, database, int64(i+1), "A.OtherKind", tags)
	}
	mustExec(t, database, "UPDATE books SET deleted_at = 1 WHERE id = 5")
	owner := mustUser(t, database, "owner", RoleMember)
	reader := mustUser(t, database, "reader", RoleReader)
	shelf, err := database.CreateShelf(t.Context(), owner.ID, ShelfShared, "Visible", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2, 5} {
		mustExec(t, database, "INSERT INTO shelf_books (shelf_id, book_id) VALUES (?, ?)", shelf.ID, id)
	}
	if _, err := database.UpdateUserAccess(t.Context(), reader.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []int64{shelf.ID}}); err != nil {
		t.Fatal(err)
	}
	scoped := VisibilityScope{UserID: reader.ID, ContentScope: ContentScopeShelves}
	type entry struct {
		name     string
		count    int
		children bool
	}
	for _, tc := range []struct {
		name          string
		scope         VisibilityScope
		parent, query string
		want          []entry
	}{
		{"full roots", FullVisibilityScope(), "", "", []entry{{"A", 4, true}, {"Flat", 3, true}, {"Hidden", 1, false}}},
		{"scoped roots", scoped, "", "", []entry{{"A", 2, true}, {"Flat", 2, false}}},
		{"full children", FullVisibilityScope(), "A", "", []entry{{"A.Hidden", 1, true}, {"A.Left", 2, true}, {"A.Right", 1, false}}},
		{"scoped children", scoped, "A", "", []entry{{"A.Left", 1, true}, {"A.Right", 1, false}}},
		{"hidden children", scoped, "Flat", "", nil},
		{"search across branches and levels", scoped, "Flat", "LEFT", []entry{{"A.Left", 1, true}, {"A.Left.Deep", 1, false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := ListTagCountsPage(database.Read(t.Context()), tc.scope, TagListOptions{
				Kind: TagKindTag, Query: tc.query, ParentName: tc.parent,
			})
			if err != nil {
				t.Fatal(err)
			}
			var got []entry
			for _, row := range rows {
				got = append(got, entry{row.Name, row.BookCount, row.HasChildren})
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("dictionary = %+v; want %+v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		scope VisibilityScope
		want  []string
	}{
		{FullVisibilityScope(), []string{"A", "A.Hidden", "A.Hidden.Child", "A.Left", "A.Left.Deep", "A.Right", "Flat", "Flat.Hidden", "Hidden"}},
		{scoped, []string{"A", "A.Left", "A.Left.Deep", "A.Right", "Flat"}},
	} {
		got, err := ListTags(database.Read(t.Context()), tc.scope, TagKindTag, "", 0)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("suggestions = %v, %v; want %v", got, err, tc.want)
		}
	}
}

func TestTagHierarchyCleanup(t *testing.T) {
	database := newTestDB(t)
	seedHierarchyBook(t, database, 1, "Keep.Live.Leaf, .NET", "")
	seedHierarchyBook(t, database, 2, "Keep.Trash.Leaf", "")
	seedHierarchyBook(t, database, 3, strings.Repeat("Level.", bookmeta.MaxTagDepth-1)+"Leaf", "Unused")
	mustExec(t, database, "UPDATE books SET deleted_at = 1 WHERE id = 2")
	mustExec(t, database, "DELETE FROM book_tags WHERE book_id = 3")
	for range 2 {
		if err := database.Transact(t.Context(), func(tx *Tx) error { return DeleteOrphanTags(tx) }); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM tags").Scan(&count); err != nil || count != 6 {
			t.Fatalf("remaining nodes = %d, %v; want 6", count, err)
		}
		values, err := TagsByBookIDs(database.Read(t.Context()), []int64{1, 2})
		if err != nil || !slices.Equal(values[1].Genres, []string{"Keep.Live.Leaf", ".NET"}) || !slices.Equal(values[2].Genres, []string{"Keep.Trash.Leaf"}) {
			t.Fatalf("live and trashed memberships = %+v, %v", values, err)
		}
	}
}

func TestTagHierarchyLiteralAndDeepNames(t *testing.T) {
	database := newTestDB(t)
	deep := strings.Repeat("Level.", bookmeta.MaxTagDepth-1) + "Leaf"
	literal := strings.Repeat("Long.", bookmeta.MaxTagDepth) + "Leaf"
	seedHierarchyBook(t, database, 1, ".NET, A..B, "+deep+", "+literal, "")
	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{`genre:".NET"`, []int64{1}},
		{`genre:"A..B"`, []int64{1}},
		{`genre:"Level"`, []int64{1}},
		{QueryTerm("genre", deep), []int64{1}},
		{`genre:"Long"`, nil},
		{QueryTerm("genre", literal), []int64{1}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			assertTagSearchResults(t, database, FullVisibilityScope(), 0, tc.query, tc.want)
		})
	}
}

func seedHierarchyBook(t *testing.T, database *DB, id int64, genres, tags string) {
	t.Helper()
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, ?, ?)", id, id, id)
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		if err := SetBookTags(tx, id, TagKindGenre, bookmeta.ParseTagList(genres)); err != nil {
			return err
		}
		if err := SetBookTags(tx, id, TagKindTag, bookmeta.ParseTagList(tags)); err != nil {
			return err
		}
		return UpdateSearchIndex(tx, id)
	}); err != nil {
		t.Fatal(err)
	}
}
