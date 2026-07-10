package models

import "time"

const DefaultReloginPromptDelayDays = 6

var ManualReloginPromptDelay = time.Duration(DefaultReloginPromptDelayDays) * 24 * time.Hour

type OAuthState struct {
	State          string
	TelegramUserID int64
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type GoogleAccount struct {
	TelegramUserID         int64
	GmailEmail             string
	AccessToken            string
	RefreshToken           string
	TokenExpiry            time.Time
	LastHistoryID          string
	ConnectedAt            time.Time
	ReloginPromptDueAt     *time.Time
	ReloginPromptSentAt    *time.Time
	ReloginPromptEnabled   bool
	ReloginPromptDelayDays int
	ReloginPromptBaseAt    *time.Time
}

type ReloginPromptPreferences struct {
	TelegramUserID         int64
	ReloginPromptEnabled   bool
	ReloginPromptDelayDays int
}

func DefaultReloginPromptPreferences(telegramUserID int64) ReloginPromptPreferences {
	return ReloginPromptPreferences{
		TelegramUserID:         telegramUserID,
		ReloginPromptEnabled:   true,
		ReloginPromptDelayDays: DefaultReloginPromptDelayDays,
	}
}

type AttachmentMeta struct {
	Filename string
	MimeType string
	Size     int
}

type IncomingMail struct {
	GmailMessageID string
	FromHeader     string
	Subject        string
	Snippet        string
	ReceivedAt     time.Time
}

type ExpandedMail struct {
	GmailMessageID string
	FromHeader     string
	Subject        string
	ReceivedAt     time.Time
	BodyText       string
	Attachments    []AttachmentMeta
}
