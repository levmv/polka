package db

import (
	"maps"
	"slices"
)

// BookListDependencies describes edits that may change a list's membership or
// order. Clients can patch other edits in place without interpreting queries.
// These are metadata field names, with separate cover, assets, author_sort,
// reading_status, added_at, and shelves changes. Nil means refresh on any edit.
// The wire names are shared with CatalogField in frontend/src/catalog-events.ts.
func BookListDependencies(scope VisibilityScope, query string, sort BookSort, manualShelf bool) []string {
	fields := make(map[string]bool)
	add := func(names ...string) {
		for _, name := range names {
			fields[name] = true
		}
	}
	// Filename search also depends on metadata used by the storage template.
	searchable := []string{
		"title", "sort_title", "authors", "author_sort", "series", "series_index",
		"tags", "description", "language", "publisher", "date", "identifiers", "assets",
	}
	if !scope.IsFull() {
		// A reader's access may come from another saved search or manual shelf.
		// Access queries use only FTS terms: no:/status: filters cannot grant
		// access (see VisibilityScope.BookWhere).
		add(searchable...)
		add("shelves")
	}
	if manualShelf {
		query = ""
		add("shelves")
		if sort != SortTitle && sort != SortAuthor && sort != SortYear && sort != SortSeries {
			sort = SortAdded
		}
	}
	parsed, _ := parseSearchQuery(query, true)
	for _, term := range parsed.terms {
		switch term.field {
		case searchTitle:
			add("title")
		case searchAuthors:
			add("authors")
		case searchSeries:
			add("series")
		case searchTags, searchExactTags:
			add("tags")
		case searchEverywhere:
			add(searchable...)
		default:
			return nil
		}
	}
	for _, filter := range parsed.filters {
		switch filter.kind {
		case searchMissingCover:
			add("cover")
		case searchMissingTags:
			add("tags")
		case searchMissingDescription:
			add("description")
		case searchMissingAuthor:
			add("authors")
		case searchMissingSeries:
			add("series")
		case searchReadingStatus:
			add("reading_status")
		default:
			return nil
		}
	}
	switch sort {
	case SortTitle:
		add("sort_title", "title")
	case SortAuthor:
		add("author_sort", "sort_title", "title")
	case SortYear:
		add("date", "added_at")
	case SortSeries:
		add("series", "series_index", "title")
	case SortRelevance:
		if len(parsed.terms) > 0 {
			// BM25 uses document length across indexed columns, including columns
			// outside a qualified term. Their edits can change relevance order.
			add(searchable...)
		} else {
			add("added_at")
		}
	case SortAdded, "":
		add("added_at")
	default:
		return nil
	}
	return slices.Sorted(maps.Keys(fields))
}
