package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/levmv/polka/internal/id"
)

// AppToken is a long-lived per-device credential ("app password"). Its secret
// remains available to its owner so device setup can be resumed at any time.
type AppToken struct {
	ID         string
	Name       string
	Token      string
	CreatedAt  int64
	LastUsedAt sql.NullInt64
}

// ErrTokenNameExists is returned when a user already has a token with the given
// name (names are unique per user so they can be revoked by name).
var ErrTokenNameExists = errors.New("token name already in use")

// ErrInvalidAppTokenInput classifies token values that are safe to return as a
// client error; its concrete error retains the useful validation detail.
var ErrInvalidAppTokenInput = errors.New("invalid app token input")

// CreateAppToken issues and stores a random token for a user. name must be
// non-empty and unique for that user.
func (db *DB) CreateAppToken(ctx context.Context, userID int64, name string) (*AppToken, error) {
	if name == "" {
		return nil, errorWithDetail(ErrInvalidAppTokenInput, "token name must not be empty")
	}

	token := &AppToken{ID: id.New(id.AppToken), Name: name, Token: newDeviceToken()}
	err := db.Transact(ctx, func(tx *Tx) error {
		return tx.QueryRow(
			"INSERT INTO app_tokens (id, user_id, name, token) VALUES (?, ?, ?, ?) RETURNING created_at",
			token.ID, userID, token.Name, token.Token,
		).Scan(&token.CreatedAt)
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTokenNameExists
		}
		return nil, fmt.Errorf("insert app token: %w", err)
	}
	return token, nil
}

// ListAppTokens returns a user's tokens, including their secrets, newest first.
func ListAppTokens(queryer Queryer, userID int64) ([]AppToken, error) {
	rows, err := queryer.Query(
		"SELECT id, name, token, created_at, last_used_at FROM app_tokens WHERE user_id = ? ORDER BY created_at DESC",
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list app tokens: %w", err)
	}
	defer rows.Close()

	var tokens []AppToken
	for rows.Next() {
		var t AppToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Token, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan app token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// RevokeAppToken deletes a user's token by name. Returns sql.ErrNoRows if no such
// token exists for that user.
func (db *DB) RevokeAppToken(ctx context.Context, userID int64, name string) error {
	res, err := db.Write(ctx).Exec("DELETE FROM app_tokens WHERE user_id = ? AND name = ?", userID, name)
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
func (db *DB) RevokeAppTokenByID(ctx context.Context, userID int64, tokenID string) error {
	res, err := db.Write(ctx).Exec("DELETE FROM app_tokens WHERE user_id = ? AND id = ?", userID, tokenID)
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
func (db *DB) AppTokenUserID(ctx context.Context, token string) (int64, bool, error) {
	token = normalizeDeviceToken(token)
	if token == "" {
		return 0, false, nil
	}
	var userID int64
	var lastUsed sql.NullInt64
	err := db.Read(ctx).QueryRow(
		"SELECT user_id, last_used_at FROM app_tokens WHERE token = ?", token,
	).Scan(&userID, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lookup app token: %w", err)
	}

	now := time.Now().Unix()
	if !lastUsed.Valid || now-lastUsed.Int64 >= 3600 {
		if _, err := db.ExecBestEffort(ctx, "UPDATE app_tokens SET last_used_at = ? WHERE token = ?", now, token); err != nil {
			return 0, false, fmt.Errorf("bump app token: %w", err)
		}
	}
	return userID, true, nil
}
