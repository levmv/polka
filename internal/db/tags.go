package db

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
)

// ListTags returns visible tags sorted and deduplicated case-insensitively,
// optionally filtered by a substring. A nonpositive limit returns all matches.
func ListTags(queryer Queryer, scope VisibilityScope, q string, limit int) ([]string, error) {
	filter := strings.ToLower(strings.TrimSpace(q))
	where := `b.deleted_at IS NULL
		  AND b.tags IS NOT NULL
		  AND trim(b.tags) <> ''`
	var withSQL string
	var args []any
	if !scope.IsFull() {
		withSQL = scope.visibleBooksCTE()
		// IN drives primary-key lookups of visible books; a join may scan the catalog.
		where += ` AND b.id IN (SELECT book_id FROM visible_scope)`
		args = append(args, scope.UserID)
	}
	if filter != "" && isASCII(filter) {
		// LIKE folds only ASCII. Different byte/character lengths let non-ASCII
		// tags reach Go's Unicode-aware filter, including İ/i and K/k matches.
		where += ` AND (b.tags LIKE ? ESCAPE '\'
			OR length(CAST(b.tags AS BLOB)) <> length(b.tags))`
		args = append(args, "%"+escapeLike(filter)+"%")
	}
	query := withClause(withSQL) + `
		SELECT b.tags FROM books b
		WHERE ` + where + `
	`

	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tags query: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]string)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("list tags scan: %w", err)
		}
		for part := range strings.SplitSeq(raw, ",") {
			tag := strings.TrimSpace(part)
			if tag == "" {
				continue
			}
			key := strings.ToLower(tag)
			if filter != "" && !strings.Contains(key, filter) {
				continue
			}
			if _, ok := seen[key]; !ok {
				seen[key] = tag
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tags rows: %w", err)
	}

	tags := slices.Sorted(maps.Keys(seen))
	if limit > 0 && len(tags) > limit {
		tags = tags[:limit]
	}
	for i, key := range tags {
		tags[i] = seen[key]
	}
	return tags, nil
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// escapeLike escapes the LIKE wildcards so a user's substring is matched
// literally (paired with `ESCAPE '\'`).
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
