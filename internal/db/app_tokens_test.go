package db

import (
	"errors"
	"strings"
	"testing"
)

func TestAppTokenLifecycle(t *testing.T) {
	database := newTestDB(t)

	u := mustUser(t, database, "alice", RoleMember)

	token, err := database.CreateAppToken(t.Context(), u.ID, "kobo")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	uid, ok, err := database.AppTokenUserID(t.Context(), token.Token)
	if err != nil || !ok || uid != u.ID {
		t.Fatalf("lookup token: uid=%d ok=%v err=%v, want %d/true", uid, ok, err, u.ID)
	}
	if uid, ok, err := database.AppTokenUserID(t.Context(), strings.ToUpper(token.Token)); err != nil || !ok || uid != u.ID {
		t.Fatalf("uppercase token: uid=%d ok=%v err=%v", uid, ok, err)
	}

	if uid, ok, err := database.AppTokenUserID(t.Context(), "deadbeef"); ok || uid != 0 || err != nil {
		t.Fatalf("unknown token: uid=%d ok=%v err=%v, want zero/false/nil", uid, ok, err)
	}

	tokens, err := ListAppTokens(database.Read(t.Context()), u.ID)
	if err != nil || len(tokens) != 1 || tokens[0].ID != token.ID || tokens[0].Token != token.Token {
		t.Fatalf("list tokens: %+v err=%v", tokens, err)
	}
	if !tokens[0].LastUsedAt.Valid {
		t.Errorf("last_used_at should be set after a successful lookup")
	}

	if _, err := database.CreateAppToken(t.Context(), u.ID, "kobo"); !errors.Is(err, ErrTokenNameExists) {
		t.Errorf("duplicate name: got %v, want ErrTokenNameExists", err)
	}
	if _, err := database.CreateAppToken(t.Context(), u.ID, ""); !errors.Is(err, ErrInvalidAppTokenInput) {
		t.Errorf("empty name: got %v, want ErrInvalidAppTokenInput", err)
	}

	if err := database.RevokeAppToken(t.Context(), u.ID, "kobo"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok, _ := database.AppTokenUserID(t.Context(), token.Token); ok {
		t.Errorf("revoked token still resolves")
	}
	if err := database.RevokeAppToken(t.Context(), u.ID, "kobo"); err == nil {
		t.Errorf("revoking a missing token should error")
	}
}
