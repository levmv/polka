package db

import (
	"fmt"

	"github.com/levmv/polka/internal/bookmeta"
)

type TagRow struct {
	ID          int64
	Name        string
	Key         string
	BookCount   int
	HasChildren bool
}

type TagSort string

const (
	TagSortName  TagSort = "name"
	TagSortBooks TagSort = "books"
)

type TagListOptions struct {
	Kind       TagKind
	Query      string
	ParentID   int64
	Sort       TagSort
	AfterName  string
	AfterCount int
	Limit      int
}

// Keep the singleton IN: it makes SQLite's full-access count substantially
// faster than a correlated equality on large libraries, without recursion.
const directTagBooksSQL = `FROM book_tags bt JOIN books b ON b.id = bt.book_id
 WHERE bt.tag_id IN (t.id) AND b.deleted_at IS NULL`

// CASE keeps leaves out of the recursive query, including unused or Trash-only
// tags. A dictionary branch is expandable only if a descendant has live books.
func liveTagChildrenSQL() string {
	return `CASE WHEN EXISTS (SELECT 1 FROM tags child WHERE child.parent_id = t.id)
 THEN EXISTS (SELECT 1 FROM book_tags bt JOIN books b ON b.id = bt.book_id
   WHERE bt.tag_id IN (` + tagDescendantsSQL("t.id") + `)
   AND bt.tag_id <> t.id AND b.deleted_at IS NULL) ELSE 0 END`
}

// ListTags returns names used by visible live books for suggestions and OPDS.
// It needs only tag identity, so neither counts nor book/ancestor pairs are built.
func ListTags(queryer Queryer, scope VisibilityScope, kind TagKind, q string, limit int) ([]string, error) {
	var withSQL string
	var args []any
	from := "tags t"
	where := "t.kind = ?"
	if scope.IsFull() {
		where += " AND (EXISTS (SELECT 1 " + directTagBooksSQL + ") OR " + liveTagChildrenSQL() + ")"
	} else {
		withSQL = "RECURSIVE " + scope.visibleBooksCTE() + `, visible_tags(id) AS (
   SELECT DISTINCT bt.tag_id FROM books b JOIN book_tags bt ON bt.book_id = b.id
   WHERE b.id IN (SELECT book_id FROM visible_scope) AND b.deleted_at IS NULL
   UNION
   SELECT t.parent_id FROM visible_tags v JOIN tags t ON t.id = v.id WHERE t.parent_id IS NOT NULL
 )`
		from += " JOIN visible_tags v ON v.id = t.id"
		args = append(args, scope.UserID)
	}
	args = append(args, kind)
	if key := bookmeta.TagKey(q); key != "" {
		where += ` AND t.name_key LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(key)+"%")
	}
	query := withClause(withSQL) + "SELECT t.name FROM " + from + " WHERE " + where + " ORDER BY t.name_key"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func ListTagCountsPage(queryer Queryer, scope VisibilityScope, opts TagListOptions) ([]TagRow, error) {
	var withSQL string
	var args []any
	from := "tags t"
	where := "t.kind = ?"
	var count, hasChildren string
	if scope.IsFull() {
		hasChildren = liveTagChildrenSQL()
		where += " AND (EXISTS (SELECT 1 " + directTagBooksSQL + ") OR " + hasChildren + ")"
		count = `CASE WHEN EXISTS (SELECT 1 FROM tags child WHERE child.parent_id = t.id)
 THEN (SELECT COUNT(DISTINCT bt.book_id) FROM book_tags bt JOIN books b ON b.id = bt.book_id
   WHERE bt.tag_id IN (` + tagDescendantsSQL("t.id") + `) AND b.deleted_at IS NULL)
 ELSE (SELECT COUNT(*) ` + directTagBooksSQL + `) END`
	} else {
		withSQL = "RECURSIVE " + scope.visibleBooksCTE() + scopedTagCountsSQL
		from += ` JOIN visible_tags v ON v.id = t.id
 LEFT JOIN direct_counts c ON c.tag_id = t.id
 LEFT JOIN (SELECT DISTINCT ancestor_id FROM ancestors) a ON a.ancestor_id = t.id`
		// Materialize branch_counts only when a displayed tag has visible
		// descendants. Flat dictionaries use just their direct counts.
		count = `CASE WHEN a.ancestor_id IS NULL THEN c.book_count
 ELSE (SELECT bc.book_count FROM branch_counts bc WHERE bc.tag_id = t.id) END`
		hasChildren = "a.ancestor_id IS NOT NULL"
		args = append(args, scope.UserID)
	}
	args = append(args, opts.Kind)
	if opts.Sort != TagSortBooks {
		where += " AND t.name_key > ?"
		args = append(args, opts.AfterName)
	}
	if key := bookmeta.TagKey(opts.Query); key != "" {
		where += ` AND t.name_key LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(key)+"%")
	} else if opts.ParentID == 0 {
		where += " AND t.parent_id IS NULL"
	} else {
		where += " AND t.parent_id = ?"
		args = append(args, opts.ParentID)
	}
	selection := "SELECT t.id, t.name, t.name_key, " + count + " AS book_count, " + hasChildren + " AS has_children FROM " + from + " WHERE " + where
	query := withClause(withSQL) + selection + " ORDER BY t.name_key"
	if opts.Sort == TagSortBooks {
		query = withClause(withSQL) + "SELECT * FROM (" + selection + ")"
		if opts.AfterName != "" {
			query += " WHERE book_count < ? OR (book_count = ? AND name_key > ?)"
			args = append(args, opts.AfterCount, opts.AfterCount, opts.AfterName)
		}
		query += " ORDER BY book_count DESC, name_key"
	}
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tag counts: %w", err)
	}
	defer rows.Close()
	var out []TagRow
	for rows.Next() {
		var tag TagRow
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Key, &tag.BookCount, &tag.HasChildren); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// Walk ancestors once per distinct visible tag. Branch counts deduplicate books
// assigned both directly and through descendants.
const scopedTagCountsSQL = `,
 direct_counts AS MATERIALIZED (
   SELECT bt.tag_id, COUNT(*) AS book_count FROM books b JOIN book_tags bt ON bt.book_id = b.id
   WHERE b.id IN (SELECT book_id FROM visible_scope) AND b.deleted_at IS NULL
   GROUP BY bt.tag_id
 ),
 ancestors(tag_id, ancestor_id) AS (
   SELECT t.id, t.parent_id FROM direct_counts c JOIN tags t ON t.id = c.tag_id WHERE t.parent_id IS NOT NULL
   UNION ALL
   SELECT a.tag_id, t.parent_id FROM ancestors a JOIN tags t ON t.id = a.ancestor_id WHERE t.parent_id IS NOT NULL
 ),
 visible_links AS MATERIALIZED (
   SELECT bt.tag_id, b.id AS book_id FROM books b JOIN book_tags bt ON bt.book_id = b.id
   WHERE b.id IN (SELECT book_id FROM visible_scope) AND b.deleted_at IS NULL
 ),
 branch_counts AS MATERIALIZED (
   SELECT tag_id, COUNT(DISTINCT book_id) AS book_count FROM (
     SELECT a.ancestor_id AS tag_id, v.book_id FROM ancestors a JOIN visible_links v ON v.tag_id = a.tag_id
     UNION ALL
     SELECT a.ancestor_id AS tag_id, v.book_id
     FROM (SELECT DISTINCT ancestor_id FROM ancestors) a JOIN visible_links v ON v.tag_id = a.ancestor_id
   ) GROUP BY tag_id
 ),
 visible_tags(id) AS (SELECT tag_id FROM direct_counts UNION SELECT ancestor_id FROM ancestors)`
