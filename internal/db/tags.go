package db

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

var ErrTagNotFound = errors.New("tag not found")

type TagKind string

const (
	TagKindTag   TagKind = "tag"
	TagKindGenre TagKind = "genre"
)

// BookTags holds the two independently ordered parts of the shared dictionary.
type BookTags struct {
	Genres []string
	Tags   []string
}

type TagRow struct {
	ID        int64
	Name      string
	Key       string
	BookCount int
}

// TagsByBookIDs loads ordered, canonical names in bounded batches, including Trash.
func TagsByBookIDs(queryer Queryer, bookIDs []int64) (map[int64]BookTags, error) {
	out := make(map[int64]BookTags, len(bookIDs))
	for batch := range slices.Chunk(DedupBookIDs(bookIDs), 500) {
		placeholders, args := idPlaceholders(batch)
		rows, err := queryer.Query(`SELECT bt.book_id, t.kind, t.name
 FROM book_tags bt JOIN tags t ON t.id = bt.tag_id
 WHERE bt.book_id IN (`+placeholders+`) ORDER BY bt.book_id, bt.position`, args...)
		if err != nil {
			return nil, fmt.Errorf("load book tags: %w", err)
		}
		for rows.Next() {
			var id int64
			var name string
			var kind TagKind
			if err := rows.Scan(&id, &kind, &name); err != nil {
				rows.Close()
				return nil, err
			}
			values := out[id]
			if kind == TagKindGenre {
				values.Genres = append(values.Genres, name)
			} else {
				values.Tags = append(values.Tags, name)
			}
			out[id] = values
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SetBookTags replaces memberships of one kind, retaining the other kind
// and existing dictionary spelling.
// Names must be trimmed, non-empty, comma-free, and unique by bookmeta.TagKey.
// Callers replacing existing links clean up orphans once per operation or batch.
func SetBookTags(tx *Tx, bookID int64, kind TagKind, names []string) error {
	if _, err := tx.Exec("DELETE FROM book_tags WHERE book_id = ? AND EXISTS (SELECT 1 FROM tags t WHERE t.id = book_tags.tag_id AND t.kind = ?)", bookID, kind); err != nil {
		return err
	}
	for position, name := range names {
		key := bookmeta.TagKey(name)
		var id int64
		err := tx.QueryRow("SELECT id FROM tags WHERE kind = ? AND name_key = ?", kind, key).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRow("INSERT INTO tags (kind, name, name_key) VALUES (?, ?, ?) RETURNING id", kind, name, key).Scan(&id)
		}
		if err != nil {
			return fmt.Errorf("resolve tag: %w", err)
		}
		if _, err := tx.Exec("INSERT INTO book_tags (book_id, tag_id, position) VALUES (?, ?, ?)", bookID, id, position); err != nil {
			return fmt.Errorf("link tag: %w", err)
		}
	}
	return nil
}

func DeleteOrphanTags(execer Execer) error {
	_, err := execer.Exec("DELETE FROM tags WHERE NOT EXISTS (SELECT 1 FROM book_tags bt WHERE bt.tag_id = tags.id)")
	return err
}

// ListTags returns names used by visible live books for suggestions and OPDS.
func ListTags(queryer Queryer, scope VisibilityScope, kind TagKind, q string, limit int) ([]string, error) {
	rows, err := listTagRows(queryer, scope, kind, q, "", limit, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Name)
	}
	return out, nil
}

func ListTagCountsPage(queryer Queryer, scope VisibilityScope, kind TagKind, q, after string, limit int) ([]TagRow, error) {
	return listTagRows(queryer, scope, kind, q, after, limit, true)
}

func listTagRows(queryer Queryer, scope VisibilityScope, kind TagKind, q, after string, limit int, counts bool) ([]TagRow, error) {
	var withSQL string
	var args []any
	from := "tags t"
	where := "t.kind = ? AND t.name_key > ?"
	count := "0"
	if scope.IsFull() {
		liveLinks := `FROM book_tags bt JOIN books b ON b.id = bt.book_id
   WHERE bt.tag_id = t.id AND b.deleted_at IS NULL`
		where += " AND EXISTS (SELECT 1 " + liveLinks + ")"
		if counts {
			count = "(SELECT COUNT(*) " + liveLinks + ")"
		}
	} else {
		// Start with visible books so a narrow grant never scans every tag's books.
		withSQL = scope.visibleBooksCTE() + `, tag_counts AS (
   SELECT bt.tag_id, COUNT(*) AS book_count FROM books b
   JOIN book_tags bt ON bt.book_id = b.id
   WHERE b.id IN (SELECT book_id FROM visible_scope) AND b.deleted_at IS NULL
   GROUP BY bt.tag_id)`
		from += " JOIN tag_counts c ON c.tag_id = t.id"
		count = "c.book_count"
		args = append(args, scope.UserID)
	}
	args = append(args, kind, after)
	if key := bookmeta.TagKey(q); key != "" {
		where += ` AND t.name_key LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(key)+"%")
	}
	query := withClause(withSQL) + "SELECT t.id, t.name, t.name_key, " + count + " FROM " + from + " WHERE " + where + " ORDER BY t.name_key"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	var out []TagRow
	for rows.Next() {
		var tag TagRow
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Key, &tag.BookCount); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// RenameOrMergeTag affects all memberships, including trashed books. Merging
// keeps the first position when a book already has both tags.
func RenameOrMergeTag(tx *Tx, tagID int64, name string) ([]int64, error) {
	var oldName string
	var kind TagKind
	if err := tx.QueryRow("SELECT kind, name FROM tags WHERE id = ?", tagID).Scan(&kind, &oldName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTagNotFound
		}
		return nil, err
	}
	if oldName == name {
		return nil, nil
	}
	ids, err := bookIDsForTag(tx, tagID)
	if err != nil {
		return nil, err
	}
	var targetID int64
	var targetName string
	err = tx.QueryRow("SELECT id, name FROM tags WHERE kind = ? AND name_key = ?", kind, bookmeta.TagKey(name)).Scan(&targetID, &targetName)
	switch {
	case errors.Is(err, sql.ErrNoRows), err == nil && targetID == tagID:
		_, err = tx.Exec("UPDATE tags SET name = ?, name_key = ? WHERE id = ?", name, bookmeta.TagKey(name), tagID)
		targetName = name
	case err != nil:
		return nil, err
	default:
		_, err = tx.Exec(`INSERT INTO book_tags (book_id, tag_id, position)
   SELECT book_id, ?, position FROM book_tags WHERE tag_id = ?
   ON CONFLICT (book_id, tag_id) DO UPDATE SET position = MIN(position, excluded.position)`, targetID, tagID)
		if err == nil {
			_, err = tx.Exec("DELETE FROM book_tags WHERE tag_id = ?", tagID)
		}
		if err == nil {
			_, err = tx.Exec("DELETE FROM tags WHERE id = ?", tagID)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := renameSavedTagQueries(tx, kind, oldName, targetName); err != nil {
		return nil, err
	}
	return ids, nil
}

func DeleteTag(tx *Tx, tagID int64) ([]int64, error) {
	var exists int
	if err := tx.QueryRow("SELECT 1 FROM tags WHERE id = ?", tagID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTagNotFound
		}
		return nil, err
	}
	ids, err := bookIDsForTag(tx, tagID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM book_tags WHERE tag_id = ?", tagID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM tags WHERE id = ?", tagID); err != nil {
		return nil, err
	}
	return ids, nil
}

func bookIDsForTag(queryer Queryer, tagID int64) ([]int64, error) {
	rows, err := queryer.Query("SELECT book_id FROM book_tags WHERE tag_id = ?", tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// escapeLike escapes literal substring searches.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
