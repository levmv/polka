package metalookup

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
)

const (
	DefaultProviderID        = "openlibrary"
	userAgent                = "github.com/levmv/polka/0 (personal library metadata lookup)"
	maxProviderResponseBytes = 4 << 20
)

var defaultHTTPClient = &http.Client{Timeout: 8 * time.Second}

type Query struct {
	ISBN   string
	Title  string
	Author string
}

type Candidate struct {
	bookmeta.Metadata
	Provider   string
	ProviderID string
	CoverURL   string
	Score      float64
}

// CandidateIdentifiers returns the external identifiers worth persisting when
// a user accepts a candidate. Provider references are provenance, not transient
// lookup handles: retaining them makes later refreshes and migrations independent
// of title matching. The common formatter also deduplicates providers such as
// Google Books that already include their reference in Metadata.Identifier.
func CandidateIdentifiers(c Candidate) string {
	ids := bookmeta.ParseIdentifiers(c.Identifier)
	ids = append(ids, bookmeta.Identifier{Type: c.Provider, Value: c.ProviderID})
	return identifiers(ids)
}

type Provider interface {
	ID() string
	Name() string
	Search(ctx context.Context, q Query) ([]Candidate, error)
}

// DescriptionFetcher is an optional provider capability: fetch the long
// description for one candidate by its provider reference (e.g. an Open Library
// work key). Search keeps responses light and often omits descriptions, so the
// UI fetches one lazily only for the candidate a user is actually reviewing.
type DescriptionFetcher interface {
	FetchDescription(ctx context.Context, ref string) (string, error)
}

type Registry map[string]Provider

func NewRegistry(client *http.Client) Registry {
	return Registry{
		"openlibrary": NewOpenLibraryProvider(client),
		"google":      NewGoogleBooksProvider(client),
	}
}

func (r Registry) Get(id string) (Provider, bool) {
	if id == "" {
		id = DefaultProviderID
	}
	p, ok := r[strings.ToLower(id)]
	return p, ok
}

func httpClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return defaultHTTPClient
}

func newProviderGET(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func decodeProviderJSON(res *http.Response, dst any) error {
	body, err := io.ReadAll(io.LimitReader(res.Body, maxProviderResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxProviderResponseBytes {
		return fmt.Errorf("metadata provider response exceeds %d bytes", maxProviderResponseBytes)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return err
	}
	return nil
}

func cleanISBN(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func authorMetas(names []string) []bookmeta.AuthorMeta {
	authors := make([]bookmeta.AuthorMeta, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		authors = append(authors, bookmeta.AuthorMeta{Name: name, SortName: bookmeta.AuthorSort(name)})
	}
	return authors
}

func firstValidISBN(values []string) string {
	for _, value := range values {
		value = cleanISBN(value)
		if bookmeta.ValidISBN(value) {
			return value
		}
	}
	return ""
}

func identifiers(parts []bookmeta.Identifier) string {
	filtered := make([]bookmeta.Identifier, 0, len(parts))
	seen := make(map[string]bool)
	for _, id := range parts {
		id.Type = strings.ToLower(strings.TrimSpace(id.Type))
		id.Value = strings.TrimSpace(id.Value)
		if id.Type == "" || id.Value == "" || bookmeta.IsInternalIdentifier(id) {
			continue
		}
		key := id.Type + "\x00" + strings.ToLower(id.Value)
		if seen[key] {
			continue
		}
		seen[key] = true
		filtered = append(filtered, id)
	}
	return bookmeta.FormatIdentifiers(filtered)
}

func trimStrings(values []string, limit int) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func score(q Query, c Candidate) float64 {
	var s float64
	qISBN := cleanISBN(q.ISBN)
	if qISBN != "" {
		for _, id := range bookmeta.ParseIdentifiers(c.Identifier) {
			if id.Type == "isbn" && cleanISBN(id.Value) == qISBN {
				s += 70
				break
			}
		}
	}
	if containsFold(c.Title, q.Title) || containsFold(q.Title, c.Title) {
		s += 18
	}
	for _, a := range c.Authors {
		if containsFold(a.Name, q.Author) || containsFold(q.Author, a.Name) {
			s += 12
			break
		}
	}
	if c.CoverURL != "" {
		s += 3
	}
	return s
}

func containsFold(s, substr string) bool {
	s = strings.TrimSpace(s)
	substr = strings.TrimSpace(substr)
	if s == "" || substr == "" {
		return false
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func validateQuery(q Query) error {
	if cleanISBN(q.ISBN) == "" && strings.TrimSpace(q.Title) == "" {
		return errors.New("metadata query needs an ISBN or title")
	}
	return nil
}
