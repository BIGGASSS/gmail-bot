package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInitializeKeepsUnusedRelogSchema(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	for _, table := range []string{"auth_versions", "oauth_attempts", "pending_relogs"} {
		var name string
		if err := db.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	rows, err := db.db.QueryContext(ctx, `PRAGMA table_info(google_accounts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "generation" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected google_accounts.generation to remain")
	}
}

func TestConsumeRetiredRelogDoesNotReturnLoginState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	expiresAt := UTCNow().Add(15 * time.Minute)
	if err := db.StoreOAuthState(ctx, "relog-state", 7, expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE oauth_attempts SET relog = 1 WHERE state = ?`, "relog-state"); err != nil {
		t.Fatal(err)
	}

	state, err := db.ConsumeOAuthState(ctx, "relog-state")
	if !errors.Is(err, ErrRetiredRelog) {
		t.Fatalf("err=%v", err)
	}
	if state == nil || state.TelegramUserID != 7 {
		t.Fatalf("state=%+v", state)
	}
	again, err := db.ConsumeOAuthState(ctx, "relog-state")
	if err != nil || again != nil {
		t.Fatalf("replay state=%+v err=%v", again, err)
	}
}

func TestConsumeLegacyLoginAttemptStillSucceeds(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	if err := db.StoreOAuthState(ctx, "login-state", 7, UTCNow().Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	state, err := db.ConsumeOAuthState(ctx, "login-state")
	if err != nil || state == nil || state.TelegramUserID != 7 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
