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
var ErrTagCycle = errors.New("a branch cannot be moved inside itself")
var ErrTagPath = fmt.Errorf("use a path with non-empty parts and at most %d levels", bookmeta.MaxTagDepth)

type TagKind string

const (
	TagKindTag   TagKind = "tag"
	TagKindGenre TagKind = "genre"
)

// Tag identifies a current dictionary entry independently of loaded list pages.
type Tag struct {
	ID   int64   `json:"id"`
	Kind TagKind `json:"kind"`
	Name string  `json:"name"`
}

func GetTag(queryer Queryer, id int64) (Tag, error) {
	var tag Tag
	err := queryer.QueryRow("SELECT id, kind, name FROM tags WHERE id = ?", id).Scan(&tag.ID, &tag.Kind, &tag.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, ErrTagNotFound
	}
	return tag, err
}

// BookTags holds the two independently ordered parts of the shared dictionary.
type BookTags struct {
	Genres []string
	Tags   []string
}

type tagNode struct {
	id   int64
	name string
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
		node, err := ensureTagPath(tx, kind, bookmeta.TagParts(name))
		if err != nil {
			return fmt.Errorf("resolve tag: %w", err)
		}
		if _, err := tx.Exec("INSERT INTO book_tags (book_id, tag_id, position) VALUES (?, ?, ?)", bookID, node.id, position); err != nil {
			return fmt.Errorf("link tag: %w", err)
		}
	}
	return nil
}

// ensureTagPath finds or creates a path, keeping existing ancestors' spelling.
// Only the returned node is linked to a book; ancestors belong to the dictionary.
func ensureTagPath(tx *Tx, kind TagKind, parts []string) (tagNode, error) {
	if len(parts) == 0 {
		return tagNode{}, nil
	}
	node := tagNode{name: strings.Join(parts, ".")}
	err := tx.QueryRow("SELECT id, name FROM tags WHERE kind = ? AND name_key = ?", kind, bookmeta.TagKey(node.name)).Scan(&node.id, &node.name)
	if !errors.Is(err, sql.ErrNoRows) {
		return node, err
	}
	parent, err := ensureTagPath(tx, kind, parts[:len(parts)-1])
	if err != nil {
		return tagNode{}, err
	}
	node.name = parts[len(parts)-1]
	if parent.id != 0 {
		node.name = parent.name + "." + node.name
	}
	err = tx.QueryRow("INSERT INTO tags (kind, name, name_key, parent_id) VALUES (?, ?, ?, ?) RETURNING id", kind, node.name, bookmeta.TagKey(node.name), sql.NullInt64{Int64: parent.id, Valid: parent.id != 0}).Scan(&node.id)
	return node, err
}

func DeleteOrphanTags(execer Execer) error {
	_, err := execer.Exec(`WITH RECURSIVE used(id) AS (
   SELECT id FROM tags WHERE EXISTS (SELECT 1 FROM book_tags bt WHERE bt.tag_id = tags.id)
   UNION
   SELECT t.parent_id FROM tags t JOIN used ON used.id = t.id WHERE t.parent_id IS NOT NULL
 ) DELETE FROM tags WHERE id NOT IN (SELECT id FROM used)`)
	return err
}

// RenameOrMergeTag moves the whole branch, including trashed books. Overlapping
// branches merge node by node, keeping the first position on each book.
func RenameOrMergeTag(tx *Tx, tagID int64, newName string) ([]int64, Tag, error) {
	nodes, kind, err := loadTagBranch(tx, tagID)
	if err != nil {
		return nil, Tag{}, err
	}
	oldName := nodes[0].name
	target := Tag{ID: tagID, Kind: kind, Name: oldName}
	newName = bookmeta.NormalizeTagName(newName)
	if oldName == newName {
		return nil, target, nil
	}
	if len(bookmeta.TagParts(newName)) > 1 && strings.HasPrefix(bookmeta.TagKey(newName), bookmeta.TagKey(oldName)+".") {
		return nil, Tag{}, ErrTagCycle
	}
	ids, err := bookIDsForTagBranch(tx, tagID)
	if err != nil {
		return nil, Tag{}, err
	}
	renamed := make(map[string]string, len(nodes))
	for _, node := range nodes {
		destination := newName + strings.TrimPrefix(node.name, oldName)
		parts := bookmeta.TagParts(destination)
		// A descendant must remain a nested path. TagParts falls back to a
		// literal name for empty components or excessive depth.
		if node.id != tagID && len(parts) == 1 {
			return nil, Tag{}, ErrTagPath
		}
		moved, err := moveTagNode(tx, kind, node.id, parts)
		if err != nil {
			return nil, Tag{}, err
		}
		if node.id == tagID {
			target.ID, target.Name = moved.id, moved.name
		}
		renamed[bookmeta.TagKey(node.name)] = moved.name
	}
	if err := renameSavedTagQueries(tx, kind, renamed); err != nil {
		return nil, Tag{}, err
	}
	if err := DeleteOrphanTags(tx); err != nil {
		return nil, Tag{}, err
	}
	return ids, target, nil
}

// moveTagNode renames one node or merges it into an existing destination.
// The caller visits parents before children and updates every descendant's path.
func moveTagNode(tx *Tx, kind TagKind, sourceID int64, parts []string) (tagNode, error) {
	parent, err := ensureTagPath(tx, kind, parts[:len(parts)-1])
	if err != nil {
		return tagNode{}, err
	}
	name := parts[len(parts)-1]
	if parent.id != 0 {
		name = parent.name + "." + name
	}
	var target tagNode
	err = tx.QueryRow("SELECT id, name FROM tags WHERE kind = ? AND name_key = ?", kind, bookmeta.TagKey(name)).Scan(&target.id, &target.name)
	if errors.Is(err, sql.ErrNoRows) || err == nil && target.id == sourceID {
		_, err = tx.Exec("UPDATE tags SET name = ?, name_key = ?, parent_id = ? WHERE id = ?", name, bookmeta.TagKey(name), sql.NullInt64{Int64: parent.id, Valid: parent.id != 0}, sourceID)
		return tagNode{id: sourceID, name: name}, err
	}
	if err != nil {
		return tagNode{}, err
	}

	if _, err := tx.Exec(`INSERT INTO book_tags (book_id, tag_id, position)
   SELECT book_id, ?, position FROM book_tags WHERE tag_id = ?
   ON CONFLICT (book_id, tag_id) DO UPDATE SET position = MIN(position, excluded.position)`, target.id, sourceID); err != nil {
		return tagNode{}, err
	}
	if _, err := tx.Exec("UPDATE tags SET parent_id = ? WHERE parent_id = ?", target.id, sourceID); err != nil {
		return tagNode{}, err
	}
	if _, err := tx.Exec("DELETE FROM book_tags WHERE tag_id = ?", sourceID); err != nil {
		return tagNode{}, err
	}
	_, err = tx.Exec("DELETE FROM tags WHERE id = ?", sourceID)
	return target, err
}

func DeleteTag(tx *Tx, tagID int64) ([]int64, error) {
	var exists int
	if err := tx.QueryRow("SELECT 1 FROM tags WHERE id = ?", tagID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTagNotFound
		}
		return nil, err
	}
	ids, err := bookIDsForTagBranch(tx, tagID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM book_tags WHERE tag_id IN ("+tagDescendantsSQL("?")+")", tagID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM tags WHERE id IN ("+tagDescendantsSQL("?")+")", tagID); err != nil {
		return nil, err
	}
	if err := DeleteOrphanTags(tx); err != nil {
		return nil, err
	}
	return ids, nil
}

func bookIDsForTagBranch(queryer Queryer, tagID int64) ([]int64, error) {
	rows, err := queryer.Query("SELECT DISTINCT book_id FROM book_tags WHERE tag_id IN ("+tagDescendantsSQL("?")+")", tagID)
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

func tagDescendantsSQL(root string) string {
	return `WITH RECURSIVE branch(id) AS (
   SELECT ` + root + `
   UNION ALL
   SELECT child.id FROM tags child JOIN branch ON child.parent_id = branch.id
 ) SELECT id FROM branch`
}

// loadTagBranch returns the root first, followed by its descendants in depth order.
func loadTagBranch(queryer Queryer, tagID int64) ([]tagNode, TagKind, error) {
	rows, err := queryer.Query(`WITH RECURSIVE branch(id, depth) AS (
   SELECT id, 0 FROM tags WHERE id = ?
   UNION ALL
   SELECT child.id, branch.depth + 1 FROM tags child JOIN branch ON child.parent_id = branch.id
 ) SELECT t.id, t.name, t.kind FROM branch JOIN tags t ON t.id = branch.id ORDER BY branch.depth, t.name_key`, tagID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var nodes []tagNode
	var kind TagKind
	for rows.Next() {
		var node tagNode
		if err := rows.Scan(&node.id, &node.name, &kind); err != nil {
			return nil, "", err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(nodes) == 0 {
		return nil, "", ErrTagNotFound
	}
	return nodes, kind, nil
}

// escapeLike escapes literal substring searches.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
