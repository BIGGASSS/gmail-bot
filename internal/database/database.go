package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/models"
	_ "modernc.org/sqlite"
)

type Database struct {
	db *sql.DB
}

func Open(databasePath string) (*Database, error) {
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// Keep a single connection so PRAGMA settings remain reliable.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	return &Database{db: db}, nil
}

func (d *Database) Close() error {
	if d.db == nil {
		return nil
	}
	err := d.db.Close()
	d.db = nil
	return err
}

func (d *Database) Initialize(ctx context.Context) error {
	schema := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS oauth_states (
    state TEXT PRIMARY KEY,
    telegram_user_id INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS google_accounts (
    telegram_user_id INTEGER PRIMARY KEY,
    gmail_email TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    token_expiry TEXT NOT NULL,
    last_history_id TEXT NOT NULL,
    connected_at TEXT NOT NULL,
    relogin_prompt_enabled INTEGER NOT NULL DEFAULT 1,
    relogin_prompt_delay_days INTEGER NOT NULL DEFAULT %d,
    relogin_prompt_base_at TEXT,
    relogin_prompt_due_at TEXT,
    relogin_prompt_sent_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS relogin_prompt_preferences (
    telegram_user_id INTEGER PRIMARY KEY,
    relogin_prompt_enabled INTEGER NOT NULL DEFAULT 1,
    relogin_prompt_delay_days INTEGER NOT NULL DEFAULT %d,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS delivered_messages (
    telegram_user_id INTEGER NOT NULL,
    gmail_message_id TEXT NOT NULL,
    telegram_chat_id INTEGER NOT NULL,
    telegram_message_id INTEGER NOT NULL,
    delivered_at TEXT NOT NULL,
    PRIMARY KEY (telegram_user_id, gmail_message_id),
    FOREIGN KEY (telegram_user_id) REFERENCES google_accounts (telegram_user_id) ON DELETE CASCADE
);
`, models.DefaultReloginPromptDelayDays, models.DefaultReloginPromptDelayDays)

	if _, err := d.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if err := d.ensureGoogleAccountReloginPromptColumns(ctx); err != nil {
		return err
	}
	if err := d.backfillGoogleAccountReloginPromptDueAt(ctx); err != nil {
		return err
	}
	if err := d.backfillReloginPromptPreferences(ctx); err != nil {
		return err
	}
	return nil
}

func (d *Database) ensureGoogleAccountReloginPromptColumns(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx, `PRAGMA table_info(google_accounts)`)
	if err != nil {
		return fmt.Errorf("inspect google_accounts columns: %w", err)
	}
	defer rows.Close()

	existing := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan table info: %w", err)
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	alters := []struct {
		column string
		sql    string
	}{
		{"relogin_prompt_enabled", "ALTER TABLE google_accounts ADD COLUMN relogin_prompt_enabled INTEGER NOT NULL DEFAULT 1"},
		{"relogin_prompt_delay_days", fmt.Sprintf("ALTER TABLE google_accounts ADD COLUMN relogin_prompt_delay_days INTEGER NOT NULL DEFAULT %d", models.DefaultReloginPromptDelayDays)},
		{"relogin_prompt_base_at", "ALTER TABLE google_accounts ADD COLUMN relogin_prompt_base_at TEXT"},
		{"relogin_prompt_due_at", "ALTER TABLE google_accounts ADD COLUMN relogin_prompt_due_at TEXT"},
		{"relogin_prompt_sent_at", "ALTER TABLE google_accounts ADD COLUMN relogin_prompt_sent_at TEXT"},
	}
	for _, alter := range alters {
		if _, ok := existing[alter.column]; ok {
			continue
		}
		if _, err := d.db.ExecContext(ctx, alter.sql); err != nil {
			return fmt.Errorf("add column %s: %w", alter.column, err)
		}
	}
	return nil
}

func (d *Database) backfillGoogleAccountReloginPromptDueAt(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx, `
SELECT
    telegram_user_id,
    connected_at,
    relogin_prompt_delay_days,
    relogin_prompt_base_at,
    relogin_prompt_due_at
FROM google_accounts
WHERE relogin_prompt_base_at IS NULL OR relogin_prompt_due_at IS NULL
`)
	if err != nil {
		return fmt.Errorf("select accounts needing relogin backfill: %w", err)
	}
	defer rows.Close()

	type pending struct {
		userID    int64
		baseAt    time.Time
		dueAt     time.Time
		delayDays int
	}
	var updates []pending

	for rows.Next() {
		var (
			userID    int64
			connected string
			delayDays sql.NullInt64
			baseAtRaw sql.NullString
			dueAtRaw  sql.NullString
		)
		if err := rows.Scan(&userID, &connected, &delayDays, &baseAtRaw, &dueAtRaw); err != nil {
			return err
		}
		days := models.DefaultReloginPromptDelayDays
		if delayDays.Valid {
			days = int(delayDays.Int64)
		}

		var baseAt time.Time
		switch {
		case baseAtRaw.Valid && baseAtRaw.String != "":
			parsed, err := FromISO8601(baseAtRaw.String)
			if err != nil {
				return err
			}
			baseAt = parsed
		case dueAtRaw.Valid && dueAtRaw.String != "":
			parsed, err := FromISO8601(dueAtRaw.String)
			if err != nil {
				return err
			}
			baseAt = parsed.Add(-time.Duration(days) * 24 * time.Hour)
		default:
			parsed, err := FromISO8601(connected)
			if err != nil {
				return err
			}
			baseAt = parsed
		}
		updates = append(updates, pending{
			userID:    userID,
			baseAt:    baseAt,
			dueAt:     baseAt.Add(time.Duration(days) * 24 * time.Hour),
			delayDays: days,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	now := ToISO8601(UTCNow())
	for _, item := range updates {
		if _, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET
    relogin_prompt_base_at = COALESCE(relogin_prompt_base_at, ?),
    relogin_prompt_due_at = COALESCE(relogin_prompt_due_at, ?),
    updated_at = ?
WHERE telegram_user_id = ?
`, ToISO8601(item.baseAt), ToISO8601(item.dueAt), now, item.userID); err != nil {
			return fmt.Errorf("backfill relogin schedule for %d: %w", item.userID, err)
		}
	}
	return nil
}

func (d *Database) backfillReloginPromptPreferences(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx, `
SELECT telegram_user_id, relogin_prompt_enabled, relogin_prompt_delay_days
FROM google_accounts
`)
	if err != nil {
		return fmt.Errorf("select accounts for preference backfill: %w", err)
	}
	defer rows.Close()

	type pref struct {
		userID    int64
		enabled   int
		delayDays int
	}
	var items []pref
	for rows.Next() {
		var item pref
		if err := rows.Scan(&item.userID, &item.enabled, &item.delayDays); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range items {
		now := ToISO8601(UTCNow())
		if _, err := d.db.ExecContext(ctx, `
INSERT INTO relogin_prompt_preferences (
    telegram_user_id,
    relogin_prompt_enabled,
    relogin_prompt_delay_days,
    created_at,
    updated_at
)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(telegram_user_id) DO NOTHING
`, item.userID, item.enabled, item.delayDays, now, now); err != nil {
			return fmt.Errorf("backfill preferences for %d: %w", item.userID, err)
		}
	}
	return nil
}

func (d *Database) StoreOAuthState(ctx context.Context, state string, telegramUserID int64, expiresAt time.Time) error {
	now := ToISO8601(UTCNow())
	_, err := d.db.ExecContext(ctx, `
INSERT INTO oauth_states (state, telegram_user_id, created_at, expires_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(state) DO UPDATE SET
    telegram_user_id = excluded.telegram_user_id,
    created_at = excluded.created_at,
    expires_at = excluded.expires_at
`, state, telegramUserID, now, ToISO8601(expiresAt))
	return err
}

func (d *Database) ConsumeOAuthState(ctx context.Context, state string) (*models.OAuthState, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
SELECT state, telegram_user_id, created_at, expires_at
FROM oauth_states
WHERE state = ?
`, state)

	var (
		rawState       string
		telegramUserID int64
		createdAtRaw   string
		expiresAtRaw   string
	)
	if err := row.Scan(&rawState, &telegramUserID, &createdAtRaw, &expiresAtRaw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE state = ?`, state); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	createdAt, err := FromISO8601(createdAtRaw)
	if err != nil {
		return nil, err
	}
	expiresAt, err := FromISO8601(expiresAtRaw)
	if err != nil {
		return nil, err
	}
	oauthState := &models.OAuthState{
		State:          rawState,
		TelegramUserID: telegramUserID,
		CreatedAt:      createdAt,
		ExpiresAt:      expiresAt,
	}
	if !oauthState.ExpiresAt.After(UTCNow()) {
		return nil, nil
	}
	return oauthState, nil
}

func (d *Database) CleanupExpiredOAuthStates(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `
DELETE FROM oauth_states WHERE expires_at <= ?
`, ToISO8601(UTCNow()))
	return err
}

type UpsertGoogleAccountParams struct {
	TelegramUserID         int64
	GmailEmail             string
	AccessToken            string
	RefreshToken           string
	TokenExpiry            time.Time
	LastHistoryID          string
	ConnectedAt            time.Time
	ReloginPromptEnabled   bool
	ReloginPromptDelayDays int
	ReloginPromptBaseAt    *time.Time
	ReloginPromptDueAt     *time.Time
}

func (d *Database) UpsertGoogleAccount(ctx context.Context, params UpsertGoogleAccountParams) error {
	if params.ReloginPromptDelayDays == 0 {
		params.ReloginPromptDelayDays = models.DefaultReloginPromptDelayDays
	}
	baseAt := params.ConnectedAt
	if params.ReloginPromptBaseAt != nil {
		baseAt = *params.ReloginPromptBaseAt
	}
	dueAt := baseAt.Add(time.Duration(params.ReloginPromptDelayDays) * 24 * time.Hour)
	if params.ReloginPromptDueAt != nil {
		dueAt = *params.ReloginPromptDueAt
	}
	now := ToISO8601(UTCNow())

	_, err := d.db.ExecContext(ctx, `
INSERT INTO google_accounts (
    telegram_user_id,
    gmail_email,
    access_token,
    refresh_token,
    token_expiry,
    last_history_id,
    connected_at,
    relogin_prompt_enabled,
    relogin_prompt_delay_days,
    relogin_prompt_base_at,
    relogin_prompt_due_at,
    relogin_prompt_sent_at,
    created_at,
    updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(telegram_user_id) DO UPDATE SET
    gmail_email = excluded.gmail_email,
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    token_expiry = excluded.token_expiry,
    last_history_id = excluded.last_history_id,
    connected_at = excluded.connected_at,
    relogin_prompt_enabled = excluded.relogin_prompt_enabled,
    relogin_prompt_delay_days = excluded.relogin_prompt_delay_days,
    relogin_prompt_base_at = excluded.relogin_prompt_base_at,
    relogin_prompt_due_at = excluded.relogin_prompt_due_at,
    relogin_prompt_sent_at = excluded.relogin_prompt_sent_at,
    updated_at = excluded.updated_at
`,
		params.TelegramUserID,
		params.GmailEmail,
		params.AccessToken,
		params.RefreshToken,
		ToISO8601(params.TokenExpiry),
		params.LastHistoryID,
		ToISO8601(params.ConnectedAt),
		boolToInt(params.ReloginPromptEnabled),
		params.ReloginPromptDelayDays,
		ToISO8601(baseAt),
		ToISO8601(dueAt),
		nil,
		now,
		now,
	)
	return err
}

func (d *Database) GetGoogleAccount(ctx context.Context, telegramUserID int64) (*models.GoogleAccount, error) {
	row := d.db.QueryRowContext(ctx, `
SELECT
    telegram_user_id,
    gmail_email,
    access_token,
    refresh_token,
    token_expiry,
    last_history_id,
    connected_at,
    relogin_prompt_enabled,
    relogin_prompt_delay_days,
    relogin_prompt_base_at,
    relogin_prompt_due_at,
    relogin_prompt_sent_at
FROM google_accounts
WHERE telegram_user_id = ?
`, telegramUserID)
	account, err := scanAccount(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return account, err
}

func (d *Database) ListGoogleAccounts(ctx context.Context) ([]models.GoogleAccount, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
    telegram_user_id,
    gmail_email,
    access_token,
    refresh_token,
    token_expiry,
    last_history_id,
    connected_at,
    relogin_prompt_enabled,
    relogin_prompt_delay_days,
    relogin_prompt_base_at,
    relogin_prompt_due_at,
    relogin_prompt_sent_at
FROM google_accounts
ORDER BY telegram_user_id ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []models.GoogleAccount
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *account)
	}
	return accounts, rows.Err()
}

func (d *Database) UpdateTokens(ctx context.Context, telegramUserID int64, accessToken string, tokenExpiry time.Time, refreshToken *string) error {
	now := ToISO8601(UTCNow())
	if refreshToken == nil {
		_, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET access_token = ?, token_expiry = ?, updated_at = ?
WHERE telegram_user_id = ?
`, accessToken, ToISO8601(tokenExpiry), now, telegramUserID)
		return err
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET access_token = ?, refresh_token = ?, token_expiry = ?, updated_at = ?
WHERE telegram_user_id = ?
`, accessToken, *refreshToken, ToISO8601(tokenExpiry), now, telegramUserID)
	return err
}

func (d *Database) UpdateLastHistoryID(ctx context.Context, telegramUserID int64, lastHistoryID string) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET last_history_id = ?, updated_at = ?
WHERE telegram_user_id = ?
`, lastHistoryID, ToISO8601(UTCNow()), telegramUserID)
	return err
}

func (d *Database) GetReloginPromptPreferences(ctx context.Context, telegramUserID int64) (models.ReloginPromptPreferences, error) {
	row := d.db.QueryRowContext(ctx, `
SELECT telegram_user_id, relogin_prompt_enabled, relogin_prompt_delay_days
FROM relogin_prompt_preferences
WHERE telegram_user_id = ?
`, telegramUserID)

	var (
		userID    int64
		enabled   int
		delayDays int
	)
	if err := row.Scan(&userID, &enabled, &delayDays); err != nil {
		if err == sql.ErrNoRows {
			return models.DefaultReloginPromptPreferences(telegramUserID), nil
		}
		return models.ReloginPromptPreferences{}, err
	}
	return models.ReloginPromptPreferences{
		TelegramUserID:         userID,
		ReloginPromptEnabled:   enabled != 0,
		ReloginPromptDelayDays: delayDays,
	}, nil
}

func (d *Database) upsertReloginPromptPreferences(ctx context.Context, preferences models.ReloginPromptPreferences, now time.Time) error {
	nowRaw := ToISO8601(now)
	_, err := d.db.ExecContext(ctx, `
INSERT INTO relogin_prompt_preferences (
    telegram_user_id,
    relogin_prompt_enabled,
    relogin_prompt_delay_days,
    created_at,
    updated_at
)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(telegram_user_id) DO UPDATE SET
    relogin_prompt_enabled = excluded.relogin_prompt_enabled,
    relogin_prompt_delay_days = excluded.relogin_prompt_delay_days,
    updated_at = excluded.updated_at
`, preferences.TelegramUserID, boolToInt(preferences.ReloginPromptEnabled), preferences.ReloginPromptDelayDays, nowRaw, nowRaw)
	return err
}

func (d *Database) MarkReloginPromptSent(ctx context.Context, telegramUserID int64, sentAt *time.Time) error {
	value := UTCNow()
	if sentAt != nil {
		value = *sentAt
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET relogin_prompt_sent_at = ?, updated_at = ?
WHERE telegram_user_id = ?
`, ToISO8601(value), ToISO8601(UTCNow()), telegramUserID)
	return err
}

func (d *Database) SetReloginPromptEnabled(ctx context.Context, telegramUserID int64, enabled bool) error {
	current, err := d.GetReloginPromptPreferences(ctx, telegramUserID)
	if err != nil {
		return err
	}
	now := UTCNow()
	if err := d.upsertReloginPromptPreferences(ctx, models.ReloginPromptPreferences{
		TelegramUserID:         telegramUserID,
		ReloginPromptEnabled:   enabled,
		ReloginPromptDelayDays: current.ReloginPromptDelayDays,
	}, now); err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `
UPDATE google_accounts
SET relogin_prompt_enabled = ?, updated_at = ?
WHERE telegram_user_id = ?
`, boolToInt(enabled), ToISO8601(now), telegramUserID)
	return err
}

func (d *Database) SetReloginPromptDelayDays(ctx context.Context, telegramUserID int64, delayDays int) error {
	current, err := d.GetReloginPromptPreferences(ctx, telegramUserID)
	if err != nil {
		return err
	}
	now := UTCNow()
	if err := d.upsertReloginPromptPreferences(ctx, models.ReloginPromptPreferences{
		TelegramUserID:         telegramUserID,
		ReloginPromptEnabled:   current.ReloginPromptEnabled,
		ReloginPromptDelayDays: delayDays,
	}, now); err != nil {
		return err
	}

	account, err := d.GetGoogleAccount(ctx, telegramUserID)
	if err != nil {
		return err
	}
	if account != nil {
		baseAt := account.ConnectedAt
		if account.ReloginPromptBaseAt != nil {
			baseAt = *account.ReloginPromptBaseAt
		}
		dueAt := baseAt.Add(time.Duration(delayDays) * 24 * time.Hour)
		if _, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET
    relogin_prompt_delay_days = ?,
    relogin_prompt_base_at = ?,
    relogin_prompt_due_at = ?,
    relogin_prompt_sent_at = NULL,
    updated_at = ?
WHERE telegram_user_id = ?
`, delayDays, ToISO8601(baseAt), ToISO8601(dueAt), ToISO8601(now), telegramUserID); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) DeleteGoogleAccount(ctx context.Context, telegramUserID int64) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM google_accounts WHERE telegram_user_id = ?`, telegramUserID)
	return err
}

func (d *Database) WasMessageDelivered(ctx context.Context, telegramUserID int64, gmailMessageID string) (bool, error) {
	row := d.db.QueryRowContext(ctx, `
SELECT 1
FROM delivered_messages
WHERE telegram_user_id = ? AND gmail_message_id = ?
`, telegramUserID, gmailMessageID)
	var one int
	if err := row.Scan(&one); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (d *Database) MarkMessageDelivered(ctx context.Context, telegramUserID int64, gmailMessageID string, telegramChatID int64, telegramMessageID int) error {
	_, err := d.db.ExecContext(ctx, `
INSERT OR IGNORE INTO delivered_messages (
    telegram_user_id,
    gmail_message_id,
    telegram_chat_id,
    telegram_message_id,
    delivered_at
)
VALUES (?, ?, ?, ?, ?)
`, telegramUserID, gmailMessageID, telegramChatID, telegramMessageID, ToISO8601(UTCNow()))
	return err
}

type scannable interface {
	Scan(dest ...any) error
}

func scanAccount(row scannable) (*models.GoogleAccount, error) {
	var (
		userID           int64
		email            string
		accessToken      string
		refreshToken     string
		tokenExpiryRaw   string
		lastHistoryID    string
		connectedAtRaw   string
		reloginEnabled   int
		reloginDelayDays int
		reloginBaseAtRaw sql.NullString
		reloginDueAtRaw  sql.NullString
		reloginSentAtRaw sql.NullString
	)
	if err := row.Scan(
		&userID,
		&email,
		&accessToken,
		&refreshToken,
		&tokenExpiryRaw,
		&lastHistoryID,
		&connectedAtRaw,
		&reloginEnabled,
		&reloginDelayDays,
		&reloginBaseAtRaw,
		&reloginDueAtRaw,
		&reloginSentAtRaw,
	); err != nil {
		return nil, err
	}

	tokenExpiry, err := FromISO8601(tokenExpiryRaw)
	if err != nil {
		return nil, err
	}
	connectedAt, err := FromISO8601(connectedAtRaw)
	if err != nil {
		return nil, err
	}

	account := &models.GoogleAccount{
		TelegramUserID:         userID,
		GmailEmail:             email,
		AccessToken:            accessToken,
		RefreshToken:           refreshToken,
		TokenExpiry:            tokenExpiry,
		LastHistoryID:          lastHistoryID,
		ConnectedAt:            connectedAt,
		ReloginPromptEnabled:   reloginEnabled != 0,
		ReloginPromptDelayDays: reloginDelayDays,
	}
	if reloginBaseAtRaw.Valid && reloginBaseAtRaw.String != "" {
		value, err := FromISO8601(reloginBaseAtRaw.String)
		if err != nil {
			return nil, err
		}
		account.ReloginPromptBaseAt = &value
	}
	if reloginDueAtRaw.Valid && reloginDueAtRaw.String != "" {
		value, err := FromISO8601(reloginDueAtRaw.String)
		if err != nil {
			return nil, err
		}
		account.ReloginPromptDueAt = &value
	}
	if reloginSentAtRaw.Valid && reloginSentAtRaw.String != "" {
		value, err := FromISO8601(reloginSentAtRaw.String)
		if err != nil {
			return nil, err
		}
		account.ReloginPromptSentAt = &value
	}
	return account, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
