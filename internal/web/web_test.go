package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/config"
	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/gmail"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

type captureNotifier struct {
	successes []string
	failures  []string
}

func (n *captureNotifier) SendLoginSuccess(ctx context.Context, chatID int64, gmailEmail string) error {
	n.successes = append(n.successes, gmailEmail)
	return nil
}

func (n *captureNotifier) SendLoginFailure(ctx context.Context, chatID int64, errorText string) error {
	n.failures = append(n.failures, errorText)
	return nil
}

func TestHealthz(t *testing.T) {
	server := newTestServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("payload=%v", payload)
	}
}

func TestRoot(t *testing.T) {
	server := newTestServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Body.String(); got == "" || !strings.Contains(got, "Gmail Telegram Bot is running") {
		t.Fatalf("body=%q", got)
	}
}

func TestCallbackMissingParameters(t *testing.T) {
	server := newTestServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Missing OAuth parameters") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestCallbackExpiredState(t *testing.T) {
	db := openDB(t)
	server := newTestServer(t, db, &captureNotifier{})
	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=missing&code=abc", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Login link expired or invalid") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestCallbackGoogleError(t *testing.T) {
	server := newTestServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?error=access_denied", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Google login failed") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestCallbackErrorParamCapped(t *testing.T) {
	server := newTestServer(t, nil, nil)
	bigError := strings.Repeat("a", 100000)
	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?error="+bigError, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Body.Len(); got >= 1024 {
		t.Fatalf("body length=%d, want < 1024", got)
	}
	if !strings.Contains(rec.Body.String(), "Google login failed") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestServerHasTimeouts(t *testing.T) {
	server := newTestServer(t, nil, nil)
	if server.httpServer.WriteTimeout == 0 {
		t.Fatal("expected WriteTimeout to be set")
	}
	if server.httpServer.ReadTimeout == 0 {
		t.Fatal("expected ReadTimeout to be set")
	}
}

func newTestServer(t *testing.T, db *database.Database, notifier LoginNotifier) *Server {
	t.Helper()
	if db == nil {
		db = openDB(t)
	}
	if notifier == nil {
		notifier = &captureNotifier{}
	}
	settings := config.Settings{
		TelegramBotToken:          "token",
		AuthorizedTelegramUserIDs: map[int64]struct{}{1: {}},
		GoogleClientID:            "client-id",
		GoogleClientSecret:        "client-secret",
		AppBaseURL:                "https://example.com",
		DatabasePath:              "bot.db",
		GmailPollIntervalSeconds:  45,
		WebHost:                   "127.0.0.1",
		WebPort:                   8080,
		LogLevel:                  "INFO",
	}
	httpClient := &http.Client{Timeout: time.Second}
	oauthClient := oauth.NewClient(settings, httpClient)
	gmailService := gmail.NewService(httpClient, oauthClient, db)
	return NewServer("127.0.0.1:0", db, oauthClient, gmailService, notifier)
}

func openDB(t *testing.T) *database.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bot.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(context.Background()); err != nil {
		t.Fatalf("init db: %v", err)
	}
	return db
}
