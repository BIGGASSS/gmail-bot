package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/database"
)

func TestRelogCommandPrivateLinkKeepsConnection(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	api := &fakeUpdateClient{}
	revoker := &fakeRevoker{}
	bot := newTestBot(t, api, revoker, db)
	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/relog"))
	if texts := api.texts(); len(texts) != 1 || !strings.Contains(texts[0], "/login") {
		t.Fatalf("missing login guidance: %v", texts)
	}
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{TelegramUserID: 1, GmailEmail: "user@example.com", AccessToken: "access", RefreshToken: "refresh", TokenExpiry: now, ConnectedAt: now, LastHistoryID: "123"}); err != nil {
		t.Fatal(err)
	}
	if err := db.StoreOAuthState(ctx, "previous", 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	api = &fakeUpdateClient{}
	bot = newTestBot(t, api, revoker, db)
	bot.HandleUpdate(ctx, commandUpdate(1, -100, "/relog"))
	private, group := api.messagesForChat(1), api.messagesForChat(-100)
	if len(private) != 1 || !strings.Contains(private[0], "15 minutes") || !strings.Contains(private[0], "Forwarding stays active") {
		t.Fatalf("private reply: %v", private)
	}
	if len(group) != 1 || strings.Contains(group[0], "https://") {
		t.Fatalf("link leaked: %v", group)
	}
	stateText := strings.Split(strings.Split(private[0], "state=")[1], "\"")[0]
	state, err := db.ConsumeOAuthState(ctx, stateText)
	if err != nil || state == nil || !state.Relog {
		t.Fatalf("missing relog state %q: %+v %v", stateText, state, err)
	}
	if state.ExpiresAt.Sub(state.CreatedAt) > 15*time.Minute || state.ExpiresAt.Sub(state.CreatedAt) < 14*time.Minute {
		t.Fatal("wrong expiration")
	}
	old, err := db.ConsumeOAuthState(ctx, "previous")
	if err != nil || old != nil {
		t.Fatal("old login still valid")
	}
	a, err := db.GetGoogleAccount(ctx, 1)
	if err != nil || a == nil || a.AccessToken != "access" || a.LastHistoryID != "123" {
		t.Fatal("relog start changed account")
	}
	if len(revoker.revoked) != 0 {
		t.Fatal("relog revoked tokens")
	}
}

func TestStaleInvalidGrantDoesNotNotifyOrDelete(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	now := database.UTCNow()
	p := database.UpsertGoogleAccountParams{TelegramUserID: 1, GmailEmail: "user@example.com", AccessToken: "old", RefreshToken: "refresh", TokenExpiry: now, ConnectedAt: now, LastHistoryID: "123"}
	if err := db.UpsertGoogleAccount(ctx, p); err != nil {
		t.Fatal(err)
	}
	old, _ := db.GetGoogleAccount(ctx, 1)
	p.AccessToken = "new"
	if err := db.UpsertGoogleAccount(ctx, p); err != nil {
		t.Fatal(err)
	}
	notifier := &dummyNotifier{}
	poller := NewGmailPoller(StaticPollerSettings{}, db, nil, notifier)
	poller.HandleInvalidGrant(ctx, *old, nil)
	if len(notifier.reloginRequests) != 0 {
		t.Fatal("sent misleading expiration notification")
	}
	a, err := db.GetGoogleAccount(ctx, 1)
	if err != nil || a == nil || a.AccessToken != "new" {
		t.Fatal("deleted refreshed account")
	}
	poller.HandleInvalidGrant(ctx, *a, nil)
	a, err = db.GetGoogleAccount(ctx, 1)
	if err != nil || a != nil || len(notifier.reloginRequests) != 1 {
		t.Fatal("current invalid_grant not handled")
	}
}
