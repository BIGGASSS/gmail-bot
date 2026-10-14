package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/BIGGASSS/gmail-bot/internal/config"
	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/gmail"
	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

func TestCallbackSuccessfulConnection(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := db.StoreOAuthState(ctx, "state-ok", 42, database.UTCNow().Add(15*time.Minute)); err != nil {
		t.Fatalf("store state: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "auth-code" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"refresh_token": "refresh-1",
			"expires_in":    3600,
		})
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"emailAddress": "connected@example.com",
			"historyId":    "12345",
		})
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	notifier := &captureNotifier{}
	server := newServerWithUpstream(t, db, notifier, upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=state-ok&code=auth-code", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Gmail connected") {
		t.Fatalf("body=%q", rec.Body.String())
	}
	if len(notifier.successes) != 1 || notifier.successes[0] != "connected@example.com" {
		t.Fatalf("success notifications=%v", notifier.successes)
	}

	account, err := db.GetGoogleAccount(ctx, 42)
	if err != nil || account == nil {
		t.Fatalf("account err=%v account=%v", err, account)
	}
	if account.GmailEmail != "connected@example.com" {
		t.Fatalf("email=%q", account.GmailEmail)
	}
	if account.RefreshToken != "refresh-1" || account.AccessToken != "access-1" {
		t.Fatalf("tokens access=%q refresh=%q", account.AccessToken, account.RefreshToken)
	}
	if account.LastHistoryID != "12345" {
		t.Fatalf("history=%q", account.LastHistoryID)
	}
	if account.ReloginPromptBaseAt == nil || account.ReloginPromptDueAt == nil {
		t.Fatal("expected reminder schedule")
	}
	wantDue := account.ReloginPromptBaseAt.Add(time.Duration(models.DefaultReloginPromptDelayDays) * 24 * time.Hour)
	if !account.ReloginPromptDueAt.Equal(wantDue) {
		t.Fatalf("due_at=%v want %v", account.ReloginPromptDueAt, wantDue)
	}

	state, err := db.ConsumeOAuthState(ctx, "state-ok")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if state != nil {
		t.Fatal("expected state consumed")
	}
}

func TestCallbackReusesExistingRefreshToken(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID:         42,
		GmailEmail:             "old@example.com",
		AccessToken:            "old-access",
		RefreshToken:           "existing-refresh",
		TokenExpiry:            now.Add(time.Hour),
		LastHistoryID:          "1",
		ConnectedAt:            now.Add(-24 * time.Hour),
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: 6,
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := db.StoreOAuthState(ctx, "state-refresh", 42, now.Add(15*time.Minute)); err != nil {
		t.Fatalf("store state: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"emailAddress": "old@example.com",
			"historyId":    "99",
		})
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	server := newServerWithUpstream(t, db, &captureNotifier{}, upstream.URL)
	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=state-refresh&code=code", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	account, err := db.GetGoogleAccount(ctx, 42)
	if err != nil || account == nil {
		t.Fatalf("account: %v", err)
	}
	if account.RefreshToken != "existing-refresh" {
		t.Fatalf("refresh token lost: %q", account.RefreshToken)
	}
	if account.AccessToken != "new-access" || account.GmailEmail != "old@example.com" {
		t.Fatalf("account not updated: %+v", account)
	}
}

func TestCallbackRejectsDifferentAccountWithoutNewRefreshToken(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID:         42,
		GmailEmail:             "old@example.com",
		AccessToken:            "old-access",
		RefreshToken:           "existing-refresh",
		TokenExpiry:            now.Add(time.Hour),
		LastHistoryID:          "1",
		ConnectedAt:            now.Add(-24 * time.Hour),
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: 6,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.StoreOAuthState(ctx, "state-other", 42, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"emailAddress": "new@example.com",
			"historyId":    "99",
		})
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	notifier := &captureNotifier{}
	server := newServerWithUpstream(t, db, notifier, upstream.URL)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=state-other&code=code", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(notifier.failures) != 1 || !strings.Contains(notifier.failures[0], "refresh token") {
		t.Fatalf("failures=%v", notifier.failures)
	}
	account, err := db.GetGoogleAccount(ctx, 42)
	if err != nil || account == nil {
		t.Fatal(err)
	}
	if account.GmailEmail != "old@example.com" || account.AccessToken != "old-access" || account.RefreshToken != "existing-refresh" || account.LastHistoryID != "1" {
		t.Fatalf("credentials changed: %+v", account)
	}
}

func TestInflightCallbackInvalidation(t *testing.T) {
	for _, action := range []string{"logout", "new attempt", "new completion"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			db := openDB(t)
			now := database.UTCNow()
			if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
				TelegramUserID: 42,
				GmailEmail:     "user@example.com",
				AccessToken:    "old",
				RefreshToken:   "old-refresh",
				TokenExpiry:    now,
				ConnectedAt:    now,
				LastHistoryID:  "1",
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.StoreOAuthAttempt(ctx, "first", 42, now.Add(15*time.Minute)); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					close(entered)
					<-release
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "stale", "refresh_token": "stale-refresh", "expires_in": 3600})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": "user@example.com", "historyId": "999"})
			}))
			defer upstream.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			s := newServerWithUpstream(t, db, &captureNotifier{}, upstream.URL)
			done := make(chan int, 1)
			go func() {
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/google/callback?state=first&code=code", nil))
				done <- rec.Code
			}()
			<-entered
			if action == "logout" {
				if _, err := db.Logout(ctx, 42); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.StoreOAuthAttempt(ctx, "second", 42, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				if action == "new completion" {
					state, err := db.ConsumeOAuthState(ctx, "second")
					if err != nil {
						t.Fatal(err)
					}
					if err := db.CompleteOAuth(ctx, *state, database.UpsertGoogleAccountParams{
						TelegramUserID: 42,
						GmailEmail:     "user@example.com",
						AccessToken:    "winner",
						RefreshToken:   "winner-refresh",
						TokenExpiry:    now,
						ConnectedAt:    now,
						LastHistoryID:  "2",
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			close(release)
			if status := <-done; status == http.StatusOK {
				t.Fatal("stale callback succeeded")
			}
			account, err := db.GetGoogleAccount(ctx, 42)
			if err != nil {
				t.Fatal(err)
			}
			if action == "logout" {
				if account != nil {
					t.Fatal("callback undid logout")
				}
				return
			}
			if account == nil || account.AccessToken == "stale" {
				t.Fatal("stale callback overwrote authorization")
			}
		})
	}
}

func TestCallbackMissingRefreshTokenFails(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := db.StoreOAuthState(ctx, "state-no-refresh", 7, database.UTCNow().Add(time.Minute)); err != nil {
		t.Fatalf("store: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-only",
			"expires_in":   3600,
		})
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	notifier := &captureNotifier{}
	server := newServerWithUpstream(t, db, notifier, upstream.URL)
	req := httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=state-no-refresh&code=code", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(notifier.failures) != 1 {
		t.Fatalf("failures=%v", notifier.failures)
	}
	if !strings.Contains(notifier.failures[0], "refresh token") {
		t.Fatalf("failure=%q", notifier.failures[0])
	}
	account, err := db.GetGoogleAccount(ctx, 7)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if account != nil {
		t.Fatal("expected no account on failure")
	}
}

func TestCallbackRejectsRetiredRelogWithoutReplacingAccount(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bot.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	now := database.UTCNow()
	if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID:         42,
		GmailEmail:             "user@example.com",
		AccessToken:            "old-access",
		RefreshToken:           "old-refresh",
		TokenExpiry:            now.Add(time.Hour),
		LastHistoryID:          "10",
		ConnectedAt:            now.Add(-24 * time.Hour),
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: 6,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.StoreOAuthState(ctx, "relog-state", 42, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	side, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = side.Close() })
	if _, err := side.ExecContext(ctx, `UPDATE oauth_attempts SET relog = 1 WHERE state = ?`, "relog-state"); err != nil {
		t.Fatal(err)
	}

	tokenCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	notifier := &captureNotifier{}
	server := newServerWithUpstream(t, db, notifier, upstream.URL)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/google/callback?state=relog-state&code=code", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/logout") || !strings.Contains(rec.Body.String(), "/login") {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if tokenCalls != 0 {
		t.Fatalf("token endpoint calls=%d", tokenCalls)
	}
	if len(notifier.failures) != 1 || !strings.Contains(notifier.failures[0], "/logout") {
		t.Fatalf("failures=%v", notifier.failures)
	}
	account, err := db.GetGoogleAccount(ctx, 42)
	if err != nil || account == nil {
		t.Fatal(err)
	}
	if account.AccessToken != "old-access" || account.RefreshToken != "old-refresh" || account.LastHistoryID != "10" {
		t.Fatalf("account changed: %+v", account)
	}
}

func newServerWithUpstream(t *testing.T, db *database.Database, notifier LoginNotifier, upstream string) *Server {
	t.Helper()
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
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		u, err := url.Parse(upstream)
		if err != nil {
			return nil, err
		}
		cloned.URL.Scheme = u.Scheme
		cloned.URL.Host = u.Host
		if strings.Contains(req.URL.Host, "oauth2.googleapis.com") || strings.HasSuffix(req.URL.Path, "/token") {
			cloned.URL.Path = "/token"
			cloned.URL.RawQuery = ""
		}
		return http.DefaultTransport.RoundTrip(cloned)
	})}
	oauthClient := oauth.NewClient(settings, client)
	gmailService := gmail.NewService(client, oauthClient, db)
	return NewServer("127.0.0.1:0", db, oauthClient, gmailService, notifier)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
