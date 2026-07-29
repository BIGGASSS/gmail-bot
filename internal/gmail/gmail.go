package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BIGGASSS/gmail-bot/internal/applog"
	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/formatting"
	"github.com/BIGGASSS/gmail-bot/internal/models"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
)

const APIBase = "https://gmail.googleapis.com/gmail/v1/users/me"

const (
	maxHistoryPages    = 10
	maxMessagesPerPoll = 50
)

type Profile struct {
	EmailAddress string
	HistoryID    string
}

type APIError struct {
	Message    string
	StatusCode int
}

func (e *APIError) Error() string {
	return e.Message
}

type HistoryExpiredError struct {
	CurrentHistoryID string
}

func (e *HistoryExpiredError) Error() string {
	return "Stored Gmail history cursor is no longer valid."
}

func (e *HistoryExpiredError) StatusCode() int {
	return 404
}

type TokenStore interface {
	UpdateTokens(ctx context.Context, telegramUserID int64, accessToken string, tokenExpiry time.Time, refreshToken *string) error
	WasMessageDelivered(ctx context.Context, telegramUserID int64, gmailMessageID string) (bool, error)
}

type Service struct {
	httpClient  *http.Client
	oauthClient *oauth.Client
	database    TokenStore
}

func NewService(httpClient *http.Client, oauthClient *oauth.Client, db TokenStore) *Service {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{
		httpClient:  httpClient,
		oauthClient: oauthClient,
		database:    db,
	}
}

func (s *Service) GetProfileForAccessToken(ctx context.Context, accessToken string) (Profile, error) {
	payload, err := s.rawJSONRequest(ctx, http.MethodGet, APIBase+"/profile", accessToken, nil)
	if err != nil {
		return Profile{}, err
	}
	return profileFromPayload(payload)
}

func (s *Service) GetProfile(ctx context.Context, account models.GoogleAccount) (Profile, error) {
	payload, err := s.requestJSON(ctx, account, http.MethodGet, APIBase+"/profile", nil)
	if err != nil {
		return Profile{}, err
	}
	return profileFromPayload(payload)
}

func (s *Service) ListNewInboxMessages(ctx context.Context, account models.GoogleAccount) ([]models.IncomingMail, string, error) {
	latestHistoryID := account.LastHistoryID
	var pageToken string
	messageIDs := make(map[string]struct{})

	pages := 0
	for {
		pages++
		if pages > maxHistoryPages {
			applog.Warningf("Gmail history fanout cap reached for Telegram user %d: processed %d pages.", account.TelegramUserID, maxHistoryPages)
			break
		}
		params := url.Values{}
		params.Set("startHistoryId", account.LastHistoryID)
		params.Set("historyTypes", "messageAdded")
		params.Set("maxResults", "100")
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}

		payload, err := s.requestJSON(ctx, account, http.MethodGet, APIBase+"/history", params)
		if err != nil {
			if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == 404 {
				profile, profileErr := s.GetProfile(ctx, account)
				if profileErr != nil {
					return nil, "", profileErr
				}
				return nil, "", &HistoryExpiredError{CurrentHistoryID: profile.HistoryID}
			}
			return nil, "", err
		}

		if historyID, ok := anyString(payload["historyId"]); ok {
			latestHistoryID = historyID
		}

		if history, ok := payload["history"].([]any); ok {
			for _, entry := range history {
				entryMap, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				if id, ok := anyString(entryMap["id"]); ok {
					latestHistoryID = id
				}
				messagesAdded, _ := entryMap["messagesAdded"].([]any)
				for _, item := range messagesAdded {
					itemMap, ok := item.(map[string]any)
					if !ok {
						continue
					}
					message, _ := itemMap["message"].(map[string]any)
					if messageID, ok := anyString(message["id"]); ok && messageID != "" {
						messageIDs[messageID] = struct{}{}
					}
				}
			}
		}

		next, _ := payload["nextPageToken"].(string)
		if next == "" {
			break
		}
		pageToken = next
	}

	var incoming []models.IncomingMail
	for messageID := range messageIDs {
		if len(incoming) >= maxMessagesPerPoll {
			applog.Warningf("Gmail message fanout cap reached for Telegram user %d: capped at %d messages.", account.TelegramUserID, maxMessagesPerPoll)
			break
		}
		delivered, err := s.database.WasMessageDelivered(ctx, account.TelegramUserID, messageID)
		if err != nil {
			return nil, "", err
		}
		if delivered {
			continue
		}
		summary, err := s.GetMessageSummary(ctx, account, messageID)
		if err != nil {
			return nil, "", err
		}
		if summary == nil {
			continue
		}
		if !summary.ReceivedAt.After(account.ConnectedAt) {
			continue
		}
		incoming = append(incoming, *summary)
	}

	sort.Slice(incoming, func(i, j int) bool {
		return incoming[i].ReceivedAt.Before(incoming[j].ReceivedAt)
	})
	return incoming, latestHistoryID, nil
}

func (s *Service) GetMessageSummary(ctx context.Context, account models.GoogleAccount, messageID string) (*models.IncomingMail, error) {
	params := url.Values{}
	params.Set("format", "metadata")
	params.Add("metadataHeaders", "From")
	params.Add("metadataHeaders", "Subject")
	params.Add("metadataHeaders", "Date")

	payload, err := s.requestJSON(ctx, account, http.MethodGet, APIBase+"/messages/"+url.PathEscape(messageID), params)
	if err != nil {
		return nil, err
	}

	labelIDs := map[string]struct{}{}
	if labels, ok := payload["labelIds"].([]any); ok {
		for _, label := range labels {
			if value, ok := anyString(label); ok {
				labelIDs[value] = struct{}{}
			}
		}
	}
	if _, ok := labelIDs["INBOX"]; !ok {
		return nil, nil
	}

	gmailMessageID, ok := anyString(payload["id"])
	if !ok || gmailMessageID == "" {
		return nil, &APIError{Message: "Gmail message response missing id"}
	}

	headers := extractHeaders(payload)
	snippet := ""
	if rawSnippet, ok := anyString(payload["snippet"]); ok {
		snippet = formatting.NormalizeGmailSnippet(rawSnippet)
	}
	if snippet == "" {
		snippet = "(no preview available)"
	}
	from := headerValue(headers, "From")
	if from == "" {
		from = "Unknown sender"
	}
	subject := headerValue(headers, "Subject")
	if subject == "" {
		subject = "(no subject)"
	}

	return &models.IncomingMail{
		GmailMessageID: gmailMessageID,
		FromHeader:     from,
		Subject:        subject,
		Snippet:        snippet,
		ReceivedAt:     internalDateToTime(payload["internalDate"]),
	}, nil
}

func (s *Service) GetExpandedMessage(ctx context.Context, account models.GoogleAccount, messageID string) (models.ExpandedMail, error) {
	params := url.Values{}
	params.Set("format", "full")
	payload, err := s.requestJSON(ctx, account, http.MethodGet, APIBase+"/messages/"+url.PathEscape(messageID), params)
	if err != nil {
		return models.ExpandedMail{}, err
	}
	gmailMessageID, ok := anyString(payload["id"])
	if !ok || gmailMessageID == "" {
		return models.ExpandedMail{}, &APIError{Message: "Gmail message response missing id"}
	}
	headers := extractHeaders(payload)
	payloadPart, _ := payload["payload"].(map[string]any)
	bodyText, attachments := ExtractBodyAndAttachments(payloadPart)
	if bodyText == "" {
		bodyText = "(no body text available)"
	}
	from := headerValue(headers, "From")
	if from == "" {
		from = "Unknown sender"
	}
	subject := headerValue(headers, "Subject")
	if subject == "" {
		subject = "(no subject)"
	}
	return models.ExpandedMail{
		GmailMessageID: gmailMessageID,
		FromHeader:     from,
		Subject:        subject,
		ReceivedAt:     internalDateToTime(payload["internalDate"]),
		BodyText:       bodyText,
		Attachments:    attachments,
	}, nil
}

func (s *Service) requestJSON(ctx context.Context, account models.GoogleAccount, method, rawURL string, params url.Values) (map[string]any, error) {
	current, err := s.ensureValidAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	resp, err := s.doAuthorized(ctx, method, rawURL, params, current.AccessToken)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		refreshed, refreshErr := s.forceRefresh(ctx, current)
		if refreshErr != nil {
			return nil, refreshErr
		}
		resp, err = s.doAuthorized(ctx, method, rawURL, params, refreshed.AccessToken)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	return decodeJSONResponse(resp)
}

func (s *Service) rawJSONRequest(ctx context.Context, method, rawURL, accessToken string, params url.Values) (map[string]any, error) {
	resp, err := s.doAuthorized(ctx, method, rawURL, params, accessToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeJSONResponse(resp)
}

func (s *Service) doAuthorized(ctx context.Context, method, rawURL string, params url.Values, accessToken string) (*http.Response, error) {
	fullURL := rawURL
	if params != nil {
		if strings.Contains(fullURL, "?") {
			fullURL += "&" + params.Encode()
		} else {
			fullURL += "?" + params.Encode()
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return s.httpClient.Do(req)
}

func (s *Service) ensureValidAccessToken(ctx context.Context, account models.GoogleAccount) (models.GoogleAccount, error) {
	if account.TokenExpiry.After(time.Now().UTC()) {
		return account, nil
	}
	return s.forceRefresh(ctx, account)
}

func (s *Service) forceRefresh(ctx context.Context, account models.GoogleAccount) (models.GoogleAccount, error) {
	tokens, err := s.oauthClient.RefreshAccessToken(ctx, account.RefreshToken)
	if err != nil {
		return models.GoogleAccount{}, err
	}
	refreshToken := account.RefreshToken
	if tokens.RefreshToken != nil && *tokens.RefreshToken != "" {
		refreshToken = *tokens.RefreshToken
	}
	if err := s.database.UpdateTokens(ctx, account.TelegramUserID, tokens.AccessToken, tokens.ExpiresAt, &refreshToken); err != nil {
		return models.GoogleAccount{}, err
	}
	account.AccessToken = tokens.AccessToken
	account.RefreshToken = refreshToken
	account.TokenExpiry = tokens.ExpiresAt
	return account, nil
}

func profileFromPayload(payload map[string]any) (Profile, error) {
	email, ok := anyString(payload["emailAddress"])
	if !ok || email == "" {
		return Profile{}, &APIError{Message: "Gmail profile response missing emailAddress"}
	}
	historyID, ok := anyString(payload["historyId"])
	if !ok || historyID == "" {
		return Profile{}, &APIError{Message: "Gmail profile response missing historyId"}
	}
	return Profile{
		EmailAddress: email,
		HistoryID:    historyID,
	}, nil
}

func decodeJSONResponse(resp *http.Response) (map[string]any, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, &APIError{
			Message:    fmt.Sprintf("Gmail API request failed: %d %s", resp.StatusCode, message),
			StatusCode: resp.StatusCode,
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func extractHeaders(payload map[string]any) []map[string]any {
	payloadPart, _ := payload["payload"].(map[string]any)
	rawHeaders, _ := payloadPart["headers"].([]any)
	headers := make([]map[string]any, 0, len(rawHeaders))
	for _, item := range rawHeaders {
		if header, ok := item.(map[string]any); ok {
			headers = append(headers, header)
		}
	}
	return headers
}

func headerValue(headers []map[string]any, name string) string {
	target := strings.ToLower(name)
	for _, header := range headers {
		headerName, ok := anyString(header["name"])
		if !ok || strings.ToLower(headerName) != target {
			continue
		}
		if value, ok := anyString(header["value"]); ok {
			return value
		}
		return ""
	}
	return ""
}

func internalDateToTime(value any) time.Time {
	var timestampMS int64
	switch v := value.(type) {
	case string:
		fmt.Sscan(v, &timestampMS)
	case float64:
		timestampMS = int64(v)
	case json.Number:
		n, _ := v.Int64()
		timestampMS = n
	case int64:
		timestampMS = v
	case int:
		timestampMS = int64(v)
	}
	return time.UnixMilli(timestampMS).UTC()
}

// ExtractBodyAndAttachments walks a Gmail MIME payload tree.
func ExtractBodyAndAttachments(payload map[string]any) (string, []models.AttachmentMeta) {
	var plainParts []string
	var htmlParts []string
	var attachments []models.AttachmentMeta

	var visit func(part map[string]any)
	visit = func(part map[string]any) {
		if part == nil {
			return
		}
		mimeType, _ := anyString(part["mimeType"])
		filename, _ := anyString(part["filename"])
		body, _ := part["body"].(map[string]any)
		if body == nil {
			body = map[string]any{}
		}
		data, _ := anyString(body["data"])
		attachmentID, _ := anyString(body["attachmentId"])

		if filename != "" || attachmentID != "" {
			size := 0
			switch v := body["size"].(type) {
			case float64:
				size = int(v)
			case string:
				fmt.Sscan(v, &size)
			case int:
				size = v
			}
			name := filename
			if name == "" {
				name = "(unnamed attachment)"
			}
			mt := mimeType
			if mt == "" {
				mt = "application/octet-stream"
			}
			attachments = append(attachments, models.AttachmentMeta{
				Filename: name,
				MimeType: mt,
				Size:     size,
			})
			return
		}

		if mimeType == "text/plain" && data != "" {
			plainParts = append(plainParts, formatting.SanitizeLinkTokenDelimiters(decodeBodyData(data)))
		} else if mimeType == "text/html" && data != "" {
			htmlParts = append(htmlParts, formatting.HTMLToTelegramText(decodeBodyData(data)))
		}

		if children, ok := part["parts"].([]any); ok {
			for _, child := range children {
				if childMap, ok := child.(map[string]any); ok {
					visit(childMap)
				}
			}
		}
	}

	visit(payload)

	var bodyPieces []string
	for _, part := range plainParts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			bodyPieces = append(bodyPieces, trimmed)
		}
	}
	bodyText := strings.Join(bodyPieces, "\n\n")
	if bodyText == "" {
		bodyPieces = bodyPieces[:0]
		for _, part := range htmlParts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				bodyPieces = append(bodyPieces, trimmed)
			}
		}
		bodyText = strings.Join(bodyPieces, "\n\n")
	}

	const maxBodyTextRunes = 50000
	if len([]rune(bodyText)) > maxBodyTextRunes {
		bodyText = string([]rune(bodyText)[:maxBodyTextRunes]) + "\n\u2026 (message truncated)"
	}
	return bodyText, attachments
}

func decodeBodyData(data string) string {
	padding := (4 - len(data)%4) % 4
	raw, err := base64.URLEncoding.DecodeString(data + strings.Repeat("=", padding))
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(data)
		if err != nil {
			return ""
		}
	}
	if utf8.Valid(raw) {
		return string(raw)
	}
	applog.Warningf("Falling back to latin-1 while decoding Gmail message body.")
	runes := make([]rune, len(raw))
	for i, b := range raw {
		runes[i] = rune(b)
	}
	return string(runes)
}

func anyString(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	switch v := value.(type) {
	case string:
		return v, true
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), true
		}
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case json.Number:
		return v.String(), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return "", false
	}
}

// Ensure database package import is used for interface compatibility docs.
var _ TokenStore = (*database.Database)(nil)
