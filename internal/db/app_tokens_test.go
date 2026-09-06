package db

import (
	"errors"
	"strings"
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
	uid, ok, err := database.AppTokenUserID(token.Token)
	if err != nil || !ok || uid != u.ID {
		t.Fatalf("lookup token: uid=%d ok=%v err=%v, want %d/true", uid, ok, err, u.ID)
	}
	if uid, ok, err := database.AppTokenUserID(strings.ToUpper(token.Token)); err != nil || !ok || uid != u.ID {
		t.Fatalf("uppercase token: uid=%d ok=%v err=%v", uid, ok, err)
	}

	if uid, ok, err := database.AppTokenUserID("deadbeef"); ok || uid != 0 || err != nil {
		t.Fatalf("unknown token: uid=%d ok=%v err=%v, want zero/false/nil", uid, ok, err)
	}

	tokens, err := database.ListAppTokens(u.ID)
	if err != nil || len(tokens) != 1 || tokens[0].ID != token.ID || tokens[0].Token != token.Token {
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
	if _, ok, _ := database.AppTokenUserID(token.Token); ok {
		t.Errorf("revoked token still resolves")
	}
	if err := database.RevokeAppToken(u.ID, "kobo"); err == nil {
		t.Errorf("revoking a missing token should error")
	}
}
