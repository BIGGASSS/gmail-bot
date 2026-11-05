package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/models"
)

// ErrStaleAuthorization means a snapshot or OAuth attempt no longer owns the authorization.
// Generations advance when credentials are replaced or the user logs out, not on an ordinary token refresh.
// auth_versions is retained after account deletion so a generation is not reused.
var ErrStaleAuthorization = errors.New("Authorization changed or link expired. Please request a new /login link.")

// ErrAccountAlreadyConnected means the Telegram user has already connected this Gmail address.
var ErrAccountAlreadyConnected = errors.New("This Gmail account is already connected.")

// StoreOAuthAttempt records the latest login link and supersedes callbacks that already consumed an older state.
func (d *Database) StoreOAuthAttempt(ctx context.Context, state string, user int64, expires time.Time) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO auth_versions (telegram_user_id) VALUES (?) ON CONFLICT DO NOTHING`, user); err != nil {
		return err
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT generation FROM auth_versions WHERE telegram_user_id = ?`, user).Scan(&generation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE auth_versions SET attempt = ? WHERE telegram_user_id = ?`, state, user); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE telegram_user_id = ?`, user); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_states (state, telegram_user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`, state, user, ToISO8601(UTCNow()), ToISO8601(expires)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_attempts (state, generation, relog) VALUES (?, ?, 0)`, state, generation); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteOAuth adds the account only when state is still the latest unexpired attempt for its generation.
// It never replaces an address the user has already connected; that returns ErrAccountAlreadyConnected.
func (d *Database) CompleteOAuth(ctx context.Context, state models.OAuthState, p UpsertGoogleAccountParams) error {
	if p.TelegramUserID != state.TelegramUserID {
		return ErrStaleAuthorization
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE auth_versions SET attempt = '' WHERE telegram_user_id = ? AND generation = ? AND attempt = ?`, state.TelegramUserID, state.Generation, state.State)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 || !state.ExpiresAt.After(UTCNow()) {
		return ErrStaleAuthorization
	}
	if strings.TrimSpace(p.RefreshToken) == "" {
		return errors.New("Google did not return a refresh token. Please retry with consent.")
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM google_accounts WHERE telegram_user_id = ? AND lower(gmail_email) = lower(?)`, p.TelegramUserID, strings.TrimSpace(p.GmailEmail)).Scan(&exists)
	if err == nil {
		return ErrAccountAlreadyConnected
	}
	if err != sql.ErrNoRows {
		return err
	}
	if _, err = upsertAccount(ctx, tx, p); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateTokensGuarded stores a refresh only when the row is still the snapshot that requested it.
func (d *Database) UpdateTokensGuarded(ctx context.Context, a models.GoogleAccount, access string, expiry time.Time, refresh *string) error {
	result, err := d.db.ExecContext(ctx, `UPDATE google_accounts SET access_token = ?, refresh_token = COALESCE(?, refresh_token), token_expiry = ?, updated_at = ? WHERE id = ? AND generation = ? AND access_token = ? AND refresh_token = ?`, access, refresh, ToISO8601(expiry), ToISO8601(UTCNow()), a.ID, a.Generation, a.AccessToken, a.RefreshToken)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStaleAuthorization
	}
	return nil
}

// DeleteGoogleAccountGuarded removes an account only when the expired snapshot still owns it.
// Logout and a newer login change the generation or tokens, so a delayed invalid_grant cannot
// delete the replacement account or its delivery records.
func (d *Database) DeleteGoogleAccountGuarded(ctx context.Context, a models.GoogleAccount) (bool, error) {
	result, err := d.db.ExecContext(ctx, `
DELETE FROM google_accounts
WHERE id = ? AND generation = ? AND refresh_token = ? AND access_token = ?
`, a.ID, a.Generation, a.RefreshToken, a.AccessToken)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// LogoutAll atomically invalidates in-flight login callbacks and removes every
// account the user has connected. It returns the removed accounts so their tokens
// can be revoked.
func (d *Database) LogoutAll(ctx context.Context, user int64) ([]models.GoogleAccount, error) {
	return d.logout(ctx, user, nil)
}

// LogoutAccount atomically invalidates in-flight login callbacks and removes one
// of the user's accounts. It returns nil when the user has no such account.
func (d *Database) LogoutAccount(ctx context.Context, user, accountID int64) (*models.GoogleAccount, error) {
	removed, err := d.logout(ctx, user, &accountID)
	if err != nil || len(removed) == 0 {
		return nil, err
	}
	return &removed[0], nil
}

func (d *Database) logout(ctx context.Context, user int64, accountID *int64) ([]models.GoogleAccount, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, refresh_token, gmail_email FROM google_accounts WHERE telegram_user_id = ? AND (? IS NULL OR id = ?) ORDER BY id`, user, accountID, accountID)
	if err != nil {
		return nil, err
	}
	var removed []models.GoogleAccount
	for rows.Next() {
		account := models.GoogleAccount{TelegramUserID: user}
		if err := rows.Scan(&account.ID, &account.RefreshToken, &account.GmailEmail); err != nil {
			rows.Close()
			return nil, err
		}
		removed = append(removed, account)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// A logout always supersedes pending login links, so a callback already in
	// flight cannot reconnect an account the user just removed.
	if _, err = tx.ExecContext(ctx, `INSERT INTO auth_versions (telegram_user_id, generation) VALUES (?, 1) ON CONFLICT(telegram_user_id) DO UPDATE SET generation = generation + 1, attempt = ''`, user); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE telegram_user_id = ?`, user); err != nil {
		return nil, err
	}
	for _, account := range removed {
		if _, err = tx.ExecContext(ctx, `DELETE FROM google_accounts WHERE id = ?`, account.ID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return removed, nil
}
