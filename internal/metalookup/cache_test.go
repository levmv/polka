package metalookup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
)

type countingProvider struct {
	searchCalls int
	descCalls   int
	failSearch  bool
}

func (p *countingProvider) ID() string { return "counting" }

func (p *countingProvider) Name() string { return "Counting" }

func (p *countingProvider) Search(_ context.Context, q Query) ([]Candidate, error) {
	p.searchCalls++
	if p.failSearch {
		return nil, errors.New("search failed")
	}
	return []Candidate{{
		Provider: "counting",
		Title:    q.Title,
		Authors:  []bookmeta.AuthorMeta{{Name: q.Author}},
		Tags:     []string{"cached"},
	}}, nil
}

func (p *countingProvider) FetchDescription(_ context.Context, ref string) (string, error) {
	p.descCalls++
	return "description for " + ref, nil
}

func TestCachedProviderCachesSuccessfulSearches(t *testing.T) {
	base := &countingProvider{}
	provider := newCachedProvider(base, 8, time.Hour)
	query := Query{Title: "Dune", Author: "Frank Herbert"}

	first, err := provider.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	first[0].Title = "mutated by caller"
	first[0].Authors[0].Name = "mutated author"
	first[0].Tags[0] = "mutated tag"

	second, err := provider.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if base.searchCalls != 1 {
		t.Fatalf("search calls = %d, want 1", base.searchCalls)
	}
	if second[0].Title != "Dune" || second[0].Authors[0].Name != "Frank Herbert" || second[0].Tags[0] != "cached" {
		t.Fatalf("cached candidates were mutated: %+v", second[0])
	}
}

func TestCachedProviderEvictsOldestSearch(t *testing.T) {
	base := &countingProvider{}
	provider := newCachedProvider(base, 1, time.Hour)

	if _, err := provider.Search(context.Background(), Query{Title: "A"}); err != nil {
		t.Fatalf("search A: %v", err)
	}
	if _, err := provider.Search(context.Background(), Query{Title: "B"}); err != nil {
		t.Fatalf("search B: %v", err)
	}
	if _, err := provider.Search(context.Background(), Query{Title: "A"}); err != nil {
		t.Fatalf("search A again: %v", err)
	}
	if base.searchCalls != 3 {
		t.Fatalf("search calls = %d, want 3 after max=1 eviction", base.searchCalls)
	}
}

func TestCachedProviderDoesNotCacheSearchErrors(t *testing.T) {
	base := &countingProvider{failSearch: true}
	provider := newCachedProvider(base, 8, time.Hour)

	if _, err := provider.Search(context.Background(), Query{Title: "Dune"}); err == nil {
		t.Fatalf("expected first search error")
	}
	base.failSearch = false
	if _, err := provider.Search(context.Background(), Query{Title: "Dune"}); err != nil {
		t.Fatalf("second search after clearing failure: %v", err)
	}
	if base.searchCalls != 2 {
		t.Fatalf("search calls = %d, want 2 because errors are not cached", base.searchCalls)
	}
}

func TestCachedProviderCachesDescriptions(t *testing.T) {
	base := &countingProvider{}
	provider := newCachedProvider(base, 8, time.Hour).(DescriptionFetcher)

	first, err := provider.FetchDescription(context.Background(), "/works/OL1W")
	if err != nil {
		t.Fatalf("first description: %v", err)
	}
	second, err := provider.FetchDescription(context.Background(), " /works/OL1W ")
	if err != nil {
		t.Fatalf("second description: %v", err)
	}
	if first != "description for /works/OL1W" || second != first {
		t.Fatalf("descriptions = %q / %q", first, second)
	}
	if base.descCalls != 1 {
		t.Fatalf("description calls = %d, want 1", base.descCalls)
	}
}
