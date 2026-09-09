package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthorizationGenerationSurvivesLogoutAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	now := UTCNow()
	p := UpsertGoogleAccountParams{TelegramUserID: 1, GmailEmail: "user@example.com", AccessToken: "same", RefreshToken: "same", TokenExpiry: now, ConnectedAt: now, LastHistoryID: "1"}
	if err := db.UpsertGoogleAccount(ctx, p); err != nil {
		t.Fatal(err)
	}
	old, _ := db.GetGoogleAccount(ctx, 1)
	if err := db.StoreOAuthAttempt(ctx, "inflight", 1, now.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	state, err := db.ConsumeOAuthState(ctx, "inflight")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Logout(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertGoogleAccount(ctx, p); err != nil {
		t.Fatal(err)
	}
	current, _ := db.GetGoogleAccount(ctx, 1)
	if current.Generation <= old.Generation {
		t.Fatal("generation reused")
	}
	if err := db.CompleteOAuth(ctx, *state, p); err != ErrStaleAuthorization {
		t.Fatalf("old callback: %v", err)
	}
	if err := db.UpdateTokensGuarded(ctx, *old, "stale", now, nil); err != ErrStaleAuthorization {
		t.Fatalf("old tokens: %v", err)
	}
	if deleted, err := db.DeleteGoogleAccountGuarded(ctx, *old); err != nil || deleted {
		t.Fatal("old grant deleted new account")
	}
}

func TestOAuthCompletionExpiryAndReplay(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	now := UTCNow()
	if err := db.StoreOAuthState(ctx, "attempt", 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	state, err := db.ConsumeOAuthState(ctx, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	p := UpsertGoogleAccountParams{TelegramUserID: 1, GmailEmail: "user@example.com", AccessToken: "access", RefreshToken: "refresh", TokenExpiry: now, ConnectedAt: now, LastHistoryID: "1"}
	expired := *state
	expired.ExpiresAt = now.Add(-time.Second)
	if err := db.CompleteOAuth(ctx, expired, p); err != ErrStaleAuthorization {
		t.Fatalf("expired completion: %v", err)
	}
	if err := db.CompleteOAuth(ctx, *state, p); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteOAuth(ctx, *state, p); err != ErrStaleAuthorization {
		t.Fatalf("replayed completion: %v", err)
	}
}
