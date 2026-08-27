package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

type SearchQueryValidation struct {
	Valid      bool
	Error      string
	scopeMatch string
}

// ValidateSearchQuery is the strict entry point used by saved searches. Normal
// library search is deliberately lenient, but a persisted query must not change
// meaning because of a missing value or quote.
func ValidateSearchQuery(q string) SearchQueryValidation {
	parsed, err := parseSearchQuery(q, false)
	if err != nil {
		return SearchQueryValidation{Error: err.Error()}
	}
	if !parsed.hasClauses() {
		return SearchQueryValidation{Error: "Search query needs at least one term"}
	}
	scopeMatch, _ := parsed.accessScope()
	return SearchQueryValidation{Valid: true, scopeMatch: scopeMatch}
}

// ParseQuery converts a user query into the FTS5 MATCH part of the search. It
// remains public for small query builders and diagnostics; relational filters
// such as no:cover and status:reading intentionally do not appear in the result.
func ParseQuery(q string) string {
	parsed, _ := parseSearchQuery(q, true)
	return parsed.ftsMatch()
}

// QueryTerm builds a qualified search term (for example series:"Name"),
// doubling embedded quotes so parseSearchQuery reconstructs the literal value.
func QueryTerm(field, value string) string {
	return field + `:"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// UpdateSearchIndex rebuilds the search table row for a work from the
// relational catalog.
func UpdateSearchIndex(tx *sql.Tx, workID string) error {
	if _, err := tx.Exec("DELETE FROM search WHERE work_id = ?", workID); err != nil {
		return fmt.Errorf("delete search: %w", err)
	}

	// Gather every indexed field in one pass: the direct works columns plus the
	// authors (reusing colAuthors) and filenames as correlated subqueries.
	var title, series, tags, description, identifiers, authors, filenames string
	err := tx.QueryRow(fmt.Sprintf(`
		SELECT
			w.title,
			COALESCE(w.series, ''),
			COALESCE(w.tags, ''),
			COALESCE(w.description, ''),
			COALESCE(w.identifiers, ''),
			%s,
			COALESCE((SELECT group_concat(filename, ' ') FROM assets WHERE work_id = w.id), '')
		FROM works w
		WHERE w.id = ?
	`, colAuthors), workID).Scan(&title, &series, &tags, &description, &identifiers, &authors, &filenames)
	if err != nil {
		return fmt.Errorf("query work fields: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO search (rowid, work_id, title, authors, series, tags, description, identifiers, filename)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, nil, workID, title, authors, series, tags, description, identifiers, filenames); err != nil {
		return fmt.Errorf("insert search: %w", err)
	}
	return nil
}

type searchField uint8

const (
	searchEverywhere searchField = iota
	searchAuthors
	searchSeries
	searchTags
	searchTitle
)

type searchTerm struct {
	field searchField
	value string
}

type searchFilterKind uint8

const (
	searchFilterUnknown searchFilterKind = iota
	searchMissingCover
	searchMissingTags
	searchMissingDescription
	searchMissingAuthor
	searchMissingSeries
	searchReadingStatus
	searchFilterKindCount
)

type searchFilter struct {
	kind  searchFilterKind
	value string
}

// parsedSearchQuery is the semantic form of the mini-language. Parsing owns
// syntax only: it does not carry SQL snippets or know which catalog projection
// will consume the query.
type parsedSearchQuery struct {
	terms   []searchTerm
	filters []searchFilter
}

func (q parsedSearchQuery) hasClauses() bool {
	return len(q.terms) > 0 || len(q.filters) > 0
}

func (q parsedSearchQuery) ftsMatch() string {
	parts := make([]string, 0, len(q.terms))
	for _, term := range q.terms {
		value := `"` + strings.ReplaceAll(term.value, `"`, `""`) + `"`
		switch term.field {
		case searchAuthors:
			value = "authors:" + value
		case searchSeries:
			value = "series:" + value
		case searchTags:
			value = "tags:" + value
		case searchTitle:
			value = "title:" + value
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, " ")
}

const (
	noCoverScopeShelfReason       = "A smart shelf using no:cover cannot define access because no:cover is not yet supported in access rules; use a manual or tag-based shelf instead"
	noTagsScopeShelfReason        = "A smart shelf using no:tags cannot define access because no:tags is not yet supported in access rules; use a manual or tag-based shelf instead"
	noDescriptionScopeShelfReason = "A smart shelf using no:description cannot define access because no:description is not yet supported in access rules; use a manual or tag-based shelf instead"
	noAuthorScopeShelfReason      = "A smart shelf using no:author cannot define access because no:author is not yet supported in access rules; use a manual or tag-based shelf instead"
	noSeriesScopeShelfReason      = "A smart shelf using no:series cannot define access because no:series is not yet supported in access rules; use a manual or tag-based shelf instead"
	statusScopeShelfReason        = "A smart shelf using status: cannot define access because reading status is personal to each account; use a tag-based shelf instead"
	queryScopeShelfReason         = "This smart shelf cannot define access; use a manual or tag-based shelf instead"
)

// accessScope is the single policy for turning a complete semantic query into
// a reader access boundary. Every relational filter is an explicit decision;
// an unknown future kind fails closed with a neutral explanation.
func (q parsedSearchQuery) accessScope() (string, string) {
	for _, filter := range q.filters {
		switch filter.kind {
		case searchMissingCover:
			return "", noCoverScopeShelfReason
		case searchMissingTags:
			return "", noTagsScopeShelfReason
		case searchMissingDescription:
			return "", noDescriptionScopeShelfReason
		case searchMissingAuthor:
			return "", noAuthorScopeShelfReason
		case searchMissingSeries:
			return "", noSeriesScopeShelfReason
		case searchReadingStatus:
			// Status is viewer-relative and mutable. In particular, the absence of
			// a reading-state row means unread, so a new reader could otherwise be
			// granted most of the library by an unread shelf.
			return "", statusScopeShelfReason
		default:
			return "", queryScopeShelfReason
		}
	}
	match := q.ftsMatch()
	if match == "" {
		return "", queryScopeShelfReason
	}
	return match, ""
}

func parseSearchQuery(q string, lenient bool) (parsedSearchQuery, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return parsedSearchQuery{}, nil
	}

	var parsed parsedSearchQuery
	var currentToken strings.Builder
	inQuote := false
	var key string

	flushToken := func() error {
		value := currentToken.String()
		currentToken.Reset()
		if value == "" {
			if key == "" {
				return nil
			}
			missingKey := key
			key = ""
			if lenient {
				return nil
			}
			return fmt.Errorf("%s: requires a value", missingKey)
		}

		switch key {
		case "author":
			parsed.terms = append(parsed.terms, searchTerm{field: searchAuthors, value: value})
		case "series":
			parsed.terms = append(parsed.terms, searchTerm{field: searchSeries, value: value})
		case "tag":
			parsed.terms = append(parsed.terms, searchTerm{field: searchTags, value: value})
		case "title":
			parsed.terms = append(parsed.terms, searchTerm{field: searchTitle, value: value})
		case "no":
			kind, ok := missingFilterKind(value)
			if ok {
				parsed.filters = append(parsed.filters, searchFilter{kind: kind})
			} else if lenient {
				parsed.terms = append(parsed.terms, searchTerm{value: "no:" + value})
			} else {
				return fmt.Errorf("no:%s is not a supported filter", value)
			}
		case "status":
			status := strings.ToLower(strings.TrimSpace(value))
			if ValidReadingStatus(status) {
				parsed.filters = append(parsed.filters, searchFilter{kind: searchReadingStatus, value: status})
			} else if lenient {
				parsed.terms = append(parsed.terms, searchTerm{value: "status:" + value})
			} else {
				return fmt.Errorf("status:%s is not a supported status", value)
			}
		default:
			parsed.terms = append(parsed.terms, searchTerm{value: value})
		}
		key = ""
		return nil
	}

	runes := []rune(q)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '"' {
			// A doubled quote inside a quoted value is a literal quote. This is
			// the inverse of QueryTerm and ftsMatch escaping.
			if inQuote && i+1 < len(runes) && runes[i+1] == '"' {
				currentToken.WriteRune('"')
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}

		if unicode.IsSpace(r) && !inQuote {
			if err := flushToken(); err != nil && !lenient {
				return parsedSearchQuery{}, err
			}
			continue
		}

		if r == ':' && !inQuote && key == "" && currentToken.Len() > 0 {
			candidate := currentToken.String()
			if isSearchQualifier(candidate) {
				key = candidate
				currentToken.Reset()
				continue
			}
		}

		currentToken.WriteRune(r)
	}
	if inQuote && !lenient {
		return parsedSearchQuery{}, errors.New("Close the quote")
	}
	if err := flushToken(); err != nil && !lenient {
		return parsedSearchQuery{}, err
	}
	return parsed, nil
}

func isSearchQualifier(value string) bool {
	switch value {
	case "author", "series", "tag", "title", "no", "status":
		return true
	default:
		return false
	}
}

func missingFilterKind(value string) (searchFilterKind, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cover":
		return searchMissingCover, true
	case "tags":
		return searchMissingTags, true
	case "description":
		return searchMissingDescription, true
	case "author":
		return searchMissingAuthor, true
	case "series":
		return searchMissingSeries, true
	default:
		return 0, false
	}
}

func searchFilterCondition(kind searchFilterKind) string {
	switch kind {
	case searchMissingCover:
		return noCoverCondition
	case searchMissingTags:
		return noTagsCondition
	case searchMissingDescription:
		return noDescriptionCondition
	case searchMissingAuthor:
		return noAuthorCondition
	case searchMissingSeries:
		return noSeriesCondition
	case searchReadingStatus:
		return `COALESCE((
			SELECT rs.status FROM user_work_reading_state rs
			WHERE rs.user_id = ? AND rs.work_id = w.id
		), 'unread') = ?`
	default:
		panic("unknown search filter")
	}
}

// bookSearchPlan is the one compiled representation shared by catalog list,
// sequence, OPDS, and device projections. Callers still own their SELECT,
// product-specific predicates, ordering, and paging.
type bookSearchPlan struct {
	withSQL    string
	fromSQL    string
	whereSQL   string
	args       []any
	hasClauses bool
	hasRank    bool
}

func newBookSearchPlan(scope VisibilityScope, userID, rawQuery string) bookSearchPlan {
	parsed, _ := parseSearchQuery(rawQuery, true)
	match := parsed.ftsMatch()
	joined := "works w"
	where := "w.deleted_at IS NULL"
	if match != "" {
		joined = "search s JOIN works w ON s.work_id = w.id"
		where = "search MATCH ? AND " + where
	}

	withSQL, fromSQL, args := scope.joinVisibleWorks(joined)
	if match != "" {
		args = append(args, match)
	}
	for _, filter := range parsed.filters {
		where += " AND (" + searchFilterCondition(filter.kind) + ")"
		if filter.kind == searchReadingStatus {
			args = append(args, userID, filter.value)
		}
	}

	return bookSearchPlan{
		withSQL:    withSQL,
		fromSQL:    fromSQL,
		whereSQL:   where,
		args:       args,
		hasClauses: parsed.hasClauses(),
		hasRank:    match != "",
	}
}

func (p bookSearchPlan) argsWith(extra ...any) []any {
	args := make([]any, 0, len(p.args)+len(extra))
	args = append(args, p.args...)
	return append(args, extra...)
}

// searchQueryScope applies the semantic access policy to a persisted query.
func searchQueryScope(rawQuery string) (string, string) {
	parsed, err := parseSearchQuery(rawQuery, false)
	if err != nil {
		return "", queryScopeShelfReason
	}
	return parsed.accessScope()
}
