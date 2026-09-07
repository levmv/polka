package db

import (
	"context"
	"slices"
	"testing"
)

func TestSeriesQueries(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Isaac Asimov', 'Asimov, Isaac')")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index) VALUES (2, 'Second', 'Second', 'Foundation', 2)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index) VALUES (1, 'First', 'First', 'Foundation', 1)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index) VALUES (3, 'No Number', 'No Number', 'Foundation', NULL)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index) VALUES (6, 'Alpha Unnumbered', 'Alpha Unnumbered', 'Foundation', 0)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index) VALUES (7, 'Zeta Unnumbered', 'Zeta Unnumbered', 'Foundation', -1)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index) VALUES (4, 'Other', 'Other', 'Other Series', 1)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index, deleted_at) VALUES (5, 'Deleted', 'Deleted', 'Foundation', 3, unixepoch())")
	for _, id := range []int64{1, 2, 3, 4, 5, 6, 7} {
		mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 1, 0)", id)
	}

	series, err := ListSeriesCountsPage(database.Read(t.Context()), FullVisibilityScope(), "", "", 10)
	if err != nil {
		t.Fatalf("ListSeriesCountsPage: %v", err)
	}
	if len(series) != 2 || series[0].Name != "Foundation" || series[0].BookCount != 5 || series[1].Name != "Other Series" || series[1].BookCount != 1 {
		t.Fatalf("series = %+v, want Foundation(5), Other Series(1)", series)
	}
	firstSeriesPage, err := ListSeriesCountsPage(database.Read(t.Context()), FullVisibilityScope(), "", "", 1)
	if err != nil {
		t.Fatalf("first series page: %v", err)
	}
	secondSeriesPage, err := ListSeriesCountsPage(database.Read(t.Context()), FullVisibilityScope(), "", firstSeriesPage[0].Name, 1)
	if err != nil {
		t.Fatalf("second series page: %v", err)
	}
	if len(firstSeriesPage) != 1 || firstSeriesPage[0].Name != "Foundation" || len(secondSeriesPage) != 1 || secondSeriesPage[0].Name != "Other Series" {
		t.Fatalf("series pages = %+v then %+v", firstSeriesPage, secondSeriesPage)
	}
	filteredSeries, err := ListSeriesCountsPage(database.Read(t.Context()), FullVisibilityScope(), "other", "", 10)
	if err != nil {
		t.Fatalf("filtered series page: %v", err)
	}
	if len(filteredSeries) != 1 || filteredSeries[0].Name != "Other Series" {
		t.Fatalf("filtered series = %+v; want Other Series", filteredSeries)
	}

	// Series order groups by series name, then numbered volumes by index, then
	// the unnumbered ones by title; series-less books come last.
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (8, 'Standalone', 'Standalone')")
	ordered, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), 0, "", SortSeries, 10, 0)
	if err != nil {
		t.Fatalf("list books by series order: %v", err)
	}
	got := make([]int64, 0, len(ordered))
	for _, b := range ordered {
		got = append(got, b.ID)
	}
	want := []int64{1, 2, 6, 3, 7, 4, 8}
	if !slices.Equal(got, want) {
		t.Fatalf("series-ordered books = %v, want %v", got, want)
	}
}

// The Series page tiles carry a representative cover and the viewer's own
// finished count, both of which have to survive volumes without covers, other
// users' progress, and trashed books.
func TestSeriesCardsPage(t *testing.T) {
	database := newTestDB(t)

	reader, err := database.CreateUser(t.Context(), "reader", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	other, err := database.CreateUser(t.Context(), "other", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Isaac Asimov', 'Asimov, Isaac')")
	// Volume 1 has no cover, so volume 2 represents the series.
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index, cover_version) VALUES (1, 'First', 'First', 'Foundation', 1, 0)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index, cover_version) VALUES (2, 'Second', 'Second', 'Foundation', 2, 3)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index, cover_version) VALUES (3, 'Third', 'Third', 'Foundation', 3, 1)")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index, cover_version, deleted_at) VALUES (4, 'Trashed', 'Trashed', 'Foundation', 4, 1, unixepoch())")
	// No volume of this series has a cover: the first one still represents it.
	mustExec(t, database, "INSERT INTO books (id, title, sort_title, series, series_index, cover_version) VALUES (5, 'Only', 'Only', 'Other Series', 1, 0)")

	for _, id := range []int64{1, 2, 3, 4} {
		mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 1, 0)", id)
	}

	ctx := context.Background()
	if _, err := database.SetReadingStatus(ctx, reader.ID, 1, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("finish 1: %v", err)
	}
	if _, err := database.SetReadingStatus(ctx, reader.ID, 3, ReadingStatusReading, ReadingStatusSourceManual); err != nil {
		t.Fatalf("start 3: %v", err)
	}
	if _, err := database.SetReadingStatus(ctx, other.ID, 2, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("finish 2 for other user: %v", err)
	}

	cards, err := ListSeriesCardsPage(database.Read(ctx), FullVisibilityScope(), reader.ID, "", "", 10)
	if err != nil {
		t.Fatalf("ListSeriesCardsPage: %v", err)
	}
	want := []SeriesCard{
		{Name: "Foundation", Author: "Isaac Asimov", BookCount: 3, FinishedCount: 1, CoverBookID: 2, CoverVersion: 3},
		// "Other Series" has no author linked, so the tile carries none.
		{Name: "Other Series", Author: "", BookCount: 1, FinishedCount: 0, CoverBookID: 5, CoverVersion: 0},
	}
	if !slices.Equal(cards, want) {
		t.Fatalf("series cards = %+v, want %+v", cards, want)
	}

	// A scoped reader sees only the volumes on their shelf, so counts, cover,
	// and finished count all have to be computed inside that scope.
	scoped, err := database.CreateUser(t.Context(), "scoped", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create scoped user: %v", err)
	}
	shelf, err := database.CreateShelf(t.Context(), reader.ID, ShelfShared, "Shared", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	for _, bookID := range []int64{1, 3} {
		if err := database.AddBookToShelf(t.Context(), shelf.ID, 0, bookID); err != nil {
			t.Fatalf("add %d to shelf: %v", bookID, err)
		}
	}
	if _, err := database.UpdateUserAccess(t.Context(), scoped.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []int64{shelf.ID}}); err != nil {
		t.Fatalf("scope user: %v", err)
	}
	if _, err := database.SetReadingStatus(ctx, scoped.ID, 3, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("finish 3 for scoped user: %v", err)
	}
	scope, err := VisibilityScopeForUser(database.Read(ctx), scoped.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}

	scopedCards, err := ListSeriesCardsPage(database.Read(ctx), scope, scoped.ID, "", "", 10)
	if err != nil {
		t.Fatalf("scoped ListSeriesCardsPage: %v", err)
	}
	// w2 carries the cover but is out of scope, so w3 represents the series.
	wantScoped := []SeriesCard{
		{Name: "Foundation", Author: "Isaac Asimov", BookCount: 2, FinishedCount: 1, CoverBookID: 3, CoverVersion: 1},
	}
	if !slices.Equal(scopedCards, wantScoped) {
		t.Fatalf("scoped series cards = %+v, want %+v", scopedCards, wantScoped)
	}
}
