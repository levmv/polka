package web

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/opds"
)

func TestOPDSRequiresAuth(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	s := newTestServer(database, dir)

	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, httptest.NewRequest("GET", "/opds", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != `Basic realm="polka"` {
		t.Fatalf("WWW-Authenticate = %q, want Basic realm", got)
	}
}

func TestOPDSRootFeed(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	s := newTestServer(database, dir)

	req := httptest.NewRequest("GET", "/opds", nil)
	req.SetBasicAuth("alice", "pw")
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != opds.NavigationFeedType+"; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want OPDS navigation", got)
	}
	assertValidXML(t, w.Body.Bytes())

	body := w.Body.String()
	for _, want := range []string{
		`<id>urn:polka:opds:root</id>`,
		`<title>All books</title>`,
		`<title>Recently added</title>`,
		`<title>By shelf</title>`,
		`<title>By series</title>`,
		`<title>By tag</title>`,
		`href="http://example.com/opds/books"`,
		`href="http://example.com/opds/recent"`,
		`href="http://example.com/opds/shelves"`,
		`href="http://example.com/opds/series"`,
		`href="http://example.com/opds/tags"`,
		`type="application/atom+xml;profile=opds-catalog;kind=acquisition"`,
		`type="application/atom+xml;profile=opds-catalog;kind=navigation"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("root feed missing %q:\n%s", want, body)
		}
	}
}

func TestOPDSShelves(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice", db.RoleMember)
	bob := mustUser(t, database, "bob", db.RoleMember)
	shared, err := database.CreateShelf(alice.ID, db.ShelfShared, "Shared", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create shared shelf: %v", err)
	}
	personal, err := database.CreateShelf(alice.ID, db.ShelfPersonal, "Mine & Yours", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create personal shelf: %v", err)
	}
	query, err := database.CreateShelf(alice.ID, db.ShelfPersonal, "Tolkien", db.ShelfQuery, "Hobbit")
	if err != nil {
		t.Fatalf("create query shelf: %v", err)
	}
	hidden, err := database.CreateShelf(bob.ID, db.ShelfPersonal, "Bob only", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create hidden shelf: %v", err)
	}
	for _, shelfID := range []string{shared.ID, personal.ID} {
		if err := database.AddBookToShelf(shelfID, alice.ID, "w_1"); err != nil {
			t.Fatalf("add Hobbit to %s: %v", shelfID, err)
		}
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth("alice", "pw")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	w := get("/opds/shelves")
	if w.Code != http.StatusOK {
		t.Fatalf("shelf navigation status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != opds.NavigationFeedType+"; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want OPDS navigation", got)
	}
	assertValidXML(t, w.Body.Bytes())
	body := w.Body.String()
	for _, want := range []string{
		`<title>Shared</title>`,
		`<title>Mine &amp; Yours</title>`,
		`<title>Tolkien</title>`,
		`<summary type="text">Shelf</summary>`,
		`<summary type="text">Saved search</summary>`,
		`href="http://example.com/opds/shelves/` + shared.ID + `"`,
		`href="http://example.com/opds/shelves/` + personal.ID + `"`,
		`href="http://example.com/opds/shelves/` + query.ID + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("shelf navigation missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Bob only") || strings.Contains(body, hidden.ID) {
		t.Fatalf("shelf navigation exposed another user's personal shelf:\n%s", body)
	}

	for _, shelf := range []*db.Shelf{shared, personal, query} {
		w = get("/opds/shelves/" + shelf.ID)
		if w.Code != http.StatusOK {
			t.Fatalf("shelf %s status = %d, want 200; body: %s", shelf.Name, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Content-Type"); got != opds.AcquisitionFeedType+"; charset=utf-8" {
			t.Fatalf("shelf %s Content-Type = %q, want OPDS acquisition", shelf.Name, got)
		}
		assertValidXML(t, w.Body.Bytes())
		if !strings.Contains(w.Body.String(), `<title>The Hobbit</title>`) {
			t.Fatalf("shelf %s missing Hobbit:\n%s", shelf.Name, w.Body.String())
		}
	}

	w = get("/opds/shelves/" + hidden.ID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("hidden shelf status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestOPDSRecentFeed(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	s := newTestServer(database, dir)

	req := httptest.NewRequest("GET", "/opds/recent", nil)
	req.SetBasicAuth("alice", "pw")
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != opds.AcquisitionFeedType+"; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want OPDS acquisition", got)
	}
	assertValidXML(t, w.Body.Bytes())
	body := w.Body.String()
	if !strings.Contains(body, `<title>The Hobbit</title>`) {
		t.Fatalf("recent feed missing The Hobbit:\n%s", body)
	}
}

func TestOPDSSeriesNav(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	if _, err := database.Exec(`UPDATE works SET series = 'Middle-earth' WHERE id = 'w_1'`); err != nil {
		t.Fatalf("set series: %v", err)
	}
	s := newTestServer(database, dir)

	req := httptest.NewRequest("GET", "/opds/series", nil)
	req.SetBasicAuth("alice", "pw")
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != opds.NavigationFeedType+"; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want OPDS navigation", got)
	}
	assertValidXML(t, w.Body.Bytes())
	body := w.Body.String()
	for _, want := range []string{
		`<title>Middle-earth</title>`,
		`q=series%3A%22Middle-earth%22`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("series nav missing %q:\n%s", want, body)
		}
	}
}

// The series facet is bounded like the acquisition feeds: one page plus a
// cursor, so a library with thousands of series never builds one giant XML.
func TestOPDSSeriesNavPaging(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	for i := range opdsDefaultLimit + 5 {
		id := "sw_" + strconv.Itoa(i)
		name := "Series " + fmt.Sprintf("%03d", i)
		if _, err := database.Exec(
			`INSERT INTO works (id, title, sort_title, series) VALUES (?, ?, ?, ?)`,
			id, name, name, name,
		); err != nil {
			t.Fatalf("insert work: %v", err)
		}
	}
	s := newTestServer(database, dir)

	fetch := func(target string) string {
		req := httptest.NewRequest("GET", target, nil)
		req.SetBasicAuth("alice", "pw")
		w := httptest.NewRecorder()
		testRoutes(t, s).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body: %s", target, w.Code, w.Body.String())
		}
		assertValidXML(t, w.Body.Bytes())
		return w.Body.String()
	}

	first := fetch("/opds/series")
	if got := strings.Count(first, "<entry>"); got != opdsDefaultLimit {
		t.Fatalf("first page entries = %d, want %d", got, opdsDefaultLimit)
	}
	if !strings.Contains(first, `rel="next"`) {
		t.Fatalf("first page has no next link:\n%s", first)
	}
	if strings.Contains(first, "<title>Series 050</title>") {
		t.Fatal("first page leaked an entry past the page size")
	}

	second := fetch("/opds/series?after=" + url.QueryEscape("Series 049"))
	if !strings.Contains(second, "<title>Series 050</title>") {
		t.Fatalf("cursor page did not continue after the first:\n%s", second)
	}
	if strings.Contains(second, `rel="next"`) {
		t.Fatalf("last page still advertises a next link:\n%s", second)
	}
}

func TestOPDSTagsNav(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	if _, err := database.Exec(`UPDATE works SET tags = 'fantasy, classics' WHERE id = 'w_1'`); err != nil {
		t.Fatalf("set tags: %v", err)
	}
	s := newTestServer(database, dir)

	req := httptest.NewRequest("GET", "/opds/tags", nil)
	req.SetBasicAuth("alice", "pw")
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	assertValidXML(t, w.Body.Bytes())
	body := w.Body.String()
	for _, want := range []string{
		`<title>fantasy</title>`,
		`<title>classics</title>`,
		`q=tag%3A%22fantasy%22`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("tags nav missing %q:\n%s", want, body)
		}
	}
}

func TestOPDSBooksFeed(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	if _, err := database.Exec(`
		UPDATE works
		SET description = '<p>A small <strong>adventure</strong>.</p>',
		    tags = 'fantasy, classics',
		    cover_version = 2,
		    publisher = 'Allen & Unwin',
		    published_date = '1937-09-21',
		    language = 'en',
		    identifiers = 'isbn:978-0-306-40615-7, uuid:550e8400-e29b-41d4-a716-446655440000',
		    updated_at = 100
		WHERE id = 'w_1'
	`); err != nil {
		t.Fatalf("update work: %v", err)
	}
	s := newTestServer(database, dir)

	req := httptest.NewRequest("GET", "/opds/books", nil)
	req.SetBasicAuth("alice", "pw")
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != opds.AcquisitionFeedType+"; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want OPDS acquisition", got)
	}
	assertValidXML(t, w.Body.Bytes())

	body := w.Body.String()
	for _, want := range []string{
		`<title>The Hobbit</title>`,
		`<name>J.R.R. Tolkien</name>`,
		`A small adventure.`,
		`term="fantasy"`,
		`term="classics"`,
		`xmlns:dc="http://purl.org/dc/elements/1.1/"`,
		`xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/"`,
		`<opensearch:totalResults>1</opensearch:totalResults>`,
		`<opensearch:itemsPerPage>50</opensearch:itemsPerPage>`,
		`<opensearch:startIndex>1</opensearch:startIndex>`,
		`<dc:publisher>Allen &amp; Unwin</dc:publisher>`,
		`<dc:issued>1937-09-21</dc:issued>`,
		`<dc:language>en</dc:language>`,
		`<dc:identifier>isbn:978-0-306-40615-7</dc:identifier>`,
		`rel="http://opds-spec.org/acquisition"`,
		`href="http://example.com/download/asset_1"`,
		`type="application/epub+zip"`,
		`rel="http://opds-spec.org/image/thumbnail"`,
		`href="http://example.com/covers/w_1?v=2&amp;variant=thumb"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("books feed missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `<title>Dune</title>`) {
		t.Fatalf("book without assets appeared in OPDS acquisition feed:\n%s", body)
	}
	if strings.Contains(body, `550e8400`) {
		t.Fatalf("internal UUID identifier leaked into OPDS feed:\n%s", body)
	}
}

func TestOPDSDeliveryAcceptsAppToken(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "alice", db.RoleMember)
	token, err := database.CreateAppToken(u.ID, "koreader")
	if err != nil {
		t.Fatalf("create app token: %v", err)
	}
	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	for _, tc := range []struct {
		name        string
		path        string
		contentType string
		body        string
	}{
		{
			name:        "root",
			path:        "/opds",
			contentType: opds.NavigationFeedType,
			body:        "<title>All books</title>",
		},
		{
			name:        "search",
			path:        "/opds/search?q=Hobbit",
			contentType: opds.AcquisitionFeedType,
			body:        "<title>The Hobbit</title>",
		},
		{
			name:        "opensearch",
			path:        "/opds/osd",
			contentType: opds.OpenSearchType,
			body:        `template="http://example.com/opds/search?q={searchTerms}"`,
		},
		{
			name:        "download",
			path:        "/download/asset_1",
			contentType: "application/epub+zip",
			body:        "epub content",
		},
		{
			name:        "cover",
			path:        "/covers/w_1?variant=thumb",
			contentType: covers.ContentTypeJPEG,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			req.SetBasicAuth("device-name-is-ignored", token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
			}
			if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.contentType) {
				t.Fatalf("Content-Type = %q, want prefix %q", got, tc.contentType)
			}
			if tc.body != "" && !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("body missing %q:\n%s", tc.body, w.Body.String())
			}
		})
	}
}

func TestOPDSPaginationBoundaries(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES
			('w_3', 'Boundary One', 'Boundary One'),
			('w_4', 'Boundary Two', 'Boundary Two'),
			('w_5', 'Boundary Three', 'Boundary Three');
		INSERT INTO assets (id, work_id, storage_path, filename, extension) VALUES
			('asset_3', 'w_3', 'one.epub', 'one.epub', '.epub'),
			('asset_4', 'w_4', 'two.epub', 'two.epub', '.epub'),
			('asset_5', 'w_5', 'three.epub', 'three.epub', '.epub');
		INSERT INTO search (rowid, work_id, title, authors) VALUES
			(3, 'w_3', 'Boundary One', ''),
			(4, 'w_4', 'Boundary Two', ''),
			(5, 'w_5', 'Boundary Three', '');
	`); err != nil {
		t.Fatalf("insert paginated works: %v", err)
	}
	s := newTestServer(database, dir)

	type paginationCase struct {
		path       string
		total      int
		perPage    int
		startIndex int
		entries    int
		links      map[string]string
		noLinks    []string
	}
	cases := map[string]paginationCase{
		"zero results": {
			path:       "/opds/search?q=Missing&limit=2",
			total:      0,
			perPage:    2,
			startIndex: 1,
			links: map[string]string{
				"first": "http://example.com/opds/search?limit=2&offset=0&q=Missing",
				"last":  "http://example.com/opds/search?limit=2&offset=0&q=Missing",
			},
			noLinks: []string{"next", "previous"},
		},
		"one result": {
			path:       "/opds/search?q=Hobbit&limit=2",
			total:      1,
			perPage:    2,
			startIndex: 1,
			entries:    1,
			links: map[string]string{
				"first": "http://example.com/opds/search?limit=2&offset=0&q=Hobbit",
				"last":  "http://example.com/opds/search?limit=2&offset=0&q=Hobbit",
			},
			noLinks: []string{"next", "previous"},
		},
		"exact page": {
			path:       "/opds/books?limit=4",
			total:      4,
			perPage:    4,
			startIndex: 1,
			entries:    4,
			links: map[string]string{
				"first": "http://example.com/opds/books?limit=4&offset=0",
				"last":  "http://example.com/opds/books?limit=4&offset=0",
			},
			noLinks: []string{"next", "previous"},
		},
		"exact multiple first page": {
			path:       "/opds/books?limit=2",
			total:      4,
			perPage:    2,
			startIndex: 1,
			entries:    2,
			links: map[string]string{
				"first": "http://example.com/opds/books?limit=2&offset=0",
				"last":  "http://example.com/opds/books?limit=2&offset=2",
				"next":  "http://example.com/opds/books?limit=2&offset=2",
			},
			noLinks: []string{"previous"},
		},
		"exact multiple last page": {
			path:       "/opds/books?limit=2&offset=2",
			total:      4,
			perPage:    2,
			startIndex: 3,
			entries:    2,
			links: map[string]string{
				"first":    "http://example.com/opds/books?limit=2&offset=0",
				"last":     "http://example.com/opds/books?limit=2&offset=2",
				"previous": "http://example.com/opds/books?limit=2&offset=0",
			},
			noLinks: []string{"next"},
		},
		"final partial page": {
			path:       "/opds/books?limit=3&offset=3",
			total:      4,
			perPage:    3,
			startIndex: 4,
			entries:    1,
			links: map[string]string{
				"first":    "http://example.com/opds/books?limit=3&offset=0",
				"last":     "http://example.com/opds/books?limit=3&offset=3",
				"previous": "http://example.com/opds/books?limit=3&offset=0",
			},
			noLinks: []string{"next"},
		},
		"oversized offset": {
			path:       "/opds/books?limit=2&offset=99",
			total:      4,
			perPage:    2,
			startIndex: 100,
			links: map[string]string{
				"first":    "http://example.com/opds/books?limit=2&offset=0",
				"last":     "http://example.com/opds/books?limit=2&offset=2",
				"previous": "http://example.com/opds/books?limit=2&offset=2",
			},
			noLinks: []string{"next"},
		},
		"recent last page": {
			path:       "/opds/recent?limit=2&offset=2",
			total:      4,
			perPage:    2,
			startIndex: 3,
			entries:    2,
			links: map[string]string{
				"first":    "http://example.com/opds/recent?limit=2&offset=0",
				"last":     "http://example.com/opds/recent?limit=2&offset=2",
				"previous": "http://example.com/opds/recent?limit=2&offset=0",
			},
			noLinks: []string{"next"},
		},
		"search keeps query in next": {
			path:       "/opds/search?q=Boundary&limit=2",
			total:      3,
			perPage:    2,
			startIndex: 1,
			entries:    2,
			links: map[string]string{
				"first": "http://example.com/opds/search?limit=2&offset=0&q=Boundary",
				"last":  "http://example.com/opds/search?limit=2&offset=2&q=Boundary",
				"next":  "http://example.com/opds/search?limit=2&offset=2&q=Boundary",
			},
			noLinks: []string{"previous"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			req.SetBasicAuth("alice", "pw")
			w := httptest.NewRecorder()
			testRoutes(t, s).ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
			}
			body := w.Body.String()
			assertValidXML(t, w.Body.Bytes())
			for _, want := range []string{
				`<opensearch:totalResults>` + strconv.Itoa(tc.total) + `</opensearch:totalResults>`,
				`<opensearch:itemsPerPage>` + strconv.Itoa(tc.perPage) + `</opensearch:itemsPerPage>`,
				`<opensearch:startIndex>` + strconv.Itoa(tc.startIndex) + `</opensearch:startIndex>`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("paginated feed missing %q:\n%s", want, body)
				}
			}
			if got := strings.Count(body, `<entry>`); got != tc.entries {
				t.Fatalf("entry count = %d, want %d:\n%s", got, tc.entries, body)
			}

			links := opdsFeedLinks(t, w.Body.Bytes())
			for rel, wantHref := range tc.links {
				got, ok := links[rel]
				if !ok {
					t.Fatalf("feed missing rel=%q link:\n%s", rel, body)
				}
				if got.Href != wantHref {
					t.Fatalf("rel=%q href = %q, want %q", rel, got.Href, wantHref)
				}
				if got.Type != opds.AcquisitionFeedType {
					t.Fatalf("rel=%q type = %q, want %q", rel, got.Type, opds.AcquisitionFeedType)
				}
			}
			for _, rel := range tc.noLinks {
				if _, ok := links[rel]; ok {
					t.Fatalf("feed unexpectedly contains rel=%q link:\n%s", rel, body)
				}
			}
		})
	}
}

func opdsFeedLinks(t *testing.T, body []byte) map[string]struct {
	Href string
	Type string
} {
	t.Helper()
	var feed struct {
		Links []struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
			Type string `xml:"type,attr"`
		} `xml:"link"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		t.Fatalf("decode OPDS feed links: %v", err)
	}
	links := make(map[string]struct {
		Href string
		Type string
	}, len(feed.Links))
	for _, link := range feed.Links {
		links[link.Rel] = struct {
			Href string
			Type string
		}{Href: link.Href, Type: link.Type}
	}
	return links
}

func TestOPDSDownloadAcceptsBasicAuth(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	s := newTestServer(database, dir)

	req := httptest.NewRequest("GET", "/download/asset_1", nil)
	req.SetBasicAuth("alice", "pw")
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Body.String(); got != "epub content" {
		t.Fatalf("download body = %q, want fixture bytes", got)
	}
}

func TestOPDSSearchFeed(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	s := newTestServer(database, dir)

	req := httptest.NewRequest("GET", "/opds/search?q=Hobbit", nil)
	req.SetBasicAuth("alice", "pw")
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != opds.AcquisitionFeedType+"; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want OPDS acquisition", got)
	}
	assertValidXML(t, w.Body.Bytes())
	body := w.Body.String()
	for _, want := range []string{
		`<title>The Hobbit</title>`,
		`href="http://example.com/download/asset_1"`,
		`<opensearch:totalResults>1</opensearch:totalResults>`,
		`<opensearch:itemsPerPage>50</opensearch:itemsPerPage>`,
		`<opensearch:startIndex>1</opensearch:startIndex>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("search feed missing %q:\n%s", want, body)
		}
	}

	// "Dune" (w_2) matches by title but has no asset, so it must not appear in a
	// search acquisition feed.
	req2 := httptest.NewRequest("GET", "/opds/search?q=Dune", nil)
	req2.SetBasicAuth("alice", "pw")
	w2 := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w2.Code, http.StatusOK, w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "<entry>") {
		t.Fatalf("assetless match appeared in search feed:\n%s", w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `<opensearch:totalResults>0</opensearch:totalResults>`) {
		t.Fatalf("assetless search feed missing zero totalResults:\n%s", w2.Body.String())
	}
}

func TestOPDSOpenSearchDescription(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	_ = mustUser(t, database, "alice", db.RoleMember)
	s := newTestServer(database, dir)

	req := httptest.NewRequest("GET", "/opds/osd", nil)
	req.SetBasicAuth("alice", "pw")
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != opds.OpenSearchType+"; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want OpenSearch", got)
	}
	assertValidXML(t, w.Body.Bytes())
	body := w.Body.String()
	for _, want := range []string{
		`xmlns="http://a9.com/-/spec/opensearch/1.1/"`,
		`template="http://example.com/opds/search?q={searchTerms}"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("OSD missing %q:\n%s", want, body)
		}
	}
}

func assertValidXML(t *testing.T, body []byte) {
	t.Helper()
	var v struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(body, &v); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, string(body))
	}
}
