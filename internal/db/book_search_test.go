package db

import (
	"context"
	"slices"
	"testing"
)

func TestSearchFilterAccessScopeReasons(t *testing.T) {
	tests := []struct {
		name   string
		kind   searchFilterKind
		reason string
	}{
		{name: "unknown", kind: searchFilterUnknown, reason: queryScopeShelfReason},
		{name: "missing cover", kind: searchMissingCover, reason: noCoverScopeShelfReason},
		{name: "missing tags", kind: searchMissingTags, reason: noTagsScopeShelfReason},
		{name: "missing description", kind: searchMissingDescription, reason: noDescriptionScopeShelfReason},
		{name: "missing author", kind: searchMissingAuthor, reason: noAuthorScopeShelfReason},
		{name: "missing series", kind: searchMissingSeries, reason: noSeriesScopeShelfReason},
		{name: "reading status", kind: searchReadingStatus, reason: statusScopeShelfReason},
	}
	if got, want := len(tests), int(searchFilterKindCount); got != want {
		t.Fatalf("tested filter kinds = %d; want %d (add an explicit access-scope decision for the new kind)", got, want)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := parsedSearchQuery{filters: []searchFilter{{kind: test.kind}}}
			match, reason := query.accessScope()
			if match != "" || reason != test.reason {
				t.Fatalf("access scope = match %q, reason %q; want empty match and %q", match, reason, test.reason)
			}
			if test.kind != searchFilterUnknown && searchFilterCondition(test.kind) == "" {
				t.Fatal("search filter condition is empty")
			}
		})
	}

	query := parsedSearchQuery{filters: []searchFilter{{kind: searchFilterKind(255)}}}
	if match, reason := query.accessScope(); match != "" || reason != queryScopeShelfReason {
		t.Fatalf("unexpected filter access scope = match %q, reason %q; want fail-closed reason %q", match, reason, queryScopeShelfReason)
	}
}

func TestBookSearchConsumersSelectTheSameWorks(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("search-reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title, cover_version) VALUES
			('w1', 'Alpha Needle', 'Alpha Needle', 0),
			('w2', 'Beta Needle', 'Beta Needle', 1),
			('w3', 'Gamma', 'Gamma', 0);
		INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES
			('a1', 'w1', 'a.epub', 'a.epub', '.epub'),
			('a2', 'w2', 'b.epub', 'b.epub', '.epub'),
			('a3', 'w3', 'c.epub', 'c.epub', '.epub');
		INSERT INTO search (work_id, title, authors) VALUES
			('w1', 'Alpha Needle', ''), ('w2', 'Beta Needle', ''), ('w3', 'Gamma', '');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), user.ID, "w2", ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("set status: %v", err)
	}

	tests := []struct {
		query string
		want  []string
	}{
		{query: "need", want: []string{"w1", "w2"}},
		{query: "no:cover", want: []string{"w1", "w3"}},
		{query: "no:cover need", want: []string{"w1"}},
		{query: "status:finished need", want: []string{"w2"}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			books, err := ListBooks(database, FullVisibilityScope(), user.ID, tt.query, SortTitle, 10, 0)
			if err != nil {
				t.Fatalf("list books: %v", err)
			}
			if got := bookIDs(books); !slices.Equal(got, tt.want) {
				t.Fatalf("list ids = %v; want %v", got, tt.want)
			}

			publications, err := SearchOPDSPublications(database, FullVisibilityScope(), user.ID, tt.query, 10, 0)
			if err != nil {
				t.Fatalf("search OPDS: %v", err)
			}
			opdsIDs := make([]string, 0, len(publications))
			for _, publication := range publications {
				opdsIDs = append(opdsIDs, publication.ID)
			}
			slices.Sort(opdsIDs)
			if !slices.Equal(opdsIDs, tt.want) {
				t.Fatalf("OPDS ids = %v; want %v", opdsIDs, tt.want)
			}
			count, err := CountSearchOPDSPublications(database, FullVisibilityScope(), user.ID, tt.query)
			if err != nil || count != len(tt.want) {
				t.Fatalf("OPDS count = %d, err %v; want %d", count, err, len(tt.want))
			}

			sequence, err := BookSequenceInList(database, FullVisibilityScope(), user.ID, tt.want[0], tt.query, SortTitle, 10, 10)
			if err != nil {
				t.Fatalf("book sequence: %v", err)
			}
			sequenceIDs := make([]string, 0, len(sequence.Items))
			for _, item := range sequence.Items {
				sequenceIDs = append(sequenceIDs, item.ID)
			}
			if sequence.Total != len(tt.want) || !slices.Equal(sequenceIDs, tt.want) {
				t.Fatalf("sequence = total %d ids %v; want %d %v", sequence.Total, sequenceIDs, len(tt.want), tt.want)
			}
		})
	}
}

func TestSearchRelevancePrefersIdentityFields(t *testing.T) {
	database := newTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES
			('w_title', 'Needle', 'Needle'),
			('w_author', 'Other', 'Other'),
			('w_series', 'Different', 'Different'),
			('w_description', 'Another', 'Another');
		INSERT INTO search (work_id, title, authors, series, description) VALUES
			('w_title', 'Needle', 'Other', '', ''),
			('w_author', 'Other', 'Needle', '', ''),
			('w_series', 'Different', 'Someone', 'Needle', ''),
			('w_description', 'Another', 'Someone', '', 'Needle');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	books, err := ListBooks(database, FullVisibilityScope(), "", "needle", SortRelevance, 10, 0)
	if err != nil {
		t.Fatalf("search books: %v", err)
	}
	want := []string{"w_title", "w_author", "w_series", "w_description"}
	if got := bookIDs(books); !slices.Equal(got, want) {
		t.Fatalf("relevance order = %v; want %v", got, want)
	}
}

func TestSearchPrefixMatching(t *testing.T) {
	database := newTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES
			('w_latin', 'Foundation Base', 'Foundation Base'),
			('w_han', '地球往事', '地球往事'),
			('w_kana', 'ねこ物語', 'ねこ物語');
		INSERT INTO search (work_id, title) VALUES
			('w_latin', 'Foundation Base'),
			('w_han', '地球往事'),
			('w_kana', 'ねこ物語');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "single Latin character stays exact", query: "f"},
		{name: "Latin prefix", query: "fo", want: []string{"w_latin"}},
		{name: "trailing word prefix", query: "foundation ba", want: []string{"w_latin"}},
		{name: "completed phrase stays exact", query: `"foundation ba"`},
		{name: "single Han character prefix", query: "地", want: []string{"w_han"}},
		{name: "single kana character prefix", query: "ね", want: []string{"w_kana"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			books, err := ListBooks(database, FullVisibilityScope(), "", tt.query, SortTitle, 10, 0)
			if err != nil {
				t.Fatalf("search books: %v", err)
			}
			if got := bookIDs(books); !slices.Equal(got, tt.want) {
				t.Fatalf("book IDs = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestUpdateSearchIndexReplacesContentlessRow(t *testing.T) {
	database := newTestDB(t)

	if _, err := database.Exec("INSERT INTO works (id, title, sort_title) VALUES ('w_min', 'Minimal Book', 'Minimal Book')"); err != nil {
		t.Fatalf("insert work: %v", err)
	}

	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := UpdateSearchIndex(tx, "w_min"); err != nil {
		tx.Rollback()
		t.Fatalf("UpdateSearchIndex: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var workID string
	if err := database.QueryRow(`SELECT work_id FROM search WHERE search MATCH 'minimal'`).Scan(&workID); err != nil {
		t.Fatalf("find indexed work: %v", err)
	}
	if workID != "w_min" {
		t.Fatalf("indexed work = %q; want w_min", workID)
	}

	if _, err := database.Exec("UPDATE works SET title = 'Renamed Book', sort_title = 'Renamed Book' WHERE id = 'w_min'"); err != nil {
		t.Fatalf("rename work: %v", err)
	}
	tx, err = database.Begin()
	if err != nil {
		t.Fatalf("begin reindex: %v", err)
	}
	if err := UpdateSearchIndex(tx, "w_min"); err != nil {
		tx.Rollback()
		t.Fatalf("reindex: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit reindex: %v", err)
	}

	var oldMatches, newMatches int
	if err := database.QueryRow(`SELECT count(*) FROM search WHERE search MATCH 'minimal'`).Scan(&oldMatches); err != nil {
		t.Fatalf("query old title: %v", err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM search WHERE search MATCH 'renamed'`).Scan(&newMatches); err != nil {
		t.Fatalf("query new title: %v", err)
	}
	if oldMatches != 0 || newMatches != 1 {
		t.Fatalf("matches after reindex = old %d, new %d; want 0, 1", oldMatches, newMatches)
	}
}
