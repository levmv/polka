package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/id"
)

var ErrAuthorNotFound = errors.New("no author named")

// RenameOrMergeAuthor changes every book credited to oldName so it is credited
// to newName instead, and returns the ids of the affected works. The caller owns
// the catalog bookkeeping (metadata_rev, search index, and relayout) after this
// pure DB mutation.
//
// If no author named newName exists yet it is a pure rename (the oldName row's
// name/sort_name are updated in place). If an author named newName already
// exists it is a merge: oldName's work links are repointed onto the existing row
// (dropping any link that would duplicate one the work already has) and the
// now-orphaned oldName row is deleted. This matches polka's model where an
// author's identity is its exact name string.
func RenameOrMergeAuthor(tx *sql.Tx, oldName, newName, newSortName string) ([]string, error) {
	var oldID string
	err := tx.QueryRow("SELECT id FROM authors WHERE name = ?", oldName).Scan(&oldID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w %q", ErrAuthorNotFound, oldName)
	} else if err != nil {
		return nil, fmt.Errorf("find author: %w", err)
	}

	if oldName == newName {
		return nil, nil
	}

	affected, err := workIDsForAuthor(tx, oldID)
	if err != nil {
		return nil, err
	}

	var targetID string
	err = tx.QueryRow("SELECT id FROM authors WHERE name = ?", newName).Scan(&targetID)
	if errors.Is(err, sql.ErrNoRows) {
		// Pure rename in place.
		if _, err := tx.Exec("UPDATE authors SET name = ?, sort_name = ? WHERE id = ?", newName, newSortName, oldID); err != nil {
			return nil, fmt.Errorf("rename author: %w", err)
		}
		if err := updatePrimaryAuthorSorts(tx, affected); err != nil {
			return nil, err
		}
		return affected, nil
	} else if err != nil {
		return nil, fmt.Errorf("find target author: %w", err)
	}

	if targetID == oldID {
		return nil, nil
	}

	// Merge: drop old links on works that already credit the target, repoint the
	// rest, then delete the orphaned source author.
	if _, err := tx.Exec(`
		DELETE FROM work_authors
		WHERE author_id = ?
		  AND work_id IN (SELECT work_id FROM work_authors WHERE author_id = ?)
	`, oldID, targetID); err != nil {
		return nil, fmt.Errorf("merge dedup links: %w", err)
	}
	if _, err := tx.Exec("UPDATE work_authors SET author_id = ? WHERE author_id = ?", targetID, oldID); err != nil {
		return nil, fmt.Errorf("merge repoint links: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM authors WHERE id = ?", oldID); err != nil {
		return nil, fmt.Errorf("delete merged author: %w", err)
	}
	if err := updatePrimaryAuthorSorts(tx, affected); err != nil {
		return nil, err
	}
	return affected, nil
}

// SetAuthorSortName overrides an author's sort_name (the canonical-path sort
// key) without touching the display name. Returns the work IDs the author is
// linked to; the caller owns metadata_rev/search/relayout bookkeeping.
// sort_name selects both the bucket and the author folder, so a change moves
// files for works where the author is primary.
func SetAuthorSortName(tx *sql.Tx, name, sortName string) ([]string, error) {
	var id, existingSortName string
	err := tx.QueryRow("SELECT id, sort_name FROM authors WHERE name = ?", name).Scan(&id, &existingSortName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w %q", ErrAuthorNotFound, name)
	} else if err != nil {
		return nil, fmt.Errorf("find author: %w", err)
	}
	if sortName == existingSortName {
		return nil, nil
	}
	if _, err := tx.Exec("UPDATE authors SET sort_name = ? WHERE id = ?", sortName, id); err != nil {
		return nil, fmt.Errorf("set sort_name: %w", err)
	}
	affected, err := workIDsForAuthor(tx, id)
	if err != nil {
		return nil, err
	}
	if err := updatePrimaryAuthorSorts(tx, affected); err != nil {
		return nil, err
	}
	return affected, nil
}

func workIDsForAuthor(tx *sql.Tx, authorID string) ([]string, error) {
	rows, err := tx.Query("SELECT work_id FROM work_authors WHERE author_id = ?", authorID)
	if err != nil {
		return nil, fmt.Errorf("works for author: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("works for author scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpsertWorkAuthors sets the ordered author list for a work. It clears any
// existing work_authors links (a no-op for a freshly inserted work), then for
// each author resolves the row by exact name — reusing an existing author and
// adopting its persisted sort_name, or inserting a new row with the supplied
// sort_name — and links it via work_authors in slice order (author_order = i,
// role from the AuthorMeta). It returns the primary (author_order 0) author's
// resolved name and sort_name.
//
// Adopting the existing row's sort_name is load-bearing: the canonical storage
// path buckets on the primary author's sort_name, so the returned value must
// match the persisted author row or `polka check` reports a divergence. This is
// the single place import and edit share the "find-or-insert author, link work"
// logic, keeping either path from independently recomputing AuthorSort and
// disagreeing with an overridden sort_name.
//
// authors must be non-empty and pre-normalized (SortName filled). It does not
// garbage-collect orphaned authors; a caller re-linking an existing work should
// follow with DeleteOrphanAuthors.
//
// Authors repeating an exact name are collapsed to their first occurrence:
// author identity is the exact name string, so a duplicate would resolve to the
// same author row and collide on the work_authors (work_id, author_id) primary
// key. Real inputs hit this — an EPUB listing one creator twice, or an editor
// typing "Ivanov; Ivanov" — so every caller (edit, bulk, import) relies on this
// silent dedup rather than surfacing a constraint error.
func UpsertWorkAuthors(tx *sql.Tx, workID string, authors []bookmeta.AuthorMeta) (primaryName, primarySortName string, err error) {
	authors = dedupAuthorsByName(authors)

	if _, err := tx.Exec("DELETE FROM work_authors WHERE work_id = ?", workID); err != nil {
		return "", "", fmt.Errorf("clear work_authors: %w", err)
	}

	for i, a := range authors {
		authorID := id.New(id.Author)
		var existingID, existingSort string
		err := tx.QueryRow("SELECT id, sort_name FROM authors WHERE name = ?", a.Name).Scan(&existingID, &existingSort)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.Exec("INSERT INTO authors (id, name, sort_name) VALUES (?, ?, ?)", authorID, a.Name, a.SortName); err != nil {
				return "", "", fmt.Errorf("insert author: %w", err)
			}
		} else if err != nil {
			return "", "", fmt.Errorf("query author: %w", err)
		} else {
			authorID = existingID
			a.SortName = existingSort
		}

		if _, err := tx.Exec("INSERT INTO work_authors (work_id, author_id, role, author_order) VALUES (?, ?, ?, ?)", workID, authorID, a.Role, i); err != nil {
			return "", "", fmt.Errorf("insert work_author: %w", err)
		}
		if i == 0 {
			primaryName = a.Name
			primarySortName = a.SortName
		}
	}
	if _, err := tx.Exec("UPDATE works SET primary_author_sort = ? WHERE id = ?", primarySortName, workID); err != nil {
		return "", "", fmt.Errorf("update primary author sort: %w", err)
	}
	return primaryName, primarySortName, nil
}

// dedupAuthorsByName drops authors that repeat an exact earlier name, preserving
// the first occurrence and the original order (so author_order 0 stays primary).
func dedupAuthorsByName(authors []bookmeta.AuthorMeta) []bookmeta.AuthorMeta {
	seen := make(map[string]struct{}, len(authors))
	out := make([]bookmeta.AuthorMeta, 0, len(authors))
	for _, a := range authors {
		if _, ok := seen[a.Name]; ok {
			continue
		}
		seen[a.Name] = struct{}{}
		out = append(out, a)
	}
	return out
}

// updatePrimaryAuthorSorts refreshes works.primary_author_sort from the
// authoritative ordered author links. It intentionally does not touch
// works.updated_at: this is an internal denormalized projection, not a user
// metadata edit.
func updatePrimaryAuthorSorts(execer Execer, workIDs []string) error {
	if len(workIDs) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(workIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(workIDs))
	for i, id := range workIDs {
		args[i] = id
	}

	_, err := execer.Exec(`
		UPDATE works
		SET primary_author_sort = COALESCE((
			SELECT a.sort_name
			FROM work_authors wa
			JOIN authors a ON a.id = wa.author_id
			WHERE wa.work_id = works.id
			ORDER BY wa.author_order ASC, wa.rowid ASC
			LIMIT 1
		), '')
		WHERE id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return fmt.Errorf("update primary author sort: %w", err)
	}
	return nil
}

// DeleteOrphanAuthors removes authors rows no longer referenced by any
// work_authors (e.g. left behind when a book is re-linked to a different author
// spelling). Returns the number deleted; safe to run repeatedly.
func DeleteOrphanAuthors(execer Execer) (int64, error) {
	res, err := execer.Exec(`
		DELETE FROM authors
		WHERE NOT EXISTS (SELECT 1 FROM work_authors WHERE work_authors.author_id = authors.id)
	`)
	if err != nil {
		return 0, fmt.Errorf("delete orphan authors: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

type AuthorRow struct {
	WorkID   string
	Name     string
	SortName string
	Role     string
	Order    int
}

// AuthorsByWorkIDs returns each work's authors in display order, keyed by work
// id. This is the structured counterpart to the comma-joined `authors` string
// in the list/detail projections — callers build both the authors array and the
// `&`-joined display string from it.
func AuthorsByWorkIDs(queryer Queryer, workIDs []string) (map[string][]AuthorRow, error) {
	if len(workIDs) == 0 {
		return map[string][]AuthorRow{}, nil
	}

	placeholders := strings.Repeat("?,", len(workIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(workIDs))
	for i, id := range workIDs {
		args[i] = id
	}

	rows, err := queryer.Query(`
		SELECT wa.work_id, a.name, a.sort_name, wa.role, wa.author_order
		FROM work_authors wa
		JOIN authors a ON wa.author_id = a.id
		WHERE wa.work_id IN (`+placeholders+`)
		ORDER BY wa.work_id, wa.author_order ASC, wa.rowid ASC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("authors by works query: %w", err)
	}
	defer rows.Close()

	byWork := make(map[string][]AuthorRow)
	for rows.Next() {
		var a AuthorRow
		var role sql.NullString
		if err := rows.Scan(&a.WorkID, &a.Name, &a.SortName, &role, &a.Order); err != nil {
			return nil, fmt.Errorf("authors by works scan: %w", err)
		}
		a.Role = role.String
		byWork[a.WorkID] = append(byWork[a.WorkID], a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authors by works rows: %w", err)
	}
	return byWork, nil
}

// ListAuthorNames returns distinct authors that are referenced by at least one
// work (i.e. not orphans), optionally filtered by a case-insensitive substring,
// ordered by sort_name. Used for edit autocomplete so a re-spelling can reuse an
// existing author instead of spawning a duplicate.
func ListAuthorNames(queryer Queryer, scope VisibilityScope, q string, limit int) ([]AuthorRow, error) {
	query := `
		SELECT a.name, a.sort_name FROM authors a
		WHERE EXISTS (
			SELECT 1
			FROM work_authors wa
			JOIN works w ON w.id = wa.work_id
			WHERE wa.author_id = a.id
			  AND w.deleted_at IS NULL`
	var args []any
	scopeWhere, scopeArgs := scope.WorkWhere("w.id")
	if scopeWhere != "1 = 1" {
		query += ` AND ` + scopeWhere
		args = append(args, scopeArgs...)
	}
	query += `)`
	if q != "" {
		query += ` AND a.name LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(q)+"%")
	}
	query += ` ORDER BY a.sort_name COLLATE NOCASE ASC LIMIT ?`
	args = append(args, limit)

	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list author names query: %w", err)
	}
	defer rows.Close()

	var authors []AuthorRow
	for rows.Next() {
		var a AuthorRow
		if err := rows.Scan(&a.Name, &a.SortName); err != nil {
			return nil, fmt.Errorf("list author names scan: %w", err)
		}
		authors = append(authors, a)
	}
	return authors, rows.Err()
}

// AuthorCount is an author plus how many works reference it. Used by the
// manage-authors screen.
type AuthorCount struct {
	ID        string
	Name      string
	SortName  string
	BookCount int
}

// ListAuthorCountsPage returns one stable keyset page ordered by the
// persisted sort_name and author id. The id is internal cursor state; API rows
// continue exposing only the human-facing name/sort/count fields. limit is the
// positive page size.
func ListAuthorCountsPage(queryer Queryer, scope VisibilityScope, afterSortName, afterID string, limit int) ([]AuthorCount, error) {
	scopeWhere, scopeArgs := scope.WorkWhere("w.id")
	where := "w.deleted_at IS NULL"
	args := scopeArgs
	if scopeWhere != "1 = 1" {
		where += " AND " + scopeWhere
	}
	if afterID != "" {
		where += ` AND (
			a.sort_name COLLATE NOCASE > ? COLLATE NOCASE OR
			(a.sort_name COLLATE NOCASE = ? COLLATE NOCASE AND a.id > ?)
		)`
		args = append(args, afterSortName, afterSortName, afterID)
	}
	args = append(args, limit)
	rows, err := queryer.Query(`
		SELECT a.id, a.name, a.sort_name, COUNT(wa.work_id) AS book_count
		FROM authors a
		JOIN work_authors wa ON wa.author_id = a.id
		JOIN works w ON w.id = wa.work_id
		WHERE `+where+`
		GROUP BY a.id
		ORDER BY a.sort_name COLLATE NOCASE ASC, a.id ASC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list authors with counts query: %w", err)
	}
	defer rows.Close()

	var authors []AuthorCount
	for rows.Next() {
		var a AuthorCount
		if err := rows.Scan(&a.ID, &a.Name, &a.SortName, &a.BookCount); err != nil {
			return nil, fmt.Errorf("list authors with counts scan: %w", err)
		}
		authors = append(authors, a)
	}
	return authors, rows.Err()
}

// GetAuthorInfo returns one author's sort_name and the number of works crediting
// it, looked up by exact name, plus whether such an author exists. Author identity
// is the exact name string (matching the import/edit reuse-by-name rule), so the
// lookup is exact-match, not fuzzy. Powers the book-edit convergence prompt, which
// asks how many *other* books still credit a just-renamed author.
func GetAuthorInfo(queryer Queryer, scope VisibilityScope, name string) (AuthorCount, bool, error) {
	where, args := scope.AppendWorkWhere("a.name = ? AND w.deleted_at IS NULL", "w.id", name)
	var a AuthorCount
	err := queryer.QueryRow(`
		SELECT a.id, a.name, a.sort_name, COUNT(wa.work_id)
		FROM authors a
		JOIN work_authors wa ON wa.author_id = a.id
		JOIN works w ON w.id = wa.work_id
		WHERE `+where+`
		GROUP BY a.id
	`, args...).Scan(&a.ID, &a.Name, &a.SortName, &a.BookCount)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorCount{}, false, nil
	}
	if err != nil {
		return AuthorCount{}, false, fmt.Errorf("get scoped author info: %w", err)
	}
	return a, true, nil
}

func PrimaryAuthor(queryer Queryer, workID string) (string, string, error) {
	var name, sortName string
	err := queryer.QueryRow(`
		SELECT a.name, a.sort_name
		FROM work_authors wa
		JOIN authors a ON wa.author_id = a.id
		WHERE wa.work_id = ?
		ORDER BY wa.author_order ASC, wa.rowid ASC
		LIMIT 1
	`, workID).Scan(&name, &sortName)
	return name, sortName, err
}
