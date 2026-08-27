package metalookup

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

type OpenLibraryProvider struct {
	client  *http.Client
	baseURL string
}

func NewOpenLibraryProvider(client *http.Client) *OpenLibraryProvider {
	return &OpenLibraryProvider{
		client:  httpClient(client),
		baseURL: "https://openlibrary.org",
	}
}

func (p *OpenLibraryProvider) ID() string { return "openlibrary" }

func (p *OpenLibraryProvider) Name() string { return "Open Library" }

func (p *OpenLibraryProvider) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if err := validateQuery(q); err != nil {
		return nil, err
	}

	u, err := url.Parse(p.baseURL + "/search.json")
	if err != nil {
		return nil, err
	}
	params := u.Query()
	params.Set("limit", "8")
	params.Set("fields", "key,title,author_name,first_publish_year,publish_year,isbn,language,publisher,subject,cover_i")
	if isbn := cleanISBN(q.ISBN); isbn != "" {
		params.Set("isbn", isbn)
	} else {
		params.Set("title", q.Title)
		if q.Author != "" {
			params.Set("author", q.Author)
		}
	}
	u.RawQuery = params.Encode()

	req, err := newProviderGET(ctx, u.String())
	if err != nil {
		return nil, err
	}

	res, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openlibrary search request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("openlibrary search: %s", res.Status)
	}

	var payload openLibrarySearchResponse
	if err := decodeProviderJSON(res, &payload); err != nil {
		return nil, fmt.Errorf("openlibrary search response: %w", err)
	}
	candidates := openLibraryCandidates(q, payload)
	sortCandidates(candidates)
	return candidates, nil
}

type openLibrarySearchResponse struct {
	Docs []openLibraryDoc `json:"docs"`
}

type openLibraryDoc struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	AuthorName       []string `json:"author_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	PublishYear      []int    `json:"publish_year"`
	ISBN             []string `json:"isbn"`
	Language         []string `json:"language"`
	Publisher        []string `json:"publisher"`
	Subject          []string `json:"subject"`
	CoverID          int      `json:"cover_i"`
}

func openLibraryCandidates(q Query, payload openLibrarySearchResponse) []Candidate {
	out := make([]Candidate, 0, len(payload.Docs))
	for _, doc := range payload.Docs {
		c := openLibraryCandidate(q, doc)
		if c.Title == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func openLibraryCandidate(q Query, doc openLibraryDoc) Candidate {
	var ids []bookmeta.Identifier
	if isbn := firstValidISBN(doc.ISBN); isbn != "" {
		ids = append(ids, bookmeta.Identifier{Type: "isbn", Value: isbn})
	}

	date := ""
	if doc.FirstPublishYear > 0 {
		date = strconv.Itoa(doc.FirstPublishYear)
	} else if len(doc.PublishYear) > 0 {
		date = strconv.Itoa(doc.PublishYear[0])
	}

	c := Candidate{
		Provider:   "openlibrary",
		ProviderID: doc.Key,
		Title:      doc.Title,
		Authors:    authorMetas(doc.AuthorName),
		Publisher:  firstNonEmpty(doc.Publisher...),
		Date:       bookmeta.NormalizeMetadataDate(date),
		Language:   firstNonEmpty(doc.Language...),
		Identifier: identifiers(ids),
		Tags:       trimStrings(doc.Subject, 8),
	}
	if doc.CoverID > 0 {
		c.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg?default=false", doc.CoverID)
	}
	c.Score = score(q, c)
	return c
}

// olWorkKeyPattern matches an Open Library work key ("/works/OL82563W") — the
// shape of an openlibrary candidate's ProviderID. Validating before building the
// URL pins the lazy description fetch to a work record on the OL host.
var olWorkKeyPattern = regexp.MustCompile(`^/works/OL[0-9]+W$`)

// FetchDescription returns the work-level description for an Open Library work
// key (the candidate's ProviderID, e.g. "/works/OL82563W"). Search results omit
// descriptions, so this is the lazy second call made only for a candidate under
// review. OL encodes description as either a plain string or a {type, value}
// object; both are handled.
func (p *OpenLibraryProvider) FetchDescription(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if !olWorkKeyPattern.MatchString(ref) {
		return "", fmt.Errorf("openlibrary: invalid work key %q", ref)
	}

	req, err := newProviderGET(ctx, p.baseURL+ref+".json")
	if err != nil {
		return "", err
	}

	res, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openlibrary work request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("openlibrary work: %s", res.Status)
	}

	var payload openLibraryWork
	if err := decodeProviderJSON(res, &payload); err != nil {
		return "", fmt.Errorf("openlibrary work response: %w", err)
	}
	return parseOpenLibraryDescription(payload.Description), nil
}

type openLibraryWork struct {
	Description jsontext.Value `json:"description"`
}

// parseOpenLibraryDescription reads OL's polymorphic description field, which is
// either a plain string or a {type, value} object.
func parseOpenLibraryDescription(raw jsontext.Value) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.Value)
	}
	return ""
}

func sortCandidates(candidates []Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
}
