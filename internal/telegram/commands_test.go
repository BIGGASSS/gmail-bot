package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/config"
	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type fakeUpdateClient struct {
	mu       sync.Mutex
	messages []sentMessage
	requests []tgbotapi.Chattable
}

type sentMessage struct {
	chatID    int64
	text      string
	parseMode string
}

func (f *fakeUpdateClient) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch msg := c.(type) {
	case tgbotapi.MessageConfig:
		f.messages = append(f.messages, sentMessage{
			chatID:    msg.ChatID,
			text:      msg.Text,
			parseMode: msg.ParseMode,
		})
		return tgbotapi.Message{MessageID: len(f.messages), Chat: &tgbotapi.Chat{ID: msg.ChatID}}, nil
	case tgbotapi.EditMessageTextConfig:
		f.messages = append(f.messages, sentMessage{
			chatID:    msg.ChatID,
			text:      msg.Text,
			parseMode: msg.ParseMode,
		})
		return tgbotapi.Message{MessageID: msg.MessageID, Chat: &tgbotapi.Chat{ID: msg.ChatID}}, nil
	default:
		return tgbotapi.Message{}, nil
	}
}

func (f *fakeUpdateClient) Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, c)
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (f *fakeUpdateClient) GetUpdates(config tgbotapi.UpdateConfig) ([]tgbotapi.Update, error) {
	return nil, nil
}

func (f *fakeUpdateClient) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.messages))
	for i, msg := range f.messages {
		out[i] = msg.text
	}
	return out
}

type fakeRevoker struct {
	revoked []string
	fail    bool
	authURL string
}

func (f *fakeRevoker) BuildAuthorizationURL(state string) string {
	if f.authURL != "" {
		return f.authURL + state
	}
	return "https://accounts.google.com/o/oauth2/v2/auth?state=" + state
}

func (f *fakeRevoker) RevokeToken(ctx context.Context, token string) error {
	f.revoked = append(f.revoked, token)
	if f.fail {
		return &oauth.OAuthError{Message: "revoke failed", StatusCode: 500}
	}
	return nil
}

func TestUnauthorizedUpdateProducesNoReply(t *testing.T) {
	api := &fakeUpdateClient{}
	bot := newTestBot(t, api, &fakeRevoker{}, nil)

	bot.HandleUpdate(context.Background(), tgbotapi.Update{
		Message: &tgbotapi.Message{
			MessageID: 1,
			From:      &tgbotapi.User{ID: 99},
			Chat:      &tgbotapi.Chat{ID: 99},
			Text:      "/status",
			Entities:  []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 7}},
		},
	})

	if texts := api.texts(); len(texts) != 0 {
		t.Fatalf("expected no replies for unauthorized user, got %v", texts)
	}
}

func TestStatusCommandThroughHandleUpdate(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID:         1,
		GmailEmail:             "user@example.com",
		AccessToken:            "access",
		RefreshToken:           "refresh",
		TokenExpiry:            now.Add(time.Hour),
		LastHistoryID:          "10",
		ConnectedAt:            now,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: models.DefaultReloginPromptDelayDays,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	api := &fakeUpdateClient{}
	bot := newTestBot(t, api, &fakeRevoker{}, db)

	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/status"))

	texts := api.texts()
	if len(texts) != 1 {
		t.Fatalf("expected 1 reply, got %v", texts)
	}
	if !strings.Contains(texts[0], "Connected Gmail account: user@example.com") {
		t.Fatalf("status reply=%q", texts[0])
	}
	if !strings.Contains(texts[0], "Manual reconnect reminder: on") {
		t.Fatalf("status reply missing reminder: %q", texts[0])
	}
	if !strings.Contains(texts[0], "Polling interval: 45 seconds") {
		t.Fatalf("status reply missing poll interval: %q", texts[0])
	}
}

func TestLogoutCommandRevokesTokenAndDeletesAccount(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID:         1,
		GmailEmail:             "user@example.com",
		AccessToken:            "access",
		RefreshToken:           "refresh-token",
		TokenExpiry:            now.Add(time.Hour),
		LastHistoryID:          "10",
		ConnectedAt:            now,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: 6,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	api := &fakeUpdateClient{}
	revoker := &fakeRevoker{}
	bot := newTestBot(t, api, revoker, db)

	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/logout"))

	if len(revoker.revoked) != 1 || revoker.revoked[0] != "refresh-token" {
		t.Fatalf("revoked=%v", revoker.revoked)
	}
	account, err := db.GetGoogleAccount(ctx, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if account != nil {
		t.Fatal("expected account deleted")
	}
	texts := api.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "Disconnected your Gmail account") {
		t.Fatalf("logout reply=%v", texts)
	}
}

func TestLogoutStillDisconnectsWhenRevokeFails(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID:         1,
		GmailEmail:             "user@example.com",
		AccessToken:            "access",
		RefreshToken:           "refresh-token",
		TokenExpiry:            now.Add(time.Hour),
		LastHistoryID:          "10",
		ConnectedAt:            now,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: 6,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	api := &fakeUpdateClient{}
	bot := newTestBot(t, api, &fakeRevoker{fail: true}, db)
	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/logout"))

	account, err := db.GetGoogleAccount(ctx, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if account != nil {
		t.Fatal("expected account deleted even if revoke fails")
	}
	texts := api.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "Disconnected your Gmail account") {
		t.Fatalf("logout reply=%v", texts)
	}
}

func TestLoginCommandStoresStateAndSendsLink(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	api := &fakeUpdateClient{}
	bot := newTestBot(t, api, &fakeRevoker{authURL: "https://example.com/auth?state="}, db)

	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/login"))

	texts := api.texts()
	if len(texts) != 1 {
		t.Fatalf("replies=%v", texts)
	}
	if !strings.Contains(texts[0], "Open this link to connect Gmail:") {
		t.Fatalf("login reply=%q", texts[0])
	}
	if !strings.Contains(texts[0], "https://example.com/auth?state=") {
		t.Fatalf("missing auth url: %q", texts[0])
	}
	if !strings.Contains(texts[0], "expires in 15 minutes") {
		t.Fatalf("missing expiry text: %q", texts[0])
	}
}

func TestLogoutThroughRealOAuthRevokeHTTP(t *testing.T) {
	ctx := context.Background()
	db := openTelegramTestDB(t)
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID:         1,
		GmailEmail:             "user@example.com",
		AccessToken:            "access",
		RefreshToken:           "refresh-token",
		TokenExpiry:            now.Add(time.Hour),
		LastHistoryID:          "10",
		ConnectedAt:            now,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: 6,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var revokedToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokedToken = r.URL.Query().Get("token")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	oauthClient := oauth.NewClient(config.Settings{
		GoogleClientID:     "id",
		GoogleClientSecret: "secret",
		AppBaseURL:         "https://example.com",
	}, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		u, _ := url.Parse(server.URL)
		cloned.URL.Scheme = u.Scheme
		cloned.URL.Host = u.Host
		cloned.URL.Path = "/"
		return http.DefaultTransport.RoundTrip(cloned)
	})})

	api := &fakeUpdateClient{}
	bot := NewBot(testSettings(), api, db, oauthClient, nil, NewNotifier(api))
	bot.HandleUpdate(ctx, commandUpdate(1, 1, "/logout"))

	if revokedToken != "refresh-token" {
		t.Fatalf("revokedToken=%q", revokedToken)
	}
	account, err := db.GetGoogleAccount(ctx, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if account != nil {
		t.Fatal("expected account deleted")
	}
}

func commandUpdate(userID, chatID int64, text string) tgbotapi.Update {
	entityLen := len(text)
	if i := strings.IndexByte(text, ' '); i >= 0 {
		entityLen = i
	}
	return tgbotapi.Update{
		Message: &tgbotapi.Message{
			MessageID: 1,
			From:      &tgbotapi.User{ID: userID},
			Chat:      &tgbotapi.Chat{ID: chatID},
			Text:      text,
			Entities: []tgbotapi.MessageEntity{{
				Type:   "bot_command",
				Offset: 0,
				Length: entityLen,
			}},
		},
	}
}

func newTestBot(t *testing.T, api UpdateClient, revoker TokenRevoker, db *database.Database) *Bot {
	t.Helper()
	if db == nil {
		db = openTelegramTestDB(t)
	}
	return NewBot(testSettings(), api, db, revoker, nil, NewNotifier(api))
}

func testSettings() config.Settings {
	return config.Settings{
		TelegramBotToken:          "token",
		AuthorizedTelegramUserIDs: map[int64]struct{}{1: {}},
		GoogleClientID:            "client",
		GoogleClientSecret:        "secret",
		AppBaseURL:                "https://example.com",
		GmailPollIntervalSeconds:  45,
		WebHost:                   "127.0.0.1",
		WebPort:                   8080,
		LogLevel:                  "INFO",
	}
}

func openTelegramTestDB(t *testing.T) *database.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bot.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	return db
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
