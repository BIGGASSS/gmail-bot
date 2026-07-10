package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

type dummyDatabase struct {
	deletedUserIDs           []int64
	reloginPromptSentUserIDs []int64
}

func (d *dummyDatabase) ListGoogleAccounts(ctx context.Context) ([]models.GoogleAccount, error) {
	return nil, nil
}
func (d *dummyDatabase) UpdateLastHistoryID(ctx context.Context, telegramUserID int64, lastHistoryID string) error {
	return nil
}
func (d *dummyDatabase) MarkMessageDelivered(ctx context.Context, telegramUserID int64, gmailMessageID string, telegramChatID int64, telegramMessageID int) error {
	return nil
}
func (d *dummyDatabase) MarkReloginPromptSent(ctx context.Context, telegramUserID int64, sentAt *time.Time) error {
	d.reloginPromptSentUserIDs = append(d.reloginPromptSentUserIDs, telegramUserID)
	return nil
}
func (d *dummyDatabase) DeleteGoogleAccount(ctx context.Context, telegramUserID int64) error {
	d.deletedUserIDs = append(d.deletedUserIDs, telegramUserID)
	return nil
}

type dummyNotifier struct {
	manualReloginPrompts []manualPrompt
	reloginRequests      []reloginRequest
}

type manualPrompt struct {
	chatID     int64
	gmailEmail string
	delayDays  int
}

type reloginRequest struct {
	chatID     int64
	gmailEmail string
}

func (n *dummyNotifier) SendMailNotification(ctx context.Context, chatID int64, mail models.IncomingMail) (SentMessage, error) {
	return SentMessage{}, nil
}
func (n *dummyNotifier) SendManualReloginPrompt(ctx context.Context, chatID int64, gmailEmail string, delayDays int) error {
	n.manualReloginPrompts = append(n.manualReloginPrompts, manualPrompt{chatID: chatID, gmailEmail: gmailEmail, delayDays: delayDays})
	return nil
}
func (n *dummyNotifier) SendReloginRequired(ctx context.Context, chatID int64, gmailEmail string) error {
	n.reloginRequests = append(n.reloginRequests, reloginRequest{chatID: chatID, gmailEmail: gmailEmail})
	return nil
}

type dummyGmail struct{}

func (dummyGmail) ListNewInboxMessages(ctx context.Context, account models.GoogleAccount) ([]models.IncomingMail, string, error) {
	return nil, account.LastHistoryID, nil
}

func TestManualReloginPromptSendsOnceWhenDue(t *testing.T) {
	database := &dummyDatabase{}
	notifier := &dummyNotifier{}
	poller := NewGmailPoller(StaticPollerSettings{Interval: 45 * time.Second}, database, dummyGmail{}, notifier)
	connectedAt := time.Now().UTC().Add(-models.ManualReloginPromptDelay)
	dueAt := connectedAt.Add(models.ManualReloginPromptDelay)
	account := models.GoogleAccount{
		TelegramUserID:         123,
		GmailEmail:             "user@example.com",
		AccessToken:            "access-token",
		RefreshToken:           "refresh-token",
		TokenExpiry:            time.Now().UTC().Add(time.Hour),
		LastHistoryID:          "100",
		ConnectedAt:            connectedAt,
		ReloginPromptDueAt:     &dueAt,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: models.DefaultReloginPromptDelayDays,
	}

	if err := poller.SendManualReloginPromptIfDue(context.Background(), account); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.manualReloginPrompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(notifier.manualReloginPrompts))
	}
	prompt := notifier.manualReloginPrompts[0]
	if prompt.chatID != 123 || prompt.gmailEmail != "user@example.com" || prompt.delayDays != 6 {
		t.Fatalf("unexpected prompt: %+v", prompt)
	}
	if len(database.reloginPromptSentUserIDs) != 1 || database.reloginPromptSentUserIDs[0] != 123 {
		t.Fatalf("expected sent mark: %+v", database.reloginPromptSentUserIDs)
	}
}

func TestManualReloginPromptSkipsWhenDisabled(t *testing.T) {
	database := &dummyDatabase{}
	notifier := &dummyNotifier{}
	poller := NewGmailPoller(StaticPollerSettings{Interval: 45 * time.Second}, database, dummyGmail{}, notifier)
	connectedAt := time.Now().UTC().Add(-models.ManualReloginPromptDelay)
	dueAt := connectedAt.Add(models.ManualReloginPromptDelay)
	account := models.GoogleAccount{
		TelegramUserID:         123,
		GmailEmail:             "user@example.com",
		AccessToken:            "access-token",
		RefreshToken:           "refresh-token",
		TokenExpiry:            time.Now().UTC().Add(time.Hour),
		LastHistoryID:          "100",
		ConnectedAt:            connectedAt,
		ReloginPromptDueAt:     &dueAt,
		ReloginPromptEnabled:   false,
		ReloginPromptDelayDays: models.DefaultReloginPromptDelayDays,
	}

	if err := poller.SendManualReloginPromptIfDue(context.Background(), account); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.manualReloginPrompts) != 0 {
		t.Fatalf("expected no prompts, got %d", len(notifier.manualReloginPrompts))
	}
	if len(database.reloginPromptSentUserIDs) != 0 {
		t.Fatalf("expected no sent marks")
	}
}

func TestManualReloginPromptSkipsWhenNotDue(t *testing.T) {
	database := &dummyDatabase{}
	notifier := &dummyNotifier{}
	poller := NewGmailPoller(StaticPollerSettings{Interval: 45 * time.Second}, database, dummyGmail{}, notifier)
	connectedAt := time.Now().UTC()
	dueAt := connectedAt.Add(models.ManualReloginPromptDelay)
	account := models.GoogleAccount{
		TelegramUserID:         123,
		GmailEmail:             "user@example.com",
		AccessToken:            "access-token",
		RefreshToken:           "refresh-token",
		TokenExpiry:            time.Now().UTC().Add(time.Hour),
		LastHistoryID:          "100",
		ConnectedAt:            connectedAt,
		ReloginPromptDueAt:     &dueAt,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: models.DefaultReloginPromptDelayDays,
	}

	if err := poller.SendManualReloginPromptIfDue(context.Background(), account); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.manualReloginPrompts) != 0 {
		t.Fatalf("expected no prompts")
	}
}

func TestHandleInvalidGrantNotifiesUserAndDisconnectsAccount(t *testing.T) {
	database := &dummyDatabase{}
	notifier := &dummyNotifier{}
	poller := NewGmailPoller(StaticPollerSettings{Interval: 45 * time.Second}, database, dummyGmail{}, notifier)
	account := models.GoogleAccount{
		TelegramUserID: 123,
		GmailEmail:     "user@example.com",
		AccessToken:    "access-token",
		RefreshToken:   "refresh-token",
		TokenExpiry:    time.Now().UTC().Add(time.Hour),
		LastHistoryID:  "100",
		ConnectedAt:    time.Now().UTC(),
	}
	errorValue := &oauth.OAuthError{
		Message:          "Google OAuth request failed: 400 invalid_grant",
		StatusCode:       400,
		ErrorCode:        "invalid_grant",
		ErrorDescription: "Token has been expired or revoked.",
	}

	poller.HandleInvalidGrant(context.Background(), account, errorValue)

	if len(notifier.reloginRequests) != 1 {
		t.Fatalf("expected 1 relogin request, got %d", len(notifier.reloginRequests))
	}
	if notifier.reloginRequests[0].chatID != 123 || notifier.reloginRequests[0].gmailEmail != "user@example.com" {
		t.Fatalf("unexpected request: %+v", notifier.reloginRequests[0])
	}
	if len(database.deletedUserIDs) != 1 || database.deletedUserIDs[0] != 123 {
		t.Fatalf("expected account deletion: %+v", database.deletedUserIDs)
	}
}
