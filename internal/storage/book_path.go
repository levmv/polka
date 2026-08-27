package storage

import (
	"strings"
	"unicode"
)

// Book files live under a managed, human-readable layout:
//
//	<bucket>/<author sort>/<title> [<asset id>].<ext>
//
// This file is the source of truth for that layout. Keep the path stable by
// including only durable file identity fields: primary author, title, asset_id,
// and extension. Series, tags, dates, ISBNs, publishers, language, and covers
// are metadata and must not move files on disk.
//
// The books root *is* the books tree, so the returned path is relative to the
// root itself (bucket directories sit directly under it). It is stored in
// assets.storage_path after the file physically exists there, and HTTP handlers
// resolve it from the database at request time rather than caching it.

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
	if author == "" || author == "Unknown" || author == "Unknown Author" {
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
