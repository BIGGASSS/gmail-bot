package database

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/models"
)

func seedPendingRelogAccount(t *testing.T, db *Database) (UpsertGoogleAccountParams, *models.GoogleAccount) {
	t.Helper()
	ctx := context.Background()
	now := UTCNow().Add(-24 * time.Hour)
	p := UpsertGoogleAccountParams{TelegramUserID: 1, GmailEmail: "user@example.com", AccessToken: "expired-access", RefreshToken: "expired-refresh", TokenExpiry: now, ConnectedAt: now, LastHistoryID: "history-123", ReloginPromptEnabled: false, ReloginPromptDelayDays: 17}
	if err := db.UpsertGoogleAccount(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkReloginPromptSent(ctx, 1, &now); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageDelivered(ctx, 1, "message", 1, 42); err != nil {
		t.Fatal(err)
	}
	a, err := db.GetGoogleAccount(ctx, 1)
	if err != nil || a == nil {
		t.Fatalf("account: %v, %v", a, err)
	}
	return p, a
}

func assertPendingRelogDeletion(t *testing.T, db *Database, a *models.GoogleAccount, wantDeleted bool) {
	t.Helper()
	ctx := context.Background()
	deleted, err := db.DeleteGoogleAccountGuarded(ctx, *a)
	if err != nil || deleted != wantDeleted {
		t.Fatalf("deleted = %v, err = %v; want %v", deleted, err, wantDeleted)
	}
	current, err := db.GetGoogleAccount(ctx, a.TelegramUserID)
	if err != nil {
		t.Fatal(err)
	}
	if wantDeleted && current != nil {
		t.Fatal("account survived deletion")
	}
	if !wantDeleted && !reflect.DeepEqual(a, current) {
		t.Fatalf("account metadata changed: before %+v, after %+v", a, current)
	}
	delivered, err := db.WasMessageDelivered(ctx, 1, "message")
	if err != nil || delivered == wantDeleted {
		t.Fatalf("delivery retained = %v, err = %v", delivered, err)
	}
}

func TestPendingRelogPreservesAccountThroughConsumptionAndCompletion(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	p, original := seedPendingRelogAccount(t, db)
	if err := db.StoreOAuthAttempt(ctx, "relog", 1, UTCNow().Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	assertPendingRelogDeletion(t, db, original, false)
	state, err := db.ConsumeOAuthState(ctx, "relog")
	if err != nil || state == nil {
		t.Fatalf("consume: %v, %v", state, err)
	}
	if replay, err := db.ConsumeOAuthState(ctx, "relog"); err != nil || replay != nil {
		t.Fatalf("state reused: %v, %v", replay, err)
	}
	assertPendingRelogDeletion(t, db, original, false)
	p.AccessToken, p.RefreshToken = "new-access", "new-refresh"
	p.LastHistoryID = "must-not-replace-history"
	if err := db.CompleteOAuth(ctx, *state, p); err != nil {
		t.Fatal(err)
	}
	current, err := db.GetGoogleAccount(ctx, 1)
	if err != nil || current == nil {
		t.Fatalf("account: %v, %v", current, err)
	}
	if current.LastHistoryID != original.LastHistoryID || !current.ConnectedAt.Equal(original.ConnectedAt) || current.ReloginPromptEnabled != original.ReloginPromptEnabled || current.ReloginPromptDelayDays != original.ReloginPromptDelayDays || current.ReloginPromptSentAt != nil || current.RefreshToken != p.RefreshToken {
		t.Fatalf("incorrect relog completion: %+v", current)
	}
	if delivered, err := db.WasMessageDelivered(ctx, 1, "message"); err != nil || !delivered {
		t.Fatalf("completion lost delivery: %v, %v", delivered, err)
	}
	// The completed attempt must no longer protect the new credentials.
	assertPendingRelogDeletion(t, db, current, true)
}

func TestPendingRelogProtectionInvalidation(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		for _, action := range []string{"expiry", "logout", "new-login", "new-expired-relog", "new-relog", "invalidate", "replace-account"} {
			t.Run(action+map[bool]string{false: "/unconsumed", true: "/consumed"}[consumed], func(t *testing.T) {
				ctx := context.Background()
				db := openTestDB(t)
				defer db.Close()
				p, original := seedPendingRelogAccount(t, db)
				if err := db.StoreOAuthAttempt(ctx, "old", 1, UTCNow().Add(time.Hour), true); err != nil {
					t.Fatal(err)
				}
				var state *models.OAuthState
				if consumed {
					var err error
					state, err = db.ConsumeOAuthState(ctx, "old")
					if err != nil || state == nil {
						t.Fatalf("consume: %v, %v", state, err)
					}
				}
				var err error
				switch action {
				case "expiry":
					// Advance persisted deadlines deterministically, with no sleeps.
					past := UTCNow().Add(-time.Hour)
					_, err = db.db.ExecContext(ctx, `UPDATE pending_relogs SET expires_at = ?`, ToISO8601(past))
					if err == nil {
						_, err = db.db.ExecContext(ctx, `UPDATE oauth_states SET expires_at = ?`, ToISO8601(past))
					}
					if state != nil {
						state.ExpiresAt = past
					}
				case "logout":
					_, err = db.Logout(ctx, 1)
				case "new-login":
					err = db.StoreOAuthAttempt(ctx, "new", 1, UTCNow().Add(time.Hour), false)
				case "new-expired-relog":
					err = db.StoreOAuthAttempt(ctx, "new", 1, UTCNow().Add(-time.Hour), true)
				case "new-relog":
					err = db.StoreOAuthAttempt(ctx, "new", 1, UTCNow().Add(time.Hour), true)
				case "invalidate":
					err = db.DeleteOAuthStatesForUser(ctx, 1)
				case "replace-account":
					err = db.UpsertGoogleAccount(ctx, p)
					if err == nil {
						original, err = db.GetGoogleAccount(ctx, 1)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				// Exercise expiry without a cleanup sweep; consumed attempts no longer
				// have oauth_states rows for that sweep to remove anyway.
				if action != "expiry" {
					if err := db.CleanupExpiredOAuthStates(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if action == "logout" {
					account, err := db.GetGoogleAccount(ctx, 1)
					if err != nil || account != nil {
						t.Fatalf("logout kept account: %v, %v", account, err)
					}
					if delivered, err := db.WasMessageDelivered(ctx, 1, "message"); err != nil || delivered {
						t.Fatalf("logout kept delivery: %v, %v", delivered, err)
					}
				} else {
					assertPendingRelogDeletion(t, db, original, action != "new-relog")
				}
				if state != nil {
					if err := db.CompleteOAuth(ctx, *state, p); err != ErrStaleAuthorization {
						t.Fatalf("invalidated callback completed: %v", err)
					}
				} else if old, err := db.ConsumeOAuthState(ctx, "old"); err != nil || old != nil {
					t.Fatalf("invalidated state consumed: %v, %v", old, err)
				}
			})
		}
	}
}

func TestPendingRelogMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	if err := db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	p, original := seedPendingRelogAccount(t, db)
	if err := db.StoreOAuthAttempt(ctx, "relog", 1, UTCNow().Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	// Recreate the previous schema, where only the single-use state held expiry.
	if _, err := db.db.ExecContext(ctx, `DROP TABLE pending_relogs`); err != nil {
		t.Fatal(err)
	}
	restart := func() {
		t.Helper()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Initialize(ctx); err != nil {
			t.Fatal(err)
		}
	}
	restart()
	assertPendingRelogDeletion(t, db, original, false)
	state, err := db.ConsumeOAuthState(ctx, "relog")
	if err != nil || state == nil {
		t.Fatalf("consume: %v, %v", state, err)
	}
	restart()
	assertPendingRelogDeletion(t, db, original, false)
	if err := db.CompleteOAuth(ctx, *state, p); err != nil {
		t.Fatal(err)
	}
}
