package web

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/levmv/polka/internal/db"
)

const (
	sessionCookieName  = "polka_session"
	sessionIdleTTL     = 90 * 24 * time.Hour
	sessionAbsoluteTTL = 365 * 24 * time.Hour
	sessionBumpEvery   = time.Hour
)

// sessionStore holds opaque session ids issued after a successful login. The
// browser cookie carries the random id; SQLite stores only its SHA-256 hash, so
// a DB dump does not contain ready-to-use bearer tokens. Sessions are persistent
// across server restarts, support multiple devices per user, and expire by both
// idle and absolute age.
type sessionStore struct {
	db  *db.DB
	now func() time.Time
}

func newSessionStore(database *db.DB) *sessionStore {
	return &sessionStore{db: database, now: time.Now}
}

// issue creates and records a new random session id (256 bits, hex-encoded)
// bound to userID. The returned id is the only value that can authenticate; only
// its hash is persisted.
func (s *sessionStore) issue(userID string) (string, error) {
	now := s.now().Unix()
	if err := s.cleanupExpired(now); err != nil {
		return "", err
	}

	buf := make([]byte, 32)
	rand.Read(buf)
	sid := hex.EncodeToString(buf)
	if _, err := s.db.Exec(`
		INSERT INTO sessions (token_hash, user_id, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, sessionTokenHash(sid), userID, now, now, now+int64(sessionAbsoluteTTL.Seconds())); err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}
	return sid, nil
}

// lookup returns the user id bound to a session id, and whether the session is
// live. Expired rows are removed opportunistically. last_seen_at is bumped at
// most once per sessionBumpEvery so ordinary page loads and asset requests do
// not turn every authenticated request into a write.
func (s *sessionStore) lookup(sid string) (string, bool, error) {
	if sid == "" {
		return "", false, nil
	}

	now := s.now().Unix()
	tokenHash := sessionTokenHash(sid)

	var uid string
	var lastSeen, expiresAt int64
	err := s.db.QueryRow(`
		SELECT user_id, last_seen_at, expires_at
		FROM sessions
		WHERE token_hash = ?
	`, tokenHash).Scan(&uid, &lastSeen, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup session: %w", err)
	}

	if sessionExpired(now, lastSeen, expiresAt) {
		if err := s.deleteByHash(tokenHash); err != nil {
			return "", false, err
		}
		return "", false, nil
	}

	if now-lastSeen >= int64(sessionBumpEvery.Seconds()) {
		if _, err := s.db.Exec("UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", now, tokenHash); err != nil {
			return "", false, fmt.Errorf("bump session: %w", err)
		}
	}

	return uid, true, nil
}

// revoke drops a session id (logout). A no-op for an unknown id.
func (s *sessionStore) revoke(sid string) error {
	if sid == "" {
		return nil
	}
	return s.deleteByHash(sessionTokenHash(sid))
}

// revokeUser drops every live browser session for userID. Admin password resets
// use this to force the target user to log in again on every device.
func (s *sessionStore) revokeUser(userID string) error {
	if _, err := s.db.Exec("DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

// revokeUserExcept drops every session for userID except keepSID. Password
// self-change uses this to preserve the browser that initiated the change while
// invalidating other devices.
func (s *sessionStore) revokeUserExcept(userID, keepSID string) error {
	if keepSID == "" {
		return s.revokeUser(userID)
	}
	if _, err := s.db.Exec(
		"DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?",
		userID,
		sessionTokenHash(keepSID),
	); err != nil {
		return fmt.Errorf("revoke other user sessions: %w", err)
	}
	return nil
}

func (s *sessionStore) cleanupExpired(now int64) error {
	if _, err := s.db.Exec(`
		DELETE FROM sessions
		WHERE expires_at <= ? OR last_seen_at <= ?
	`, now, now-int64(sessionIdleTTL.Seconds())); err != nil {
		return fmt.Errorf("cleanup sessions: %w", err)
	}
	return nil
}

func (s *sessionStore) deleteByHash(tokenHash string) error {
	if _, err := s.db.Exec("DELETE FROM sessions WHERE token_hash = ?", tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func sessionTokenHash(sid string) string {
	sum := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(sum[:])
}

func sessionExpired(now, lastSeen, expiresAt int64) bool {
	return expiresAt <= now || lastSeen <= now-int64(sessionIdleTTL.Seconds())
}
