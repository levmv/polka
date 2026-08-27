package bookmeta

import "strings"

// Tags are stored in the works.tags column as a single comma-list string (import
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
// Both inputs are normalized like ParseTagList (values may themselves contain
// commas, so a raw dialog input is accepted as-is). Matching is case-insensitive.
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
		return normalizeTagValues(values)
	case TagRemove:
		drop := make(map[string]struct{})
		for _, v := range normalizeTagValues(values) {
			drop[strings.ToLower(v)] = struct{}{}
		}
		var out []string
		for _, t := range normalizeTagValues(current) {
			if _, ok := drop[strings.ToLower(t)]; ok {
				continue
			}
			out = append(out, t)
		}
		return out
	case TagAdd:
		out := normalizeTagValues(current)
		have := make(map[string]struct{}, len(out))
		for _, t := range out {
			have[strings.ToLower(t)] = struct{}{}
		}
		for _, v := range normalizeTagValues(values) {
			key := strings.ToLower(v)
			if _, ok := have[key]; ok {
				continue
			}
			have[key] = struct{}{}
			out = append(out, v)
		}
		return out
	default:
		return normalizeTagValues(current)
	}
}

// normalizeTagValues turns a slice of raw tag strings (each possibly a comma
// list) into a clean tag list, reusing the stored-string parser.
func normalizeTagValues(values []string) []string {
	return ParseTagList(strings.Join(values, ","))
}
