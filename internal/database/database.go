package database

import (
	"context"
	"database/sql"
	"errors"
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
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	if _, err := os.Stat(databasePath); os.IsNotExist(err) {
		f, err := os.OpenFile(databasePath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create database file: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close database file: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat database file: %w", err)
	}
	// Restrict existing files before SQLite reads secrets or creates sidecars.
	if err := restrictDBPermissions(databasePath); err != nil {
		return nil, err
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

	if err := restrictDBPermissions(databasePath); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Database{db: db}, nil
}

func restrictDBPermissions(databasePath string) error {
	for i, f := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if err := os.Chmod(f, 0o600); err != nil {
			// SQLite creates sidecars lazily; only those may be absent.
			if i > 0 && os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("restrict database permissions for %s: %w", f, err)
		}
	}
	return nil
}

func (d *Database) Close() error {
	if d.db == nil {
		return nil
	}
	err := d.db.Close()
	d.db = nil
	return err
}

// ErrRetiredRelog means the callback belongs to a /relog link issued before that
// command was removed. The state is consumed and stored credentials are left unchanged.
var ErrRetiredRelog = errors.New("seamless reauthorization (/relog) has been removed. Send /logout, then /login, to reconnect.")

func (d *Database) Initialize(ctx context.Context) error {
	// auth_versions, oauth_attempts, and google_accounts.generation identify a login
	// attempt so logout and a newer login can invalidate a callback that already
	// consumed its oauth_states row. pending_relogs is unused and kept so existing
	// databases are not migrated destructively. oauth_attempts.relog is read only
	// to reject in-flight /relog links.
	schema := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS auth_versions (telegram_user_id INTEGER PRIMARY KEY, generation INTEGER NOT NULL DEFAULT 0, attempt TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS oauth_attempts (state TEXT PRIMARY KEY REFERENCES oauth_states(state) ON DELETE CASCADE, generation INTEGER NOT NULL, relog INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS pending_relogs (
    telegram_user_id INTEGER PRIMARY KEY REFERENCES auth_versions(telegram_user_id),
    state TEXT NOT NULL,
    generation INTEGER NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_states (
    state TEXT PRIMARY KEY,
    telegram_user_id INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

%s
CREATE TABLE IF NOT EXISTS relogin_prompt_preferences (
    telegram_user_id INTEGER PRIMARY KEY,
    relogin_prompt_enabled INTEGER NOT NULL DEFAULT 1,
    relogin_prompt_delay_days INTEGER NOT NULL DEFAULT %d,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

%s
`, googleAccountsDDL("google_accounts"), models.DefaultReloginPromptDelayDays, deliveredMessagesDDL("delivered_messages", "google_accounts"))

	if _, err := d.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if err := d.ensureGoogleAccountReloginPromptColumns(ctx); err != nil {
		return err
	}
	if err := d.migrateToMultiAccount(ctx); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS delivered_messages_telegram_message ON delivered_messages (telegram_chat_id, telegram_message_id)`); err != nil {
		return fmt.Errorf("create delivered_messages index: %w", err)
	}
	if err := d.backfillGoogleAccountReloginPromptDueAt(ctx); err != nil {
		return err
	}
	if err := d.backfillReloginPromptPreferences(ctx); err != nil {
		return err
	}
	return nil
}

// googleAccountsDDL defines one row per connected Gmail mailbox. A Telegram user
// may connect several mailboxes, but each address only once.
func googleAccountsDDL(table string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_user_id INTEGER NOT NULL,
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
    updated_at TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 0,
    UNIQUE (telegram_user_id, gmail_email)
);`, table, models.DefaultReloginPromptDelayDays)
}

// deliveredMessagesDDL keys deliveries by account because Gmail message IDs are
// only unique within one mailbox.
func deliveredMessagesDDL(table, accountsTable string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
    account_id INTEGER NOT NULL,
    gmail_message_id TEXT NOT NULL,
    telegram_chat_id INTEGER NOT NULL,
    telegram_message_id INTEGER NOT NULL,
    delivered_at TEXT NOT NULL,
    PRIMARY KEY (account_id, gmail_message_id),
    FOREIGN KEY (account_id) REFERENCES %s (id) ON DELETE CASCADE
);`, table, accountsTable)
}

// migrateToMultiAccount rebuilds single-account tables, which were keyed by
// telegram_user_id, into the multi-account layout. Existing rows keep their data;
// each delivery is attached to its user's only account.
func (d *Database) migrateToMultiAccount(ctx context.Context) error {
	columns, err := d.tableColumns(ctx, "google_accounts")
	if err != nil {
		return err
	}
	if _, ok := columns["id"]; ok {
		return nil
	}

	// Foreign keys must be off while tables are swapped; the pragma is ignored inside a transaction.
	if _, err := d.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for migration: %w", err)
	}
	defer func() { _, _ = d.db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`) }()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	const accountColumnsCopy = `telegram_user_id, gmail_email, access_token, refresh_token, token_expiry,
    last_history_id, connected_at, relogin_prompt_enabled, relogin_prompt_delay_days,
    relogin_prompt_base_at, relogin_prompt_due_at, relogin_prompt_sent_at, created_at, updated_at, generation`
	statements := []string{
		googleAccountsDDL("google_accounts_v2"),
		`INSERT INTO google_accounts_v2 (` + accountColumnsCopy + `) SELECT ` + accountColumnsCopy + ` FROM google_accounts ORDER BY telegram_user_id`,
		deliveredMessagesDDL("delivered_messages_v2", "google_accounts_v2"),
		`INSERT INTO delivered_messages_v2 (account_id, gmail_message_id, telegram_chat_id, telegram_message_id, delivered_at)
SELECT a.id, m.gmail_message_id, m.telegram_chat_id, m.telegram_message_id, m.delivered_at
FROM delivered_messages m JOIN google_accounts_v2 a ON a.telegram_user_id = m.telegram_user_id`,
		`DROP TABLE delivered_messages`,
		`DROP TABLE google_accounts`,
		`ALTER TABLE google_accounts_v2 RENAME TO google_accounts`,
		`ALTER TABLE delivered_messages_v2 RENAME TO delivered_messages`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate to multi-account schema: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check foreign keys after migration: %w", err)
	}
	violation := rows.Next()
	if err := rows.Close(); err != nil {
		return err
	}
	if violation {
		return errors.New("migrate to multi-account schema: foreign key violation")
	}
	return tx.Commit()
}

func (d *Database) tableColumns(ctx context.Context, table string) (map[string]struct{}, error) {
	rows, err := d.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()

	existing := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("scan table info: %w", err)
		}
		existing[name] = struct{}{}
	}
	return existing, rows.Err()
}

func (d *Database) ensureGoogleAccountReloginPromptColumns(ctx context.Context) error {
	existing, err := d.tableColumns(ctx, "google_accounts")
	if err != nil {
		return err
	}

	alters := []struct {
		column string
		sql    string
	}{
		{"generation", "ALTER TABLE google_accounts ADD COLUMN generation INTEGER NOT NULL DEFAULT 0"},
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
    id,
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
WHERE id = ?
`, ToISO8601(item.baseAt), ToISO8601(item.dueAt), now, item.userID); err != nil {
			return fmt.Errorf("backfill relogin schedule for account %d: %w", item.userID, err)
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
	return d.StoreOAuthAttempt(ctx, state, telegramUserID, expiresAt)
}

func (d *Database) ConsumeOAuthState(ctx context.Context, state string) (*models.OAuthState, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
SELECT s.state, s.telegram_user_id, s.created_at, s.expires_at, a.generation, a.relog
FROM oauth_states s
JOIN oauth_attempts a ON a.state = s.state
JOIN auth_versions v ON v.telegram_user_id = s.telegram_user_id
    AND v.attempt = s.state AND v.generation = a.generation
WHERE s.state = ?
`, state)

	var (
		rawState       string
		telegramUserID int64
		createdAtRaw   string
		expiresAtRaw   string
		generation     int64
		relog          int
	)
	if err := row.Scan(&rawState, &telegramUserID, &createdAtRaw, &expiresAtRaw, &generation, &relog); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	retiredRelog := relog != 0

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
		Generation:     generation,
		State:          rawState,
		TelegramUserID: telegramUserID,
		CreatedAt:      createdAt,
		ExpiresAt:      expiresAt,
	}
	if !oauthState.ExpiresAt.After(UTCNow()) {
		return nil, nil
	}
	if retiredRelog {
		return oauthState, ErrRetiredRelog
	}
	return oauthState, nil
}

func (d *Database) DeleteOAuthStatesForUser(ctx context.Context, telegramUserID int64) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE auth_versions SET attempt = '' WHERE telegram_user_id = ?`, telegramUserID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE telegram_user_id = ?`, telegramUserID); err != nil {
		return err
	}
	return tx.Commit()
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

// UpsertGoogleAccount inserts an account, or replaces the credentials of the
// same address for the same Telegram user, and returns the account ID.
func (d *Database) UpsertGoogleAccount(ctx context.Context, params UpsertGoogleAccountParams) (int64, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := upsertAccount(ctx, tx, params)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func upsertAccount(ctx context.Context, tx *sql.Tx, params UpsertGoogleAccountParams) (int64, error) {
	// Each write takes a fresh value from the user's counter, so account generations are never reused.
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_versions (telegram_user_id, generation) VALUES (?, 1) ON CONFLICT(telegram_user_id) DO UPDATE SET generation = generation + 1`, params.TelegramUserID); err != nil {
		return 0, err
	}
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

	var id int64
	err := tx.QueryRowContext(ctx, `
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
    updated_at,
    generation
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT generation FROM auth_versions WHERE telegram_user_id = ?))
ON CONFLICT(telegram_user_id, gmail_email) DO UPDATE SET
    generation = excluded.generation,
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
RETURNING id
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
		params.TelegramUserID,
	).Scan(&id)
	return id, err
}

const accountColumns = `
    id,
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
    generation`

// GetGoogleAccountByID returns the account with the given ID, or nil if it no longer exists.
func (d *Database) GetGoogleAccountByID(ctx context.Context, accountID int64) (*models.GoogleAccount, error) {
	row := d.db.QueryRowContext(ctx, `SELECT`+accountColumns+` FROM google_accounts WHERE id = ?`, accountID)
	account, err := scanAccount(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return account, err
}

// ListGoogleAccountsForUser returns a Telegram user's accounts in the order they were connected.
func (d *Database) ListGoogleAccountsForUser(ctx context.Context, telegramUserID int64) ([]models.GoogleAccount, error) {
	return d.listAccounts(ctx, `SELECT`+accountColumns+` FROM google_accounts WHERE telegram_user_id = ? ORDER BY id ASC`, telegramUserID)
}

func (d *Database) ListGoogleAccounts(ctx context.Context) ([]models.GoogleAccount, error) {
	return d.listAccounts(ctx, `SELECT`+accountColumns+` FROM google_accounts ORDER BY telegram_user_id ASC, id ASC`)
}

func (d *Database) listAccounts(ctx context.Context, query string, args ...any) ([]models.GoogleAccount, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
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

func (d *Database) UpdateTokens(ctx context.Context, accountID int64, accessToken string, tokenExpiry time.Time, refreshToken *string) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET access_token = ?, refresh_token = COALESCE(?, refresh_token), token_expiry = ?, updated_at = ?
WHERE id = ?
`, accessToken, refreshToken, ToISO8601(tokenExpiry), ToISO8601(UTCNow()), accountID)
	return err
}

func (d *Database) UpdateLastHistoryID(ctx context.Context, accountID int64, lastHistoryID string) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET last_history_id = ?, updated_at = ?
WHERE id = ?
`, lastHistoryID, ToISO8601(UTCNow()), accountID)
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

func (d *Database) MarkReloginPromptSent(ctx context.Context, accountID int64, sentAt *time.Time) error {
	value := UTCNow()
	if sentAt != nil {
		value = *sentAt
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE google_accounts
SET relogin_prompt_sent_at = ?, updated_at = ?
WHERE id = ?
`, ToISO8601(value), ToISO8601(UTCNow()), accountID)
	return err
}

func (d *Database) SetReloginPromptEnabled(ctx context.Context, user int64, enabled bool) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ToISO8601(UTCNow())
	if _, err = tx.ExecContext(ctx, `INSERT INTO relogin_prompt_preferences (telegram_user_id, relogin_prompt_enabled, created_at, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(telegram_user_id) DO UPDATE SET relogin_prompt_enabled = excluded.relogin_prompt_enabled, updated_at = excluded.updated_at`, user, boolToInt(enabled), now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE google_accounts SET relogin_prompt_enabled = ?, updated_at = ? WHERE telegram_user_id = ?`, boolToInt(enabled), now, user); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Database) SetReloginPromptDelayDays(ctx context.Context, user int64, days int) error {
	if days < 1 {
		return fmt.Errorf("reminder delay must be positive")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ToISO8601(UTCNow())
	if _, err = tx.ExecContext(ctx, `INSERT INTO relogin_prompt_preferences (telegram_user_id, relogin_prompt_delay_days, created_at, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(telegram_user_id) DO UPDATE SET relogin_prompt_delay_days = excluded.relogin_prompt_delay_days, updated_at = excluded.updated_at`, user, days, now, now); err != nil {
		return err
	}
	type schedule struct {
		id   int64
		base string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(relogin_prompt_base_at, connected_at) FROM google_accounts WHERE telegram_user_id = ?`, user)
	if err != nil {
		return err
	}
	var schedules []schedule
	for rows.Next() {
		var item schedule
		if err := rows.Scan(&item.id, &item.base); err != nil {
			rows.Close()
			return err
		}
		schedules = append(schedules, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range schedules {
		base, err := FromISO8601(item.base)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE google_accounts SET relogin_prompt_delay_days = ?, relogin_prompt_base_at = ?, relogin_prompt_due_at = ?, relogin_prompt_sent_at = NULL, updated_at = ? WHERE id = ?`, days, item.base, ToISO8601(base.Add(time.Duration(days)*24*time.Hour)), now, item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *Database) DeleteGoogleAccount(ctx context.Context, accountID int64) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM google_accounts WHERE id = ?`, accountID)
	return err
}

func (d *Database) WasMessageDelivered(ctx context.Context, accountID int64, gmailMessageID string) (bool, error) {
	row := d.db.QueryRowContext(ctx, `
SELECT 1
FROM delivered_messages
WHERE account_id = ? AND gmail_message_id = ?
`, accountID, gmailMessageID)
	var one int
	if err := row.Scan(&one); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (d *Database) MarkMessageDelivered(ctx context.Context, accountID int64, gmailMessageID string, telegramChatID int64, telegramMessageID int) error {
	_, err := d.db.ExecContext(ctx, `
INSERT OR IGNORE INTO delivered_messages (
    account_id,
    gmail_message_id,
    telegram_chat_id,
    telegram_message_id,
    delivered_at
)
VALUES (?, ?, ?, ?, ?)
`, accountID, gmailMessageID, telegramChatID, telegramMessageID, ToISO8601(UTCNow()))
	return err
}

// GetDeliveredMessageAccount returns the account whose notification is the given
// Telegram message, along with the Gmail message ID it announced. It returns a nil
// account when the message is unknown or its account has been disconnected.
func (d *Database) GetDeliveredMessageAccount(ctx context.Context, telegramChatID int64, telegramMessageID int) (*models.GoogleAccount, string, error) {
	var (
		accountID      int64
		gmailMessageID string
	)
	err := d.db.QueryRowContext(ctx, `
SELECT account_id, gmail_message_id
FROM delivered_messages
WHERE telegram_chat_id = ? AND telegram_message_id = ?
ORDER BY delivered_at DESC
LIMIT 1
`, telegramChatID, telegramMessageID).Scan(&accountID, &gmailMessageID)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	account, err := d.GetGoogleAccountByID(ctx, accountID)
	if err != nil || account == nil {
		return nil, "", err
	}
	return account, gmailMessageID, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanAccount(row scannable) (*models.GoogleAccount, error) {
	var (
		id               int64
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
		generation       int64
	)
	if err := row.Scan(
		&id,
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
		&generation,
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
		ID:                     id,
		Generation:             generation,
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
