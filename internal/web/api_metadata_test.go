package web

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/metalookup"
)

type fakeMetadataProvider struct {
	query metalookup.Query
	err   error
}

func (p *fakeMetadataProvider) ID() string { return "fake" }

func (p *fakeMetadataProvider) Name() string { return "Fake Metadata" }

func (p *fakeMetadataProvider) Search(_ context.Context, q metalookup.Query) ([]metalookup.Candidate, error) {
	p.query = q
	if p.err != nil {
		return nil, p.err
	}
	return []metalookup.Candidate{{
		Provider:    "fake",
		ProviderID:  "fake-1",
		Score:       42,
		CoverURL:    "https://example.test/cover.jpg",
		Title:       "Fetched Hobbit",
		Authors:     []bookmeta.AuthorMeta{{Name: "J. R. R. Tolkien", SortName: "Tolkien, J. R. R."}},
		Publisher:   "Fetched Press",
		Date:        "1937",
		Identifier:  "isbn:9780000000002",
		Description: "Fetched description.",
		Tags:        []string{"Fantasy", "Classic"},
	}}, nil
}

func TestAPIMetadataCandidates(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	u := mustUser(t, database, "Alice", db.RoleMember)

	provider := &fakeMetadataProvider{}
	s := &Server{
		db:       database,
		dataDir:  dir,
		sessions: newSessionStore(database),
		metadata: metalookup.Registry{"fake": provider},
	}

	req := httptest.NewRequest("GET", "/api/books/w_1/metadata-candidates?provider=fake", nil)
	addSessionCookie(t, s, req, u.ID)
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if provider.query.Title != "The Hobbit" || provider.query.Author != "J.R.R. Tolkien" {
		t.Fatalf("provider query = %+v", provider.query)
	}

	var got []MetadataCandidateDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	c := got[0]
	if c.Provider != "fake" || c.ProviderName != "Fake Metadata" || c.Title != "Fetched Hobbit" {
		t.Fatalf("candidate = %+v", c)
	}
	if c.Authors != "J. R. R. Tolkien" || c.Tags != "Fantasy, Classic" || c.Identifiers != "isbn:9780000000002, fake:fake-1" {
		t.Fatalf("candidate fields = %+v", c)
	}
}

func TestAPIMetadataProviderFailuresAreIsolated(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	u := mustUser(t, database, "Alice", db.RoleMember)

	s := &Server{
		db:       database,
		dataDir:  dir,
		sessions: newSessionStore(database),
		metadata: metalookup.Registry{
			"bad":  &fakeMetadataProvider{err: errors.New("provider down")},
			"fake": &fakeMetadataProvider{},
		},
	}
	handler := testRoutes(t, s)

	req := httptest.NewRequest("GET", "/api/books/w_1/metadata-candidates?provider=bad", nil)
	addSessionCookie(t, s, req, u.ID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("bad provider status = %d, want 502", w.Code)
	}

	req = httptest.NewRequest("GET", "/api/books/w_1/metadata-candidates?provider=fake", nil)
	addSessionCookie(t, s, req, u.ID)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("good provider status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// fakeDescProvider implements the optional DescriptionFetcher capability.
type fakeDescProvider struct {
	ref  string
	desc string
}

func (p *fakeDescProvider) ID() string   { return "fakedesc" }
func (p *fakeDescProvider) Name() string { return "Fake Desc" }
func (p *fakeDescProvider) Search(context.Context, metalookup.Query) ([]metalookup.Candidate, error) {
	return nil, nil
}

func (p *fakeDescProvider) FetchDescription(_ context.Context, ref string) (string, error) {
	p.ref = ref
	return p.desc, nil
}

func TestAPIMetadataDescription(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	u := mustUser(t, database, "Alice", db.RoleMember)

	desc := &fakeDescProvider{desc: "Lazy fetched description."}
	s := &Server{
		db:       database,
		dataDir:  dir,
		sessions: newSessionStore(database),
		// "plain" has no DescriptionFetcher capability.
		metadata: metalookup.Registry{"fakedesc": desc, "plain": &fakeMetadataProvider{}},
	}
	handler := testRoutes(t, s)
	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/metadata/description"+query, nil)
		addSessionCookie(t, s, req, u.ID)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	decodeDesc := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", w.Code, w.Body)
		}
		var body struct {
			Description string `json:"description"`
		}
		if err := json.UnmarshalRead(w.Body, &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Description
	}

	// A capable provider returns the lazily-fetched description for the ref.
	if got := decodeDesc(get("?provider=fakedesc&ref=/works/OL1W")); got != "Lazy fetched description." {
		t.Fatalf("description = %q", got)
	}
	if desc.ref != "/works/OL1W" {
		t.Fatalf("ref passed to provider = %q", desc.ref)
	}

	// A provider without the capability yields an empty description, not an error.
	if got := decodeDesc(get("?provider=plain&ref=/works/OL1W")); got != "" {
		t.Fatalf("plain provider description = %q, want empty", got)
	}

	// Missing ref → 400; unknown provider → 400.
	if w := get("?provider=fakedesc"); w.Code != http.StatusBadRequest {
		t.Fatalf("missing ref status = %d, want 400", w.Code)
	}
	if w := get("?provider=nope&ref=/works/OL1W"); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown provider status = %d, want 400", w.Code)
	}
}

func TestAPIMetadataCandidatesRejectsUnknownProvider(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()
	u := mustUser(t, database, "Alice", db.RoleMember)

	s := &Server{
		db:       database,
		dataDir:  dir,
		sessions: newSessionStore(database),
		metadata: metalookup.Registry{},
	}

	req := httptest.NewRequest("GET", "/api/books/w_1/metadata-candidates?provider=missing", nil)
	addSessionCookie(t, s, req, u.ID)
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
