package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/levmv/polka/internal/id"
)

// User is an authentication identity. Library content is shared across all
// users; only per-user state references the user id. password_hash is a bcrypt
// hash and is never exposed in any DTO or API response.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	ContentScope string
	CreatedAt    int64
	UpdatedAt    int64
}

// UserAccess describes an account's catalog permissions. ShelfViewerID is the
// curator whose visible personal shelves may be newly selected for ShelfIDs;
// it is validation context and is not persisted on the target account.
type UserAccess struct {
	Role          string
	ContentScope  string
	ShelfIDs      []string
	ShelfViewerID string
}

const userColumns = `id, username, password_hash, role, content_scope, created_at, updated_at`

// Roles are deliberately fixed and ordered, not a configurable permission
// matrix. admin handles server-level and irreversible operations (users,
// storage, purge), member curates the shared catalog, reader is a read-only
// account with personal state. reader is the default for new non-admin
// accounts; member is a co-librarian and should be granted deliberately.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleReader = "reader"
)

// ContentScope is the visibility half of access control (see db/access.go):
// 'all', or 'shelves' — a reader-only allowlist of assigned scope shelves.
// A child account is the expected use: reader + shelves.
const (
	ContentScopeAll     = "all"
	ContentScopeShelves = "shelves"
)

// ErrUserExists is returned when creating a user whose username is already taken
// (usernames are compared case-insensitively).
var ErrUserExists = errors.New("username already taken")

// ErrInvalidUserInput classifies account values that are safe to return as a
// client error; its concrete error retains the useful validation detail.
var ErrInvalidUserInput = errors.New("invalid user input")

// ErrUserIDRequired reports a missing user identity at the DB API boundary.
var ErrUserIDRequired = errors.New("user id required")

// ErrLastAdmin is returned when an access change or deletion would leave the
// library without an administrator.
var ErrLastAdmin = errors.New("cannot remove or demote the last admin")

// ErrScopeShelfNotVisible is returned when an access scope tries to use a shelf
// that is not visible to the curator changing the account. Scope shelves follow
// the same shelf visibility rule as the UI: shared shelves plus the curator's
// own personal shelves. A scoped user's own personal shelves are never accepted
// as access policy.
var ErrScopeShelfNotVisible = errors.New("scope shelf is not visible")

// normalizeUsername lowercases and trims a username so lookups and the unique
// index agree regardless of how the name was typed.
func normalizeUsername(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// hashPassword turns a clear-text password into a bcrypt hash. Bcrypt embeds its
// own salt and cost in the returned string, so verification needs no extra
// stored fields. We keep the credential logic here (rather than in core) because
// it is only ever exercised at the persistence boundary by both the web login
// and the `polka user` CLI.
func hashPassword(password string) (string, error) {
	if password == "" {
		return "", errorWithDetail(ErrInvalidUserInput, "password must not be empty")
	}
	// Bcrypt's limit is bytes, not characters. Classify it before hashing so an
	// expected value error cannot be confused with a hashing failure.
	if len(password) > 72 {
		return "", errorWithDetail(ErrInvalidUserInput, "password must not exceed 72 bytes")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// CountUsers reports how many accounts exist. Zero means the server has not been
// bootstrapped yet (the first-run setup path applies).
func (db *DB) CountUsers() (int, error) {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

func ValidRole(role string) bool {
	switch role {
	case RoleAdmin, RoleMember, RoleReader:
		return true
	default:
		return false
	}
}

func RoleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 3
	case RoleMember:
		return 2
	case RoleReader:
		return 1
	default:
		return 0
	}
}

func RoleAtLeast(role, minRole string) bool {
	if !ValidRole(role) || !ValidRole(minRole) {
		return false
	}
	return RoleRank(role) >= RoleRank(minRole)
}

func validContentScope(scope string) bool {
	return scope == ContentScopeAll || scope == ContentScopeShelves
}

func normalizeUserScope(role, contentScope string, scopeShelfIDs []string) (string, []string, error) {
	if role != RoleReader {
		return ContentScopeAll, nil, nil
	}
	if contentScope == "" {
		contentScope = ContentScopeAll
	}
	if !validContentScope(contentScope) {
		return "", nil, errorWithDetail(ErrInvalidUserInput, fmt.Sprintf("invalid content scope %q", contentScope))
	}
	return contentScope, scopeShelfIDs, nil
}

// CreateUser inserts a new account with the given clear-text password (hashed
// here). The username is normalized; a collision returns ErrUserExists.
func (db *DB) CreateUser(username, password, role string) (*User, error) {
	return db.CreateUserWithAccess(username, password, UserAccess{
		Role:         role,
		ContentScope: ContentScopeAll,
	})
}

// CreateUserWithAccess inserts an account and its final shelf scope in one
// transaction.
func (db *DB) CreateUserWithAccess(username, password string, access UserAccess) (*User, error) {
	uname := normalizeUsername(username)
	if uname == "" {
		return nil, errorWithDetail(ErrInvalidUserInput, "username must not be empty")
	}
	if access.Role == "" {
		access.Role = RoleReader
	}
	if !ValidRole(access.Role) {
		return nil, errorWithDetail(ErrInvalidUserInput, fmt.Sprintf("invalid role %q", access.Role))
	}
	contentScope, shelfIDs, err := normalizeUserScope(access.Role, access.ContentScope, access.ShelfIDs)
	if err != nil {
		return nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	u := &User{ID: id.New(id.User), Username: uname, PasswordHash: hash, Role: access.Role, ContentScope: contentScope}
	err = db.Transact(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			"INSERT INTO users (id, username, password_hash, role, content_scope) VALUES (?, ?, ?, ?, ?)",
			u.ID, u.Username, u.PasswordHash, u.Role, u.ContentScope,
		); err != nil {
			return fmt.Errorf("insert user: %w", err)
		}
		return replaceUserScopeShelves(tx, u.ID, access.ShelfViewerID, contentScope, shelfIDs)
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return u, nil
}

// GetUserByUsername looks an account up by its (normalized) username. A missing
// user returns (nil, nil) so callers can distinguish "not found" from an error.
func (db *DB) GetUserByUsername(username string) (*User, error) {
	return scanOptionalUser(db.QueryRow(
		"SELECT "+userColumns+" FROM users WHERE username = ?",
		normalizeUsername(username),
	))
}

// GetUserByID looks an account up by id. A missing user returns (nil, nil).
func (db *DB) GetUserByID(userID string) (*User, error) {
	return scanOptionalUser(db.QueryRow(
		"SELECT "+userColumns+" FROM users WHERE id = ?",
		userID,
	))
}

func scanOptionalUser(row *sql.Row) (*User, error) {
	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func scanUser(row rowScanner) (User, error) {
	var user User
	if err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.ContentScope, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return User{}, fmt.Errorf("scan user: %w", err)
	}
	return user, nil
}

// ListUsers returns all accounts ordered by username (admin screens / CLI).
func (db *DB) ListUsers() ([]User, error) {
	rows, err := db.Query(
		"SELECT " + userColumns + " FROM users ORDER BY username",
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (db *DB) UpdateUserAccess(userID string, access UserAccess) (*User, error) {
	if !ValidRole(access.Role) {
		return nil, errorWithDetail(ErrInvalidUserInput, fmt.Sprintf("invalid role %q", access.Role))
	}
	contentScope, shelfIDs, err := normalizeUserScope(access.Role, access.ContentScope, access.ShelfIDs)
	if err != nil {
		return nil, err
	}

	if err := db.Transact(context.Background(), func(tx *sql.Tx) error {
		if access.Role != RoleAdmin {
			if err := guardAdminRemoval(tx, userID); err != nil {
				return err
			}
		}
		res, err := tx.Exec(
			`UPDATE users SET role = ?, content_scope = ?, updated_at = unixepoch() WHERE id = ?`,
			access.Role, contentScope, userID,
		)
		if err != nil {
			return fmt.Errorf("update user access: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return replaceUserScopeShelves(tx, userID, access.ShelfViewerID, contentScope, shelfIDs)
	}); err != nil {
		return nil, err
	}
	return db.GetUserByID(userID)
}

func replaceUserScopeShelves(tx *sql.Tx, userID, shelfViewerID, contentScope string, scopeShelfIDs []string) error {
	if contentScope != ContentScopeShelves {
		if _, err := tx.Exec(`DELETE FROM user_scope_shelves WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("clear user scope shelves: %w", err)
		}
		return nil
	}

	existingScopeShelves := map[string]struct{}{}
	rows, err := tx.Query(`SELECT shelf_id FROM user_scope_shelves WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("list existing scope shelves: %w", err)
	}
	for rows.Next() {
		var shelfID string
		if err := rows.Scan(&shelfID); err != nil {
			rows.Close()
			return fmt.Errorf("scan existing scope shelf: %w", err)
		}
		existingScopeShelves[shelfID] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close existing scope shelves: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate existing scope shelves: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM user_scope_shelves WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clear user scope shelves: %w", err)
	}
	seenShelves := make(map[string]struct{}, len(scopeShelfIDs))
	for _, shelfID := range scopeShelfIDs {
		shelfID = strings.TrimSpace(shelfID)
		if shelfID == "" {
			continue
		}
		if _, ok := seenShelves[shelfID]; ok {
			continue
		}
		seenShelves[shelfID] = struct{}{}
		var ownerID, visibility, kind string
		var query, queryMatch sql.NullString
		if err := tx.QueryRow(`
			SELECT owner_id, visibility, kind, query, query_match
			FROM shelves
			WHERE id = ?
		`, shelfID).Scan(&ownerID, &visibility, &kind, &query, &queryMatch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrShelfNotFound
			}
			return fmt.Errorf("check scope shelf: %w", err)
		}
		if visibility != string(ShelfShared) && ownerID == userID {
			return ErrScopeShelfNotVisible
		}
		if visibility != string(ShelfShared) && ownerID != shelfViewerID {
			if _, ok := existingScopeShelves[shelfID]; !ok {
				return ErrScopeShelfNotVisible
			}
		}
		scopeShelf := Shelf{Kind: ShelfKind(kind), Query: query.String, QueryMatch: queryMatch.String}
		if err := scopeShelf.scopeEligibilityError(); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO user_scope_shelves (user_id, shelf_id) VALUES (?, ?)`,
			userID, shelfID,
		); err != nil {
			return fmt.Errorf("insert user scope shelf: %w", err)
		}
	}
	return nil
}

// SetUserPassword replaces the stored password hash for an account.
func (db *DB) SetUserPassword(userID, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	res, err := db.Exec(
		"UPDATE users SET password_hash = ?, updated_at = unixepoch() WHERE id = ?",
		hash, userID,
	)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteUser removes an account by id.
func (db *DB) DeleteUser(userID string) error {
	return db.Transact(context.Background(), func(tx *sql.Tx) error {
		if err := guardAdminRemoval(tx, userID); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM users WHERE id = ?", userID)
		if err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// guardAdminRemoval must run in the same immediate transaction as the role
// change or deletion. Non-admin accounts need no special handling.
func guardAdminRemoval(tx *sql.Tx, userID string) error {
	var role string
	var hasOtherAdmin int
	err := tx.QueryRow(`
		SELECT role, EXISTS(
			SELECT 1 FROM users AS other
			WHERE other.role = ? AND other.id <> ?
		)
		FROM users
		WHERE id = ?
	`, RoleAdmin, userID, userID).Scan(&role, &hasOtherAdmin)
	if err != nil {
		return fmt.Errorf("check remaining admins: %w", err)
	}
	if role == RoleAdmin && hasOtherAdmin == 0 {
		return ErrLastAdmin
	}
	return nil
}

// Authenticate verifies a username/password pair and returns the matching user.
// It returns (nil, nil) on any mismatch (unknown user or wrong password) so both
// yield the same "invalid credentials" response. The responses are identical;
// the timings are not, because an unknown username returns before bcrypt runs.
// That enumeration oracle is accepted deliberately for a household-sized user
// list: usernames are not treated as secrets, while a dummy bcrypt comparison
// would add CPU cost to every unknown-user attempt without protecting passwords.
func (db *DB) Authenticate(username, password string) (*User, error) {
	u, err := db.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, nil
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, nil
	}
	return u, nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
// modernc.org/sqlite surfaces these in the error text; matching on the message
// avoids importing the driver's error type into this layer.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
