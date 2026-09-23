package opds

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestNavigationFeed(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	body, err := Navigation(now, NavigationMeta{
		ID:         "urn:polka:opds:root",
		Title:      "polka",
		SelfHref:   "https://polka.example/opds",
		StartHref:  "https://polka.example/opds",
		SearchHref: "https://polka.example/opds/osd",
	}, []NavEntry{
		{
			ID:       "urn:polka:opds:books",
			Title:    "All books",
			Summary:  "Browse all books.",
			Href:     "https://polka.example/opds/books",
			LinkType: AcquisitionFeedType,
		},
	})
	if err != nil {
		t.Fatalf("Navigation: %v", err)
	}
	assertXML(t, body)

	s := string(body)
	for _, want := range []string{
		`xmlns="http://www.w3.org/2005/Atom"`,
		`<id>urn:polka:opds:root</id>`,
		`<title>All books</title>`,
		`rel="subsection"`,
		`type="application/atom+xml;profile=opds-catalog;kind=acquisition"`,
		`rel="search"`,
		`type="application/opensearchdescription+xml"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("feed missing %q:\n%s", want, s)
		}
	}
}

func TestAcquisitionFeedEscapesPublicationData(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	body, err := Acquisition(
		now,
		AcquisitionMeta{
			ID:           "urn:polka:opds:books",
			Title:        "All books",
			SelfHref:     "https://polka.example/opds/books",
			StartHref:    "https://polka.example/opds",
			FirstHref:    "https://polka.example/opds/books?offset=0",
			LastHref:     "https://polka.example/opds/books?offset=2",
			NextHref:     "https://polka.example/opds/books?offset=50",
			PreviousHref: "https://polka.example/opds/books?offset=0",
			SearchHref:   "https://polka.example/opds/osd",
			TotalResults: 3,
			ItemsPerPage: 1,
			StartIndex:   2,
		},
		[]Publication{
			{
				ID:            "urn:polka:book:w_1",
				Title:         "A & B",
				Updated:       now.Add(-time.Hour),
				Authors:       []string{"Ada <Lovelace>"},
				Summary:       "Use <tags> as text.",
				Categories:    []string{"math", "math", "history"},
				Publisher:     "Analytical Press",
				PublishedDate: "1843",
				Language:      "en",
				Identifiers:   []string{"isbn:978-0-306-40615-7", "doi:10.1000/example"},
				Links: []Link{
					{Rel: AcquisitionRel, Href: "https://polka.example/download/a_1", Type: "application/epub+zip"},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Acquisition: %v", err)
	}
	assertXML(t, body)

	s := string(body)
	for _, want := range []string{
		`xmlns:dc="http://purl.org/dc/elements/1.1/"`,
		`xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/"`,
		`rel="first"`,
		`rel="last"`,
		`rel="next"`,
		`rel="previous"`,
		`<opensearch:totalResults>3</opensearch:totalResults>`,
		`<opensearch:itemsPerPage>1</opensearch:itemsPerPage>`,
		`<opensearch:startIndex>2</opensearch:startIndex>`,
		`<title>A &amp; B</title>`,
		`<name>Ada &lt;Lovelace&gt;</name>`,
		`Use &lt;tags&gt; as text.`,
		`term="math"`,
		`term="history"`,
		`<dc:publisher>Analytical Press</dc:publisher>`,
		`<dc:issued>1843</dc:issued>`,
		`<dc:language>en</dc:language>`,
		`<dc:identifier>isbn:978-0-306-40615-7</dc:identifier>`,
		`<dc:identifier>doi:10.1000/example</dc:identifier>`,
		`rel="http://opds-spec.org/acquisition"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("feed missing %q:\n%s", want, s)
		}
	}
	if strings.Count(s, `term="math"`) != 1 {
		t.Fatalf("category was not deduplicated:\n%s", s)
	}
}

func TestOpenSearchDescription(t *testing.T) {
	body, err := OpenSearchDescription("https://polka.example/opds/search?q={searchTerms}")
	if err != nil {
		t.Fatalf("OpenSearchDescription: %v", err)
	}
	assertXML(t, body)

	s := string(body)
	for _, want := range []string{
		`xmlns="http://a9.com/-/spec/opensearch/1.1/"`,
		`<ShortName>polka</ShortName>`,
		`type="application/atom+xml;profile=opds-catalog;kind=acquisition"`,
		`template="https://polka.example/opds/search?q={searchTerms}"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("OSD missing %q:\n%s", want, s)
		}
	}
}

func assertXML(t *testing.T, body []byte) {
	t.Helper()
	var v struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(body, &v); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, string(body))
	}
}
