package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// VisibilityScope narrows content queries to what one account may see.
// Authorization is two separate primitives composed with AND — deliberately
// no single authorize(user, action, book) entry point:
//
//   - capability ("may this user perform action X?") — a minimum-role check,
//     declared per route in web/server.go's route table;
//   - visibility ("may this user see book B?") — this scope, ANDed into every
//     content query unconditionally: lists, search, authors, series, tags,
//     OPDS, covers, downloads, reader entry points, sequence navigation.
//
// Contract: a scoped account sees the scoped catalog as its whole library —
// direct access to an out-of-scope book or asset is a 404, exactly like
// content that doesn't exist. Access is book-centric (asset resolves to its
// book). Enforcement covers every authenticated entry point: browser
// sessions, basic auth, app tokens, kosync tokens. An empty scope is valid
// and fail-closed: the account sees an empty library, not an error.
type VisibilityScope struct {
	UserID       int64
	ContentScope string
}

func FullVisibilityScope() VisibilityScope {
	return VisibilityScope{ContentScope: ContentScopeAll}
}

func VisibilityScopeForUser(queryer Queryer, userID int64) (VisibilityScope, error) {
	if userID <= 0 {
		return FullVisibilityScope(), nil
	}
	u, err := GetUserByID(queryer, userID)
	if err != nil {
		return VisibilityScope{}, err
	}
	if u == nil {
		return VisibilityScope{}, sql.ErrNoRows
	}
	// Only reader accounts can be shelf-scoped. Members and admins always get
	// the full shared library because they can curate catalog state; applying a
	// partial content scope to mutating roles would make ownership and cleanup
	// workflows incoherent.
	if u.Role != RoleReader || u.ContentScope != ContentScopeShelves {
		return FullVisibilityScope(), nil
	}
	return VisibilityScope{UserID: u.ID, ContentScope: ContentScopeShelves}, nil
}

func (s VisibilityScope) IsFull() bool {
	return s.ContentScope == "" || s.ContentScope == ContentScopeAll
}

// BookWhere returns a predicate restricting bookIDExpr to the scope. A shelf
// counts as a scope source when it is shared, or personal but owned by
// someone *else* (a curator's hidden allowlist) — the owner_id <> user_id
// half excludes the scoped reader's own personal shelves, which is what
// keeps "nothing a reader does can widen their scope" true: readers organize
// already-visible books, only curators grant access.
//
// Eligible query shelves are access boundaries, so "list all matching books"
// and "does book B match" must stay one predicate (the same MATCH against the
// same non-empty query_match here and in visibleBooksCTE). New books matching
// one become visible with no review: dynamic by design, accepted for the
// household trust model. Relational no: filters and per-user status: filters
// are never eligible because query_match cannot represent their full meaning.
// If the grammar ever grows OR/NOT/grouping, re-audit scope eligibility before
// allowing those expressions at an authorization boundary.
func (s VisibilityScope) BookWhere(bookIDExpr string) (string, []any) {
	if s.IsFull() {
		return "1 = 1", nil
	}
	return `EXISTS (
		SELECT 1
		FROM user_scope_shelves us
		JOIN shelves scope_shelf ON scope_shelf.id = us.shelf_id
		WHERE us.user_id = ?
		  AND (scope_shelf.visibility = 'shared' OR scope_shelf.owner_id <> us.user_id)
		  AND (
			(scope_shelf.kind = 'manual' AND EXISTS (
				SELECT 1
				FROM shelf_books scope_books
				WHERE scope_books.shelf_id = scope_shelf.id
				  AND scope_books.book_id = ` + bookIDExpr + `
			))
			OR
			(scope_shelf.kind = 'query'
			 AND scope_shelf.query_match IS NOT NULL
			 AND scope_shelf.query_match <> ''
			 AND EXISTS (
				SELECT 1
				FROM search
				WHERE search.rowid = ` + bookIDExpr + `
				  AND search MATCH scope_shelf.query_match
			))
		  )
	)`, []any{s.UserID}
}

func (s VisibilityScope) AppendBookWhere(where, bookIDExpr string, args ...any) (string, []any) {
	scopeWhere, scopeArgs := s.BookWhere(bookIDExpr)
	if scopeWhere == "1 = 1" {
		return where, args
	}
	return where + " AND " + scopeWhere, append(args, scopeArgs...)
}

func (s VisibilityScope) joinVisibleBooks(fromSQL string) (string, string, []any) {
	if s.IsFull() {
		return "", fromSQL, nil
	}
	return s.visibleBooksCTE(), fromSQL + `
		JOIN visible_scope scope_visible ON scope_visible.book_id = b.id`, []any{s.UserID}
}

// visibleBooksCTE builds the scoped library as a source set. List/sequence
// queries join this instead of running BookWhere as a per-book predicate; query
// shelves are then evaluated FTS-first instead of once per candidate book.
func (s VisibilityScope) visibleBooksCTE() string {
	return `
		scope_shelves AS MATERIALIZED (
			SELECT scope_shelf.id, scope_shelf.kind, scope_shelf.query_match
			FROM user_scope_shelves us
			JOIN shelves scope_shelf ON scope_shelf.id = us.shelf_id
			WHERE us.user_id = ?
			  AND (scope_shelf.visibility = 'shared' OR scope_shelf.owner_id <> us.user_id)
		),
		visible_scope AS MATERIALIZED (
			SELECT scope_books.book_id
			FROM scope_shelves scope_shelf
			JOIN shelf_books scope_books ON scope_books.shelf_id = scope_shelf.id
			WHERE scope_shelf.kind = 'manual'
			UNION
			SELECT search.rowid
			FROM scope_shelves scope_shelf
			JOIN search ON search MATCH scope_shelf.query_match
			WHERE scope_shelf.kind = 'query'
			  AND scope_shelf.query_match IS NOT NULL
			  AND scope_shelf.query_match <> ''
		)`
}

func withClause(withSQL string) string {
	if withSQL == "" {
		return ""
	}
	return "WITH " + withSQL
}

func CanAccessBook(queryer Queryer, scope VisibilityScope, bookID int64) (bool, error) {
	where, args := scope.AppendBookWhere("b.id = ? AND b.deleted_at IS NULL", "b.id", bookID)
	var exists int
	err := queryer.QueryRow(`SELECT 1 FROM books b WHERE `+where+` LIMIT 1`, args...).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check book access: %w", err)
	}
	return true, nil
}

func CanAccessTrashedBook(queryer Queryer, scope VisibilityScope, bookID int64) (bool, error) {
	where, args := scope.AppendBookWhere("b.id = ? AND b.deleted_at IS NOT NULL", "b.id", bookID)
	var exists int
	err := queryer.QueryRow(`SELECT 1 FROM books b WHERE `+where+` LIMIT 1`, args...).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check trashed book access: %w", err)
	}
	return true, nil
}

func CanAccessAsset(queryer Queryer, scope VisibilityScope, assetID string) (bool, error) {
	where, args := scope.AppendBookWhere("a.id = ? AND b.deleted_at IS NULL", "b.id", assetID)
	var exists int
	err := queryer.QueryRow(`
		SELECT 1
		FROM assets a
		JOIN books b ON b.id = a.book_id
		WHERE `+where+`
		LIMIT 1
	`, args...).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check asset access: %w", err)
	}
	return true, nil
}

func UserScopeShelfIDs(queryer Queryer, userID int64) ([]string, error) {
	rows, err := queryer.Query(`
		SELECT shelf_id
		FROM user_scope_shelves
		WHERE user_id = ?
		ORDER BY shelf_id
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user scope shelves: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user scope shelf: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
