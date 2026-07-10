package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/config"
	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

type memoryStore struct {
	mu        sync.Mutex
	delivered map[string]bool
	tokens    map[int64]tokenSnapshot
}

type tokenSnapshot struct {
	accessToken  string
	refreshToken string
	expiry       time.Time
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		delivered: make(map[string]bool),
		tokens:    make(map[int64]tokenSnapshot),
	}
}

func (m *memoryStore) UpdateTokens(ctx context.Context, telegramUserID int64, accessToken string, tokenExpiry time.Time, refreshToken *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.tokens[telegramUserID]
	snap.accessToken = accessToken
	snap.expiry = tokenExpiry
	if refreshToken != nil {
		snap.refreshToken = *refreshToken
	}
	m.tokens[telegramUserID] = snap
	return nil
}

func (m *memoryStore) WasMessageDelivered(ctx context.Context, telegramUserID int64, gmailMessageID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.delivered[key(telegramUserID, gmailMessageID)], nil
}

func (m *memoryStore) markDelivered(telegramUserID int64, gmailMessageID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivered[key(telegramUserID, gmailMessageID)] = true
}

func key(userID int64, messageID string) string {
	return strings.Join([]string{itoa(userID), messageID}, ":")
}

func itoa(v int64) string {
	return jsonNumber(v)
}

func jsonNumber(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestProfileFromPayloadRequiresFields(t *testing.T) {
	if _, err := profileFromPayload(map[string]any{"historyId": "1"}); err == nil {
		t.Fatal("expected missing emailAddress error")
	}
	if _, err := profileFromPayload(map[string]any{"emailAddress": "a@example.com"}); err == nil {
		t.Fatal("expected missing historyId error")
	}
	profile, err := profileFromPayload(map[string]any{
		"emailAddress": "a@example.com",
		"historyId":    float64(42),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.EmailAddress != "a@example.com" || profile.HistoryID != "42" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
}

func TestListNewInboxMessagesPaginatesAndFilters(t *testing.T) {
	store := newMemoryStore()
	store.markDelivered(7, "already")

	var historyCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/history"):
			historyCalls++
			auth := r.Header.Get("Authorization")
			if auth != "Bearer access-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if historyCalls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"historyId": "200",
					"history": []any{
						map[string]any{
							"id": "150",
							"messagesAdded": []any{
								map[string]any{"message": map[string]any{"id": "msg-new"}},
								map[string]any{"message": map[string]any{"id": "already"}},
								map[string]any{"message": map[string]any{"id": "not-inbox"}},
								map[string]any{"message": map[string]any{"id": "old-msg"}},
							},
						},
					},
					"nextPageToken": "page-2",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"historyId": "210",
				"history": []any{
					map[string]any{
						"id": "210",
						"messagesAdded": []any{
							map[string]any{"message": map[string]any{"id": "msg-page-2"}},
						},
					},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/messages/"):
			id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/messages/")
			switch id {
			case "msg-new":
				writeMessage(w, id, []string{"INBOX"}, "New", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC))
			case "msg-page-2":
				writeMessage(w, id, []string{"INBOX"}, "Page2", time.Date(2024, 2, 2, 0, 0, 0, 0, time.UTC))
			case "not-inbox":
				writeMessage(w, id, []string{"SPAM"}, "Spam", time.Date(2024, 2, 3, 0, 0, 0, 0, time.UTC))
			case "old-msg":
				writeMessage(w, id, []string{"INBOX"}, "Old", time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := newTestService(t, server.URL, store)
	connectedAt := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	account := models.GoogleAccount{
		TelegramUserID: 7,
		GmailEmail:     "user@example.com",
		AccessToken:    "access-token",
		RefreshToken:   "refresh-token",
		TokenExpiry:    time.Now().UTC().Add(time.Hour),
		LastHistoryID:  "100",
		ConnectedAt:    connectedAt,
	}

	messages, latest, err := service.ListNewInboxMessages(context.Background(), account)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if historyCalls != 2 {
		t.Fatalf("expected 2 history pages, got %d", historyCalls)
	}
	if latest != "210" {
		t.Fatalf("latest history=%q", latest)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %+v", messages)
	}
	if messages[0].GmailMessageID != "msg-new" || messages[1].GmailMessageID != "msg-page-2" {
		t.Fatalf("unexpected order: %+v", messages)
	}
}

func TestListNewInboxMessagesHistoryExpired(t *testing.T) {
	store := newMemoryStore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/history"):
			http.Error(w, `{"error":{"code":404}}`, http.StatusNotFound)
		case r.URL.Path == "/gmail/v1/users/me/profile":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"emailAddress": "user@example.com",
				"historyId":    "999",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := newTestService(t, server.URL, store)
	account := models.GoogleAccount{
		TelegramUserID: 1,
		AccessToken:    "access-token",
		RefreshToken:   "refresh-token",
		TokenExpiry:    time.Now().UTC().Add(time.Hour),
		LastHistoryID:  "stale",
		ConnectedAt:    time.Now().UTC().Add(-time.Hour),
	}
	_, _, err := service.ListNewInboxMessages(context.Background(), account)
	histErr, ok := err.(*HistoryExpiredError)
	if !ok {
		t.Fatalf("expected HistoryExpiredError, got %T %v", err, err)
	}
	if histErr.CurrentHistoryID != "999" {
		t.Fatalf("history id=%q", histErr.CurrentHistoryID)
	}
}

func TestRequestJSONRefreshesExpiredTokenBeforeRequest(t *testing.T) {
	store := newMemoryStore()
	var profileAuths []string
	var tokenCalls int

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "refresh-token" {
			http.Error(w, "bad refresh", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "refreshed-access",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		profileAuths = append(profileAuths, auth)
		if auth != "Bearer refreshed-access" {
			http.Error(w, "unexpected auth", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"emailAddress": "user@example.com",
			"historyId":    "55",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	service := newTestServiceWithTokenURL(t, server.URL, store)
	account := models.GoogleAccount{
		TelegramUserID: 9,
		AccessToken:    "expired-access",
		RefreshToken:   "refresh-token",
		TokenExpiry:    time.Now().UTC().Add(-time.Minute),
	}
	profile, err := service.GetProfile(context.Background(), account)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if profile.EmailAddress != "user@example.com" || profile.HistoryID != "55" {
		t.Fatalf("profile=%+v", profile)
	}
	if tokenCalls != 1 {
		t.Fatalf("tokenCalls=%d", tokenCalls)
	}
	if len(profileAuths) != 1 || profileAuths[0] != "Bearer refreshed-access" {
		t.Fatalf("profileAuths=%v", profileAuths)
	}
	snap := store.tokens[9]
	if snap.accessToken != "refreshed-access" {
		t.Fatalf("stored token=%q", snap.accessToken)
	}
	if snap.refreshToken != "refresh-token" {
		t.Fatalf("refresh token not retained: %q", snap.refreshToken)
	}
}

func TestRequestJSONRetriesOnceAfter401(t *testing.T) {
	store := newMemoryStore()
	var profileAuths []string
	var tokenCalls int

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "refreshed-access",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		profileAuths = append(profileAuths, auth)
		if auth == "Bearer stale-access" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"emailAddress": "user@example.com",
			"historyId":    "77",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	service := newTestServiceWithTokenURL(t, server.URL, store)
	account := models.GoogleAccount{
		TelegramUserID: 11,
		AccessToken:    "stale-access",
		RefreshToken:   "refresh-token",
		TokenExpiry:    time.Now().UTC().Add(time.Hour),
	}
	profile, err := service.GetProfile(context.Background(), account)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if profile.HistoryID != "77" {
		t.Fatalf("profile=%+v", profile)
	}
	if tokenCalls != 1 {
		t.Fatalf("tokenCalls=%d", tokenCalls)
	}
	if len(profileAuths) != 2 {
		t.Fatalf("profileAuths=%v", profileAuths)
	}
	if profileAuths[0] != "Bearer stale-access" || profileAuths[1] != "Bearer refreshed-access" {
		t.Fatalf("profileAuths=%v", profileAuths)
	}
}

func TestGetProfileForAccessTokenRejectsMalformed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": "only@example.com"})
	}))
	defer server.Close()
	service := newTestService(t, server.URL, newMemoryStore())
	_, err := service.GetProfileForAccessToken(context.Background(), "token")
	if err == nil || !strings.Contains(err.Error(), "missing historyId") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeMessage(w http.ResponseWriter, id string, labels []string, subject string, received time.Time) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":           id,
		"labelIds":     labels,
		"snippet":      subject + " snippet",
		"internalDate": received.UnixMilli(),
		"payload": map[string]any{
			"headers": []any{
				map[string]any{"name": "From", "value": "sender@example.com"},
				map[string]any{"name": "Subject", "value": subject},
			},
		},
	})
}

func newTestService(t *testing.T, baseURL string, store TokenStore) *Service {
	t.Helper()
	settings := config.Settings{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AppBaseURL:         "https://example.com",
	}
	client := &http.Client{Transport: rewriteTransport{baseURL: baseURL}}
	oauthClient := oauth.NewClient(settings, client)
	return NewService(client, oauthClient, store)
}

func newTestServiceWithTokenURL(t *testing.T, baseURL string, store TokenStore) *Service {
	t.Helper()
	settings := config.Settings{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AppBaseURL:         "https://example.com",
	}
	client := &http.Client{Transport: rewriteTransport{baseURL: baseURL, rewriteToken: true}}
	oauthClient := oauth.NewClient(settings, client)
	return NewService(client, oauthClient, store)
}

type rewriteTransport struct {
	baseURL      string
	rewriteToken bool
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	target, err := url.Parse(t.baseURL)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(req.URL.Host, "googleapis.com") && strings.Contains(req.URL.Path, "/token"):
		cloned.URL.Scheme = target.Scheme
		cloned.URL.Host = target.Host
		cloned.URL.Path = "/token"
	case strings.Contains(req.URL.Host, "googleapis.com"):
		cloned.URL.Scheme = target.Scheme
		cloned.URL.Host = target.Host
		// keep path
	case t.rewriteToken && strings.Contains(req.URL.String(), "oauth2.googleapis.com/token"):
		cloned.URL.Scheme = target.Scheme
		cloned.URL.Host = target.Host
		cloned.URL.Path = "/token"
	}
	return http.DefaultTransport.RoundTrip(cloned)
}
