package metalookup

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
)

const (
	defaultProviderCacheMaxEntries = 64
	defaultProviderCacheTTL        = 5 * time.Minute
)

type cachedProvider struct {
	provider   Provider
	maxEntries int
	ttl        time.Duration

	mu          sync.Mutex
	search      map[string]cachedSearchEntry
	searchOrder []string
	desc        map[string]cachedDescriptionEntry
	descOrder   []string
}

type cachedSearchEntry struct {
	expires time.Time
	value   []Candidate
}

type cachedDescriptionEntry struct {
	expires time.Time
	value   string
}

// NewCachedRegistry wraps the normal metadata providers with a tiny in-process,
// success-only cache. It is deliberately bounded and non-persistent: it smooths
// repeated review/fetch clicks without becoming another source of truth.
func NewCachedRegistry(client *http.Client) Registry {
	registry := NewRegistry(client)
	for id, provider := range registry {
		registry[id] = newCachedProvider(provider, defaultProviderCacheMaxEntries, defaultProviderCacheTTL)
	}
	return registry
}

func newCachedProvider(provider Provider, maxEntries int, ttl time.Duration) Provider {
	if provider == nil || maxEntries <= 0 || ttl <= 0 {
		return provider
	}
	return &cachedProvider{
		provider:   provider,
		maxEntries: maxEntries,
		ttl:        ttl,
		search:     make(map[string]cachedSearchEntry),
		desc:       make(map[string]cachedDescriptionEntry),
	}
}

func (p *cachedProvider) ID() string { return p.provider.ID() }

func (p *cachedProvider) Name() string { return p.provider.Name() }

func (p *cachedProvider) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := searchCacheKey(q)
	if cached, ok := p.getSearch(key); ok {
		return cached, nil
	}

	candidates, err := p.provider.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	p.putSearch(key, candidates)
	return cloneCandidates(candidates), nil
}

func (p *cachedProvider) FetchDescription(ctx context.Context, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fetcher, ok := p.provider.(DescriptionFetcher)
	if !ok {
		return "", nil
	}

	key := strings.TrimSpace(ref)
	if cached, ok := p.getDescription(key); ok {
		return cached, nil
	}
	description, err := fetcher.FetchDescription(ctx, ref)
	if err != nil {
		return "", err
	}
	p.putDescription(key, description)
	return description, nil
}

func (p *cachedProvider) getSearch(key string) ([]Candidate, bool) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.search[key]
	if !ok {
		return nil, false
	}
	if now.After(entry.expires) {
		delete(p.search, key)
		p.searchOrder = removeCacheKey(p.searchOrder, key)
		return nil, false
	}
	return cloneCandidates(entry.value), true
}

func (p *cachedProvider) putSearch(key string, candidates []Candidate) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.searchOrder = removeCacheKey(p.searchOrder, key)
	p.searchOrder = append(p.searchOrder, key)
	p.search[key] = cachedSearchEntry{
		expires: time.Now().Add(p.ttl),
		value:   cloneCandidates(candidates),
	}
	for len(p.search) > p.maxEntries && len(p.searchOrder) > 0 {
		oldest := p.searchOrder[0]
		p.searchOrder = p.searchOrder[1:]
		delete(p.search, oldest)
	}
}

func (p *cachedProvider) getDescription(key string) (string, bool) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.desc[key]
	if !ok {
		return "", false
	}
	if now.After(entry.expires) {
		delete(p.desc, key)
		p.descOrder = removeCacheKey(p.descOrder, key)
		return "", false
	}
	return entry.value, true
}

func (p *cachedProvider) putDescription(key string, description string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.descOrder = removeCacheKey(p.descOrder, key)
	p.descOrder = append(p.descOrder, key)
	p.desc[key] = cachedDescriptionEntry{
		expires: time.Now().Add(p.ttl),
		value:   description,
	}
	for len(p.desc) > p.maxEntries && len(p.descOrder) > 0 {
		oldest := p.descOrder[0]
		p.descOrder = p.descOrder[1:]
		delete(p.desc, oldest)
	}
}

func searchCacheKey(q Query) string {
	return strings.Join([]string{
		cleanISBN(q.ISBN),
		strings.ToLower(strings.TrimSpace(q.Title)),
		strings.ToLower(strings.TrimSpace(q.Author)),
	}, "\x00")
}

func cloneCandidates(in []Candidate) []Candidate {
	out := make([]Candidate, len(in))
	for i, candidate := range in {
		out[i] = candidate
		out[i].Authors = append([]bookmeta.AuthorMeta(nil), candidate.Authors...)
		out[i].Tags = append([]string(nil), candidate.Tags...)
	}
	return out
}

func removeCacheKey(keys []string, key string) []string {
	out := keys[:0]
	for _, existing := range keys {
		if existing != key {
			out = append(out, existing)
		}
	}
	return out
}
