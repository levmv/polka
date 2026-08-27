package opds

import (
	"encoding/xml"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	NavigationFeedType  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	AcquisitionFeedType = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	AcquisitionRel      = "http://opds-spec.org/acquisition"
	ImageRel            = "http://opds-spec.org/image"
	ThumbnailRel        = "http://opds-spec.org/image/thumbnail"
	SearchRel           = "search"
	OpenSearchType      = "application/opensearchdescription+xml"
)

type Link struct {
	Rel   string `xml:"rel,attr,omitempty"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr,omitempty"`
	Title string `xml:"title,attr,omitempty"`
}

type Publication struct {
	ID            string
	Title         string
	Updated       time.Time
	Authors       []string
	Summary       string
	Categories    []string
	Publisher     string
	PublishedDate string
	Language      string
	Identifiers   []string
	Links         []Link
}

type feed struct {
	XMLName           xml.Name `xml:"feed"`
	XMLNS             string   `xml:"xmlns,attr"`
	XMLNSDC           string   `xml:"xmlns:dc,attr,omitempty"`
	XMLNSOpenSearch   string   `xml:"xmlns:opensearch,attr,omitempty"`
	ID                string   `xml:"id"`
	Title             string   `xml:"title"`
	Updated           string   `xml:"updated"`
	Author            person   `xml:"author"`
	Links             []Link   `xml:"link"`
	OpenSearchTotal   *int     `xml:"opensearch:totalResults,omitempty"`
	OpenSearchPerPage *int     `xml:"opensearch:itemsPerPage,omitempty"`
	OpenSearchStart   *int     `xml:"opensearch:startIndex,omitempty"`
	Entries           []entry  `xml:"entry"`
}

type entry struct {
	Title         string     `xml:"title"`
	ID            string     `xml:"id"`
	Updated       string     `xml:"updated"`
	Authors       []person   `xml:"author,omitempty"`
	Summary       *textValue `xml:"summary,omitempty"`
	Categories    []category `xml:"category,omitempty"`
	DCPublisher   string     `xml:"dc:publisher,omitempty"`
	DCIssued      string     `xml:"dc:issued,omitempty"`
	DCLanguage    string     `xml:"dc:language,omitempty"`
	DCIdentifiers []string   `xml:"dc:identifier,omitempty"`
	Links         []Link     `xml:"link"`
}

type person struct {
	Name string `xml:"name"`
}

type textValue struct {
	Type string `xml:"type,attr,omitempty"`
	Text string `xml:",chardata"`
}

type category struct {
	Term  string `xml:"term,attr"`
	Label string `xml:"label,attr,omitempty"`
}

// NavigationMeta carries the feed-level fields of a navigation feed (the root
// catalog, or a facet listing like "By series").
type NavigationMeta struct {
	ID        string
	Title     string
	SelfHref  string
	StartHref string
	// NextHref continues a facet listing that is too large for one feed. Facets
	// are walked by keyset cursor rather than offset, so there is no first/last
	// pair to publish alongside it.
	NextHref   string
	SearchHref string
}

// NavEntry is one navigation-feed item linking to a sub-feed. LinkType is the
// linked feed's media type — AcquisitionFeedType for a list of books, or
// NavigationFeedType for a further facet listing.
type NavEntry struct {
	ID       string
	Title    string
	Summary  string
	Href     string
	LinkType string
}

func Navigation(now time.Time, meta NavigationMeta, entries []NavEntry) ([]byte, error) {
	updated := atomTime(now)
	links := []Link{
		{Rel: "self", Href: meta.SelfHref, Type: NavigationFeedType},
		{Rel: "start", Href: meta.StartHref, Type: NavigationFeedType},
	}
	if meta.NextHref != "" {
		links = append(links, Link{Rel: "next", Href: meta.NextHref, Type: NavigationFeedType})
	}
	if meta.SearchHref != "" {
		links = append(links, Link{Rel: SearchRel, Href: meta.SearchHref, Type: OpenSearchType})
	}
	f := feed{
		XMLNS:   "http://www.w3.org/2005/Atom",
		ID:      meta.ID,
		Title:   meta.Title,
		Updated: updated,
		Author:  person{Name: "polka"},
		Links:   links,
	}
	for _, e := range entries {
		ne := entry{
			Title:   e.Title,
			ID:      e.ID,
			Updated: updated,
			Links:   []Link{{Rel: "subsection", Href: e.Href, Type: e.LinkType, Title: e.Title}},
		}
		if e.Summary != "" {
			ne.Summary = &textValue{Type: "text", Text: e.Summary}
		}
		f.Entries = append(f.Entries, ne)
	}
	return marshalFeed(f)
}

// AcquisitionMeta carries the feed-level fields that vary between acquisition
// feeds (the all-books listing vs a search result), so the entry-building logic
// can stay shared.
type AcquisitionMeta struct {
	ID           string
	Title        string
	SelfHref     string
	StartHref    string
	FirstHref    string
	LastHref     string
	NextHref     string
	PreviousHref string
	SearchHref   string
	TotalResults int
	ItemsPerPage int
	StartIndex   int
}

func Acquisition(now time.Time, meta AcquisitionMeta, pubs []Publication) ([]byte, error) {
	links := []Link{
		{Rel: "self", Href: meta.SelfHref, Type: AcquisitionFeedType},
		{Rel: "start", Href: meta.StartHref, Type: NavigationFeedType},
		{Rel: "up", Href: meta.StartHref, Type: NavigationFeedType},
	}
	if meta.FirstHref != "" {
		links = append(links, Link{Rel: "first", Href: meta.FirstHref, Type: AcquisitionFeedType})
	}
	if meta.LastHref != "" {
		links = append(links, Link{Rel: "last", Href: meta.LastHref, Type: AcquisitionFeedType})
	}
	if meta.NextHref != "" {
		links = append(links, Link{Rel: "next", Href: meta.NextHref, Type: AcquisitionFeedType})
	}
	if meta.PreviousHref != "" {
		links = append(links, Link{Rel: "previous", Href: meta.PreviousHref, Type: AcquisitionFeedType})
	}
	if meta.SearchHref != "" {
		links = append(links, Link{Rel: SearchRel, Href: meta.SearchHref, Type: OpenSearchType})
	}
	f := feed{
		XMLNS:   "http://www.w3.org/2005/Atom",
		XMLNSDC: "http://purl.org/dc/elements/1.1/",
		ID:      meta.ID,
		Title:   meta.Title,
		Updated: atomTime(now),
		Author:  person{Name: "polka"},
		Links:   links,
	}
	if meta.TotalResults > 0 || meta.ItemsPerPage > 0 || meta.StartIndex > 0 {
		f.XMLNSOpenSearch = "http://a9.com/-/spec/opensearch/1.1/"
		f.OpenSearchTotal = new(meta.TotalResults)
		f.OpenSearchPerPage = new(meta.ItemsPerPage)
		f.OpenSearchStart = new(meta.StartIndex)
	}
	for _, pub := range pubs {
		f.Entries = append(f.Entries, publicationEntry(pub, now))
	}
	return marshalFeed(f)
}

// openSearchDescription is the OpenSearch description document advertised through
// each feed's rel="search" link, so OPDS clients (KOReader, etc.) can discover
// how to query the catalog.
type openSearchDescription struct {
	XMLName       xml.Name `xml:"OpenSearchDescription"`
	XMLNS         string   `xml:"xmlns,attr"`
	ShortName     string   `xml:"ShortName"`
	Description   string   `xml:"Description"`
	InputEncoding string   `xml:"InputEncoding"`
	URL           osdURL   `xml:"Url"`
}

type osdURL struct {
	Type     string `xml:"type,attr"`
	Template string `xml:"template,attr"`
}

// OpenSearchDescription builds the OpenSearch description document. searchTemplate
// must contain the literal {searchTerms} placeholder the client substitutes — do
// not URL-encode the braces.
func OpenSearchDescription(searchTemplate string) ([]byte, error) {
	return marshalDoc(openSearchDescription{
		XMLNS:         "http://a9.com/-/spec/opensearch/1.1/",
		ShortName:     "polka",
		Description:   "Search the polka library",
		InputEncoding: "UTF-8",
		URL: osdURL{
			Type:     AcquisitionFeedType,
			Template: searchTemplate,
		},
	})
}

func publicationEntry(pub Publication, fallbackUpdated time.Time) entry {
	updated := pub.Updated
	if updated.IsZero() {
		updated = fallbackUpdated
	}
	e := entry{
		Title:   pub.Title,
		ID:      pub.ID,
		Updated: atomTime(updated),
		Links:   pub.Links,
	}
	for _, name := range pub.Authors {
		if name = strings.TrimSpace(name); name != "" {
			e.Authors = append(e.Authors, person{Name: name})
		}
	}
	if summary := strings.TrimSpace(pub.Summary); summary != "" {
		e.Summary = &textValue{Type: "text", Text: summary}
	}
	e.DCPublisher = strings.TrimSpace(pub.Publisher)
	e.DCIssued = strings.TrimSpace(pub.PublishedDate)
	e.DCLanguage = strings.TrimSpace(pub.Language)
	for _, id := range pub.Identifiers {
		if id = strings.TrimSpace(id); id != "" {
			e.DCIdentifiers = append(e.DCIdentifiers, id)
		}
	}
	seen := make(map[string]bool, len(pub.Categories))
	for _, c := range pub.Categories {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		e.Categories = append(e.Categories, category{Term: c, Label: c})
	}
	return e
}

func marshalFeed(f feed) ([]byte, error) {
	return marshalDoc(f)
}

func marshalDoc(v any) ([]byte, error) {
	b, err := xml.MarshalIndent(sanitizeXMLValue(reflect.ValueOf(v)).Interface(), "", "  ")
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(xml.Header)+len(b))
	out = append(out, xml.Header...)
	out = append(out, b...)
	return out, nil
}

func sanitizeXMLValue(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(sanitizeXMLValue(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if field.PkgPath != "" {
				continue
			}
			out.Field(i).Set(sanitizeXMLValue(v.Field(i)))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(sanitizeXMLValue(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			out.Index(i).Set(sanitizeXMLValue(v.Index(i)))
		}
		return out
	case reflect.String:
		return reflect.ValueOf(sanitizeXMLString(v.String())).Convert(v.Type())
	default:
		return v
	}
}

func sanitizeXMLString(s string) string {
	var b strings.Builder
	changed := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		valid := !(r == utf8.RuneError && size == 1) && validXMLChar(r)
		if !valid {
			if !changed {
				b.Grow(len(s))
				b.WriteString(s[:i])
				changed = true
			}
			i += size
			continue
		}
		if changed {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	if !changed {
		return s
	}
	return b.String()
}

func validXMLChar(r rune) bool {
	return r == 0x9 ||
		r == 0xA ||
		r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

func atomTime(t time.Time) string {
	if t.IsZero() {
		t = time.Unix(0, 0)
	}
	return t.UTC().Format(time.RFC3339)
}
