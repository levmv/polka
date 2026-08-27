package db

import (
	"context"
	"slices"
	"testing"
)

func TestSeriesQueries(t *testing.T) {
	database := newTestDB(t)

	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Isaac Asimov', 'Asimov, Isaac')")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index) VALUES ('w2', 'Second', 'Second', 'Foundation', 2)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index) VALUES ('w1', 'First', 'First', 'Foundation', 1)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index) VALUES ('w3', 'No Number', 'No Number', 'Foundation', NULL)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index) VALUES ('w6', 'Alpha Unnumbered', 'Alpha Unnumbered', 'Foundation', 0)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index) VALUES ('w7', 'Zeta Unnumbered', 'Zeta Unnumbered', 'Foundation', -1)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index) VALUES ('w4', 'Other', 'Other', 'Other Series', 1)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index, deleted_at) VALUES ('w5', 'Deleted', 'Deleted', 'Foundation', 3, unixepoch())")
	for _, id := range []string{"w1", "w2", "w3", "w4", "w5", "w6", "w7"} {
		database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'a1', 0)", id)
	}

	series, err := ListSeriesCountsPage(database, FullVisibilityScope(), "", "", 10)
	if err != nil {
		t.Fatalf("ListSeriesCountsPage: %v", err)
	}
	if len(series) != 2 || series[0].Name != "Foundation" || series[0].BookCount != 5 || series[1].Name != "Other Series" || series[1].BookCount != 1 {
		t.Fatalf("series = %+v, want Foundation(5), Other Series(1)", series)
	}
	firstSeriesPage, err := ListSeriesCountsPage(database, FullVisibilityScope(), "", "", 1)
	if err != nil {
		t.Fatalf("first series page: %v", err)
	}
	secondSeriesPage, err := ListSeriesCountsPage(database, FullVisibilityScope(), "", firstSeriesPage[0].Name, 1)
	if err != nil {
		t.Fatalf("second series page: %v", err)
	}
	if len(firstSeriesPage) != 1 || firstSeriesPage[0].Name != "Foundation" || len(secondSeriesPage) != 1 || secondSeriesPage[0].Name != "Other Series" {
		t.Fatalf("series pages = %+v then %+v", firstSeriesPage, secondSeriesPage)
	}
	filteredSeries, err := ListSeriesCountsPage(database, FullVisibilityScope(), "other", "", 10)
	if err != nil {
		t.Fatalf("filtered series page: %v", err)
	}
	if len(filteredSeries) != 1 || filteredSeries[0].Name != "Other Series" {
		t.Fatalf("filtered series = %+v; want Other Series", filteredSeries)
	}

	// Series order groups by series name, then numbered volumes by index, then
	// the unnumbered ones by title; series-less books come last.
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w8', 'Standalone', 'Standalone')")
	ordered, err := ListBooks(database, FullVisibilityScope(), "", "", SortSeries, 10, 0)
	if err != nil {
		t.Fatalf("list books by series order: %v", err)
	}
	got := make([]string, 0, len(ordered))
	for _, b := range ordered {
		got = append(got, b.ID)
	}
	want := []string{"w1", "w2", "w6", "w3", "w7", "w4", "w8"}
	if !slices.Equal(got, want) {
		t.Fatalf("series-ordered books = %v, want %v", got, want)
	}
}

// The Series page tiles carry a representative cover and the viewer's own
// finished count, both of which have to survive volumes without covers, other
// users' progress, and trashed books.
func TestSeriesCardsPage(t *testing.T) {
	database := newTestDB(t)

	reader, err := database.CreateUser("reader", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	other, err := database.CreateUser("other", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}

	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('a1', 'Isaac Asimov', 'Asimov, Isaac')")
	// Volume 1 has no cover, so volume 2 represents the series.
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index, cover_version) VALUES ('w1', 'First', 'First', 'Foundation', 1, 0)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index, cover_version) VALUES ('w2', 'Second', 'Second', 'Foundation', 2, 3)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index, cover_version) VALUES ('w3', 'Third', 'Third', 'Foundation', 3, 1)")
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index, cover_version, deleted_at) VALUES ('w4', 'Trashed', 'Trashed', 'Foundation', 4, 1, unixepoch())")
	// No volume of this series has a cover: the first one still represents it.
	database.Exec("INSERT INTO works (id, title, sort_title, series, series_index, cover_version) VALUES ('w5', 'Only', 'Only', 'Other Series', 1, 0)")

	for _, id := range []string{"w1", "w2", "w3", "w4"} {
		database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'a1', 0)", id)
	}

	ctx := context.Background()
	if _, err := database.SetReadingStatus(ctx, reader.ID, "w1", ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("finish w1: %v", err)
	}
	if _, err := database.SetReadingStatus(ctx, reader.ID, "w3", ReadingStatusReading, ReadingStatusSourceManual); err != nil {
		t.Fatalf("start w3: %v", err)
	}
	if _, err := database.SetReadingStatus(ctx, other.ID, "w2", ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("finish w2 for other user: %v", err)
	}

	cards, err := ListSeriesCardsPage(database, FullVisibilityScope(), reader.ID, "", "", 10)
	if err != nil {
		t.Fatalf("ListSeriesCardsPage: %v", err)
	}
	want := []SeriesCard{
		{Name: "Foundation", Author: "Isaac Asimov", BookCount: 3, FinishedCount: 1, CoverWorkID: "w2", CoverVersion: 3},
		// "Other Series" has no author linked, so the tile carries none.
		{Name: "Other Series", Author: "", BookCount: 1, FinishedCount: 0, CoverWorkID: "w5", CoverVersion: 0},
	}
	if !slices.Equal(cards, want) {
		t.Fatalf("series cards = %+v, want %+v", cards, want)
	}

	// A scoped reader sees only the volumes on their shelf, so counts, cover,
	// and finished count all have to be computed inside that scope.
	scoped, err := database.CreateUser("scoped", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create scoped user: %v", err)
	}
	shelf, err := database.CreateShelf(reader.ID, ShelfShared, "Shared", ShelfManual, "")
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	for _, workID := range []string{"w1", "w3"} {
		if err := database.AddBookToShelf(shelf.ID, "", workID); err != nil {
			t.Fatalf("add %s to shelf: %v", workID, err)
		}
	}
	if _, err := database.UpdateUserAccess(scoped.ID, UserAccess{Role: RoleReader, ContentScope: ContentScopeShelves, ShelfIDs: []string{shelf.ID}}); err != nil {
		t.Fatalf("scope user: %v", err)
	}
	if _, err := database.SetReadingStatus(ctx, scoped.ID, "w3", ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("finish w3 for scoped user: %v", err)
	}
	scope, err := database.VisibilityScopeForUser(scoped.ID)
	if err != nil {
		t.Fatalf("visibility scope: %v", err)
	}

	scopedCards, err := ListSeriesCardsPage(database, scope, scoped.ID, "", "", 10)
	if err != nil {
		t.Fatalf("scoped ListSeriesCardsPage: %v", err)
	}
	// w2 carries the cover but is out of scope, so w3 represents the series.
	wantScoped := []SeriesCard{
		{Name: "Foundation", Author: "Isaac Asimov", BookCount: 2, FinishedCount: 1, CoverWorkID: "w3", CoverVersion: 1},
	}
	if !slices.Equal(scopedCards, wantScoped) {
		t.Fatalf("scoped series cards = %+v, want %+v", scopedCards, wantScoped)
	}
}
