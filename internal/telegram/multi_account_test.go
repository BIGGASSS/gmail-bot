package telegram

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/config"
	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/gmail"
	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func seedAccounts(t *testing.T, db *database.Database, user int64, emails ...string) []models.GoogleAccount {
	t.Helper()
	ctx := context.Background()
	now := database.UTCNow()
	for _, email := range emails {
		if _, err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
			TelegramUserID:       user,
			GmailEmail:           email,
			AccessToken:          "access-" + email,
			RefreshToken:         "refresh-" + email,
			TokenExpiry:          now.Add(time.Hour),
			LastHistoryID:        "10",
			ConnectedAt:          now,
			ReloginPromptEnabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	accounts, err := db.ListGoogleAccountsForUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	return accounts
}

func callbackUpdate(userID, chatID int64, messageID int, data string) tgbotapi.Update {
	return tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "cb",
		From:    &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{MessageID: messageID, Chat: &tgbotapi.Chat{ID: chatID}},
		Data:    data,
	}}
}

func callbackAlerts(api *fakeUpdateClient) []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	var out []string
	for _, req := range api.requests {
		if cb, ok := req.(tgbotapi.CallbackConfig); ok && cb.Text != "" {
			out = append(out, cb.Text)
		}
	}
	return out
}

func TestStatusAndStartListEveryAccount(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	seedAccounts(t, db, 1, "a@example.com", "b@example.com")
	api := &fakeUpdateClient{}
	bot := newTestBot(t, api, &fakeRevoker{}, db)

	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/status"))
	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/start"))
	texts := api.texts()
	if len(texts) != 2 {
		t.Fatalf("replies=%v", texts)
	}
	for _, want := range []string{"Connected Gmail account: a@example.com", "Connected Gmail account: b@example.com", "Polling interval: 45 seconds"} {
		if !strings.Contains(texts[0], want) {
			t.Fatalf("status missing %q: %q", want, texts[0])
		}
	}
	if strings.Count(texts[0], "Manual reconnect reminder due:") != 2 {
		t.Fatalf("status missing per-account due dates: %q", texts[0])
	}
	if !strings.Contains(texts[1], "Connected Gmail accounts: a@example.com, b@example.com") {
		t.Fatalf("start=%q", texts[1])
	}
}

func TestLogoutWithSeveralAccountsShowsPicker(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	accounts := seedAccounts(t, db, 1, "a@example.com", "b@example.com")
	if err := db.StoreOAuthState(ctx, "pending", 1, database.UTCNow().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	api := &fakeUpdateClient{}
	revoker := &fakeRevoker{}
	bot := newTestBot(t, api, revoker, db)

	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/logout"))

	if len(revoker.revoked) != 0 {
		t.Fatalf("picker revoked tokens: %v", revoker.revoked)
	}
	if remaining, _ := db.ListGoogleAccountsForUser(ctx, 1); len(remaining) != 2 {
		t.Fatalf("picker removed accounts: %+v", remaining)
	}
	if len(api.messages) != 1 || !strings.Contains(api.messages[0].text, "Which Gmail account") {
		t.Fatalf("messages=%+v", api.messages)
	}
	markup, ok := api.messages[0].markup.(tgbotapi.InlineKeyboardMarkup)
	if !ok || len(markup.InlineKeyboard) != 3 {
		t.Fatalf("markup=%#v", api.messages[0].markup)
	}
	var labels, data []string
	for _, row := range markup.InlineKeyboard {
		labels = append(labels, row[0].Text)
		data = append(data, *row[0].CallbackData)
	}
	wantData := []string{
		BuildLogoutCallbackData(1, accounts[0].ID),
		BuildLogoutCallbackData(1, accounts[1].ID),
		BuildLogoutCallbackData(1, LogoutAllAccounts),
	}
	if strings.Join(labels, ",") != "a@example.com,b@example.com,All accounts" || strings.Join(data, ",") != strings.Join(wantData, ",") {
		t.Fatalf("labels=%v data=%v", labels, data)
	}
	for _, d := range data {
		if len(d) > 64 {
			t.Fatalf("callback data exceeds Telegram limit: %q", d)
		}
	}
	// Showing the picker does not yet invalidate a pending login.
	if state, err := db.ConsumeOAuthState(ctx, "pending"); err != nil || state == nil {
		t.Fatalf("pending login invalidated by picker: %+v %v", state, err)
	}
}

func TestLogoutButtonDisconnectsOnlyThatAccount(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	accounts := seedAccounts(t, db, 1, "a@example.com", "b@example.com")
	api := &fakeUpdateClient{}
	revoker := &fakeRevoker{}
	bot := newTestBot(t, api, revoker, db)

	bot.HandleUpdate(ctx, callbackUpdate(1, 1, 7, BuildLogoutCallbackData(1, accounts[0].ID)))

	if len(revoker.revoked) != 1 || revoker.revoked[0] != "refresh-a@example.com" {
		t.Fatalf("revoked=%v", revoker.revoked)
	}
	remaining, err := db.ListGoogleAccountsForUser(ctx, 1)
	if err != nil || len(remaining) != 1 || remaining[0].GmailEmail != "b@example.com" {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
	}
	if len(api.messages) != 1 || !api.messages[0].edit || !strings.Contains(api.messages[0].text, "Disconnected your Gmail account a@example.com") {
		t.Fatalf("messages=%+v", api.messages)
	}

	// Pressing the same button again is harmless.
	bot.HandleUpdate(ctx, callbackUpdate(1, 1, 7, BuildLogoutCallbackData(1, accounts[0].ID)))
	if len(revoker.revoked) != 1 || !strings.Contains(api.texts()[1], "no longer connected") {
		t.Fatalf("second press: revoked=%v texts=%v", revoker.revoked, api.texts())
	}
}

func TestLogoutAllButtonReportsRevocationFailures(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	seedAccounts(t, db, 1, "a@example.com", "b@example.com")
	api := &fakeUpdateClient{}
	revoker := &fakeRevoker{fail: true}
	bot := newTestBot(t, api, revoker, db)

	bot.HandleUpdate(ctx, callbackUpdate(1, 1, 7, BuildLogoutCallbackData(1, LogoutAllAccounts)))

	if len(revoker.revoked) != 2 {
		t.Fatalf("revoked=%v", revoker.revoked)
	}
	if remaining, _ := db.ListGoogleAccountsForUser(ctx, 1); len(remaining) != 0 {
		t.Fatalf("remaining=%+v", remaining)
	}
	texts := api.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "a@example.com, b@example.com") || !strings.Contains(texts[0], "revocation failed") || !strings.Contains(texts[0], "https://myaccount.google.com/connections") {
		t.Fatalf("texts=%v", texts)
	}
}

func TestLogoutButtonRejectsAnotherUser(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	accounts := seedAccounts(t, db, 1, "a@example.com", "b@example.com")
	seedAccounts(t, db, 2, "c@example.com")
	settings := testSettings()
	settings.AuthorizedTelegramUserIDs = map[int64]struct{}{1: {}, 2: {}}
	api := &fakeUpdateClient{}
	revoker := &fakeRevoker{}
	bot := NewBot(settings, api, db, revoker, nil, NewNotifier(api))

	for _, data := range []string{BuildLogoutCallbackData(1, accounts[0].ID), BuildLogoutCallbackData(1, LogoutAllAccounts)} {
		bot.HandleUpdate(ctx, callbackUpdate(2, -100, 7, data))
	}

	if len(revoker.revoked) != 0 {
		t.Fatalf("revoked=%v", revoker.revoked)
	}
	for _, user := range []int64{1, 2} {
		if remaining, _ := db.ListGoogleAccountsForUser(ctx, user); len(remaining) == 0 {
			t.Fatalf("user %d lost accounts", user)
		}
	}
	if alerts := callbackAlerts(api); len(alerts) != 2 || !strings.Contains(alerts[0], "someone else") {
		t.Fatalf("alerts=%v", alerts)
	}
}

func TestExpandUsesAccountThatReceivedMessage(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	accounts := seedAccounts(t, db, 1, "a@example.com", "b@example.com")
	if err := db.MarkMessageDelivered(ctx, accounts[1].ID, "gmail-b", 1, 42); err != nil {
		t.Fatal(err)
	}

	var authHeaders []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "gmail-b",
			"payload": map[string]any{
				"mimeType": "text/plain",
				"headers":  []map[string]any{{"name": "Subject", "value": "Quarterly report"}},
				"body":     map[string]any{"data": base64.URLEncoding.EncodeToString([]byte("body from b"))},
			},
		})
	}))
	defer upstream.Close()
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		u, _ := url.Parse(upstream.URL)
		cloned.URL.Scheme, cloned.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(cloned)
	})}
	oauthClient := oauth.NewClient(config.Settings{GoogleClientID: "id", GoogleClientSecret: "secret", AppBaseURL: "https://example.com"}, client)
	gmailService := gmail.NewService(client, oauthClient, db)
	api := &fakeUpdateClient{}
	bot := NewBot(testSettings(), api, db, oauthClient, gmailService, NewNotifier(api))

	bot.HandleUpdate(ctx, callbackUpdate(1, 1, 42, BuildExpandCallbackData("gmail-b", 0)))

	if len(authHeaders) != 1 || authHeaders[0] != "Bearer access-b@example.com" {
		t.Fatalf("authorization=%v", authHeaders)
	}
	texts := api.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "body from b") {
		t.Fatalf("texts=%v", texts)
	}

	// A button whose message ID does not match the delivered notification is rejected.
	bot.HandleUpdate(ctx, callbackUpdate(1, 1, 42, BuildExpandCallbackData("gmail-other", 0)))
	if len(authHeaders) != 1 {
		t.Fatalf("mismatched expand fetched mail: %v", authHeaders)
	}
	if alerts := callbackAlerts(api); len(alerts) != 1 || !strings.Contains(alerts[0], "no longer available") {
		t.Fatalf("alerts=%v", alerts)
	}
}
