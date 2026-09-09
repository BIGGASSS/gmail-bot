package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/database"
)

func TestRelogCallback(t *testing.T) {
	for _, tc := range []struct {
		name, email string
		refresh     any
		ok          bool
	}{
		{"success", "USER@example.com", "fresh-refresh", true},
		{"wrong account", "other@example.com", "fresh-refresh", false},
		{"missing refresh", "user@example.com", nil, false},
		{"empty refresh", "user@example.com", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openDB(t)
			connected := database.UTCNow().Add(-10 * 24 * time.Hour)
			if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{TelegramUserID: 42, GmailEmail: "user@example.com", AccessToken: "old", RefreshToken: "old-refresh", TokenExpiry: connected, LastHistoryID: "10", ConnectedAt: connected, ReloginPromptEnabled: false, ReloginPromptDelayDays: 3}); err != nil {
				t.Fatal(err)
			}
			if err := db.MarkMessageDelivered(ctx, 42, "mail", 42, 1); err != nil {
				t.Fatal(err)
			}
			if err := db.MarkReloginPromptSent(ctx, 42, nil); err != nil {
				t.Fatal(err)
			}
			before, _ := db.GetGoogleAccount(ctx, 42)
			if err := db.StoreOAuthAttempt(ctx, "relog", 42, database.UTCNow().Add(15*time.Minute), true); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					p := map[string]any{"access_token": "new", "expires_in": 3600}
					if tc.refresh != nil {
						p["refresh_token"] = tc.refresh
					}
					_ = json.NewEncoder(w).Encode(p)
					return
				}
				// Simulate polling and a preference change while OAuth is in progress.
				if err := db.UpdateLastHistoryID(ctx, 42, "20"); err != nil {
					t.Error(err)
				}
				if err := db.SetReloginPromptDelayDays(ctx, 42, 4); err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": tc.email, "historyId": "999"})
			}))
			defer upstream.Close()
			s := newServerWithUpstream(t, db, &captureNotifier{}, upstream.URL)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/google/callback?state=relog&code=code", nil))
			if (rec.Code == 200) != tc.ok {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			after, _ := db.GetGoogleAccount(ctx, 42)
			if !after.ConnectedAt.Equal(before.ConnectedAt) || after.ReloginPromptEnabled {
				t.Fatalf("changed account metadata: %+v", after)
			}
			delivered, err := db.WasMessageDelivered(ctx, 42, "mail")
			if err != nil || !delivered {
				t.Fatalf("delivery lost: %v", err)
			}
			if tc.ok {
				if after.AccessToken != "new" || after.RefreshToken != "fresh-refresh" || after.LastHistoryID != "20" || after.ReloginPromptDelayDays != 4 || after.Generation <= before.Generation || after.ReloginPromptSentAt != nil {
					t.Fatalf("bad refreshed account: %+v", after)
				}
				if !after.ReloginPromptDueAt.Equal(after.ReloginPromptBaseAt.Add(4*24*time.Hour)) || !after.ReloginPromptBaseAt.After(connected) {
					t.Fatal("schedule not reset")
				}
				if err := db.UpdateTokensGuarded(ctx, *before, "stale", time.Now(), nil); err != database.ErrStaleAuthorization {
					t.Fatalf("stale write: %v", err)
				}
				if deleted, err := db.DeleteGoogleAccountGuarded(ctx, *before); err != nil || deleted {
					t.Fatalf("stale invalid_grant deleted refreshed account: %v", err)
				}
			} else if after.AccessToken != before.AccessToken || after.RefreshToken != before.RefreshToken || after.Generation != before.Generation {
				t.Fatal("failed relog changed credentials")
			}
		})
	}
}

func TestInflightCallbackInvalidation(t *testing.T) {
	for _, relog := range []bool{false, true} {
		for _, action := range []string{"logout", "new attempt", "new completion"} {
			t.Run(action+map[bool]string{true: " relog", false: " login"}[relog], func(t *testing.T) {
				ctx := context.Background()
				db := openDB(t)
				now := database.UTCNow()
				if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{TelegramUserID: 42, GmailEmail: "user@example.com", AccessToken: "old", RefreshToken: "old-refresh", TokenExpiry: now, ConnectedAt: now, LastHistoryID: "1"}); err != nil {
					t.Fatal(err)
				}
				if err := db.StoreOAuthAttempt(ctx, "first", 42, now.Add(15*time.Minute), relog); err != nil {
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
					if err := db.StoreOAuthAttempt(ctx, "second", 42, now.Add(time.Minute), relog); err != nil {
						t.Fatal(err)
					}
					if action == "new completion" {
						state, err := db.ConsumeOAuthState(ctx, "second")
						if err != nil {
							t.Fatal(err)
						}
						if err := db.CompleteOAuth(ctx, *state, database.UpsertGoogleAccountParams{TelegramUserID: 42, GmailEmail: "user@example.com", AccessToken: "winner", RefreshToken: "winner-refresh", TokenExpiry: now, ConnectedAt: now, LastHistoryID: "2"}); err != nil {
							t.Fatal(err)
						}
					}
				}
				close(release)
				if status := <-done; status == 200 {
					t.Fatal("stale callback succeeded")
				}
				a, err := db.GetGoogleAccount(ctx, 42)
				if err != nil {
					t.Fatal(err)
				}
				if action == "logout" {
					if a != nil {
						t.Fatal("callback undid logout")
					}
				} else if a == nil || a.AccessToken == "stale" {
					t.Fatal("stale callback overwrote authorization")
				}
			})
		}
	}
}
