package web

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
)

func TestSessionStorePersistsAcrossDBReopen(t *testing.T) {
	database, dir := setupTestDB(t)

	u := mustUser(t, database, "Alice", db.RoleMember)

	sid, err := newSessionStore(database).issue(u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	database.Close()

	reopened, err := db.InitPath(filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer reopened.Close()

	uid, ok, err := newSessionStore(reopened).lookup(sid)
	if err != nil {
		t.Fatalf("lookup session: %v", err)
	}
	if !ok || uid != u.ID {
		t.Fatalf("lookup = (%q, %v), want (%q, true)", uid, ok, u.ID)
	}

	var storedHash string
	if err := reopened.QueryRow("SELECT token_hash FROM sessions").Scan(&storedHash); err != nil {
		t.Fatalf("query session hash: %v", err)
	}
	if storedHash == sid {
		t.Fatal("raw session token was stored in SQLite")
	}
	if want := sessionTokenHash(sid); storedHash != want {
		t.Fatalf("stored hash = %q, want %q", storedHash, want)
	}
}

func TestSessionStoreIdleExpiryAndLastSeenBump(t *testing.T) {
	database, _ := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "Alice", db.RoleMember)

	now := time.Unix(1700000000, 0)
	store := newSessionStore(database)
	store.now = func() time.Time { return now }

	sid, err := store.issue(u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	now = now.Add(2 * time.Hour)
	if uid, ok, err := store.lookup(sid); err != nil || !ok || uid != u.ID {
		t.Fatalf("lookup after activity = (%q, %v, %v), want (%q, true, nil)", uid, ok, err, u.ID)
	}

	var lastSeen int64
	if err := database.QueryRow("SELECT last_seen_at FROM sessions WHERE token_hash = ?", sessionTokenHash(sid)).Scan(&lastSeen); err != nil {
		t.Fatalf("query last_seen_at: %v", err)
	}
	if lastSeen != now.Unix() {
		t.Fatalf("last_seen_at = %d, want %d", lastSeen, now.Unix())
	}

	now = now.Add(sessionIdleTTL + time.Second)
	if uid, ok, err := store.lookup(sid); err != nil || ok {
		t.Fatalf("expired lookup = (%q, %v, %v), want not live", uid, ok, err)
	}
	assertSessionRows(t, database, sid, 0)
}

func TestSessionStoreAbsoluteExpiry(t *testing.T) {
	database, _ := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "Alice", db.RoleMember)

	start := time.Unix(1700000000, 0)
	now := start
	store := newSessionStore(database)
	store.now = func() time.Time { return now }

	sid, err := store.issue(u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	recentLastSeen := start.Add(sessionAbsoluteTTL - time.Hour).Unix()
	if _, err := database.Exec("UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", recentLastSeen, sessionTokenHash(sid)); err != nil {
		t.Fatalf("set recent last_seen_at: %v", err)
	}

	now = start.Add(sessionAbsoluteTTL + time.Second)
	if uid, ok, err := store.lookup(sid); err != nil || ok {
		t.Fatalf("absolute expired lookup = (%q, %v, %v), want not live", uid, ok, err)
	}
	assertSessionRows(t, database, sid, 0)
}

func TestSessionStoreRevokesOtherUserSessions(t *testing.T) {
	database, _ := setupTestDB(t)
	defer database.Close()

	u := mustUser(t, database, "Alice", db.RoleMember)
	other := mustUser(t, database, "Bob", db.RoleMember)

	store := newSessionStore(database)
	keepSID, err := store.issue(u.ID)
	if err != nil {
		t.Fatalf("issue kept session: %v", err)
	}
	dropSID, err := store.issue(u.ID)
	if err != nil {
		t.Fatalf("issue dropped session: %v", err)
	}
	otherSID, err := store.issue(other.ID)
	if err != nil {
		t.Fatalf("issue other session: %v", err)
	}

	if err := store.revokeUserExcept(u.ID, keepSID); err != nil {
		t.Fatalf("revoke other sessions: %v", err)
	}
	assertSessionLive(t, store, keepSID, true)
	assertSessionLive(t, store, dropSID, false)
	assertSessionLive(t, store, otherSID, true)

	if err := store.revokeUser(u.ID); err != nil {
		t.Fatalf("revoke user: %v", err)
	}
	assertSessionLive(t, store, keepSID, false)
	assertSessionLive(t, store, otherSID, true)
}

func assertSessionRows(t *testing.T, database *db.DB, sid string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow("SELECT COUNT(*) FROM sessions WHERE token_hash = ?", sessionTokenHash(sid)).Scan(&got); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if got != want {
		t.Fatalf("session row count = %d, want %d", got, want)
	}
}

func assertSessionLive(t *testing.T, store *sessionStore, sid string, want bool) {
	t.Helper()
	_, got, err := store.lookup(sid)
	if err != nil {
		t.Fatalf("lookup session: %v", err)
	}
	if got != want {
		t.Fatalf("session live = %v, want %v", got, want)
	}
}
