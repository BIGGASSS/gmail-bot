package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

func TestInflightTokenRefreshCannotChangeNewAuthorization(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful stale refresh", true: "stale invalid_grant"}[invalid], func(t *testing.T) {
			ctx := context.Background()
			db, err := database.Open(filepath.Join(t.TempDir(), "auth.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			now := database.UTCNow()
			p := database.UpsertGoogleAccountParams{TelegramUserID: 1, GmailEmail: "user@example.com", AccessToken: "old", RefreshToken: "old-refresh", TokenExpiry: now.Add(-time.Hour), ConnectedAt: now, LastHistoryID: "1"}
			if err := db.UpsertGoogleAccount(ctx, p); err != nil {
				t.Fatal(err)
			}
			account, _ := db.GetGoogleAccount(ctx, 1)
			entered, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				if invalid {
					w.WriteHeader(400)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "stale", "refresh_token": "stale-refresh", "expires_in": 3600})
			}))
			defer upstream.Close()
			service := newTestServiceWithTokenURL(t, upstream.URL, db)
			done := make(chan error, 1)
			go func() { _, err := service.forceRefresh(ctx, *account); done <- err }()
			<-entered
			if err := db.StoreOAuthAttempt(ctx, "relog", 1, now.Add(time.Minute), true); err != nil {
				t.Fatal(err)
			}
			state, err := db.ConsumeOAuthState(ctx, "relog")
			if err != nil {
				t.Fatal(err)
			}
			p.AccessToken, p.RefreshToken = "new", "new-refresh"
			if err := db.CompleteOAuth(ctx, *state, p); err != nil {
				t.Fatal(err)
			}
			close(release)
			err = <-done
			if invalid {
				var oauthErr *oauth.OAuthError
				if !errors.As(err, &oauthErr) || !oauthErr.IsInvalidGrant() {
					t.Fatalf("expected invalid_grant: %v", err)
				}
				if deleted, err := db.DeleteGoogleAccountGuarded(ctx, *account); err != nil || deleted {
					t.Fatal("stale grant deleted account")
				}
			} else if !errors.Is(err, database.ErrStaleAuthorization) {
				t.Fatalf("stale write accepted: %v", err)
			}
			current, err := db.GetGoogleAccount(ctx, 1)
			if err != nil || current == nil || current.AccessToken != "new" || current.RefreshToken != "new-refresh" {
				t.Fatal("new authorization changed")
			}
		})
	}
}
