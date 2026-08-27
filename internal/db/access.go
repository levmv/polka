package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// VisibilityScope narrows content queries to what one account may see.
// Authorization is two separate primitives composed with AND — deliberately
// no single authorize(user, action, work) entry point:
//
//   - capability ("may this user perform action X?") — a minimum-role check,
//     declared per route in web/server.go's route table;
//   - visibility ("may this user see work W?") — this scope, ANDed into every
//     content query unconditionally: lists, search, authors, series, tags,
//     OPDS, covers, downloads, reader entry points, sequence navigation.
//
// Contract: a scoped account sees the scoped catalog as its whole library —
// direct access to an out-of-scope work or asset is a 404, exactly like
// content that doesn't exist. Access is work-centric (asset resolves to its
// work). Enforcement covers every authenticated entry point: browser
// sessions, basic auth, app tokens, kosync tokens. An empty scope is valid
// and fail-closed: the account sees an empty library, not an error.
type VisibilityScope struct {
	UserID       string
	ContentScope string
}

func FullVisibilityScope() VisibilityScope {
	return VisibilityScope{ContentScope: ContentScopeAll}
}

func (db *DB) VisibilityScopeForUser(userID string) (VisibilityScope, error) {
	if userID == "" {
		return FullVisibilityScope(), nil
	}
	u, err := db.GetUserByID(userID)
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

// WorkWhere returns a predicate restricting workIDExpr to the scope. A shelf
// counts as a scope source when it is shared, or personal but owned by
// someone *else* (a curator's hidden allowlist) — the owner_id <> user_id
// half excludes the scoped reader's own personal shelves, which is what
// keeps "nothing a reader does can widen their scope" true: readers organize
// already-visible books, only curators grant access.
//
// Eligible query shelves are access boundaries, so "list all matching works"
// and "does work W match" must stay one predicate (the same MATCH against the
// same non-empty query_match here and in visibleWorksCTE). New books matching
// one become visible with no review: dynamic by design, accepted for the
// household trust model. Relational no: filters and per-user status: filters
// are never eligible because query_match cannot represent their full meaning.
// If the grammar ever grows OR/NOT/grouping, re-audit scope eligibility before
// allowing those expressions at an authorization boundary.
func (s VisibilityScope) WorkWhere(workIDExpr string) (string, []any) {
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
				  AND scope_books.work_id = ` + workIDExpr + `
			))
			OR
			(scope_shelf.kind = 'query'
			 AND scope_shelf.query_match IS NOT NULL
			 AND scope_shelf.query_match <> ''
			 AND EXISTS (
				SELECT 1
				FROM search
				WHERE search.work_id = ` + workIDExpr + `
				  AND search MATCH scope_shelf.query_match
			))
		  )
	)`, []any{s.UserID}
}

func (s VisibilityScope) AppendWorkWhere(where, workIDExpr string, args ...any) (string, []any) {
	scopeWhere, scopeArgs := s.WorkWhere(workIDExpr)
	if scopeWhere == "1 = 1" {
		return where, args
	}
	return where + " AND " + scopeWhere, append(args, scopeArgs...)
}

func (s VisibilityScope) joinVisibleWorks(fromSQL string) (string, string, []any) {
	if s.IsFull() {
		return "", fromSQL, nil
	}
	return s.visibleWorksCTE(), fromSQL + `
		JOIN visible_scope scope_visible ON scope_visible.work_id = w.id`, []any{s.UserID}
}

// visibleWorksCTE builds the scoped library as a source set. List/sequence
// queries join this instead of running WorkWhere as a per-work predicate; query
// shelves are then evaluated FTS-first instead of once per candidate work.
func (s VisibilityScope) visibleWorksCTE() string {
	return `
		scope_shelves AS MATERIALIZED (
			SELECT scope_shelf.id, scope_shelf.kind, scope_shelf.query_match
			FROM user_scope_shelves us
			JOIN shelves scope_shelf ON scope_shelf.id = us.shelf_id
			WHERE us.user_id = ?
			  AND (scope_shelf.visibility = 'shared' OR scope_shelf.owner_id <> us.user_id)
		),
		visible_scope AS MATERIALIZED (
			SELECT scope_books.work_id
			FROM scope_shelves scope_shelf
			JOIN shelf_books scope_books ON scope_books.shelf_id = scope_shelf.id
			WHERE scope_shelf.kind = 'manual'
			UNION
			SELECT search.work_id
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

func CanAccessWork(queryer Queryer, scope VisibilityScope, workID string) (bool, error) {
	where, args := scope.AppendWorkWhere("w.id = ? AND w.deleted_at IS NULL", "w.id", workID)
	var exists int
	err := queryer.QueryRow(`SELECT 1 FROM works w WHERE `+where+` LIMIT 1`, args...).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check work access: %w", err)
	}
	return true, nil
}

func CanAccessTrashedWork(queryer Queryer, scope VisibilityScope, workID string) (bool, error) {
	where, args := scope.AppendWorkWhere("w.id = ? AND w.deleted_at IS NOT NULL", "w.id", workID)
	var exists int
	err := queryer.QueryRow(`SELECT 1 FROM works w WHERE `+where+` LIMIT 1`, args...).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check trashed work access: %w", err)
	}
	return true, nil
}

func CanAccessAsset(queryer Queryer, scope VisibilityScope, assetID string) (bool, error) {
	where, args := scope.AppendWorkWhere("a.id = ? AND w.deleted_at IS NULL", "w.id", assetID)
	var exists int
	err := queryer.QueryRow(`
		SELECT 1
		FROM assets a
		JOIN works w ON w.id = a.work_id
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

func UserScopeShelfIDs(queryer Queryer, userID string) ([]string, error) {
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
