package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/config"
)

const (
	GoogleAuthURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenURL  = "https://oauth2.googleapis.com/token"
	GoogleRevokeURL = "https://oauth2.googleapis.com/revoke"
	GmailScope      = "https://www.googleapis.com/auth/gmail.readonly"
)

type TokenResponse struct {
	AccessToken  string
	RefreshToken *string
	ExpiresAt    time.Time
}

type OAuthError struct {
	Message          string
	StatusCode       int
	ErrorCode        string
	ErrorDescription string
}

func (e *OAuthError) Error() string {
	return e.Message
}

func (e *OAuthError) IsInvalidGrant() bool {
	return e.ErrorCode == "invalid_grant"
}

type Client struct {
	settings   config.Settings
	httpClient *http.Client
}

func NewClient(settings config.Settings, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{settings: settings, httpClient: httpClient}
}

func (c *Client) BuildAuthorizationURL(state string) string {
	query := url.Values{}
	query.Set("client_id", c.settings.GoogleClientID)
	query.Set("redirect_uri", c.settings.GoogleRedirectURI())
	query.Set("response_type", "code")
	query.Set("scope", GmailScope)
	query.Set("access_type", "offline")
	query.Set("include_granted_scopes", "true")
	query.Set("prompt", "consent")
	query.Set("state", state)
	return GoogleAuthURL + "?" + query.Encode()
}

func (c *Client) ExchangeCode(ctx context.Context, code string) (TokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", c.settings.GoogleClientID)
	form.Set("client_secret", c.settings.GoogleClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", c.settings.GoogleRedirectURI())
	return c.postToken(ctx, form)
}

func (c *Client) RefreshAccessToken(ctx context.Context, refreshToken string) (TokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", c.settings.GoogleClientID)
	form.Set("client_secret", c.settings.GoogleClientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")
	return c.postToken(ctx, form)
}

func (c *Client) RevokeToken(ctx context.Context, token string) error {
	form := url.Values{}
	form.Set("token", token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GoogleRevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke token: %w", redactURLError(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		message := strings.TrimSpace(string(body))
		if len(message) > 256 {
			message = message[:256] + "\u2026"
		}
		if message == "" {
			message = resp.Status
		}
		return &OAuthError{Message: fmt.Sprintf("Google token revocation failed: %d %s", resp.StatusCode, message), StatusCode: resp.StatusCode}
	}
	return nil
}

// redactURLError strips the URL from transport errors so tokens in request
// URLs cannot leak into logs. The wrapped error still supports errors.Is/As.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func (c *Client) postToken(ctx context.Context, form url.Values) (TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GoogleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TokenResponse{}, err
	}
	defer resp.Body.Close()
	payload, err := decodeJSONResponse(resp)
	if err != nil {
		return TokenResponse{}, err
	}
	return tokenResponseFromPayload(payload), nil
}

func decodeJSONResponse(resp *http.Response) (map[string]any, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		errCode := ""
		errDesc := ""
		if payload != nil {
			if v, ok := payload["error"].(string); ok {
				errCode = v
			}
			if v, ok := payload["error_description"].(string); ok {
				errDesc = v
			}
		}
		return nil, &OAuthError{
			Message:          fmt.Sprintf("Google OAuth request failed: %d %s", resp.StatusCode, message),
			StatusCode:       resp.StatusCode,
			ErrorCode:        errCode,
			ErrorDescription: errDesc,
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if _, ok := payload["access_token"]; !ok {
		return nil, &OAuthError{Message: "Google OAuth response did not include an access token."}
	}
	return payload, nil
}

func tokenResponseFromPayload(payload map[string]any) TokenResponse {
	expiresIn := 3600
	switch v := payload["expires_in"].(type) {
	case float64:
		expiresIn = int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			expiresIn = int(n)
		}
	case int:
		expiresIn = v
	}
	seconds := expiresIn - 60
	if seconds < 60 {
		seconds = 60
	}
	var refresh *string
	if v, ok := payload["refresh_token"].(string); ok && v != "" {
		refresh = &v
	}
	return TokenResponse{
		AccessToken:  fmt.Sprint(payload["access_token"]),
		RefreshToken: refresh,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(seconds) * time.Second),
	}
}

// DecodeJSONResponseForTest exposes error decoding for unit tests.
func DecodeJSONResponseForTest(resp *http.Response) (map[string]any, error) {
	return decodeJSONResponse(resp)
}
