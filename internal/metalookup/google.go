package metalookup

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

type GoogleBooksProvider struct {
	client  *http.Client
	baseURL string
}

func NewGoogleBooksProvider(client *http.Client) *GoogleBooksProvider {
	return &GoogleBooksProvider{
		client:  httpClient(client),
		baseURL: "https://www.googleapis.com/books/v1",
	}
}

func (p *GoogleBooksProvider) ID() string { return "google" }

func (p *GoogleBooksProvider) Name() string { return "Google Books" }

func (p *GoogleBooksProvider) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if err := validateQuery(q); err != nil {
		return nil, err
	}

	u, err := url.Parse(p.baseURL + "/volumes")
	if err != nil {
		return nil, err
	}
	params := u.Query()
	params.Set("maxResults", "8")
	params.Set("printType", "books")
	params.Set("projection", "full")
	if isbn := cleanISBN(q.ISBN); isbn != "" {
		params.Set("q", "isbn:"+isbn)
	} else {
		terms := []string{"intitle:" + q.Title}
		if q.Author != "" {
			terms = append(terms, "inauthor:"+q.Author)
		}
		params.Set("q", strings.Join(terms, " "))
	}
	u.RawQuery = params.Encode()

	req, err := newProviderGET(ctx, u.String())
	if err != nil {
		return nil, err
	}

	res, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google books search request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("google books search: %s", res.Status)
	}

	var payload googleBooksResponse
	if err := decodeProviderJSON(res, &payload); err != nil {
		return nil, fmt.Errorf("google books search response: %w", err)
	}
	candidates := googleBooksCandidates(q, payload)
	sortCandidates(candidates)
	return candidates, nil
}

type googleBooksResponse struct {
	Items []googleBookItem `json:"items"`
}

type googleBookItem struct {
	ID         string           `json:"id"`
	VolumeInfo googleVolumeInfo `json:"volumeInfo"`
}

type googleVolumeInfo struct {
	Title               string                     `json:"title"`
	Subtitle            string                     `json:"subtitle"`
	Authors             []string                   `json:"authors"`
	Publisher           string                     `json:"publisher"`
	PublishedDate       string                     `json:"publishedDate"`
	Description         string                     `json:"description"`
	Language            string                     `json:"language"`
	Categories          []string                   `json:"categories"`
	IndustryIdentifiers []googleIndustryIdentifier `json:"industryIdentifiers"`
	ImageLinks          googleImageLinks           `json:"imageLinks"`
}

type googleIndustryIdentifier struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

type googleImageLinks struct {
	SmallThumbnail string `json:"smallThumbnail"`
	Thumbnail      string `json:"thumbnail"`
	Small          string `json:"small"`
	Medium         string `json:"medium"`
	Large          string `json:"large"`
	ExtraLarge     string `json:"extraLarge"`
}

func googleBooksCandidates(q Query, payload googleBooksResponse) []Candidate {
	out := make([]Candidate, 0, len(payload.Items))
	for _, item := range payload.Items {
		c := googleBooksCandidate(q, item)
		if c.Title == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func googleBooksCandidate(q Query, item googleBookItem) Candidate {
	info := item.VolumeInfo
	var ids []bookmeta.Identifier
	for _, raw := range info.IndustryIdentifiers {
		switch strings.ToUpper(strings.TrimSpace(raw.Type)) {
		case "ISBN_10", "ISBN_13":
			if isbn := cleanISBN(raw.Identifier); bookmeta.ValidISBN(isbn) {
				ids = append(ids, bookmeta.Identifier{Type: "isbn", Value: isbn})
			}
		}
	}
	if item.ID != "" {
		ids = append(ids, bookmeta.Identifier{Type: "google", Value: item.ID})
	}

	title := strings.TrimSpace(info.Title)
	if subtitle := strings.TrimSpace(info.Subtitle); subtitle != "" && !strings.Contains(title, subtitle) {
		title += ": " + subtitle
	}

	c := Candidate{
		Provider:    "google",
		ProviderID:  item.ID,
		Title:       title,
		Authors:     authorMetas(info.Authors),
		Publisher:   info.Publisher,
		Date:        bookmeta.NormalizeMetadataDate(info.PublishedDate),
		Description: info.Description,
		Language:    info.Language,
		Identifier:  identifiers(ids),
		Tags:        trimStrings(info.Categories, 8),
		CoverURL: httpsImageURL(firstNonEmpty(
			info.ImageLinks.ExtraLarge,
			info.ImageLinks.Large,
			info.ImageLinks.Medium,
			info.ImageLinks.Small,
			info.ImageLinks.Thumbnail,
			info.ImageLinks.SmallThumbnail,
		)),
	}
	c.Score = score(q, c)
	return c
}

func httpsImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if after, ok := strings.CutPrefix(raw, "http://"); ok {
		return "https://" + after
	}
	return raw
}
