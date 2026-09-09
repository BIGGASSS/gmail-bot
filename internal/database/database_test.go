package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/models"
)

func TestDatabaseStoresAndConsumesOAuthState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	expiresAt := UTCNow().Add(5 * time.Minute)
	if err := db.StoreOAuthState(ctx, "state-1", 123, expiresAt); err != nil {
		t.Fatalf("store oauth state: %v", err)
	}

	state, err := db.ConsumeOAuthState(ctx, "state-1")
	if err != nil {
		t.Fatalf("consume oauth state: %v", err)
	}
	if state == nil {
		t.Fatal("expected oauth state")
	}
	if state.TelegramUserID != 123 {
		t.Fatalf("expected user 123, got %d", state.TelegramUserID)
	}

	second, err := db.ConsumeOAuthState(ctx, "state-1")
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if second != nil {
		t.Fatal("expected state to be single-use")
	}
}

func TestDatabaseFilePermissions(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "data", "bot.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.Initialize(ctx); err != nil {
		t.Fatalf("initialize db: %v", err)
	}

	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected db file mode 0600, got %o", perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("stat db dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("expected db dir mode 0700, got %o", perm)
	}
}

func TestDatabaseDeleteOAuthStatesForUser(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	expiresAt := UTCNow().Add(5 * time.Minute)
	if err := db.StoreOAuthState(ctx, "state-user-100", 100, expiresAt); err != nil {
		t.Fatalf("store state for user 100: %v", err)
	}
	if err := db.StoreOAuthState(ctx, "state-user-200", 200, expiresAt); err != nil {
		t.Fatalf("store state for user 200: %v", err)
	}

	if err := db.DeleteOAuthStatesForUser(ctx, 100); err != nil {
		t.Fatalf("delete states for user 100: %v", err)
	}

	deleted, err := db.ConsumeOAuthState(ctx, "state-user-100")
	if err != nil {
		t.Fatalf("consume user 100 state: %v", err)
	}
	if deleted != nil {
		t.Fatal("expected user 100 state to be deleted")
	}

	kept, err := db.ConsumeOAuthState(ctx, "state-user-200")
	if err != nil {
		t.Fatalf("consume user 200 state: %v", err)
	}
	if kept == nil {
		t.Fatal("expected user 200 state to remain")
	}
	if kept.TelegramUserID != 200 {
		t.Fatalf("expected user 200, got %d", kept.TelegramUserID)
	}
}

func TestDatabaseTracksDeliveredMessages(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	now := UTCNow()
	if err := db.UpsertGoogleAccount(ctx, UpsertGoogleAccountParams{
		TelegramUserID:         321,
		GmailEmail:             "user@example.com",
		AccessToken:            "access-token",
		RefreshToken:           "refresh-token",
		TokenExpiry:            now.Add(time.Hour),
		LastHistoryID:          "100",
		ConnectedAt:            now,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: models.DefaultReloginPromptDelayDays,
	}); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	if err := db.MarkMessageDelivered(ctx, 321, "gmail-message-1", 321, 10); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	delivered, err := db.WasMessageDelivered(ctx, 321, "gmail-message-1")
	if err != nil {
		t.Fatalf("was delivered: %v", err)
	}
	if !delivered {
		t.Fatal("expected message to be marked delivered")
	}
}

func TestDatabaseTracksManualReloginPromptSchedule(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	connectedAt := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertGoogleAccount(ctx, UpsertGoogleAccountParams{
		TelegramUserID:         654,
		GmailEmail:             "user@example.com",
		AccessToken:            "access-token",
		RefreshToken:           "refresh-token",
		TokenExpiry:            connectedAt.Add(time.Hour),
		LastHistoryID:          "100",
		ConnectedAt:            connectedAt,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: models.DefaultReloginPromptDelayDays,
	}); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	account, err := db.GetGoogleAccount(ctx, 654)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account == nil {
		t.Fatal("expected account")
	}
	if !account.ReloginPromptEnabled {
		t.Fatal("expected relogin prompt enabled")
	}
	if account.ReloginPromptDelayDays != models.DefaultReloginPromptDelayDays {
		t.Fatalf("unexpected delay days: %d", account.ReloginPromptDelayDays)
	}
	if account.ReloginPromptBaseAt == nil || !account.ReloginPromptBaseAt.Equal(connectedAt) {
		t.Fatalf("unexpected base at: %v", account.ReloginPromptBaseAt)
	}
	wantDue := connectedAt.Add(models.ManualReloginPromptDelay)
	if account.ReloginPromptDueAt == nil || !account.ReloginPromptDueAt.Equal(wantDue) {
		t.Fatalf("unexpected due at: %v", account.ReloginPromptDueAt)
	}
	if account.ReloginPromptSentAt != nil {
		t.Fatalf("expected sent_at nil, got %v", account.ReloginPromptSentAt)
	}

	if err := db.SetReloginPromptEnabled(ctx, 654, false); err != nil {
		t.Fatalf("disable prompt: %v", err)
	}
	account, err = db.GetGoogleAccount(ctx, 654)
	if err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if account.ReloginPromptEnabled {
		t.Fatal("expected prompt disabled")
	}
	prefs, err := db.GetReloginPromptPreferences(ctx, 654)
	if err != nil {
		t.Fatalf("get prefs: %v", err)
	}
	if prefs.ReloginPromptEnabled {
		t.Fatal("expected preferences disabled")
	}

	if err := db.SetReloginPromptDelayDays(ctx, 654, 3); err != nil {
		t.Fatalf("set delay: %v", err)
	}
	account, err = db.GetGoogleAccount(ctx, 654)
	if err != nil {
		t.Fatalf("reload after delay: %v", err)
	}
	if account.ReloginPromptDelayDays != 3 {
		t.Fatalf("expected delay 3, got %d", account.ReloginPromptDelayDays)
	}
	wantDue = connectedAt.Add(3 * 24 * time.Hour)
	if account.ReloginPromptDueAt == nil || !account.ReloginPromptDueAt.Equal(wantDue) {
		t.Fatalf("unexpected due at after delay change: %v", account.ReloginPromptDueAt)
	}
	prefs, err = db.GetReloginPromptPreferences(ctx, 654)
	if err != nil {
		t.Fatalf("get prefs after delay: %v", err)
	}
	if prefs.ReloginPromptDelayDays != 3 {
		t.Fatalf("expected preference delay 3, got %d", prefs.ReloginPromptDelayDays)
	}

	sentAt := connectedAt.Add(3*24*time.Hour + time.Minute)
	if err := db.MarkReloginPromptSent(ctx, 654, &sentAt); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	account, err = db.GetGoogleAccount(ctx, 654)
	if err != nil {
		t.Fatalf("reload after sent: %v", err)
	}
	if account.ReloginPromptSentAt == nil || !account.ReloginPromptSentAt.Equal(sentAt) {
		t.Fatalf("unexpected sent_at: %v", account.ReloginPromptSentAt)
	}
}

func TestLegacySchemaMigrationAndBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Create a pre-reminder schema similar to older Python databases.
	raw, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := raw.db.Exec(`
DROP TABLE IF EXISTS google_accounts;
DROP TABLE IF EXISTS delivered_messages;
DROP TABLE IF EXISTS relogin_prompt_preferences;
DROP TABLE IF EXISTS oauth_states;
CREATE TABLE google_accounts (
    telegram_user_id INTEGER PRIMARY KEY,
    gmail_email TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    token_expiry TEXT NOT NULL,
    last_history_id TEXT NOT NULL,
    connected_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
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
CREATE TABLE oauth_states (
    state TEXT PRIMARY KEY,
    telegram_user_id INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	connectedAt := "2024-01-01T12:00:00+00:00"
	if _, err := raw.db.Exec(`
INSERT INTO google_accounts (
    telegram_user_id, gmail_email, access_token, refresh_token, token_expiry,
    last_history_id, connected_at, created_at, updated_at
) VALUES (99, 'legacy@example.com', 'access', 'refresh', '2024-01-01T13:00:00+00:00', '10', ?, ?, ?)
`, connectedAt, connectedAt, connectedAt); err != nil {
		t.Fatalf("insert legacy account: %v", err)
	}
	if _, err := raw.db.Exec(`
INSERT INTO delivered_messages (
    telegram_user_id, gmail_message_id, telegram_chat_id, telegram_message_id, delivered_at
) VALUES (99, 'msg-1', 99, 1, ?)
`, connectedAt); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}
	_ = raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if err := db.Initialize(ctx); err != nil {
		t.Fatalf("initialize migration: %v", err)
	}

	account, err := db.GetGoogleAccount(ctx, 99)
	if err != nil {
		t.Fatalf("get migrated account: %v", err)
	}
	if account == nil {
		t.Fatal("expected migrated account")
	}
	if !account.ReloginPromptEnabled {
		t.Fatal("expected enabled after migration")
	}
	if account.ReloginPromptDelayDays != models.DefaultReloginPromptDelayDays {
		t.Fatalf("unexpected delay: %d", account.ReloginPromptDelayDays)
	}
	if account.ReloginPromptBaseAt == nil {
		t.Fatal("expected base_at backfill")
	}
	if account.ReloginPromptDueAt == nil {
		t.Fatal("expected due_at backfill")
	}
	prefs, err := db.GetReloginPromptPreferences(ctx, 99)
	if err != nil {
		t.Fatalf("prefs: %v", err)
	}
	if prefs.ReloginPromptDelayDays != models.DefaultReloginPromptDelayDays {
		t.Fatalf("unexpected pref delay: %d", prefs.ReloginPromptDelayDays)
	}
	delivered, err := db.WasMessageDelivered(ctx, 99, "msg-1")
	if err != nil {
		t.Fatalf("delivery check: %v", err)
	}
	if !delivered {
		t.Fatal("expected existing delivery row to remain readable")
	}
}

func TestPythonTimestampCompatibility(t *testing.T) {
	raw := "2024-01-01T12:00:00.123456+00:00"
	parsed, err := FromISO8601(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	formatted := ToISO8601(parsed)
	if formatted != raw {
		t.Fatalf("round-trip mismatch: got %q want %q", formatted, raw)
	}
	reparsed, err := FromISO8601(formatted)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if !reparsed.Equal(parsed) {
		t.Fatalf("time mismatch: %v vs %v", reparsed, parsed)
	}
}

func openTestDB(t *testing.T) *Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bot.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize db: %v", err)
	}
	return db
}
