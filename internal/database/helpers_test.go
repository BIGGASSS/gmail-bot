package database

import (
	"context"
	"fmt"

	"github.com/BIGGASSS/gmail-bot/internal/models"
)

// userAccount returns the Telegram user's only connected account, or nil if none.
func userAccount(ctx context.Context, db *Database, telegramUserID int64) (*models.GoogleAccount, error) {
	accounts, err := db.ListGoogleAccountsForUser(ctx, telegramUserID)
	if err != nil || len(accounts) == 0 {
		return nil, err
	}
	if len(accounts) > 1 {
		return nil, fmt.Errorf("user %d has %d accounts", telegramUserID, len(accounts))
	}
	return &accounts[0], nil
}
