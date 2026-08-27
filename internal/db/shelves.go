package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/id"
)

type ShelfKind string

const (
	ShelfManual ShelfKind = "manual"
	ShelfQuery  ShelfKind = "query"
)

type ShelfVisibility string

const (
	ShelfPersonal ShelfVisibility = "personal"
	ShelfShared   ShelfVisibility = "shared"
)

var (
	ErrShelfNotFound      = errors.New("shelf not found")
	ErrQueryShelf         = errors.New("query shelves do not have explicit membership")
	ErrEmptyShelfName     = errors.New("shelf name must not be empty")
	ErrShelfOwnerRequired = errors.New("shelf owner is required")
)

// ErrScopeShelfNotEligible classifies a smart shelf whose complete query
// cannot safely be represented as a reader's access boundary.
var ErrScopeShelfNotEligible = errors.New("scope shelf query cannot be used for access")

type Shelf struct {
	ID         string
	Name       string
	Kind       ShelfKind
	Query      string
	QueryMatch string
	OwnerID    string
	Visibility ShelfVisibility
	Position   int
	CreatedAt  int64
	UpdatedAt  int64
}

// scopeEligibilityError checks whether the shelf can safely act as a reader's
// content boundary. Manual shelves are explicit allowlists. Query shelves are
// accepted only when their complete meaning is the persisted FTS match.
func (s Shelf) scopeEligibilityError() error {
	if s.Kind == ShelfManual {
		return nil
	}
	match, reason := searchQueryScope(s.Query)
	if reason != "" {
		return errorWithDetail(ErrScopeShelfNotEligible, reason)
	}
	if match != s.QueryMatch {
		// normalizeShelfInput writes query and query_match together. If a row
		// bypassed that path and the two disagree, fail closed rather than use a
		// partial or stale authorization predicate.
		return errorWithDetail(ErrScopeShelfNotEligible, queryScopeShelfReason)
	}
	return nil
}

type ShelfMembership struct {
	Shelf
	InShelf bool
}

func normalizeShelfInput(name string, kind ShelfKind, query string) (string, ShelfKind, string, string, error) {
	name = strings.TrimSpace(name)
	query = strings.TrimSpace(query)
	if name == "" {
		return "", "", "", "", ErrEmptyShelfName
	}
	if kind == "" {
		kind = ShelfManual
	}
	switch kind {
	case ShelfManual:
		return name, kind, "", "", nil
	case ShelfQuery:
		if query == "" {
			return "", "", "", "", errors.New("query shelf requires a query")
		}
		validation := ValidateSearchQuery(query)
		if !validation.Valid {
			if validation.Error != "" {
				return "", "", "", "", errors.New(validation.Error)
			}
			return "", "", "", "", errors.New("query shelf requires a searchable query")
		}
		// query_match is the authorization-safe FTS cache, not a partial
		// serialization of every query. A smart shelf with relational filters
		// still works from query, but cannot define reader access.
		return name, kind, query, validation.scopeMatch, nil
	default:
		return "", "", "", "", fmt.Errorf("invalid shelf kind %q", kind)
	}
}

func normalizeShelfVisibility(visibility ShelfVisibility) (ShelfVisibility, error) {
	if visibility == "" {
		return ShelfPersonal, nil
	}
	switch visibility {
	case ShelfPersonal, ShelfShared:
		return visibility, nil
	default:
		return "", fmt.Errorf("invalid shelf visibility %q", visibility)
	}
}

func nextShelfPosition(database *DB, ownerID string, visibility ShelfVisibility) (int, error) {
	var pos int
	var err error
	if visibility == ShelfShared {
		err = database.QueryRow("SELECT COALESCE(MAX(position) + 1, 0) FROM shelves WHERE visibility = ?", string(ShelfShared)).Scan(&pos)
	} else {
		err = database.QueryRow("SELECT COALESCE(MAX(position) + 1, 0) FROM shelves WHERE visibility = ? AND owner_id = ?", string(ShelfPersonal), ownerID).Scan(&pos)
	}
	if err != nil {
		return 0, fmt.Errorf("next shelf position: %w", err)
	}
	return pos, nil
}

// CreateShelf inserts either a manual shelf or a query-backed shelf. ownerID is
// always the shelf owner; visibility controls whether other users can see it.
func (db *DB) CreateShelf(ownerID string, visibility ShelfVisibility, name string, kind ShelfKind, query string) (*Shelf, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, ErrShelfOwnerRequired
	}
	visibility, err := normalizeShelfVisibility(visibility)
	if err != nil {
		return nil, err
	}
	name, kind, query, queryMatch, err := normalizeShelfInput(name, kind, query)
	if err != nil {
		return nil, err
	}

	position, err := nextShelfPosition(db, ownerID, visibility)
	if err != nil {
		return nil, err
	}

	shelf := &Shelf{
		ID:         id.New(id.Shelf),
		Name:       name,
		Kind:       kind,
		Query:      query,
		QueryMatch: queryMatch,
		OwnerID:    ownerID,
		Visibility: visibility,
		Position:   position,
	}

	var q sql.NullString
	if kind == ShelfQuery {
		q = sql.NullString{String: query, Valid: true}
	}
	var qm sql.NullString
	if kind == ShelfQuery {
		qm = sql.NullString{String: queryMatch, Valid: true}
	}

	if _, err := db.Exec(
		`INSERT INTO shelves (id, name, kind, query, query_match, owner_id, visibility, position) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		shelf.ID, shelf.Name, string(shelf.Kind), q, qm, shelf.OwnerID, string(shelf.Visibility), shelf.Position,
	); err != nil {
		return nil, fmt.Errorf("insert shelf: %w", err)
	}
	return db.GetShelf(shelf.ID, ownerID)
}

// ListShelves returns shared shelves plus the viewer's personal shelves. With an
// empty viewerID it returns only shared shelves.
func (db *DB) ListShelves(viewerID string) ([]Shelf, error) {
	query := `
			SELECT id, name, kind, query, query_match, owner_id, visibility, position, created_at, updated_at
			FROM shelves
			WHERE visibility = ?`
	args := []any{string(ShelfShared)}
	if viewerID != "" {
		query += ` OR owner_id = ?`
		args = append(args, viewerID)
	}
	query += ` ORDER BY visibility <> 'shared', position ASC, name COLLATE NOCASE ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list shelves: %w", err)
	}
	defer rows.Close()

	var shelves []Shelf
	for rows.Next() {
		s, err := scanShelf(rows)
		if err != nil {
			return nil, err
		}
		shelves = append(shelves, s)
	}
	return shelves, rows.Err()
}

// SharedShelfNamesOwnedBy returns the names of shared shelves owned by a user,
// ordered by name. Deleting the user cascades these away for the whole
// household (shelves.owner_id ON DELETE CASCADE), so the user-delete UI warns
// with this list before confirming.
func SharedShelfNamesOwnedBy(queryer Queryer, ownerID string) ([]string, error) {
	rows, err := queryer.Query(`
		SELECT name FROM shelves
		WHERE owner_id = ? AND visibility = ?
		ORDER BY name COLLATE NOCASE ASC
	`, ownerID, string(ShelfShared))
	if err != nil {
		return nil, fmt.Errorf("list shared shelves owned by user: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan shared shelf name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// ListShelvesForUser returns shelves visible in the user's current library
// scope. Full-scope accounts see shared shelves plus their personal shelves.
// Shelf-scoped accounts see assigned shared scope shelves plus their personal
// shelves, so scope-defining shelves are navigable without exposing unrelated
// shared shelf names.
func (db *DB) ListShelvesForUser(userID string) ([]Shelf, error) {
	if userID == "" {
		return db.ListShelves("")
	}
	u, err := db.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, sql.ErrNoRows
	}
	if u.Role != RoleReader || u.ContentScope != ContentScopeShelves {
		return db.ListShelves(userID)
	}

	rows, err := db.Query(`
			SELECT s.id, s.name, s.kind, s.query, s.query_match, s.owner_id, s.visibility, s.position, s.created_at, s.updated_at
			FROM shelves s
			WHERE (s.owner_id = ? AND s.visibility = 'personal')
			   OR (s.visibility = 'shared' AND EXISTS (
				SELECT 1
				FROM user_scope_shelves us
				WHERE us.user_id = ? AND us.shelf_id = s.id
			   ))
			ORDER BY s.visibility <> 'shared', s.position ASC, s.name COLLATE NOCASE ASC
		`, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("list user shelves: %w", err)
	}
	defer rows.Close()

	var shelves []Shelf
	for rows.Next() {
		s, err := scanShelf(rows)
		if err != nil {
			return nil, err
		}
		shelves = append(shelves, s)
	}
	return shelves, rows.Err()
}

// GetShelf returns a shelf visible to viewerID. Empty viewerID can only see
// shared shelves.
func (db *DB) GetShelf(shelfID, viewerID string) (*Shelf, error) {
	query := `
			SELECT id, name, kind, query, query_match, owner_id, visibility, position, created_at, updated_at
			FROM shelves
			WHERE id = ? AND (visibility = ?`
	args := []any{shelfID, string(ShelfShared)}
	if viewerID != "" {
		query += ` OR owner_id = ?`
		args = append(args, viewerID)
	}
	query += `)`

	s, err := scanShelf(db.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShelfNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get shelf: %w", err)
	}
	return &s, nil
}

// GetShelfForUser returns a shelf only if it is visible in the user's current
// library scope. Use GetShelf for low-level owner/shared checks that should not
// apply content-scope narrowing.
func (db *DB) GetShelfForUser(shelfID, userID string) (*Shelf, error) {
	if userID == "" {
		return db.GetShelf(shelfID, "")
	}
	u, err := db.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, sql.ErrNoRows
	}
	if u.Role != RoleReader || u.ContentScope != ContentScopeShelves {
		return db.GetShelf(shelfID, userID)
	}

	row := db.QueryRow(`
			SELECT s.id, s.name, s.kind, s.query, s.query_match, s.owner_id, s.visibility, s.position, s.created_at, s.updated_at
			FROM shelves s
			WHERE s.id = ?
			  AND (
				(s.owner_id = ? AND s.visibility = 'personal')
				OR (s.visibility = 'shared' AND EXISTS (
					SELECT 1
					FROM user_scope_shelves us
					WHERE us.user_id = ? AND us.shelf_id = s.id
			))
		  )
	`, shelfID, userID, userID)
	s, err := scanShelf(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShelfNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func scanShelf(row rowScanner) (Shelf, error) {
	var s Shelf
	var kind, visibility string
	var q, qm sql.NullString
	if err := row.Scan(&s.ID, &s.Name, &kind, &q, &qm, &s.OwnerID, &visibility, &s.Position, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return s, fmt.Errorf("scan shelf: %w", err)
	}
	s.Kind = ShelfKind(kind)
	s.Visibility = ShelfVisibility(visibility)
	s.Query = q.String
	s.QueryMatch = qm.String
	return s, nil
}

// UpdateShelf edits a shelf without changing its type. Query shelves receive a
// new search string; manual shelves ignore query and keep explicit membership.
// visibility changes whether the shelf is personal or shared without changing
// ownership.
func (db *DB) UpdateShelf(shelfID, viewerID, name, query string, visibility ShelfVisibility) (*Shelf, error) {
	shelf, err := db.GetShelf(shelfID, viewerID)
	if err != nil {
		return nil, err
	}
	if visibility == "" {
		visibility = shelf.Visibility
	}
	visibility, err = normalizeShelfVisibility(visibility)
	if err != nil {
		return nil, err
	}
	name, _, query, queryMatch, err := normalizeShelfInput(name, shelf.Kind, query)
	if err != nil {
		return nil, err
	}
	position := shelf.Position
	if visibility != shelf.Visibility {
		position, err = nextShelfPosition(db, shelf.OwnerID, visibility)
		if err != nil {
			return nil, err
		}
	}

	var q, qm sql.NullString
	if shelf.Kind == ShelfQuery {
		q = sql.NullString{String: query, Valid: true}
		qm = sql.NullString{String: queryMatch, Valid: true}
	}

	res, err := db.Exec(
		`UPDATE shelves
		SET name = ?, query = ?, query_match = ?, visibility = ?, position = ?, updated_at = unixepoch()
		WHERE id = ?`,
		name, q, qm, string(visibility), position, shelfID,
	)
	if err != nil {
		return nil, fmt.Errorf("update shelf: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrShelfNotFound
	}
	return db.GetShelf(shelfID, viewerID)
}

func (db *DB) DeleteShelf(shelfID, viewerID string) error {
	if _, err := db.GetShelf(shelfID, viewerID); err != nil {
		return err
	}
	res, err := db.Exec("DELETE FROM shelves WHERE id = ?", shelfID)
	if err != nil {
		return fmt.Errorf("delete shelf: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrShelfNotFound
	}
	return nil
}

func (db *DB) AddBookToShelf(shelfID, viewerID, workID string) error {
	shelf, err := db.GetShelf(shelfID, viewerID)
	if err != nil {
		return err
	}
	if shelf.Kind != ShelfManual {
		return ErrQueryShelf
	}
	_, err = db.Exec(`
		INSERT INTO shelf_books (shelf_id, work_id, position)
		VALUES (?, ?, COALESCE((SELECT MAX(position) + 1 FROM shelf_books WHERE shelf_id = ?), 0))
		ON CONFLICT(shelf_id, work_id) DO NOTHING
	`, shelfID, workID, shelfID)
	if err != nil {
		return fmt.Errorf("add book to shelf: %w", err)
	}
	return nil
}

// AddBooksToShelf adds every workID to the manual shelf in one transaction,
// skipping any already present, and returns how many rows were newly inserted.
// Each insert recomputes the next position, so the selection is appended in the
// given order after whatever the shelf already held.
func (db *DB) AddBooksToShelf(ctx context.Context, shelfID, viewerID string, workIDs []string) (int, error) {
	shelf, err := db.GetShelf(shelfID, viewerID)
	if err != nil {
		return 0, err
	}
	if shelf.Kind != ShelfManual {
		return 0, ErrQueryShelf
	}
	if len(workIDs) == 0 {
		return 0, nil
	}
	changed := 0
	err = db.Transact(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO shelf_books (shelf_id, work_id, position)
			VALUES (?, ?, COALESCE((SELECT MAX(position) + 1 FROM shelf_books WHERE shelf_id = ?), 0))
			ON CONFLICT(shelf_id, work_id) DO NOTHING
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, workID := range workIDs {
			res, err := stmt.ExecContext(ctx, shelfID, workID, shelfID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				changed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("add books to shelf: %w", err)
	}
	return changed, nil
}

// RemoveBooksFromShelf drops every workID from the manual shelf in one statement
// and returns how many rows were actually removed.
func (db *DB) RemoveBooksFromShelf(shelfID, viewerID string, workIDs []string) (int, error) {
	shelf, err := db.GetShelf(shelfID, viewerID)
	if err != nil {
		return 0, err
	}
	if shelf.Kind != ShelfManual {
		return 0, ErrQueryShelf
	}
	if len(workIDs) == 0 {
		return 0, nil
	}
	placeholders, args := idPlaceholders(workIDs)
	args = append([]any{shelfID}, args...)
	res, err := db.Exec(
		"DELETE FROM shelf_books WHERE shelf_id = ? AND work_id IN ("+placeholders+")", args...,
	)
	if err != nil {
		return 0, fmt.Errorf("remove books from shelf: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (db *DB) RemoveBookFromShelf(shelfID, viewerID, workID string) error {
	shelf, err := db.GetShelf(shelfID, viewerID)
	if err != nil {
		return err
	}
	if shelf.Kind != ShelfManual {
		return ErrQueryShelf
	}
	if _, err := db.Exec("DELETE FROM shelf_books WHERE shelf_id = ? AND work_id = ?", shelfID, workID); err != nil {
		return fmt.Errorf("remove book from shelf: %w", err)
	}
	return nil
}

// ListBookShelfMemberships returns visible manual shelves and whether the work
// is currently assigned to each. Query shelves are omitted because they cannot
// be hand-edited.
func (db *DB) ListBookShelfMemberships(viewerID, workID string) ([]ShelfMembership, error) {
	query := `
			SELECT s.id, s.name, s.kind, s.query, s.query_match, s.owner_id, s.visibility, s.position, s.created_at, s.updated_at,
			       CASE WHEN sb.work_id IS NULL THEN 0 ELSE 1 END AS in_shelf
			FROM shelves s
			LEFT JOIN shelf_books sb ON sb.shelf_id = s.id AND sb.work_id = ?
			WHERE s.kind = 'manual' AND (s.visibility = ?`
	args := []any{workID, string(ShelfShared)}
	if viewerID != "" {
		query += ` OR s.owner_id = ?`
		args = append(args, viewerID)
	}
	query += `)
			ORDER BY s.visibility <> 'shared', s.position ASC, s.name COLLATE NOCASE ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list book shelf memberships: %w", err)
	}
	defer rows.Close()

	var memberships []ShelfMembership
	for rows.Next() {
		var s Shelf
		var kind, visibility string
		var q, qm sql.NullString
		var inShelf int
		if err := rows.Scan(&s.ID, &s.Name, &kind, &q, &qm, &s.OwnerID, &visibility, &s.Position, &s.CreatedAt, &s.UpdatedAt, &inShelf); err != nil {
			return nil, fmt.Errorf("scan shelf membership: %w", err)
		}
		s.Kind = ShelfKind(kind)
		s.Visibility = ShelfVisibility(visibility)
		s.Query = q.String
		s.QueryMatch = qm.String
		memberships = append(memberships, ShelfMembership{Shelf: s, InShelf: inShelf != 0})
	}
	return memberships, rows.Err()
}
