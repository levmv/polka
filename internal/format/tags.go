package format

import "strings"

type (
	tagSeparatorFunc func(rune) bool
	tagCleanFunc     func(string) string
)

func splitTagFields(value string, isSeparator tagSeparatorFunc, clean tagCleanFunc) []string {
	fields := strings.FieldsFunc(value, isSeparator)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if tag := clean(field); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

func uniqueTagList(values []string, isSeparator tagSeparatorFunc, clean tagCleanFunc) []string {
	var tags []string
	seen := make(map[string]bool)
	for _, value := range values {
		for _, tag := range splitTagFields(value, isSeparator, clean) {
			key := strings.ToLower(tag)
			if seen[key] {
				continue
			}
			seen[key] = true
			tags = append(tags, tag)
		}
	}
	return tags
}

func commaSemicolonNewlineTabSeparator(r rune) bool {
	return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
}

func semicolonNewlineTabSeparator(r rune) bool {
	return r == ';' || r == '\n' || r == '\r' || r == '\t'
}

func commaSemicolonNewlineSeparator(r rune) bool {
	return r == ',' || r == ';' || r == '\n' || r == '\r'
}
