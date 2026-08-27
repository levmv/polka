package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/opds"
)

const (
	opdsDefaultLimit = 50
	opdsMaxLimit     = 200
)

type opdsAcquisitionPage struct {
	ID    string
	Title string
	Path  string
	Query url.Values
}

func (s *Server) handleOPDSRoot(w http.ResponseWriter, r *http.Request) {
	rootHref := absoluteURL(r, "/opds", nil)
	entries := []opds.NavEntry{
		{
			ID:       "urn:polka:opds:books",
			Title:    "All books",
			Summary:  "Every book in the library.",
			Href:     absoluteURL(r, "/opds/books", nil),
			LinkType: opds.AcquisitionFeedType,
		},
		{
			ID:       "urn:polka:opds:recent",
			Title:    "Recently added",
			Summary:  "Newest books first.",
			Href:     absoluteURL(r, "/opds/recent", nil),
			LinkType: opds.AcquisitionFeedType,
		},
		{
			ID:       "urn:polka:opds:shelves",
			Title:    "By shelf",
			Summary:  "Browse personal and shared shelves.",
			Href:     absoluteURL(r, "/opds/shelves", nil),
			LinkType: opds.NavigationFeedType,
		},
		{
			ID:       "urn:polka:opds:series",
			Title:    "By series",
			Summary:  "Browse books grouped by series.",
			Href:     absoluteURL(r, "/opds/series", nil),
			LinkType: opds.NavigationFeedType,
		},
		{
			ID:       "urn:polka:opds:tags",
			Title:    "By tag",
			Summary:  "Browse books grouped by tag.",
			Href:     absoluteURL(r, "/opds/tags", nil),
			LinkType: opds.NavigationFeedType,
		},
	}
	body, err := opds.Navigation(time.Now(), opds.NavigationMeta{
		ID:         "urn:polka:opds:root",
		Title:      "polka",
		SelfHref:   rootHref,
		StartHref:  rootHref,
		SearchHref: absoluteURL(r, "/opds/osd", nil),
	}, entries)
	if err != nil {
		serverError(w, err)
		return
	}
	writeOPDS(w, opds.NavigationFeedType, body)
}

func (s *Server) handleOPDSRecent(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseOPDSPagination(w, r)
	if !ok {
		return
	}
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}

	rows, err := db.ListRecentOPDSPublications(s.db, scope, limit, offset)
	if err != nil {
		serverError(w, err)
		return
	}
	total, err := db.CountOPDSPublications(s.db, scope)
	if err != nil {
		serverError(w, err)
		return
	}
	s.writeOPDSPagedAcquisition(w, r, opdsAcquisitionPage{
		ID:    "urn:polka:opds:recent",
		Title: "Recently added",
		Path:  "/opds/recent",
	}, rows, total, limit, offset)
}

// handleOPDSSeries walks series by keyset cursor rather than materializing the
// whole facet: a library at the sizes polka targets can hold thousands of
// series, and both the server's memory and the client's XML grow with the feed.
func (s *Server) handleOPDSSeries(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	series, err := db.ListSeriesCountsPage(s.db, scope, "", after, opdsDefaultLimit+1)
	if err != nil {
		serverError(w, err)
		return
	}
	nextHref := ""
	if len(series) > opdsDefaultLimit {
		series = series[:opdsDefaultLimit]
		next := url.Values{}
		next.Set("after", series[len(series)-1].Name)
		nextHref = absoluteURL(r, "/opds/series", next)
	}

	entries := make([]opds.NavEntry, 0, len(series))
	for _, sc := range series {
		q := url.Values{}
		q.Set("q", db.QueryTerm("series", sc.Name))
		entries = append(entries, opds.NavEntry{
			ID:       "urn:polka:opds:series:" + sc.Name,
			Title:    sc.Name,
			Summary:  pluralBooks(sc.BookCount),
			Href:     absoluteURL(r, "/opds/search", q),
			LinkType: opds.AcquisitionFeedType,
		})
	}

	s.writeOPDSNavigation(w, r, "urn:polka:opds:series", "By series", "/opds/series", nextHref, entries)
}

func (s *Server) handleOPDSTags(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	tags, err := db.ListTags(s.db, scope, "", 0)
	if err != nil {
		serverError(w, err)
		return
	}

	entries := make([]opds.NavEntry, 0, len(tags))
	for _, tag := range tags {
		q := url.Values{}
		q.Set("q", db.QueryTerm("tag", tag))
		entries = append(entries, opds.NavEntry{
			ID:       "urn:polka:opds:tag:" + tag,
			Title:    tag,
			Href:     absoluteURL(r, "/opds/search", q),
			LinkType: opds.AcquisitionFeedType,
		})
	}

	s.writeOPDSNavigation(w, r, "urn:polka:opds:tags", "By tag", "/opds/tags", "", entries)
}

func (s *Server) handleOPDSShelves(w http.ResponseWriter, r *http.Request) {
	shelves, err := s.db.ListShelvesForUser(UserID(r.Context()))
	if err != nil {
		serverError(w, err)
		return
	}

	entries := make([]opds.NavEntry, 0, len(shelves))
	for _, shelf := range shelves {
		summary := "Shelf"
		if shelf.Kind == db.ShelfQuery {
			summary = "Saved search"
		}
		entries = append(entries, opds.NavEntry{
			ID:       "urn:polka:opds:shelf:" + shelf.ID,
			Title:    shelf.Name,
			Summary:  summary,
			Href:     absoluteURL(r, "/opds/shelves/"+shelf.ID, nil),
			LinkType: opds.AcquisitionFeedType,
		})
	}

	s.writeOPDSNavigation(w, r, "urn:polka:opds:shelves", "By shelf", "/opds/shelves", "", entries)
}

func (s *Server) handleOPDSShelf(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseOPDSPagination(w, r)
	if !ok {
		return
	}
	userID := UserID(r.Context())
	shelf, err := s.db.GetShelfForUser(r.PathValue("id"), userID)
	if errors.Is(err, db.ErrShelfNotFound) {
		http.Error(w, "Shelf not found", http.StatusNotFound)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}

	var rows []db.OPDSPublicationRow
	var total int
	if shelf.Kind == db.ShelfQuery {
		rows, err = db.SearchOPDSPublications(s.db, scope, userID, shelf.Query, limit, offset)
		if err == nil {
			total, err = db.CountSearchOPDSPublications(s.db, scope, userID, shelf.Query)
		}
	} else {
		rows, err = db.ListManualShelfOPDSPublications(s.db, scope, shelf.ID, limit, offset)
		if err == nil {
			total, err = db.CountManualShelfOPDSPublications(s.db, scope, shelf.ID)
		}
	}
	if err != nil {
		serverError(w, err)
		return
	}

	path := "/opds/shelves/" + shelf.ID
	s.writeOPDSPagedAcquisition(w, r, opdsAcquisitionPage{
		ID:    "urn:polka:opds:shelf:" + shelf.ID,
		Title: shelf.Name,
		Path:  path,
	}, rows, total, limit, offset)
}

// writeOPDSNavigation writes a facet navigation feed (shelves/series/tags) that
// hangs off the root catalog. nextHref is empty for a facet that fits in one
// feed; a paged facet passes its cursor link.
func (s *Server) writeOPDSNavigation(w http.ResponseWriter, r *http.Request, id, title, selfPath, nextHref string, entries []opds.NavEntry) {
	body, err := opds.Navigation(time.Now(), opds.NavigationMeta{
		ID:         id,
		Title:      title,
		SelfHref:   absoluteURL(r, selfPath, r.URL.Query()),
		StartHref:  absoluteURL(r, "/opds", nil),
		NextHref:   nextHref,
		SearchHref: absoluteURL(r, "/opds/osd", nil),
	}, entries)
	if err != nil {
		serverError(w, err)
		return
	}
	writeOPDS(w, opds.NavigationFeedType, body)
}

func pluralBooks(n int) string {
	if n == 1 {
		return "1 book"
	}
	return fmt.Sprintf("%d books", n)
}

func (s *Server) handleOPDSBooks(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseOPDSPagination(w, r)
	if !ok {
		return
	}
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}

	rows, err := db.ListOPDSPublications(s.db, scope, limit, offset)
	if err != nil {
		serverError(w, err)
		return
	}
	total, err := db.CountOPDSPublications(s.db, scope)
	if err != nil {
		serverError(w, err)
		return
	}
	s.writeOPDSPagedAcquisition(w, r, opdsAcquisitionPage{
		ID:    "urn:polka:opds:books",
		Title: "All books",
		Path:  "/opds/books",
	}, rows, total, limit, offset)
}

func (s *Server) handleOPDSSearch(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseOPDSPagination(w, r)
	if !ok {
		return
	}
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	var rows []db.OPDSPublicationRow
	total := 0
	if query != "" {
		var err error
		rows, err = db.SearchOPDSPublications(s.db, scope, UserID(r.Context()), query, limit, offset)
		if err != nil {
			serverError(w, err)
			return
		}
		total, err = db.CountSearchOPDSPublications(s.db, scope, UserID(r.Context()), query)
		if err != nil {
			serverError(w, err)
			return
		}
	}
	title := "Search"
	nextQuery := url.Values{}
	if query != "" {
		title = "Search: " + query
		nextQuery.Set("q", query)
	}
	s.writeOPDSPagedAcquisition(w, r, opdsAcquisitionPage{
		ID:    "urn:polka:opds:search",
		Title: title,
		Path:  "/opds/search",
		Query: nextQuery,
	}, rows, total, limit, offset)
}

func (s *Server) writeOPDSPagedAcquisition(w http.ResponseWriter, r *http.Request, page opdsAcquisitionPage, rows []db.OPDSPublicationRow, total, limit, offset int) {
	hasNext := offset+limit < total

	nextHref := ""
	if hasNext {
		nextHref = absoluteURL(r, page.Path, opdsPageQuery(page.Query, limit, offset+limit))
	}
	previousHref := ""
	if offset > 0 {
		previousOffset := max(0, offset-limit)
		lastOffset := opdsLastPageOffset(total, limit)
		if previousOffset > lastOffset {
			previousOffset = lastOffset
		}
		previousHref = absoluteURL(r, page.Path, opdsPageQuery(page.Query, limit, previousOffset))
	}

	s.writeOPDSAcquisition(w, r, opds.AcquisitionMeta{
		ID:           page.ID,
		Title:        page.Title,
		SelfHref:     absoluteURL(r, page.Path, r.URL.Query()),
		StartHref:    absoluteURL(r, "/opds", nil),
		FirstHref:    absoluteURL(r, page.Path, opdsPageQuery(page.Query, limit, 0)),
		LastHref:     absoluteURL(r, page.Path, opdsPageQuery(page.Query, limit, opdsLastPageOffset(total, limit))),
		NextHref:     nextHref,
		PreviousHref: previousHref,
		SearchHref:   absoluteURL(r, "/opds/osd", nil),
		TotalResults: total,
		ItemsPerPage: limit,
		StartIndex:   offset + 1,
	}, rows)
}

func opdsPageQuery(base url.Values, limit, offset int) url.Values {
	q := cloneValues(base)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	return q
}

func opdsLastPageOffset(total, limit int) int {
	if total == 0 {
		return 0
	}
	return (total - 1) / limit * limit
}

func (s *Server) handleOPDSOpenSearch(w http.ResponseWriter, r *http.Request) {
	// The {searchTerms} placeholder must stay literal, so build the template by
	// hand rather than through url.Values (which would percent-encode the braces).
	template := absoluteURL(r, "/opds/search", nil) + "?q={searchTerms}"
	body, err := opds.OpenSearchDescription(template)
	if err != nil {
		serverError(w, err)
		return
	}
	writeOPDS(w, opds.OpenSearchType, body)
}

// writeOPDSAcquisition resolves assets/authors/covers for the given publication
// rows and writes an acquisition feed. Shared by the all-books and search feeds;
// works without a downloadable asset are skipped (an acquisition entry without an
// acquisition link is useless to a reader).
func (s *Server) writeOPDSAcquisition(w http.ResponseWriter, r *http.Request, meta opds.AcquisitionMeta, rows []db.OPDSPublicationRow) {
	workIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		workIDs = append(workIDs, row.ID)
	}

	assetRows, err := db.AssetsByWorkIDs(s.db, workIDs)
	if err != nil {
		serverError(w, err)
		return
	}
	assetsByWork := make(map[string][]db.AssetRow)
	for _, a := range assetRows {
		assetsByWork[a.WorkID] = append(assetsByWork[a.WorkID], a)
	}

	authorsByWork, err := db.AuthorsByWorkIDs(s.db, workIDs)
	if err != nil {
		serverError(w, err)
		return
	}

	pubs := make([]opds.Publication, 0, len(rows))
	for _, row := range rows {
		links := opdsAssetLinks(r, assetsByWork[row.ID])
		if len(links) == 0 {
			continue
		}
		links = append(links, opdsCoverLinks(r, row.ID, row.CoverVersion)...)

		pub := opds.Publication{
			ID:            "urn:polka:work:" + row.ID,
			Title:         row.Title,
			Updated:       time.Unix(row.UpdatedAt, 0),
			Authors:       opdsAuthorNames(authorsByWork[row.ID]),
			Categories:    opdsCategories(row.Tags.String),
			Publisher:     row.Publisher.String,
			PublishedDate: row.PublishedDate.String,
			Language:      row.Language.String,
			Identifiers:   opdsIdentifiers(row.Identifiers.String),
			Links:         links,
		}
		if row.Description.Valid {
			pub.Summary = htmlText(row.Description.String)
		}
		pubs = append(pubs, pub)
	}

	body, err := opds.Acquisition(time.Now(), meta, pubs)
	if err != nil {
		serverError(w, err)
		return
	}
	writeOPDS(w, opds.AcquisitionFeedType, body)
}

func opdsIdentifiers(raw string) []string {
	ids := bookmeta.ParseIdentifiers(raw)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if bookmeta.IsInternalIdentifier(id) {
			continue
		}
		typ := strings.TrimSpace(strings.ToLower(id.Type))
		value := strings.TrimSpace(id.Value)
		if typ == "" || value == "" {
			continue
		}
		out = append(out, typ+":"+value)
	}
	return out
}

func parseOPDSPagination(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit, ok = parseOPDSInt(r.URL.Query().Get("limit"), opdsDefaultLimit, 1, opdsMaxLimit)
	if !ok {
		http.Error(w, "Invalid limit", http.StatusBadRequest)
		return 0, 0, false
	}
	offset, ok = parseOPDSInt(r.URL.Query().Get("offset"), 0, 0, 1<<31-1)
	if !ok {
		http.Error(w, "Invalid offset", http.StatusBadRequest)
		return 0, 0, false
	}
	return limit, offset, true
}

func parseOPDSInt(raw string, fallback, minValue, maxValue int) (int, bool) {
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < minValue || n > maxValue {
		return 0, false
	}
	return n, true
}

func opdsAssetLinks(r *http.Request, assets []db.AssetRow) []opds.Link {
	links := make([]opds.Link, 0, len(assets))
	for _, a := range assets {
		links = append(links, opds.Link{
			Rel:   opds.AcquisitionRel,
			Href:  absoluteURL(r, "/download/"+url.PathEscape(a.ID), nil),
			Type:  format.MediaTypeForExtension(a.Extension),
			Title: strings.TrimPrefix(strings.ToUpper(a.Extension), "."),
		})
	}
	return links
}

func opdsCoverLinks(r *http.Request, workID string, coverVersion int) []opds.Link {
	q := url.Values{}
	if coverVersion > 0 {
		q.Set("v", strconv.Itoa(coverVersion))
	}
	thumbQ := cloneValues(q)
	thumbQ.Set("variant", "thumb")

	escapedID := url.PathEscape(workID)
	return []opds.Link{
		{Rel: opds.ImageRel, Href: absoluteURL(r, "/covers/"+escapedID, q), Type: "image/jpeg"},
		{Rel: opds.ThumbnailRel, Href: absoluteURL(r, "/covers/"+escapedID, thumbQ), Type: "image/jpeg"},
	}
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, items := range values {
		for _, item := range items {
			out.Add(key, item)
		}
	}
	return out
}

func opdsAuthorNames(rows []db.AuthorRow) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if name := strings.TrimSpace(row.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func opdsCategories(raw string) []string {
	parts := strings.Split(raw, ",")
	categories := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			categories = append(categories, part)
		}
	}
	return categories
}

func htmlText(raw string) string {
	z := html.NewTokenizer(strings.NewReader(raw))
	var text strings.Builder
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(text.String()), " ")
		case html.StartTagToken, html.EndTagToken:
			tag, _ := z.TagName()
			if opdsBlockTag(string(tag)) {
				text.WriteByte(' ')
			}
		case html.TextToken:
			text.Write(z.Text())
		}
	}
}

func opdsBlockTag(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "br", "div", "footer", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav", "ol", "p", "pre", "section", "table", "ul":
		return true
	default:
		return false
	}
}

func absoluteURL(r *http.Request, path string, q url.Values) string {
	u := url.URL{
		Scheme: requestScheme(r),
		Host:   r.Host,
		Path:   path,
	}
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func requestScheme(r *http.Request) string {
	// Polka has no trusted-proxy configuration. Accepting the first valid
	// X-Forwarded-Proto value is pragmatic reverse-proxy compatibility: it only
	// shapes absolute links in this authenticated feed and never grants access.
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		proto = strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
		if proto == "http" || proto == "https" {
			return proto
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func writeOPDS(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}
