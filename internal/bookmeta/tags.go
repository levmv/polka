package bookmeta

import (
	"slices"
	"strings"
)

// Tags are stored in the books.tags column as a single comma-list string (import
// writes strings.Join(tags, ", ")). These helpers are the pure, tested transform
// core shared by single-book edit and bulk edit; matching is case-insensitive and
// the first-seen spelling wins, mirroring how db.ListTags and the table view read
// the column.

// TagMode is a bulk tag transform selected in the bulk Tags dialog.
type TagMode string

const (
	TagAdd     TagMode = "add"
	TagRemove  TagMode = "remove"
	TagReplace TagMode = "replace"
	TagClear   TagMode = "clear"
)

// ParseTagList splits a stored comma-list tags string into trimmed, non-empty
// tags. Order is preserved and duplicates are dropped case-insensitively, keeping
// the first spelling.
func ParseTagList(s string) []string {
	var tags []string
	seen := make(map[string]struct{})
	for part := range strings.SplitSeq(s, ",") {
		t := strings.TrimSpace(part)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tags = append(tags, t)
	}
	return tags
}

// FormatTagList joins tags back into the canonical stored form: ", "-separated,
// matching the import writer.
func FormatTagList(tags []string) string {
	return strings.Join(tags, ", ")
}

// ApplyTagMode returns the tag list after applying mode with values to current.
// Both inputs must contain trimmed, non-empty tags without case-insensitive
// duplicates. The result does not share its backing array with either input.
// Matching is case-insensitive.
//
//   - add: append each value not already present, keeping existing order/casing
//     and the value's typed casing for genuinely new tags.
//   - remove: drop every tag matching a value.
//   - replace: set exactly to the provided values.
//   - clear: empty.
func ApplyTagMode(current []string, mode TagMode, values []string) []string {
	switch mode {
	case TagClear:
		return nil
	case TagReplace:
		return slices.Clone(values)
	case TagRemove:
		drop := make(map[string]struct{})
		for _, v := range values {
			drop[strings.ToLower(v)] = struct{}{}
		}
		var out []string
		for _, t := range current {
			if _, ok := drop[strings.ToLower(t)]; ok {
				continue
			}
			out = append(out, t)
		}
		return out
	case TagAdd:
		out := slices.Clone(current)
		have := make(map[string]struct{}, len(out))
		for _, t := range out {
			have[strings.ToLower(t)] = struct{}{}
		}
		for _, v := range values {
			key := strings.ToLower(v)
			if _, ok := have[key]; ok {
				continue
			}
			have[key] = struct{}{}
			out = append(out, v)
		}
		return out
	default:
		return slices.Clone(current)
	}
}
