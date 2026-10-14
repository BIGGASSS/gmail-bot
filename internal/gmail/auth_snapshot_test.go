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
	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

func TestConcurrentRefreshReusesWinner(t *testing.T) {
	for _, action := range []string{"valid winner", "expired winner", "logout"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			db := openAuthDB(t)
			now := database.UTCNow().Truncate(time.Microsecond)
			if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
				TelegramUserID: 1,
				GmailEmail:     "user@example.com",
				AccessToken:    "old",
				RefreshToken:   "old-refresh",
				TokenExpiry:    now.Add(-time.Hour),
				ConnectedAt:    now,
				LastHistoryID:  "123",
			}); err != nil {
				t.Fatal(err)
			}
			account, err := db.GetGoogleAccount(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "loser", "refresh_token": "loser-refresh", "expires_in": 3600})
			}))
			defer upstream.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			service := newTestServiceWithTokenURL(t, upstream.URL, db)
			type result struct {
				account models.GoogleAccount
				err     error
			}
			done := make(chan result, 1)
			go func() {
				refreshed, err := service.forceRefresh(ctx, *account)
				done <- result{refreshed, err}
			}()
			<-entered
			if action == "logout" {
				if _, err := db.Logout(ctx, 1); err != nil {
					t.Fatal(err)
				}
			} else {
				expiry := now.Add(time.Hour)
				if action == "expired winner" {
					expiry = now.Add(-time.Minute)
				}
				refresh := "winner-refresh"
				if err := db.UpdateTokensGuarded(ctx, *account, "winner", expiry, &refresh); err != nil {
					t.Fatal(err)
				}
				if err := db.UpdateLastHistoryID(ctx, 1, "456"); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			got := <-done
			if action != "valid winner" {
				if !errors.Is(got.err, database.ErrStaleAuthorization) {
					t.Fatalf("expected stale authorization, got %v", got.err)
				}
				return
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.account.AccessToken != "winner" || got.account.RefreshToken != "winner-refresh" || !got.account.TokenExpiry.Equal(now.Add(time.Hour)) || got.account.Generation != account.Generation || got.account.LastHistoryID != "123" {
				t.Fatalf("did not reuse winning credentials with original snapshot metadata: %+v", got.account)
			}
			current, err := db.GetGoogleAccount(ctx, 1)
			if err != nil || current.AccessToken != "winner" || current.RefreshToken != "winner-refresh" || current.LastHistoryID != "456" {
				t.Fatalf("losing refresh modified database: %+v %v", current, err)
			}
		})
	}
}

func TestInflightRefreshDoesNotClobberNewLogin(t *testing.T) {
	for _, action := range []string{"login", "logout then login"} {
		for _, invalid := range []bool{false, true} {
			name := action
			if invalid {
				name += " invalid_grant"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				db := openAuthDB(t)
				now := database.UTCNow()
				params := database.UpsertGoogleAccountParams{
					TelegramUserID: 1,
					GmailEmail:     "user@example.com",
					AccessToken:    "old",
					RefreshToken:   "old-refresh",
					TokenExpiry:    now.Add(-time.Hour),
					ConnectedAt:    now,
					LastHistoryID:  "1",
				}
				if err := db.UpsertGoogleAccount(ctx, params); err != nil {
					t.Fatal(err)
				}
				account, err := db.GetGoogleAccount(ctx, 1)
				if err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(entered)
					<-release
					if invalid {
						w.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "stale", "refresh_token": "stale-refresh", "expires_in": 3600})
				}))
				defer upstream.Close()
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				service := newTestServiceWithTokenURL(t, upstream.URL, db)
				done := make(chan error, 1)
				go func() {
					_, err := service.forceRefresh(ctx, *account)
					done <- err
				}()
				<-entered
				if action == "logout then login" {
					if _, err := db.Logout(ctx, 1); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.StoreOAuthAttempt(ctx, "login", 1, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				state, err := db.ConsumeOAuthState(ctx, "login")
				if err != nil || state == nil {
					t.Fatalf("consume login: %+v %v", state, err)
				}
				params.GmailEmail = "other@example.com"
				params.AccessToken = "new"
				params.RefreshToken = "new-refresh"
				params.LastHistoryID = "2"
				params.TokenExpiry = now.Add(time.Hour)
				if err := db.CompleteOAuth(ctx, *state, params); err != nil {
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
						t.Fatalf("stale grant deleted replacement: deleted=%v err=%v", deleted, err)
					}
				} else if !errors.Is(err, database.ErrStaleAuthorization) {
					t.Fatalf("stale write accepted: %v", err)
				}
				current, err := db.GetGoogleAccount(ctx, 1)
				if err != nil || current == nil || current.AccessToken != "new" || current.RefreshToken != "new-refresh" || current.GmailEmail != "other@example.com" || current.LastHistoryID != "2" {
					t.Fatalf("new login changed: %+v %v", current, err)
				}
			})
		}
	}
}

func openAuthDB(t *testing.T) *database.Database {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}
