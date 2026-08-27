package db

import (
	"errors"
	"testing"
)

func TestAppTokenLifecycle(t *testing.T) {
	database := newTestDB(t)

	u, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	token, err := database.CreateAppToken(u.ID, "kobo")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if len(token) < 32 {
		t.Fatalf("token looks too short: %q", token)
	}

	uid, ok, err := database.AppTokenUserID(token)
	if err != nil || !ok || uid != u.ID {
		t.Fatalf("lookup token: uid=%q ok=%v err=%v, want %q/true", uid, ok, err, u.ID)
	}

	if uid, ok, err := database.AppTokenUserID("deadbeef"); ok || uid != "" || err != nil {
		t.Fatalf("unknown token: uid=%q ok=%v err=%v, want empty/false/nil", uid, ok, err)
	}

	tokens, err := database.ListAppTokens(u.ID)
	if err != nil || len(tokens) != 1 || tokens[0].Name != "kobo" {
		t.Fatalf("list tokens: %+v err=%v", tokens, err)
	}
	if !tokens[0].LastUsedAt.Valid {
		t.Errorf("last_used_at should be set after a successful lookup")
	}

	if _, err := database.CreateAppToken(u.ID, "kobo"); !errors.Is(err, ErrTokenNameExists) {
		t.Errorf("duplicate name: got %v, want ErrTokenNameExists", err)
	}
	if _, err := database.CreateAppToken(u.ID, ""); !errors.Is(err, ErrInvalidAppTokenInput) {
		t.Errorf("empty name: got %v, want ErrInvalidAppTokenInput", err)
	}

	if err := database.RevokeAppToken(u.ID, "kobo"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok, _ := database.AppTokenUserID(token); ok {
		t.Errorf("revoked token still resolves")
	}
	if err := database.RevokeAppToken(u.ID, "kobo"); err == nil {
		t.Errorf("revoking a missing token should error")
	}
}

func TestAppTokenStoresOnlyHash(t *testing.T) {
	database := newTestDB(t)
	u, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, err := database.CreateAppToken(u.ID, "device")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	var stored string
	if err := database.QueryRow("SELECT token_hash FROM app_tokens WHERE user_id = ?", u.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored hash: %v", err)
	}
	if stored == token {
		t.Errorf("raw token stored in the database")
	}
	if stored != appTokenHash(token) {
		t.Errorf("stored value is not sha256(token)")
	}
}
