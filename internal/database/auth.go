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
// Generations advance on authorization replacement/logout, not ordinary token refresh.
// auth_versions is retained after account deletion to prevent generation reuse.
var ErrStaleAuthorization = errors.New("Authorization changed or link expired. Please request a new /login or /relog link.")

// StoreOAuthAttempt supersedes even callbacks that have already consumed their state.
func (d *Database) StoreOAuthAttempt(ctx context.Context, state string, user int64, expires time.Time, relog bool) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation int64
	if relog {
		if err := tx.QueryRowContext(ctx, `SELECT generation FROM google_accounts WHERE telegram_user_id = ?`, user).Scan(&generation); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO auth_versions (telegram_user_id) VALUES (?) ON CONFLICT DO NOTHING`, user); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT generation FROM auth_versions WHERE telegram_user_id = ?`, user).Scan(&generation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE auth_versions SET attempt = ? WHERE telegram_user_id = ?`, state, user); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE telegram_user_id = ?`, user); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_states VALUES (?, ?, ?, ?)`, state, user, ToISO8601(UTCNow()), ToISO8601(expires)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_attempts VALUES (?, ?, ?)`, state, generation, relog); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteOAuth commits only the latest, unexpired attempt against its original authorization.
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
	if state.Relog {
		var email string
		var days int
		if err := tx.QueryRowContext(ctx, `SELECT gmail_email, relogin_prompt_delay_days FROM google_accounts WHERE telegram_user_id = ? AND generation = ?`, state.TelegramUserID, state.Generation).Scan(&email, &days); err != nil {
			return ErrStaleAuthorization
		}
		if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(p.GmailEmail)) {
			return errors.New("Choose the Gmail account already connected to the bot, then retry /relog.")
		}
		now := UTCNow()
		_, err = tx.ExecContext(ctx, `UPDATE google_accounts SET access_token = ?, refresh_token = ?, token_expiry = ?, generation = generation + 1, relogin_prompt_base_at = ?, relogin_prompt_due_at = ?, relogin_prompt_sent_at = NULL, updated_at = ? WHERE telegram_user_id = ?`, p.AccessToken, p.RefreshToken, ToISO8601(p.TokenExpiry), ToISO8601(now), ToISO8601(now.Add(time.Duration(days)*24*time.Hour)), ToISO8601(now), state.TelegramUserID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE auth_versions SET generation = generation + 1 WHERE telegram_user_id = ?`, state.TelegramUserID)
	} else {
		err = upsertAccount(ctx, tx, p)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Database) UpdateTokensGuarded(ctx context.Context, a models.GoogleAccount, access string, expiry time.Time, refresh *string) error {
	result, err := d.db.ExecContext(ctx, `UPDATE google_accounts SET access_token = ?, refresh_token = COALESCE(?, refresh_token), token_expiry = ?, updated_at = ? WHERE telegram_user_id = ? AND generation = ? AND access_token = ? AND refresh_token = ?`, access, refresh, ToISO8601(expiry), ToISO8601(UTCNow()), a.TelegramUserID, a.Generation, a.AccessToken, a.RefreshToken)
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

func (d *Database) DeleteGoogleAccountGuarded(ctx context.Context, a models.GoogleAccount) (bool, error) {
	result, err := d.db.ExecContext(ctx, `DELETE FROM google_accounts WHERE telegram_user_id = ? AND generation = ? AND refresh_token = ? AND access_token = ?`, a.TelegramUserID, a.Generation, a.RefreshToken, a.AccessToken)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// Logout atomically invalidates in-flight callbacks and removes local credentials.
func (d *Database) Logout(ctx context.Context, user int64) (*models.GoogleAccount, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var refresh, email string
	err = tx.QueryRowContext(ctx, `SELECT refresh_token, gmail_email FROM google_accounts WHERE telegram_user_id = ?`, user).Scan(&refresh, &email)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	exists := err == nil
	if _, err = tx.ExecContext(ctx, `INSERT INTO auth_versions (telegram_user_id, generation) VALUES (?, 1) ON CONFLICT(telegram_user_id) DO UPDATE SET generation = generation + 1, attempt = ''`, user); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM oauth_states WHERE telegram_user_id = ?`, user); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM google_accounts WHERE telegram_user_id = ?`, user); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return &models.GoogleAccount{TelegramUserID: user, GmailEmail: email, RefreshToken: refresh}, nil
}

func (d *Database) MarkReloginPromptSentGuarded(ctx context.Context, a models.GoogleAccount) error {
	if a.ReloginPromptDueAt == nil {
		return nil
	}
	_, err := d.db.ExecContext(ctx, `UPDATE google_accounts SET relogin_prompt_sent_at = ? WHERE telegram_user_id = ? AND generation = ? AND relogin_prompt_due_at = ?`, ToISO8601(UTCNow()), a.TelegramUserID, a.Generation, ToISO8601(*a.ReloginPromptDueAt))
	return err
}
