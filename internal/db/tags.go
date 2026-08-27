package db

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// ListTags returns distinct comma-list tags from works, optionally filtered by a
// case-insensitive substring. Tags are stored as text today, so this keeps the
// read model lightweight without pretending tags are a separate entity.
func ListTags(queryer Queryer, scope VisibilityScope, q string, limit int) ([]string, error) {
	filter := strings.ToLower(strings.TrimSpace(q))
	where := `w.deleted_at IS NULL
		  AND w.tags IS NOT NULL
		  AND trim(w.tags) <> ''`
	var args []any
	if filter != "" && isASCII(filter) {
		where += ` AND w.tags LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(filter)+"%")
	}
	where, args = scope.AppendWorkWhere(where, "w.id", args...)
	query := `
		SELECT w.tags FROM works w
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

	tags := make([]string, 0, len(seen))
	for _, tag := range seen {
		tags = append(tags, tag)
	}
	slices.SortFunc(tags, func(a, b string) int {
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	if limit > 0 && len(tags) > limit {
		tags = tags[:limit]
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
