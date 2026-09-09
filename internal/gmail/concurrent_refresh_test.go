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
)

func TestConcurrentRefreshReusesWinner(t *testing.T) {
	for _, action := range []string{"valid winner", "expired winner", "logout"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			db, err := database.Open(filepath.Join(t.TempDir(), "auth.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			now := database.UTCNow().Truncate(time.Microsecond)
			if err := db.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{TelegramUserID: 1, GmailEmail: "user@example.com", AccessToken: "old", RefreshToken: "old-refresh", TokenExpiry: now.Add(-time.Hour), ConnectedAt: now, LastHistoryID: "123"}); err != nil {
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
			// Ensure failed assertions also release the HTTP handler.
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
			go func() { a, err := service.forceRefresh(ctx, *account); done <- result{a, err} }()
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
				t.Fatal("did not reuse winning credentials with original snapshot metadata")
			}
			current, err := db.GetGoogleAccount(ctx, 1)
			if err != nil || current.AccessToken != "winner" || current.RefreshToken != "winner-refresh" || current.LastHistoryID != "456" {
				t.Fatal("losing refresh modified database")
			}
		})
	}
}
