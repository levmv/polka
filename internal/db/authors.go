package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/levmv/polka/internal/bookmeta"
)

var ErrAuthorNotFound = errors.New("no author named")

// RenameOrMergeAuthor renames an author or merges its links into an existing
// author with the exact newName, preserving the target's sort_name and avoiding
// duplicate book links. It returns the affected book IDs; the caller handles
// metadata revisions, search indexing, and relayout.
func RenameOrMergeAuthor(tx *Tx, oldName, newName, newSortName string) ([]int64, error) {
	var oldID int64
	err := tx.QueryRow("SELECT id FROM authors WHERE name = ?", oldName).Scan(&oldID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w %q", ErrAuthorNotFound, oldName)
	} else if err != nil {
		return nil, fmt.Errorf("find author: %w", err)
	}

	if oldName == newName {
		return nil, nil
	}

	affected, err := bookIDsForAuthor(tx, oldID)
	if err != nil {
		return nil, err
	}

	var targetID int64
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

	// Merge: drop old links on books that already credit the target, repoint the
	// rest, then delete the orphaned source author.
	if _, err := tx.Exec(`
		DELETE FROM book_authors
		WHERE author_id = ?
		  AND book_id IN (SELECT book_id FROM book_authors WHERE author_id = ?)
	`, oldID, targetID); err != nil {
		return nil, fmt.Errorf("merge dedup links: %w", err)
	}
	if _, err := tx.Exec("UPDATE book_authors SET author_id = ? WHERE author_id = ?", targetID, oldID); err != nil {
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
// key) without touching the display name. Returns the book IDs the author is
// linked to; the caller owns metadata_rev/search/relayout bookkeeping.
// sort_name selects both the bucket and the author folder, so a change moves
// files for books where the author is primary.
func SetAuthorSortName(tx *Tx, name, sortName string) ([]int64, error) {
	var id int64
	var existingSortName string
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
	affected, err := bookIDsForAuthor(tx, id)
	if err != nil {
		return nil, err
	}
	if err := updatePrimaryAuthorSorts(tx, affected); err != nil {
		return nil, err
	}
	return affected, nil
}

func bookIDsForAuthor(tx *Tx, authorID int64) ([]int64, error) {
	rows, err := tx.Query("SELECT book_id FROM book_authors WHERE author_id = ?", authorID)
	if err != nil {
		return nil, fmt.Errorf("books for author: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("books for author scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpsertBookAuthors replaces a book's ordered author links, keeping the first
// occurrence of each exact name. It returns the primary author's name and sort
// name, or empty strings when the list is empty.
//
// Input authors must have SortName filled. Existing authors retain their stored
// sort_name so import and edit honor the same storage path overrides.
// Callers replacing existing links should follow with DeleteOrphanAuthors.
func UpsertBookAuthors(tx *Tx, bookID int64, authors []bookmeta.AuthorMeta) (primaryName, primarySortName string, err error) {
	authors = dedupAuthorsByName(authors)

	if _, err := tx.Exec("DELETE FROM book_authors WHERE book_id = ?", bookID); err != nil {
		return "", "", fmt.Errorf("clear book_authors: %w", err)
	}

	for i, a := range authors {
		var authorID int64
		var existingSort string
		err := tx.QueryRow("SELECT id, sort_name FROM authors WHERE name = ?", a.Name).Scan(&authorID, &existingSort)
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.QueryRow("INSERT INTO authors (name, sort_name) VALUES (?, ?) RETURNING id", a.Name, a.SortName).Scan(&authorID); err != nil {
				return "", "", fmt.Errorf("insert author: %w", err)
			}
		} else if err != nil {
			return "", "", fmt.Errorf("query author: %w", err)
		} else {
			a.SortName = existingSort
		}

		if _, err := tx.Exec("INSERT INTO book_authors (book_id, author_id, role, author_order) VALUES (?, ?, ?, ?)", bookID, authorID, a.Role, i); err != nil {
			return "", "", fmt.Errorf("insert book_author: %w", err)
		}
		if i == 0 {
			primaryName = a.Name
			primarySortName = a.SortName
		}
	}
	if _, err := tx.Exec("UPDATE books SET primary_author_sort = ? WHERE id = ?", primarySortName, bookID); err != nil {
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

// updatePrimaryAuthorSorts refreshes books.primary_author_sort from the
// authoritative ordered author links. It intentionally does not touch
// books.updated_at: this is an internal denormalized projection, not a user
// metadata edit.
func updatePrimaryAuthorSorts(tx *Tx, bookIDs []int64) error {
	if len(bookIDs) == 0 {
		return nil
	}
	placeholders, args := idPlaceholders(bookIDs)

	_, err := tx.Exec(`
		UPDATE books
		SET primary_author_sort = COALESCE((
			SELECT a.sort_name
			FROM book_authors ba
			JOIN authors a ON a.id = ba.author_id
			WHERE ba.book_id = books.id
			ORDER BY ba.author_order ASC, ba.rowid ASC
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
// book_authors (e.g. left behind when a book is re-linked to a different author
// spelling). Returns the number deleted; safe to run repeatedly.
func DeleteOrphanAuthors(execer Execer) (int64, error) {
	res, err := execer.Exec(`
		DELETE FROM authors
		WHERE NOT EXISTS (SELECT 1 FROM book_authors WHERE book_authors.author_id = authors.id)
	`)
	if err != nil {
		return 0, fmt.Errorf("delete orphan authors: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

type AuthorRow struct {
	BookID   int64
	Name     string
	SortName string
	Role     string
	Order    int
}

// AuthorsByBookIDs returns each book's authors in display order, keyed by book ID.
func AuthorsByBookIDs(queryer Queryer, bookIDs []int64) (map[int64][]AuthorRow, error) {
	if len(bookIDs) == 0 {
		return map[int64][]AuthorRow{}, nil
	}

	placeholders, args := idPlaceholders(bookIDs)

	rows, err := queryer.Query(`
		SELECT ba.book_id, a.name, a.sort_name, ba.role, ba.author_order
		FROM book_authors ba
		JOIN authors a ON ba.author_id = a.id
		WHERE ba.book_id IN (`+placeholders+`)
		ORDER BY ba.book_id, ba.author_order ASC, ba.rowid ASC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("authors by books query: %w", err)
	}
	defer rows.Close()

	byBook := make(map[int64][]AuthorRow)
	for rows.Next() {
		var a AuthorRow
		var role sql.NullString
		if err := rows.Scan(&a.BookID, &a.Name, &a.SortName, &role, &a.Order); err != nil {
			return nil, fmt.Errorf("authors by books scan: %w", err)
		}
		a.Role = role.String
		byBook[a.BookID] = append(byBook[a.BookID], a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authors by books rows: %w", err)
	}
	return byBook, nil
}

type AuthorNameRow struct {
	Name     string
	SortName string
}

// ListAuthorNames returns distinct authors that are referenced by at least one
// book (i.e. not orphans), optionally filtered by a case-insensitive substring,
// ordered by sort_name. Used for edit autocomplete so a re-spelling can reuse an
// existing author instead of spawning a duplicate.
func ListAuthorNames(queryer Queryer, scope VisibilityScope, q string, limit int) ([]AuthorNameRow, error) {
	query := `
		SELECT a.name, a.sort_name FROM authors a
		WHERE EXISTS (
			SELECT 1
			FROM book_authors ba
			JOIN books b ON b.id = ba.book_id
			WHERE ba.author_id = a.id
			  AND b.deleted_at IS NULL`
	var args []any
	scopeWhere, scopeArgs := scope.BookWhere("b.id")
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

	var authors []AuthorNameRow
	for rows.Next() {
		var a AuthorNameRow
		if err := rows.Scan(&a.Name, &a.SortName); err != nil {
			return nil, fmt.Errorf("list author names scan: %w", err)
		}
		authors = append(authors, a)
	}
	return authors, rows.Err()
}

// AuthorCount is an author plus how many books reference it. Used by the
// manage-authors screen.
type AuthorCount struct {
	Name      string
	SortName  string
	BookCount int
}

// ListAuthorCountsPage returns a keyset page ordered by sort_name and exact name.
func ListAuthorCountsPage(queryer Queryer, scope VisibilityScope, afterSortName, afterName string, limit int) ([]AuthorCount, error) {
	scopeWhere, scopeArgs := scope.BookWhere("b.id")
	where := "b.deleted_at IS NULL"
	args := scopeArgs
	if scopeWhere != "1 = 1" {
		where += " AND " + scopeWhere
	}
	if afterName != "" {
		where += ` AND (
			a.sort_name COLLATE NOCASE > ? COLLATE NOCASE OR
			(a.sort_name COLLATE NOCASE = ? COLLATE NOCASE AND a.name > ?)
		)`
		args = append(args, afterSortName, afterSortName, afterName)
	}
	args = append(args, limit)
	rows, err := queryer.Query(`
		SELECT a.name, a.sort_name, COUNT(ba.book_id) AS book_count
		FROM authors a
		JOIN book_authors ba ON ba.author_id = a.id
		JOIN books b ON b.id = ba.book_id
		WHERE `+where+`
		GROUP BY a.id
		ORDER BY a.sort_name COLLATE NOCASE ASC, a.name ASC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list authors with counts query: %w", err)
	}
	defer rows.Close()

	var authors []AuthorCount
	for rows.Next() {
		var a AuthorCount
		if err := rows.Scan(&a.Name, &a.SortName, &a.BookCount); err != nil {
			return nil, fmt.Errorf("list authors with counts scan: %w", err)
		}
		authors = append(authors, a)
	}
	return authors, rows.Err()
}

// GetAuthorInfo returns one author's sort_name and the number of books crediting
// it, looked up by exact name, plus whether such an author exists. Author identity
// is the exact name string (matching the import/edit reuse-by-name rule), so the
// lookup is exact-match, not fuzzy. Powers the book-edit convergence prompt, which
// asks how many *other* books still credit a just-renamed author.
func GetAuthorInfo(queryer Queryer, scope VisibilityScope, name string) (AuthorCount, bool, error) {
	where, args := scope.AppendBookWhere("a.name = ? AND b.deleted_at IS NULL", "b.id", name)
	var a AuthorCount
	err := queryer.QueryRow(`
		SELECT a.name, a.sort_name, COUNT(ba.book_id)
		FROM authors a
		JOIN book_authors ba ON ba.author_id = a.id
		JOIN books b ON b.id = ba.book_id
		WHERE `+where+`
		GROUP BY a.id
	`, args...).Scan(&a.Name, &a.SortName, &a.BookCount)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorCount{}, false, nil
	}
	if err != nil {
		return AuthorCount{}, false, fmt.Errorf("get scoped author info: %w", err)
	}
	return a, true, nil
}

// PrimaryAuthor returns empty names when a book has no authors.
func PrimaryAuthor(queryer Queryer, bookID int64) (string, string, error) {
	var name, sortName string
	err := queryer.QueryRow(`
		SELECT a.name, a.sort_name
		FROM book_authors ba
		JOIN authors a ON ba.author_id = a.id
		WHERE ba.book_id = ?
		ORDER BY ba.author_order ASC, ba.rowid ASC
		LIMIT 1
	`, bookID).Scan(&name, &sortName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return name, sortName, err
}
