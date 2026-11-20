package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func connectViaOAuth(t *testing.T, db *Database, user int64, state, email string, connectedAt time.Time) error {
	t.Helper()
	ctx := context.Background()
	if err := db.StoreOAuthAttempt(ctx, state, user, UTCNow().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	consumed, err := db.ConsumeOAuthState(ctx, state)
	if err != nil || consumed == nil {
		t.Fatalf("consume %s: %+v %v", state, consumed, err)
	}
	return db.CompleteOAuth(ctx, *consumed, UpsertGoogleAccountParams{
		TelegramUserID: user,
		GmailEmail:     email,
		AccessToken:    "access-" + state,
		RefreshToken:   "refresh-" + state,
		TokenExpiry:    connectedAt.Add(time.Hour),
		LastHistoryID:  "1",
		ConnectedAt:    connectedAt,
	})
}

func TestCompleteOAuthAddsAccountsAndRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	now := UTCNow()

	if err := connectViaOAuth(t, db, 1, "first", "personal@example.com", now); err != nil {
		t.Fatal(err)
	}
	if err := connectViaOAuth(t, db, 1, "second", "work@example.com", now); err != nil {
		t.Fatal(err)
	}
	err := connectViaOAuth(t, db, 1, "third", "Personal@Example.com", now)
	if !errors.Is(err, ErrAccountAlreadyConnected) {
		t.Fatalf("expected duplicate rejection, got %v", err)
	}
	// Another Telegram user may still connect the same address.
	if err := connectViaOAuth(t, db, 2, "other-user", "personal@example.com", now); err != nil {
		t.Fatal(err)
	}

	accounts, err := db.ListGoogleAccountsForUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].GmailEmail != "personal@example.com" || accounts[1].GmailEmail != "work@example.com" {
		t.Fatalf("accounts=%+v", accounts)
	}
	if accounts[0].RefreshToken != "refresh-first" {
		t.Fatalf("duplicate login replaced credentials: %+v", accounts[0])
	}
	if accounts[0].Generation == accounts[1].Generation {
		t.Fatalf("accounts share a generation: %d", accounts[0].Generation)
	}
	all, err := db.ListGoogleAccounts(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("all accounts=%+v err=%v", all, err)
	}
}

func TestLogoutAccountRemovesOnlyThatAccount(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	now := UTCNow()
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if err := connectViaOAuth(t, db, 1, email, email, now); err != nil {
			t.Fatal(err)
		}
	}
	accounts, err := db.ListGoogleAccountsForUser(ctx, 1)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts=%+v err=%v", accounts, err)
	}
	first, second := accounts[0], accounts[1]
	// Gmail message IDs are per mailbox, so the same ID can be delivered for both.
	if err := db.MarkMessageDelivered(ctx, first.ID, "same-id", 1, 10); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageDelivered(ctx, second.ID, "same-id", 1, 11); err != nil {
		t.Fatal(err)
	}
	if err := db.StoreOAuthAttempt(ctx, "pending", 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if removed, err := db.LogoutAccount(ctx, 2, first.ID); err != nil || removed != nil {
		t.Fatalf("another user's logout removed the account: %+v %v", removed, err)
	}
	removed, err := db.LogoutAccount(ctx, 1, first.ID)
	if err != nil || removed == nil || removed.GmailEmail != "a@example.com" || removed.RefreshToken != first.RefreshToken {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	if gone, err := db.GetGoogleAccountByID(ctx, first.ID); err != nil || gone != nil {
		t.Fatalf("account remained: %+v %v", gone, err)
	}
	if delivered, err := db.WasMessageDelivered(ctx, first.ID, "same-id"); err != nil || delivered {
		t.Fatalf("deliveries for removed account remained: %v %v", delivered, err)
	}
	if delivered, err := db.WasMessageDelivered(ctx, second.ID, "same-id"); err != nil || !delivered {
		t.Fatalf("other account's deliveries removed: %v %v", delivered, err)
	}
	if state, err := db.ConsumeOAuthState(ctx, "pending"); err != nil || state != nil {
		t.Fatalf("logout kept a pending login link: %+v %v", state, err)
	}

	all, err := db.LogoutAll(ctx, 1)
	if err != nil || len(all) != 1 || all[0].ID != second.ID {
		t.Fatalf("logout all=%+v err=%v", all, err)
	}
}

func TestGetDeliveredMessageAccount(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	now := UTCNow()
	if err := connectViaOAuth(t, db, 1, "a", "a@example.com", now); err != nil {
		t.Fatal(err)
	}
	if err := connectViaOAuth(t, db, 1, "b", "b@example.com", now); err != nil {
		t.Fatal(err)
	}
	accounts, err := db.ListGoogleAccountsForUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageDelivered(ctx, accounts[1].ID, "gmail-b", 1, 42); err != nil {
		t.Fatal(err)
	}

	account, gmailID, err := db.GetDeliveredMessageAccount(ctx, 1, 42)
	if err != nil || account == nil || account.ID != accounts[1].ID || gmailID != "gmail-b" {
		t.Fatalf("account=%+v gmailID=%q err=%v", account, gmailID, err)
	}
	if account, _, err := db.GetDeliveredMessageAccount(ctx, 1, 43); err != nil || account != nil {
		t.Fatalf("unknown message resolved: %+v %v", account, err)
	}
	if _, err := db.LogoutAccount(ctx, 1, accounts[1].ID); err != nil {
		t.Fatal(err)
	}
	if account, _, err := db.GetDeliveredMessageAccount(ctx, 1, 42); err != nil || account != nil {
		t.Fatalf("removed account resolved: %+v %v", account, err)
	}
}

func TestSetReloginPromptDelayDaysReschedulesEveryAccount(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)
	for i, connectedAt := range []time.Time{first, second} {
		if _, err := db.UpsertGoogleAccount(ctx, UpsertGoogleAccountParams{
			TelegramUserID: 1,
			GmailEmail:     []string{"a@example.com", "b@example.com"}[i],
			AccessToken:    "access",
			RefreshToken:   "refresh",
			TokenExpiry:    connectedAt,
			ConnectedAt:    connectedAt,
			LastHistoryID:  "1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetReloginPromptDelayDays(ctx, 1, 3); err != nil {
		t.Fatal(err)
	}
	accounts, err := db.ListGoogleAccountsForUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, base := range []time.Time{first, second} {
		want := base.Add(3 * 24 * time.Hour)
		if accounts[i].ReloginPromptDelayDays != 3 || accounts[i].ReloginPromptDueAt == nil || !accounts[i].ReloginPromptDueAt.Equal(want) {
			t.Fatalf("account %d: delay=%d due=%v want %v", i, accounts[i].ReloginPromptDelayDays, accounts[i].ReloginPromptDueAt, want)
		}
	}
}

func TestSingleAccountSchemaMigratesToMultiAccount(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "single.db")
	raw, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// The schema shipped before multi-account support.
	if _, err := raw.db.Exec(`
CREATE TABLE auth_versions (telegram_user_id INTEGER PRIMARY KEY, generation INTEGER NOT NULL DEFAULT 0, attempt TEXT NOT NULL DEFAULT '');
CREATE TABLE google_accounts (
    telegram_user_id INTEGER PRIMARY KEY,
    gmail_email TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    token_expiry TEXT NOT NULL,
    last_history_id TEXT NOT NULL,
    connected_at TEXT NOT NULL,
    relogin_prompt_enabled INTEGER NOT NULL DEFAULT 1,
    relogin_prompt_delay_days INTEGER NOT NULL DEFAULT 6,
    relogin_prompt_base_at TEXT,
    relogin_prompt_due_at TEXT,
    relogin_prompt_sent_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE delivered_messages (
    telegram_user_id INTEGER NOT NULL,
    gmail_message_id TEXT NOT NULL,
    telegram_chat_id INTEGER NOT NULL,
    telegram_message_id INTEGER NOT NULL,
    delivered_at TEXT NOT NULL,
    PRIMARY KEY (telegram_user_id, gmail_message_id),
    FOREIGN KEY (telegram_user_id) REFERENCES google_accounts (telegram_user_id) ON DELETE CASCADE
);
INSERT INTO auth_versions (telegram_user_id, generation) VALUES (10, 4), (20, 7);
INSERT INTO google_accounts (telegram_user_id, gmail_email, access_token, refresh_token, token_expiry, last_history_id, connected_at, relogin_prompt_base_at, relogin_prompt_due_at, created_at, updated_at, generation) VALUES
    (10, 'ten@example.com', 'a10', 'r10', '2024-01-01T13:00:00+00:00', '100', '2024-01-01T12:00:00+00:00', '2024-01-01T12:00:00+00:00', '2024-01-07T12:00:00+00:00', '2024-01-01T12:00:00+00:00', '2024-01-01T12:00:00+00:00', 4),
    (20, 'twenty@example.com', 'a20', 'r20', '2024-01-01T13:00:00+00:00', '200', '2024-01-01T12:00:00+00:00', '2024-01-01T12:00:00+00:00', '2024-01-07T12:00:00+00:00', '2024-01-01T12:00:00+00:00', '2024-01-01T12:00:00+00:00', 7);
INSERT INTO delivered_messages VALUES (10, 'm1', 10, 1, '2024-01-01T12:00:00+00:00'), (20, 'm1', 20, 5, '2024-01-01T12:00:00+00:00');
`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ {
		if err := db.Initialize(ctx); err != nil {
			t.Fatalf("initialize %d: %v", i, err)
		}
	}

	for _, tc := range []struct {
		user       int64
		email      string
		history    string
		generation int64
		chatID     int64
		messageID  int
	}{
		{10, "ten@example.com", "100", 4, 10, 1},
		{20, "twenty@example.com", "200", 7, 20, 5},
	} {
		account, err := userAccount(ctx, db, tc.user)
		if err != nil || account == nil {
			t.Fatalf("user %d: %+v %v", tc.user, account, err)
		}
		if account.GmailEmail != tc.email || account.LastHistoryID != tc.history || account.Generation != tc.generation || account.ID == 0 {
			t.Fatalf("user %d migrated as %+v", tc.user, account)
		}
		if delivered, err := db.WasMessageDelivered(ctx, account.ID, "m1"); err != nil || !delivered {
			t.Fatalf("user %d delivery lost: %v %v", tc.user, delivered, err)
		}
		owner, gmailID, err := db.GetDeliveredMessageAccount(ctx, tc.chatID, tc.messageID)
		if err != nil || owner == nil || owner.ID != account.ID || gmailID != "m1" {
			t.Fatalf("user %d delivery lookup: %+v %q %v", tc.user, owner, gmailID, err)
		}
	}

	// Foreign keys are enforced again after the migration.
	if err := db.MarkMessageDelivered(ctx, 9999, "orphan", 1, 1); err == nil {
		t.Fatal("expected foreign key violation for unknown account")
	}
	// A migrated user can add a second account.
	if err := connectViaOAuth(t, db, 10, "add", "ten-work@example.com", UTCNow()); err != nil {
		t.Fatal(err)
	}
	accounts, err := db.ListGoogleAccountsForUser(ctx, 10)
	if err != nil || len(accounts) != 2 || accounts[1].Generation <= accounts[0].Generation {
		t.Fatalf("accounts after adding: %+v %v", accounts, err)
	}
	if _, err := db.LogoutAccount(ctx, 10, accounts[0].ID); err != nil {
		t.Fatal(err)
	}
	if delivered, err := db.WasMessageDelivered(ctx, accounts[0].ID, "m1"); err != nil || delivered {
		t.Fatalf("migrated deliveries did not cascade: %v %v", delivered, err)
	}
}
