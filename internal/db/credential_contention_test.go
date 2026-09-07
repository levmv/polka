package db

import (
	"context"
	"testing"
	"time"
)

func TestDeviceAuthenticationWhileWriterIsHeld(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser(t.Context(), "device-busy", "pw", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	token, err := database.CreateAppToken(t.Context(), user.ID, "OPDS")
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := database.CreateShelf(t.Context(), user.ID, ShelfPersonal, "Kobo", ShelfManual, "")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.ReplaceKoboConnection(t.Context(), user.ID, shelf.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginWrite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	// Fresh credentials would normally update last_used_at. That optional
	// write must not prevent either device from reading during an import.
	if uid, ok, err := database.AppTokenUserID(ctx, token.Token); err != nil || !ok || uid != user.ID {
		t.Fatalf("app token with busy writer = %d, %v, %v", uid, ok, err)
	}
	if got, ok, err := database.KoboConnectionByToken(ctx, connection.Token); err != nil || !ok || got.UserID != user.ID {
		t.Fatalf("Kobo with busy writer = %+v, %v, %v", got, ok, err)
	}
}
