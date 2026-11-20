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
			if _, err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
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
			account, err := userAccount(ctx, db, 1)
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
				if _, err := db.LogoutAll(ctx, 1); err != nil {
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
				if err := db.UpdateLastHistoryID(ctx, account.ID, "456"); err != nil {
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
			current, err := userAccount(ctx, db, 1)
			if err != nil || current.AccessToken != "winner" || current.RefreshToken != "winner-refresh" || current.LastHistoryID != "456" {
				t.Fatalf("losing refresh modified database: %+v %v", current, err)
			}
		})
	}
}

func TestInflightRefreshDoesNotClobberNewLogin(t *testing.T) {
	// "logout then login" replaces the refreshing account; "add another account"
	// connects a second address and must leave the refreshing account alone.
	cases := []struct {
		name     string
		logout   bool
		newEmail string
	}{
		{"logout then login same address", true, "user@example.com"},
		{"logout then login other address", true, "other@example.com"},
		{"add another account", false, "other@example.com"},
	}
	for _, tc := range cases {
		for _, invalid := range []bool{false, true} {
			tc, invalid := tc, invalid
			name := tc.name
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
				accountID, err := db.UpsertGoogleAccount(ctx, params)
				if err != nil {
					t.Fatal(err)
				}
				account, err := db.GetGoogleAccountByID(ctx, accountID)
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
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed", "refresh_token": "refreshed-refresh", "expires_in": 3600})
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
				if tc.logout {
					if _, err := db.LogoutAll(ctx, 1); err != nil {
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
				params.GmailEmail = tc.newEmail
				params.AccessToken = "new"
				params.RefreshToken = "new-refresh"
				params.LastHistoryID = "2"
				params.TokenExpiry = now.Add(time.Hour)
				if err := db.CompleteOAuth(ctx, *state, params); err != nil {
					t.Fatal(err)
				}
				close(release)
				err = <-done

				accounts, listErr := db.ListGoogleAccountsForUser(ctx, 1)
				if listErr != nil {
					t.Fatal(listErr)
				}
				var added *models.GoogleAccount
				for i := range accounts {
					if accounts[i].ID != accountID {
						added = &accounts[i]
					}
				}
				if added == nil || added.AccessToken != "new" || added.RefreshToken != "new-refresh" || added.GmailEmail != tc.newEmail || added.LastHistoryID != "2" {
					t.Fatalf("new login changed: %+v", accounts)
				}

				if tc.logout {
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
					if len(accounts) != 1 {
						t.Fatalf("expected only the replacement account: %+v", accounts)
					}
					return
				}

				// The refreshing account was not replaced, so its refresh applies normally.
				if invalid {
					if deleted, err := db.DeleteGoogleAccountGuarded(ctx, *account); err != nil || !deleted {
						t.Fatalf("invalid_grant did not apply to its own account: deleted=%v err=%v", deleted, err)
					}
					if still, err := db.GetGoogleAccountByID(ctx, added.ID); err != nil || still == nil {
						t.Fatalf("other account removed: %+v %v", still, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("refresh of untouched account failed: %v", err)
				}
				current, err := db.GetGoogleAccountByID(ctx, accountID)
				if err != nil || current == nil || current.AccessToken != "refreshed" || current.RefreshToken != "refreshed-refresh" {
					t.Fatalf("refresh not stored: %+v %v", current, err)
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
