package db

import (
	"slices"
	"testing"
)

func TestBookListDependencies(t *testing.T) {
	tests := []struct {
		name, query    string
		sort           BookSort
		manual         bool
		refresh, patch []string
	}{
		{"recent library", "", SortAdded, false, []string{"added_at"}, []string{"title", "authors", "tags", "cover", "reading_status"}},
		{"series numbering", `series:"The Series"`, SortSeries, false, []string{"series", "series_index", "title"}, []string{"sort_title", "tags", "cover"}},
		{"manual series shelf", "", SortSeries, true, []string{"shelves", "series", "series_index", "title"}, []string{"tags", "cover"}},
		{"missing covers", "no:cover", SortTitle, false, []string{"cover", "title", "sort_title"}, []string{"tags", "reading_status"}},
		{"reading shelf", "status:reading", SortAdded, false, []string{"reading_status"}, []string{"title", "tags"}},
		{"qualified relevance", `author:"Some Author"`, SortRelevance, false, []string{"authors", "tags", "description"}, []string{"cover", "reading_status"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := BookListDependencies(FullVisibilityScope(), tt.query, tt.sort, tt.manual)
			for _, field := range tt.refresh {
				if !slices.Contains(fields, field) {
					t.Errorf("%s must refresh %v", field, fields)
				}
			}
			for _, field := range tt.patch {
				if slices.Contains(fields, field) {
					t.Errorf("%s should patch in place: %v", field, fields)
				}
			}
		})
	}
	scope := VisibilityScope{UserID: 1, ContentScope: ContentScopeShelves}
	if fields := BookListDependencies(scope, "", SortAdded, false); !slices.Contains(fields, "tags") || !slices.Contains(fields, "shelves") {
		t.Errorf("access grants missing: %v", fields)
	}
}
