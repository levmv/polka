package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/metalookup"
)

type MetadataCandidateDTO struct {
	Provider     string   `json:"provider"`
	ProviderName string   `json:"provider_name"`
	ProviderID   string   `json:"provider_id"`
	CoverURL     string   `json:"cover_url,omitempty"`
	Title        string   `json:"title,omitempty"`
	Authors      string   `json:"authors,omitempty"`
	Series       string   `json:"series,omitempty"`
	SeriesIndex  *float64 `json:"series_index,omitzero"`
	Description  string   `json:"description,omitempty"`
	Tags         string   `json:"tags,omitempty"`
	Language     string   `json:"language,omitempty"`
	Publisher    string   `json:"publisher,omitempty"`
	Date         string   `json:"date,omitempty"`
	Identifiers  string   `json:"identifiers,omitempty"`
}

func (s *Server) handleAPIMetadataCandidates(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}

	query, err := s.metadataQueryForWork(workID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Book not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}

	providerID := r.URL.Query().Get("provider")
	provider, ok := s.metadataRegistry().Get(providerID)
	if !ok {
		http.Error(w, "Unknown metadata provider", http.StatusBadRequest)
		return
	}

	candidates, err := provider.Search(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	out := make([]MetadataCandidateDTO, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, metadataCandidateDTO(c, provider.Name()))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAPIMetadataDescription lazily fetches one candidate's long description
// from a provider that supports it (Open Library search omits descriptions).
// provider + ref identify the candidate; nothing is mutated. A provider without
// the capability (Google already carries descriptions) yields an empty
// description rather than an error, so the caller can treat all providers alike.
func (s *Server) handleAPIMetadataDescription(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		http.Error(w, "ref is required", http.StatusBadRequest)
		return
	}
	provider, ok := s.metadataRegistry().Get(r.URL.Query().Get("provider"))
	if !ok {
		http.Error(w, "Unknown metadata provider", http.StatusBadRequest)
		return
	}

	description := ""
	if fetcher, ok := provider.(metalookup.DescriptionFetcher); ok {
		desc, err := fetcher.FetchDescription(r.Context(), ref)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		description = desc
	}

	writeJSON(w, http.StatusOK, map[string]string{"description": description})
}

func (s *Server) metadataRegistry() metalookup.Registry {
	if s.metadata != nil {
		return s.metadata
	}
	return metalookup.NewRegistry(nil)
}

func (s *Server) metadataQueryForWork(workID string) (metalookup.Query, error) {
	b, err := db.GetBook(s.db, db.FullVisibilityScope(), workID)
	if err != nil {
		return metalookup.Query{}, err
	}

	query := metalookup.Query{
		ISBN:  firstISBN(b.Identifiers.String),
		Title: b.Title,
	}

	authorsByWork, err := db.AuthorsByWorkIDs(s.db, []string{workID})
	if err != nil {
		return metalookup.Query{}, err
	}
	if authors := authorsByWork[workID]; len(authors) > 0 {
		query.Author = authors[0].Name
	}
	return query, nil
}

func firstISBN(identifiers string) string {
	for _, id := range bookmeta.ParseIdentifiers(identifiers) {
		if id.Type == "isbn" && bookmeta.ValidISBN(id.Value) {
			return id.Value
		}
	}
	return ""
}

func metadataCandidateDTO(c metalookup.Candidate, providerName string) MetadataCandidateDTO {
	authors := make([]string, 0, len(c.Authors))
	for _, a := range c.Authors {
		if a.Name != "" {
			authors = append(authors, a.Name)
		}
	}

	var seriesIndex *float64
	if c.SeriesIndex != 0 {
		seriesIndex = &c.SeriesIndex
	}

	return MetadataCandidateDTO{
		Provider:     c.Provider,
		ProviderName: providerName,
		ProviderID:   c.ProviderID,
		CoverURL:     c.CoverURL,
		Title:        c.Title,
		Authors:      strings.Join(authors, ", "),
		Series:       c.Series,
		SeriesIndex:  seriesIndex,
		Description:  c.Description,
		Tags:         strings.Join(c.Tags, ", "),
		Language:     c.Language,
		Publisher:    c.Publisher,
		Date:         c.Date,
		Identifiers:  metalookup.CandidateIdentifiers(c),
	}
}
