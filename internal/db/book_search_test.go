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

func TestBookSearchConsumersSelectTheSameBooks(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser(t.Context(), "search-reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title, cover_version, tags) VALUES
			(1, 'Alpha Needle', 'Alpha Needle', 0, 'Science fiction'),
			(2, 'Beta Needle', 'Beta Needle', 1, 'Science'),
			(3, 'Gamma', 'Gamma', 0, 'Other');
		INSERT INTO assets (id, book_id, storage_path, filename, extension) VALUES
			('a1', 1, 'a.epub', 'a.epub', '.epub'),
			('a2', 2, 'b.epub', 'b.epub', '.epub'),
			('a3', 3, 'c.epub', 'c.epub', '.epub');
	`)

	if err := database.Transact(t.Context(), func(tx *Tx) error {
		for _, id := range []int64{1, 2, 3} {
			if err := UpdateSearchIndex(tx, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetReadingStatus(context.Background(), user.ID, 2, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("set status: %v", err)
	}

	tests := []struct {
		query string
		want  []int64
	}{
		{query: "need", want: []int64{1, 2}},
		{query: "no:cover", want: []int64{1, 3}},
		{query: "no:cover need", want: []int64{1}},
		{query: "status:finished need", want: []int64{2}},
		{query: `tag:"science fiction"`, want: []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			books, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), user.ID, tt.query, SortTitle, 10, 0)
			if err != nil {
				t.Fatalf("list books: %v", err)
			}
			if got := bookIDs(books); !slices.Equal(got, tt.want) {
				t.Fatalf("list ids = %v; want %v", got, tt.want)
			}

			publications, err := SearchOPDSPublications(database.Read(t.Context()), FullVisibilityScope(), user.ID, tt.query, 10, 0)
			if err != nil {
				t.Fatalf("search OPDS: %v", err)
			}
			opdsIDs := make([]int64, 0, len(publications))
			for _, publication := range publications {
				opdsIDs = append(opdsIDs, publication.ID)
			}
			slices.Sort(opdsIDs)
			if !slices.Equal(opdsIDs, tt.want) {
				t.Fatalf("OPDS ids = %v; want %v", opdsIDs, tt.want)
			}
			count, err := CountSearchOPDSPublications(database.Read(t.Context()), FullVisibilityScope(), user.ID, tt.query)
			if err != nil || count != len(tt.want) {
				t.Fatalf("OPDS count = %d, err %v; want %d", count, err, len(tt.want))
			}

			sequence, err := BookSequenceInList(database.Read(t.Context()), FullVisibilityScope(), user.ID, tt.want[0], tt.query, SortTitle, 10, 10)
			if err != nil {
				t.Fatalf("book sequence: %v", err)
			}
			sequenceIDs := make([]int64, 0, len(sequence.Items))
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
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES
			(175, 'Needle', 'Needle'),
			(109, 'Other', 'Other'),
			(170, 'Different', 'Different'),
			(123, 'Another', 'Another');
		INSERT INTO search (rowid, title, authors, series, description) VALUES
			(175, 'Needle', 'Other', '', ''),
			(109, 'Other', 'Needle', '', ''),
			(170, 'Different', 'Someone', 'Needle', ''),
			(123, 'Another', 'Someone', '', 'Needle');
	`)

	books, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), 0, "needle", SortRelevance, 10, 0)
	if err != nil {
		t.Fatalf("search books: %v", err)
	}
	want := []int64{175, 109, 170, 123}
	if got := bookIDs(books); !slices.Equal(got, want) {
		t.Fatalf("relevance order = %v; want %v", got, want)
	}
}

func TestSearchPrefixMatching(t *testing.T) {
	database := newTestDB(t)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES
			(146, 'Foundation Base', 'Foundation Base'),
			(137, '地球往事', '地球往事'),
			(139, 'ねこ物語', 'ねこ物語');
		INSERT INTO search (rowid, title) VALUES
			(146, 'Foundation Base'),
			(137, '地球往事'),
			(139, 'ねこ物語');
	`)

	tests := []struct {
		name  string
		query string
		want  []int64
	}{
		{name: "single Latin character stays exact", query: "f"},
		{name: "Latin prefix", query: "fo", want: []int64{146}},
		{name: "trailing word prefix", query: "foundation ba", want: []int64{146}},
		{name: "completed phrase stays exact", query: `"foundation ba"`},
		{name: "single Han character prefix", query: "地", want: []int64{137}},
		{name: "single kana character prefix", query: "ね", want: []int64{139}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			books, err := ListBooks(database.Read(t.Context()), FullVisibilityScope(), 0, tt.query, SortTitle, 10, 0)
			if err != nil {
				t.Fatalf("search books: %v", err)
			}
			if got := bookIDs(books); !slices.Equal(got, tt.want) {
				t.Fatalf("book IDs = %v; want %v", got, tt.want)
			}
		})
	}
}
