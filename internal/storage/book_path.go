package storage

import (
	"strings"
	"unicode"
)

// The default book layout groups files by author and includes the numeric asset
// ID in each filename. SQLite stores the current path relative to the books
// root; readers resolve it when opening the file.

// sanitizePathSegment removes filesystem-unsafe characters but preserves unicode.
func sanitizePathSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*', '\x00':
		default:
			if unicode.IsControl(r) {
				b.WriteRune(' ')
			} else {
				b.WriteRune(r)
			}
		}
	}
	res := strings.Join(strings.Fields(b.String()), " ")
	// Strip leading dots (and any space they leave) so no rendered segment begins
	// with a dot. RootLooksEmpty ignores dot-entries, so a real title like ".NET
	// Core in Action" as a top-level segment would make a populated library read
	// as empty and trip ErrRootEmpty on live books; a literal ".staging" segment
	// would land books inside the staging area. Rejecting at render time is wrong
	// (real titles start with dots, and import must not fail), so we sanitize
	// here. An all-dots segment (".", "..") trims to empty and hits the caller's
	// "rendered empty" guard.
	return strings.TrimLeft(res, ". ")
}

// authorBucket returns the top-level bucket directory for an author sort key.
func authorBucket(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return "_Unknown"
	}

	r := []rune(author)[0]
	r = unicode.ToUpper(r)

	if r >= 'A' && r <= 'Z' {
		return string(r)
	}
	if (r >= 'А' && r <= 'Я') || r == 'Ё' {
		return string(r)
	}
	if r >= '0' && r <= '9' {
		return "0-9"
	}
	return "_Other"
}

func BookPath(template string, data BookPathData) (string, error) {
	if strings.TrimSpace(template) == "" {
		template = DefaultBookPathTemplate
	}
	return RenderBookPathTemplate(template, data)
}
