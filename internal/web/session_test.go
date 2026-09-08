package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
)

func TestSessionLookupWhileWriterIsHeld(t *testing.T) {
	database, _ := setupTestDB(t)
	defer database.Close()
	user := mustUser(t, database, "session-busy", db.RoleMember)
	now := time.Unix(1700000000, 0)
	store := newSessionStore(database)
	store.now = func() time.Time { return now }
	sid, err := store.issue(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Timestamp maintenance can be skipped while an import owns the writer.
	now = now.Add(2 * time.Hour)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if uid, ok, err := store.lookup(ctx, sid); err != nil || !ok || uid != user.ID {
		t.Fatalf("lookup with busy writer = %d, %v, %v", uid, ok, err)
	}
	// Skipping expiry cleanup must never make an expired credential valid.
	now = now.Add(sessionIdleTTL)
	if _, ok, err := store.lookup(ctx, sid); err != nil || ok {
		t.Fatalf("expired lookup with busy writer = %v, %v", ok, err)
	}
}

func TestSessionStorePersistsAcrossDBReopen(t *testing.T) {
	database, dir := setupTestDB(t)

	u := mustUser(t, database, "alice", db.RoleMember)

	sid, err := newSessionStore(database).issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	database.Close()

	reopened, err := db.InitPath(filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer reopened.Close()

	uid, ok, err := newSessionStore(reopened).lookup(t.Context(), sid)
	if err != nil {
		t.Fatalf("lookup session: %v", err)
	}
	if !ok || uid != u.ID {
		t.Fatalf("lookup = (%d, %v), want (%d, true)", uid, ok, u.ID)
	}

	var storedHash []byte
	if err := reopened.Read(t.Context()).QueryRow("SELECT token_hash FROM sessions").Scan(&storedHash); err != nil {
		t.Fatalf("query session hash: %v", err)
	}
	wantHash := sha256.Sum256([]byte(sid))
	if !bytes.Equal(storedHash, wantHash[:]) {
		t.Fatalf("stored hash = %x, want %x", storedHash, wantHash)
	}
}

func TestSessionStoreIdleExpiryAndLastSeenBump(t *testing.T) {
	database, _ := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "alice", db.RoleMember)

	now := time.Unix(1700000000, 0)
	store := newSessionStore(database)
	store.now = func() time.Time { return now }

	sid, err := store.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	now = now.Add(2 * time.Hour)
	if uid, ok, err := store.lookup(t.Context(), sid); err != nil || !ok || uid != u.ID {
		t.Fatalf("lookup after activity = (%d, %v, %v), want (%d, true, nil)", uid, ok, err, u.ID)
	}

	var lastSeen int64
	if err := database.Read(t.Context()).QueryRow("SELECT last_seen_at FROM sessions WHERE token_hash = ?", sessionTokenHash(sid)).Scan(&lastSeen); err != nil {
		t.Fatalf("query last_seen_at: %v", err)
	}
	if lastSeen != now.Unix() {
		t.Fatalf("last_seen_at = %d, want %d", lastSeen, now.Unix())
	}

	now = now.Add(sessionIdleTTL + time.Second)
	if uid, ok, err := store.lookup(t.Context(), sid); err != nil || ok {
		t.Fatalf("expired lookup = (%d, %v, %v), want not live", uid, ok, err)
	}
	assertSessionRows(t, database, sid, 0)
}

func TestSessionStoreAbsoluteExpiry(t *testing.T) {
	database, _ := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "alice", db.RoleMember)

	start := time.Unix(1700000000, 0)
	now := start
	store := newSessionStore(database)
	store.now = func() time.Time { return now }

	sid, err := store.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	recentLastSeen := start.Add(sessionAbsoluteTTL - time.Hour).Unix()
	mustExec(t, database, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", recentLastSeen, sessionTokenHash(sid))

	now = start.Add(sessionAbsoluteTTL + time.Second)
	if uid, ok, err := store.lookup(t.Context(), sid); err != nil || ok {
		t.Fatalf("absolute expired lookup = (%d, %v, %v), want not live", uid, ok, err)
	}
	assertSessionRows(t, database, sid, 0)
}

func TestSessionStoreRevokesOtherUserSessions(t *testing.T) {
	database, _ := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "alice", db.RoleMember)
	other := mustUser(t, database, "bob", db.RoleMember)

	store := newSessionStore(database)
	keepSID, err := store.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue kept session: %v", err)
	}
	dropSID, err := store.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue dropped session: %v", err)
	}
	otherSID, err := store.issue(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("issue other session: %v", err)
	}

	if err := store.revokeUserExcept(t.Context(), u.ID, keepSID); err != nil {
		t.Fatalf("revoke other sessions: %v", err)
	}
	assertSessionLive(t, store, keepSID, true)
	assertSessionLive(t, store, dropSID, false)
	assertSessionLive(t, store, otherSID, true)

	if err := store.revokeUser(t.Context(), u.ID); err != nil {
		t.Fatalf("revoke user: %v", err)
	}
	assertSessionLive(t, store, keepSID, false)
	assertSessionLive(t, store, otherSID, true)
}

func assertSessionRows(t *testing.T, database *db.DB, sid string, want int) {
	t.Helper()
	var got int
	if err := database.Read(t.Context()).QueryRow("SELECT COUNT(*) FROM sessions WHERE token_hash = ?", sessionTokenHash(sid)).Scan(&got); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if got != want {
		t.Fatalf("session row count = %d, want %d", got, want)
	}
}

func assertSessionLive(t *testing.T, store *sessionStore, sid string, want bool) {
	t.Helper()
	_, got, err := store.lookup(t.Context(), sid)
	if err != nil {
		t.Fatalf("lookup session: %v", err)
	}
	if got != want {
		t.Fatalf("session live = %v, want %v", got, want)
	}
}
