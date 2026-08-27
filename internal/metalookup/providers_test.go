package metalookup

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCandidateIdentifiers(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		provider   string
		providerID string
		want       string
	}{
		{
			name:       "adds provider provenance",
			identifier: "isbn:9780441478125",
			provider:   "openlibrary",
			providerID: "/works/OL82563W",
			want:       "isbn:9780441478125, openlibrary:/works/OL82563W",
		},
		{
			name:       "deduplicates an identifier already supplied by the provider",
			identifier: "isbn:9780441478125, google:abc123",
			provider:   "google",
			providerID: "abc123",
			want:       "isbn:9780441478125, google:abc123",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := Candidate{
				Provider:   test.provider,
				ProviderID: test.providerID,
				Identifier: test.identifier,
			}
			if got := CandidateIdentifiers(candidate); got != test.want {
				t.Fatalf("CandidateIdentifiers() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProviderRequestPolicy(t *testing.T) {
	var gotUserAgent, gotAccept, gotPath, gotTitle string
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		gotPath = r.URL.Path
		gotTitle = r.URL.Query().Get("title")
		w.Write([]byte(`{"docs":[{"key":"/works/OL1W","title":"Dune"}]}`))
	}))
	p := &OpenLibraryProvider{client: srv.Client(), baseURL: srv.URL}
	if _, err := p.Search(context.Background(), Query{Title: "Dune"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotUserAgent != userAgent {
		t.Fatalf("User-Agent = %q; want %q", gotUserAgent, userAgent)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q; want application/json", gotAccept)
	}
	if gotPath != "/search.json" || gotTitle != "Dune" {
		t.Fatalf("request path/title = %q/%q; want /search.json/Dune", gotPath, gotTitle)
	}
}

func TestProviderResponseBodyLimit(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", maxProviderResponseBytes+1)))
	}))
	p := &OpenLibraryProvider{client: srv.Client(), baseURL: srv.URL}
	_, err := p.Search(context.Background(), Query{Title: "Dune"})
	if err == nil || !strings.Contains(err.Error(), "metadata provider response exceeds") {
		t.Fatalf("Search err = %v; want body limit error", err)
	}
}

func TestProviderMalformedResponsesAreControlledErrors(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"docs":`))
	}))
	ol := &OpenLibraryProvider{client: srv.Client(), baseURL: srv.URL}
	if _, err := ol.Search(context.Background(), Query{Title: "Dune"}); err == nil || !strings.Contains(err.Error(), "openlibrary search response") {
		t.Fatalf("OpenLibrary Search err = %v; want contextual decode error", err)
	}

	google := &GoogleBooksProvider{client: srv.Client(), baseURL: srv.URL}
	if _, err := google.Search(context.Background(), Query{Title: "Dune"}); err == nil || !strings.Contains(err.Error(), "google books search response") {
		t.Fatalf("Google Search err = %v; want contextual decode error", err)
	}
}

func TestProviderHTTPStatusErrorsAreControlled(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusBadGateway)
	}))
	ol := &OpenLibraryProvider{client: srv.Client(), baseURL: srv.URL}
	if _, err := ol.Search(context.Background(), Query{Title: "Dune"}); err == nil || !strings.Contains(err.Error(), "openlibrary search: 502 Bad Gateway") {
		t.Fatalf("OpenLibrary Search err = %v; want status error", err)
	}

	google := &GoogleBooksProvider{client: srv.Client(), baseURL: srv.URL}
	if _, err := google.Search(context.Background(), Query{Title: "Dune"}); err == nil || !strings.Contains(err.Error(), "google books search: 502 Bad Gateway") {
		t.Fatalf("Google Search err = %v; want status error", err)
	}
}

func TestOpenLibraryCandidates(t *testing.T) {
	const payload = `{
		"docs": [{
			"key": "/works/OL82563W",
			"title": "The Left Hand of Darkness",
			"author_name": ["Ursula K. Le Guin"],
			"first_publish_year": 1969,
			"isbn": ["0441007317", "9780441478125"],
			"language": ["eng"],
			"publisher": ["Ace Books"],
			"subject": ["Science fiction", "Gender", "Hainish Cycle"],
			"cover_i": 8231856
		}]
	}`
	var res openLibrarySearchResponse
	if err := json.Unmarshal([]byte(payload), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := openLibraryCandidates(Query{ISBN: "0441007317", Title: "Left Hand", Author: "Le Guin"}, res)
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	c := got[0]
	if c.Provider != "openlibrary" || c.ProviderID != "/works/OL82563W" {
		t.Fatalf("provider ids = %q/%q", c.Provider, c.ProviderID)
	}
	if c.Title != "The Left Hand of Darkness" || len(c.Authors) != 1 || c.Authors[0].SortName != "Guin, Ursula K. Le" {
		t.Fatalf("metadata title/authors = %+v", c.Metadata)
	}
	if c.Date != "1969" || c.Publisher != "Ace Books" || c.Language != "eng" {
		t.Fatalf("metadata facts = %+v", c.Metadata)
	}
	if c.Identifier != "isbn:0441007317" {
		t.Fatalf("identifier = %q, want first valid ISBN", c.Identifier)
	}
	if c.CoverURL != "https://covers.openlibrary.org/b/id/8231856-L.jpg?default=false" {
		t.Fatalf("cover url = %q", c.CoverURL)
	}
	if c.Score <= 0 {
		t.Fatalf("score = %v, want positive", c.Score)
	}
}

func TestParseOpenLibraryDescription(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"string", `"A lone diplomat."`, "A lone diplomat."},
		{"object", `{"type":"/type/text","value":"  A lone diplomat.  "}`, "A lone diplomat."},
		{"absent", ``, ""},
		{"null", `null`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw jsontext.Value
			if tc.raw != "" {
				raw = jsontext.Value(tc.raw)
			}
			if got := parseOpenLibraryDescription(raw); got != tc.want {
				t.Fatalf("parse(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestOpenLibraryFetchDescription(t *testing.T) {
	var gotPath string
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"description":{"type":"/type/text","value":"A lone diplomat."}}`))
	}))
	p := &OpenLibraryProvider{client: srv.Client(), baseURL: srv.URL}
	desc, err := p.FetchDescription(context.Background(), "/works/OL82563W")
	if err != nil {
		t.Fatalf("FetchDescription: %v", err)
	}
	if desc != "A lone diplomat." {
		t.Fatalf("description = %q", desc)
	}
	if gotPath != "/works/OL82563W.json" {
		t.Fatalf("requested path = %q, want /works/OL82563W.json", gotPath)
	}

	// A non-work ref is rejected before any request is made.
	if _, err := p.FetchDescription(context.Background(), "/books/OL1M"); err == nil {
		t.Fatalf("expected error for non-work ref")
	}
}

func TestGoogleBooksCandidates(t *testing.T) {
	const payload = `{
		"items": [{
			"id": "abc123",
			"volumeInfo": {
				"title": "The Left Hand of Darkness",
				"subtitle": "A Novel",
				"authors": ["Ursula K. Le Guin"],
				"publisher": "Ace",
				"publishedDate": "2016-10-25",
				"description": "Winner of major awards.",
				"language": "en",
				"categories": ["Fiction", "Science Fiction"],
				"industryIdentifiers": [
					{"type": "ISBN_13", "identifier": "9780441478125"},
					{"type": "OTHER", "identifier": "ignored"}
				],
				"imageLinks": {"thumbnail": "http://books.google.com/cover.jpg"}
			}
		}]
	}`
	var res googleBooksResponse
	if err := json.Unmarshal([]byte(payload), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := googleBooksCandidates(Query{ISBN: "9780441478125", Title: "Left Hand", Author: "Le Guin"}, res)
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	c := got[0]
	if c.Provider != "google" || c.ProviderID != "abc123" {
		t.Fatalf("provider ids = %q/%q", c.Provider, c.ProviderID)
	}
	if c.Title != "The Left Hand of Darkness: A Novel" {
		t.Fatalf("title = %q", c.Title)
	}
	if c.Date != "2016-10-25" || c.Description == "" || c.Language != "en" {
		t.Fatalf("metadata = %+v", c.Metadata)
	}
	if c.Identifier != "isbn:9780441478125, google:abc123" {
		t.Fatalf("identifier = %q", c.Identifier)
	}
	if c.CoverURL != "https://books.google.com/cover.jpg" {
		t.Fatalf("cover url = %q", c.CoverURL)
	}
}
