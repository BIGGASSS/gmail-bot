package telegram

import (
	"context"
	"errors"
	"github.com/BIGGASSS/gmail-bot/internal/applog"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/gmail"
	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

type PollerSettings interface {
	PollInterval() time.Duration
}

type PollerDatabase interface {
	ListGoogleAccounts(ctx context.Context) ([]models.GoogleAccount, error)
	UpdateLastHistoryID(ctx context.Context, telegramUserID int64, lastHistoryID string) error
	MarkMessageDelivered(ctx context.Context, telegramUserID int64, gmailMessageID string, telegramChatID int64, telegramMessageID int) error
	MarkReloginPromptSent(ctx context.Context, telegramUserID int64, sentAt *time.Time) error
	DeleteGoogleAccount(ctx context.Context, telegramUserID int64) error
}

type PollerGmail interface {
	ListNewInboxMessages(ctx context.Context, account models.GoogleAccount) ([]models.IncomingMail, string, error)
}

type PollerNotifier interface {
	SendMailNotification(ctx context.Context, chatID int64, mail models.IncomingMail) (SentMessage, error)
	SendManualReloginPrompt(ctx context.Context, chatID int64, gmailEmail string, delayDays int) error
	SendReloginRequired(ctx context.Context, chatID int64, gmailEmail string) error
}

type StaticPollerSettings struct {
	Interval time.Duration
}

func (s StaticPollerSettings) PollInterval() time.Duration {
	return s.Interval
}

type GmailPoller struct {
	settings PollerSettings
	database PollerDatabase
	gmail    PollerGmail
	notifier PollerNotifier
	now      func() time.Time
}

func NewGmailPoller(settings PollerSettings, database PollerDatabase, gmailService PollerGmail, notifier PollerNotifier) *GmailPoller {
	return &GmailPoller{
		settings: settings,
		database: database,
		gmail:    gmailService,
		notifier: notifier,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (p *GmailPoller) Run(ctx context.Context) error {
	for {
		accounts, err := p.database.ListGoogleAccounts(ctx)
		if err != nil {
			applog.Errorf("Failed to list Google accounts: %v", err)
		} else {
			for _, account := range accounts {
				if err := p.processAccount(ctx, account); err != nil {
					if IsForbidden(err) {
						applog.Warningf("Telegram user %d blocked the bot.", account.TelegramUserID)
						continue
					}
					var oauthErr *oauth.OAuthError
					if errors.As(err, &oauthErr) {
						if oauthErr.IsInvalidGrant() {
							p.handleInvalidGrant(ctx, account, oauthErr)
							continue
						}
						applog.Errorf("Failed while polling Gmail for Telegram user %d: %v", account.TelegramUserID, err)
						continue
					}
					var gmailErr *gmail.APIError
					var histErr *gmail.HistoryExpiredError
					var apiErr *APIError
					if errors.As(err, &gmailErr) || errors.As(err, &histErr) || errors.As(err, &apiErr) {
						applog.Errorf("Failed while polling Gmail for Telegram user %d: %v", account.TelegramUserID, err)
						continue
					}
					applog.Errorf("Failed while polling Gmail for Telegram user %d: %v", account.TelegramUserID, err)
				}
			}
		}

		timer := time.NewTimer(p.settings.PollInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (p *GmailPoller) processAccount(ctx context.Context, account models.GoogleAccount) error {
	if err := p.SendManualReloginPromptIfDue(ctx, account); err != nil {
		return err
	}

	newMessages, latestHistoryID, err := p.gmail.ListNewInboxMessages(ctx, account)
	if err != nil {
		var histErr *gmail.HistoryExpiredError
		if errors.As(err, &histErr) {
			if updateErr := p.database.UpdateLastHistoryID(ctx, account.TelegramUserID, histErr.CurrentHistoryID); updateErr != nil {
				return updateErr
			}
			applog.Infof("Reset Gmail history cursor for user %d to %s.", account.TelegramUserID, histErr.CurrentHistoryID)
			return nil
		}
		return err
	}

	for _, mail := range newMessages {
		sent, err := p.notifier.SendMailNotification(ctx, account.TelegramUserID, mail)
		if err != nil {
			return err
		}
		if err := p.database.MarkMessageDelivered(ctx, account.TelegramUserID, mail.GmailMessageID, sent.ChatID, sent.MessageID); err != nil {
			return err
		}
	}

	// Advance the history cursor only after every message in the batch was
	// delivered, so a failed send or delivery mark does not skip mail.
	if latestHistoryID != account.LastHistoryID {
		if err := p.database.UpdateLastHistoryID(ctx, account.TelegramUserID, latestHistoryID); err != nil {
			return err
		}
	}
	return nil
}

func (p *GmailPoller) SendManualReloginPromptIfDue(ctx context.Context, account models.GoogleAccount) error {
	if !account.ReloginPromptEnabled {
		return nil
	}
	if account.ReloginPromptDueAt == nil || account.ReloginPromptSentAt != nil {
		return nil
	}
	if account.ReloginPromptDueAt.After(p.now()) {
		return nil
	}

	if err := p.notifier.SendManualReloginPrompt(ctx, account.TelegramUserID, account.GmailEmail, account.ReloginPromptDelayDays); err != nil {
		if IsForbidden(err) {
			return err
		}
		applog.Errorf("Failed to send manual Gmail reconnect prompt to Telegram user %d: %v", account.TelegramUserID, err)
		return nil
	}
	return p.database.MarkReloginPromptSent(ctx, account.TelegramUserID, nil)
}

func (p *GmailPoller) HandleInvalidGrant(ctx context.Context, account models.GoogleAccount, exc error) {
	p.handleInvalidGrant(ctx, account, exc)
}

func (p *GmailPoller) handleInvalidGrant(ctx context.Context, account models.GoogleAccount, exc error) {
	applog.Warningf(
		"Google authorization expired or was revoked for Telegram user %d (%s): %v",
		account.TelegramUserID,
		account.GmailEmail,
		exc,
	)
	if err := p.notifier.SendReloginRequired(ctx, account.TelegramUserID, account.GmailEmail); err != nil {
		if IsForbidden(err) {
			applog.Warningf("Telegram user %d blocked the bot.", account.TelegramUserID)
		} else {
			applog.Errorf("Failed to notify Telegram user %d about expired Gmail authorization: %v", account.TelegramUserID, err)
		}
	}
	if err := p.database.DeleteGoogleAccount(ctx, account.TelegramUserID); err != nil {
		applog.Errorf("Failed to disconnect Gmail account for Telegram user %d: %v", account.TelegramUserID, err)
		return
	}
	applog.Infof("Disconnected Gmail account for Telegram user %d after invalid_grant.", account.TelegramUserID)
}
