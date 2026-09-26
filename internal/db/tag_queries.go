package db

import (
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

// Quoted path and direct-membership selectors follow renames. Word searches keep
// their textual meaning. Query and authorization projection change atomically.
func renameSavedTagQueries(tx *Tx, kind TagKind, names map[string]string) error {
	rows, err := tx.Query("SELECT id, query FROM shelves WHERE kind = 'query'")
	if err != nil {
		return err
	}
	type change struct {
		id           int64
		query, match string
	}
	var changes []change
	for rows.Next() {
		var id int64
		var query string
		if err := rows.Scan(&id, &query); err != nil {
			rows.Close()
			return err
		}
		next, changed := renameTagQuery(query, kind, names)
		if changed {
			validation := ValidateSearchQuery(next)
			changes = append(changes, change{id, next, validation.scopeMatch})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := tx.Exec("UPDATE shelves SET query = ?, query_match = ?, updated_at = unixepoch() WHERE id = ?", c.query, c.match, c.id); err != nil {
			return err
		}
	}
	return nil
}

func renameTagQuery(query string, kind TagKind, names map[string]string) (string, bool) {
	parsed, err := parseSearchQuery(query, false)
	if err != nil {
		return query, false
	}
	runes := []rune(strings.TrimSpace(query))
	var out strings.Builder
	last := 0
	branchField, exactField := searchTagBranch, searchExactTags
	if kind == TagKindGenre {
		branchField, exactField = searchGenreBranch, searchExactGenres
	}
	for _, term := range parsed.terms {
		if term.field != branchField && term.field != exactField {
			continue
		}
		name, found := names[bookmeta.TagKey(term.value)]
		if !found {
			continue
		}
		out.WriteString(string(runes[last:term.start]))
		if term.field == exactField {
			out.WriteString(string(kind) + `:="` + strings.ReplaceAll(name, `"`, `""`) + `"`)
		} else {
			out.WriteString(QueryTerm(string(kind), name))
		}
		last = term.end
	}
	if last == 0 {
		return query, false
	}
	out.WriteString(string(runes[last:]))
	return out.String(), true
}
