package bookmeta

import (
	"slices"
	"strings"
)

// TagKey defines tag identity for catalog storage, editing, and exact search.
func TagKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// EqualTags compares ordered memberships; ordinary book edits cannot change
// the spelling of an existing shared tag.
func EqualTags(a, b []string) bool {
	return slices.EqualFunc(a, b, func(a, b string) bool { return TagKey(a) == TagKey(b) })
}

// TagMode selects how a bulk edit changes a genre or tag list.
type TagMode string

const (
	TagAdd     TagMode = "add"
	TagRemove  TagMode = "remove"
	TagReplace TagMode = "replace"
	TagClear   TagMode = "clear"
)

// ParseTagList splits comma-separated input into trimmed, non-empty
// tags. Order is preserved and duplicates are dropped case-insensitively, keeping
// the first spelling.
func ParseTagList(s string) []string {
	return NormalizeTags([]string{s})
}

// NormalizeTags trims, splits comma-separated input, and deduplicates names.
func NormalizeTags(values []string) []string {
	var tags []string
	seen := make(map[string]struct{})
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			t := strings.TrimSpace(part)
			if t == "" {
				continue
			}
			key := TagKey(t)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			tags = append(tags, t)
		}
	}
	return tags
}

// FormatTagList formats tags for the comma-separated text editor and API.
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
			drop[TagKey(v)] = struct{}{}
		}
		var out []string
		for _, t := range current {
			if _, ok := drop[TagKey(t)]; ok {
				continue
			}
			out = append(out, t)
		}
		return out
	case TagAdd:
		out := slices.Clone(current)
		have := make(map[string]struct{}, len(out))
		for _, t := range out {
			have[TagKey(t)] = struct{}{}
		}
		for _, v := range values {
			key := TagKey(v)
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
