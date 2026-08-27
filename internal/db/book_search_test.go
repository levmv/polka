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
		{query: "needle", want: []string{"w1", "w2"}},
		{query: "no:cover", want: []string{"w1", "w3"}},
		{query: "no:cover needle", want: []string{"w1"}},
		{query: "status:finished needle", want: []string{"w2"}},
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

func TestUpdateSearchIndexCoalescesNullableFields(t *testing.T) {
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

	var title, authors, series, tags, description, identifiers, filename string
	err = database.QueryRow(`
		SELECT title, authors, series, tags, description, identifiers, filename
		FROM search
		WHERE work_id = 'w_min'
	`).Scan(&title, &authors, &series, &tags, &description, &identifiers, &filename)
	if err != nil {
		t.Fatalf("query search row: %v", err)
	}
	if title != "Minimal Book" {
		t.Fatalf("title = %q, want Minimal Book", title)
	}
	for name, got := range map[string]string{
		"authors":     authors,
		"series":      series,
		"tags":        tags,
		"description": description,
		"identifiers": identifiers,
		"filename":    filename,
	} {
		if got != "" {
			t.Fatalf("%s = %q, want empty string", name, got)
		}
	}
}
