package db

import (
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

// Exact tag selectors follow the shared tag's name. Word/prefix searches keep
// their textual meaning. Query and authorization projection change atomically.
func renameSavedTagQueries(tx *Tx, kind TagKind, oldName, newName string) error {
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
		next, changed := renameExactTagQuery(query, kind, oldName, newName)
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

func renameExactTagQuery(query string, kind TagKind, oldName, newName string) (string, bool) {
	parsed, err := parseSearchQuery(query, false)
	if err != nil {
		return query, false
	}
	runes := []rune(strings.TrimSpace(query))
	var out strings.Builder
	last := 0
	field := searchExactTags
	if kind == TagKindGenre {
		field = searchExactGenres
	}
	for _, term := range parsed.terms {
		if term.field != field || bookmeta.TagKey(term.value) != bookmeta.TagKey(oldName) {
			continue
		}
		out.WriteString(string(runes[last:term.start]))
		out.WriteString(QueryTerm(string(kind), newName))
		last = term.end
	}
	if last == 0 {
		return query, false
	}
	out.WriteString(string(runes[last:]))
	return out.String(), true
}
