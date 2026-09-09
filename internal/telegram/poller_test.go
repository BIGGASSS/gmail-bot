package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

type dummyDatabase struct {
	calls                    []string
	deletedUserIDs           []int64
	reloginPromptSentUserIDs []int64
	updatedHistoryID         string
	markDeliveredCalls       int
	failMarkDeliveredOnCall  int
}

func (d *dummyDatabase) ListGoogleAccounts(ctx context.Context) ([]models.GoogleAccount, error) {
	return nil, nil
}
func (d *dummyDatabase) UpdateLastHistoryID(ctx context.Context, telegramUserID int64, lastHistoryID string) error {
	d.calls = append(d.calls, "UpdateLastHistoryID")
	d.updatedHistoryID = lastHistoryID
	return nil
}
func (d *dummyDatabase) MarkMessageDelivered(ctx context.Context, telegramUserID int64, gmailMessageID string, telegramChatID int64, telegramMessageID int) error {
	d.calls = append(d.calls, "MarkMessageDelivered")
	d.markDeliveredCalls++
	if d.failMarkDeliveredOnCall != 0 && d.markDeliveredCalls == d.failMarkDeliveredOnCall {
		return errors.New("mark delivered failed")
	}
	return nil
}
func (d *dummyDatabase) MarkReloginPromptSent(ctx context.Context, telegramUserID int64, sentAt *time.Time) error {
	d.calls = append(d.calls, "MarkReloginPromptSent")
	d.reloginPromptSentUserIDs = append(d.reloginPromptSentUserIDs, telegramUserID)
	return nil
}
func (d *dummyDatabase) DeleteGoogleAccount(ctx context.Context, telegramUserID int64) error {
	d.calls = append(d.calls, "DeleteGoogleAccount")
	d.deletedUserIDs = append(d.deletedUserIDs, telegramUserID)
	return nil
}

type dummyNotifier struct {
	calls                []string
	sentMessages         []models.IncomingMail
	manualReloginPrompts []manualPrompt
	reloginRequests      []reloginRequest
	sendMailCalls        int
	failSendMailOnCall   int
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
	n.calls = append(n.calls, "SendMailNotification")
	n.sendMailCalls++
	if n.failSendMailOnCall != 0 && n.sendMailCalls == n.failSendMailOnCall {
		return SentMessage{}, errors.New("send mail notification failed")
	}
	n.sentMessages = append(n.sentMessages, mail)
	return SentMessage{ChatID: chatID, MessageID: n.sendMailCalls}, nil
}
func (n *dummyNotifier) SendManualReloginPrompt(ctx context.Context, chatID int64, gmailEmail string, delayDays int) error {
	n.calls = append(n.calls, "SendManualReloginPrompt")
	n.manualReloginPrompts = append(n.manualReloginPrompts, manualPrompt{chatID: chatID, gmailEmail: gmailEmail, delayDays: delayDays})
	return nil
}
func (n *dummyNotifier) SendReloginRequired(ctx context.Context, chatID int64, gmailEmail string) error {
	n.calls = append(n.calls, "SendReloginRequired")
	n.reloginRequests = append(n.reloginRequests, reloginRequest{chatID: chatID, gmailEmail: gmailEmail})
	return nil
}

type dummyGmail struct{}

func (dummyGmail) ListNewInboxMessages(ctx context.Context, account models.GoogleAccount) ([]models.IncomingMail, string, error) {
	return nil, account.LastHistoryID, nil
}

type scriptGmail struct {
	messages  []models.IncomingMail
	historyID string
}

func (s scriptGmail) ListNewInboxMessages(ctx context.Context, account models.GoogleAccount) ([]models.IncomingMail, string, error) {
	return s.messages, s.historyID, nil
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

func testPollerAccount() models.GoogleAccount {
	return models.GoogleAccount{
		TelegramUserID: 123,
		GmailEmail:     "user@example.com",
		AccessToken:    "access-token",
		RefreshToken:   "refresh-token",
		TokenExpiry:    time.Now().UTC().Add(time.Hour),
		LastHistoryID:  "100",
		ConnectedAt:    time.Now().UTC(),
	}
}

func testIncomingMails() []models.IncomingMail {
	return []models.IncomingMail{
		{GmailMessageID: "msg-1", FromHeader: "a@example.com", Subject: "First"},
		{GmailMessageID: "msg-2", FromHeader: "b@example.com", Subject: "Second"},
	}
}

func indexOfCall(calls []string, name string) int {
	for i, call := range calls {
		if call == name {
			return i
		}
	}
	return -1
}

func lastIndexOfCall(calls []string, name string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i] == name {
			return i
		}
	}
	return -1
}

func TestProcessAccountAdvancesCursorAfterDelivery(t *testing.T) {
	database := &dummyDatabase{}
	notifier := &dummyNotifier{}
	gmailService := scriptGmail{messages: testIncomingMails(), historyID: "200"}
	poller := NewGmailPoller(StaticPollerSettings{Interval: 45 * time.Second}, database, gmailService, notifier)

	if err := poller.processAccount(context.Background(), testPollerAccount()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updateIndex := lastIndexOfCall(database.calls, "UpdateLastHistoryID")
	if updateIndex == -1 {
		t.Fatalf("expected UpdateLastHistoryID to be called, calls: %v", database.calls)
	}
	if database.updatedHistoryID != "200" {
		t.Fatalf("expected cursor 200, got %q", database.updatedHistoryID)
	}
	firstMark := indexOfCall(database.calls, "MarkMessageDelivered")
	lastMark := lastIndexOfCall(database.calls, "MarkMessageDelivered")
	if firstMark == -1 || firstMark == lastMark {
		t.Fatalf("expected 2 MarkMessageDelivered calls, calls: %v", database.calls)
	}
	if updateIndex < lastMark {
		t.Fatalf("expected UpdateLastHistoryID after all MarkMessageDelivered calls, calls: %v", database.calls)
	}
	if len(notifier.sentMessages) != 2 {
		t.Fatalf("expected 2 sent messages, got %d", len(notifier.sentMessages))
	}
}

func TestProcessAccountDoesNotAdvanceCursorOnSendFailure(t *testing.T) {
	database := &dummyDatabase{}
	notifier := &dummyNotifier{failSendMailOnCall: 2}
	gmailService := scriptGmail{messages: testIncomingMails(), historyID: "200"}
	poller := NewGmailPoller(StaticPollerSettings{Interval: 45 * time.Second}, database, gmailService, notifier)

	if err := poller.processAccount(context.Background(), testPollerAccount()); err == nil {
		t.Fatal("expected error from failing SendMailNotification")
	}

	if indexOfCall(database.calls, "UpdateLastHistoryID") != -1 {
		t.Fatalf("expected UpdateLastHistoryID to never be called, calls: %v", database.calls)
	}
	if len(notifier.sentMessages) != 1 || notifier.sentMessages[0].GmailMessageID != "msg-1" {
		t.Fatalf("expected first message to be sent, sent: %+v", notifier.sentMessages)
	}
	if indexOfCall(database.calls, "MarkMessageDelivered") == -1 {
		t.Fatalf("expected first message to be marked delivered, calls: %v", database.calls)
	}
}

func TestProcessAccountDoesNotAdvanceCursorOnMarkDeliveredFailure(t *testing.T) {
	database := &dummyDatabase{failMarkDeliveredOnCall: 2}
	notifier := &dummyNotifier{}
	gmailService := scriptGmail{messages: testIncomingMails(), historyID: "200"}
	poller := NewGmailPoller(StaticPollerSettings{Interval: 45 * time.Second}, database, gmailService, notifier)

	if err := poller.processAccount(context.Background(), testPollerAccount()); err == nil {
		t.Fatal("expected error from failing MarkMessageDelivered")
	}

	if indexOfCall(database.calls, "UpdateLastHistoryID") != -1 {
		t.Fatalf("expected UpdateLastHistoryID to never be called, calls: %v", database.calls)
	}
}
