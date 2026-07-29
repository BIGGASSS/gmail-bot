package web

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/BIGGASSS/gmail-bot/internal/applog"
	"html"
	"net/http"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/gmail"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

type LoginNotifier interface {
	SendLoginSuccess(ctx context.Context, chatID int64, gmailEmail string) error
	SendLoginFailure(ctx context.Context, chatID int64, errorText string) error
}

type Server struct {
	database    *database.Database
	oauthClient *oauth.Client
	gmail       *gmail.Service
	notifier    LoginNotifier
	httpServer  *http.Server
}

func NewServer(
	addr string,
	db *database.Database,
	oauthClient *oauth.Client,
	gmailService *gmail.Service,
	notifier LoginNotifier,
) *Server {
	s := &Server{
		database:    db,
		oauthClient: oauthClient,
		gmail:       gmailService,
		notifier:    notifier,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/oauth/google/callback", s.handleGoogleCallback)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           withRecovery(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		applog.Infof("HTTP server listening on %s", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<html><body><p>Gmail Telegram Bot is running.</p></body></html>"))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		if len(errParam) > 200 {
			errParam = errParam[:200] + "\u2026"
		}
		writeHTML(w, http.StatusBadRequest, fmt.Sprintf(
			"<html><body><h1>Google login failed</h1><p>%s</p></body></html>",
			html.EscapeString(errParam),
		))
		return
	}
	state := q.Get("state")
	code := q.Get("code")
	if state == "" || code == "" {
		writeHTML(w, http.StatusBadRequest, "<html><body><h1>Missing OAuth parameters.</h1></body></html>")
		return
	}

	oauthState, err := s.database.ConsumeOAuthState(r.Context(), state)
	if err != nil {
		applog.Errorf("Failed to consume OAuth state: %v", err)
		writeHTML(w, http.StatusInternalServerError, "<html><body><h1>Gmail connection failed.</h1><p>Return to Telegram for details.</p></body></html>")
		return
	}
	if oauthState == nil {
		writeHTML(w, http.StatusBadRequest, "<html><body><h1>Login link expired or invalid.</h1></body></html>")
		return
	}

	if err := s.completeLogin(r.Context(), oauthState.TelegramUserID, oauthState.CreatedAt, code); err != nil {
		applog.Errorf("Failed to complete Gmail OAuth callback for Telegram user %d: %v", oauthState.TelegramUserID, err)
		_ = s.notifier.SendLoginFailure(r.Context(), oauthState.TelegramUserID, err.Error())
		writeHTML(w, http.StatusInternalServerError, "<html><body><h1>Gmail connection failed.</h1><p>Return to Telegram for details.</p></body></html>")
		return
	}

	writeHTML(w, http.StatusOK, "<html><body><h1>Gmail connected.</h1><p>You can return to Telegram.</p></body></html>")
}

func (s *Server) completeLogin(ctx context.Context, telegramUserID int64, stateCreatedAt time.Time, code string) error {
	existingAccount, err := s.database.GetGoogleAccount(ctx, telegramUserID)
	if err != nil {
		return err
	}
	reloginPreferences, err := s.database.GetReloginPromptPreferences(ctx, telegramUserID)
	if err != nil {
		return err
	}

	tokenResponse, err := s.oauthClient.ExchangeCode(ctx, code)
	if err != nil {
		return err
	}

	refreshToken := ""
	if tokenResponse.RefreshToken != nil {
		refreshToken = *tokenResponse.RefreshToken
	} else if existingAccount != nil {
		refreshToken = existingAccount.RefreshToken
	}
	if refreshToken == "" {
		return &oauth.OAuthError{Message: "Google did not return a refresh token."}
	}

	profile, err := s.gmail.GetProfileForAccessToken(ctx, tokenResponse.AccessToken)
	if err != nil {
		return err
	}

	connectedAt := database.UTCNow()
	if err := s.database.UpsertGoogleAccount(ctx, database.UpsertGoogleAccountParams{
		TelegramUserID:         telegramUserID,
		GmailEmail:             profile.EmailAddress,
		AccessToken:            tokenResponse.AccessToken,
		RefreshToken:           refreshToken,
		TokenExpiry:            tokenResponse.ExpiresAt,
		LastHistoryID:          profile.HistoryID,
		ConnectedAt:            connectedAt,
		ReloginPromptEnabled:   reloginPreferences.ReloginPromptEnabled,
		ReloginPromptDelayDays: reloginPreferences.ReloginPromptDelayDays,
		ReloginPromptBaseAt:    &stateCreatedAt,
	}); err != nil {
		return err
	}

	return s.notifier.SendLoginSuccess(ctx, telegramUserID, profile.EmailAddress)
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				applog.Errorf("HTTP handler panic: %v", recovered)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
