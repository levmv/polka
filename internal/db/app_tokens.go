package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/levmv/polka/internal/id"
)

// AppToken is a long-lived per-device credential ("app password"). The raw token
// is shown to the user exactly once at creation; only its SHA-256 hash is stored,
// so this struct never carries the secret.
type AppToken struct {
	ID         string
	Name       string
	CreatedAt  int64
	LastUsedAt sql.NullInt64
}

// ErrTokenNameExists is returned when a user already has a token with the given
// name (names are unique per user so they can be revoked by name).
var ErrTokenNameExists = errors.New("token name already in use")

// ErrInvalidAppTokenInput classifies token values that are safe to return as a
// client error; its concrete error retains the useful validation detail.
var ErrInvalidAppTokenInput = errors.New("invalid app token input")

// appTokenHash hashes a raw token the same way sessions hash theirs: the stored
// value is never the secret itself, so a DB dump is not a set of working
// credentials.
func appTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateAppToken issues a new random token for a user, stores only its hash, and
// returns the raw token (the single time it is ever available). name must be
// non-empty and unique for that user.
func (db *DB) CreateAppToken(userID, name string) (string, error) {
	if name == "" {
		return "", errorWithDetail(ErrInvalidAppTokenInput, "token name must not be empty")
	}

	// 16 bytes (128-bit) is ample for a personal-library credential and keeps the
	// hex string short enough to copy comfortably; only its hash is stored.
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate app token: %w", err)
	}
	token := hex.EncodeToString(buf)

	_, err := db.Exec(
		"INSERT INTO app_tokens (id, user_id, name, token_hash) VALUES (?, ?, ?, ?)",
		id.New(id.AppToken), userID, name, appTokenHash(token),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrTokenNameExists
		}
		return "", fmt.Errorf("insert app token: %w", err)
	}
	return token, nil
}

// ListAppTokens returns a user's tokens (no secrets), newest first.
func (db *DB) ListAppTokens(userID string) ([]AppToken, error) {
	rows, err := db.Query(
		"SELECT id, name, created_at, last_used_at FROM app_tokens WHERE user_id = ? ORDER BY created_at DESC",
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list app tokens: %w", err)
	}
	defer rows.Close()

	var tokens []AppToken
	for rows.Next() {
		var t AppToken
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan app token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// RevokeAppToken deletes a user's token by name. Returns sql.ErrNoRows if no such
// token exists for that user.
func (db *DB) RevokeAppToken(userID, name string) error {
	res, err := db.Exec("DELETE FROM app_tokens WHERE user_id = ? AND name = ?", userID, name)
	if err != nil {
		return fmt.Errorf("revoke app token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RevokeAppTokenByID deletes a user's token by id. It is used by the web UI so
// token names never have to become URL path components.
func (db *DB) RevokeAppTokenByID(userID, tokenID string) error {
	res, err := db.Exec("DELETE FROM app_tokens WHERE user_id = ? AND id = ?", userID, tokenID)
	if err != nil {
		return fmt.Errorf("revoke app token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AppTokenUserID resolves a raw token to its owning user id, reporting whether a
// live token matched. It opportunistically records last_used_at (throttled to at
// most once per hour, like session bumps) so ordinary OPDS browsing does not turn
// every request into a write.
func (db *DB) AppTokenUserID(token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	hash := appTokenHash(token)

	var userID string
	var lastUsed sql.NullInt64
	err := db.QueryRow(
		"SELECT user_id, last_used_at FROM app_tokens WHERE token_hash = ?", hash,
	).Scan(&userID, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup app token: %w", err)
	}

	now := time.Now().Unix()
	if !lastUsed.Valid || now-lastUsed.Int64 >= 3600 {
		if _, err := db.Exec("UPDATE app_tokens SET last_used_at = ? WHERE token_hash = ?", now, hash); err != nil {
			return "", false, fmt.Errorf("bump app token: %w", err)
		}
	}
	return userID, true, nil
}
