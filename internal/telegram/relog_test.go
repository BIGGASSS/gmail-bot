package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/database"
)

func TestRelogCommandAsksForLogoutThenLogin(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID: 1,
		GmailEmail:     "user@example.com",
		AccessToken:    "access",
		RefreshToken:   "refresh",
		TokenExpiry:    now,
		ConnectedAt:    now,
		LastHistoryID:  "123",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.StoreOAuthState(ctx, "previous", 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	api := &fakeUpdateClient{}
	revoker := &fakeRevoker{}
	bot := newTestBot(t, api, revoker, db)
	bot.HandleUpdate(ctx, commandUpdate(1, -100, "/relog"))

	texts := api.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "/logout") || !strings.Contains(texts[0], "/login") {
		t.Fatalf("reply: %v", texts)
	}
	if strings.Contains(texts[0], "https://") || strings.Contains(texts[0], "Forwarding stays active") {
		t.Fatalf("relog still started oauth: %v", texts)
	}
	state, err := db.ConsumeOAuthState(ctx, "previous")
	if err != nil || state == nil {
		t.Fatal("relog consumed or invalidated the existing login link")
	}
	account, err := db.GetGoogleAccount(ctx, 1)
	if err != nil || account == nil || account.AccessToken != "access" || account.LastHistoryID != "123" {
		t.Fatal("relog changed the connected account")
	}
	if len(revoker.revoked) != 0 {
		t.Fatal("relog revoked tokens")
	}
}
