package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type SettingsError struct {
	Message string
}

func (e *SettingsError) Error() string {
	return e.Message
}

type Settings struct {
	TelegramBotToken          string
	AuthorizedTelegramUserIDs map[int64]struct{}
	GoogleClientID            string
	GoogleClientSecret        string
	AppBaseURL                string
	DatabasePath              string
	GmailPollIntervalSeconds  int
	WebHost                   string
	WebPort                   int
	LogLevel                  string
}

func (s Settings) GoogleRedirectURI() string {
	return s.AppBaseURL + "/oauth/google/callback"
}

func (s Settings) IsAuthorized(userID int64) bool {
	_, ok := s.AuthorizedTelegramUserIDs[userID]
	return ok
}

func ParseAuthorizedUserIDs(rawValue string) (map[int64]struct{}, error) {
	parts := strings.Split(rawValue, ",")
	parsed := make(map[int64]struct{})
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		id, err := strconv.ParseInt(entry, 10, 64)
		if err != nil {
			return nil, &SettingsError{
				Message: fmt.Sprintf("AUTHORIZED_TELEGRAM_USER_IDS contains an invalid integer value: %q", entry),
			}
		}
		parsed[id] = struct{}{}
	}
	if len(parsed) == 0 {
		return nil, &SettingsError{Message: "AUTHORIZED_TELEGRAM_USER_IDS must contain at least one user id."}
	}
	return parsed, nil
}

func LoadSettings() (Settings, error) {
	_ = godotenv.Load()

	appBaseURL, err := requiredEnv("APP_BASE_URL")
	if err != nil {
		return Settings{}, err
	}
	appBaseURL = strings.TrimRight(appBaseURL, "/")

	pollInterval, err := positiveIntEnv("GMAIL_POLL_INTERVAL_SECONDS", 45)
	if err != nil {
		return Settings{}, err
	}
	webPort, err := positiveIntEnv("WEB_PORT", 8080)
	if err != nil {
		return Settings{}, err
	}

	databasePath := envOr("DATABASE_PATH", "data/gmail_bot.db")
	if !filepath.IsAbs(databasePath) {
		// Keep relative paths as-is so existing configuration continues to work.
	}

	token, err := requiredEnv("TELEGRAM_BOT_TOKEN")
	if err != nil {
		return Settings{}, err
	}
	authorizedRaw, err := requiredEnv("AUTHORIZED_TELEGRAM_USER_IDS")
	if err != nil {
		return Settings{}, err
	}
	authorized, err := ParseAuthorizedUserIDs(authorizedRaw)
	if err != nil {
		return Settings{}, err
	}
	clientID, err := requiredEnv("GOOGLE_CLIENT_ID")
	if err != nil {
		return Settings{}, err
	}
	clientSecret, err := requiredEnv("GOOGLE_CLIENT_SECRET")
	if err != nil {
		return Settings{}, err
	}

	logLevel, err := NormalizeLogLevel(envOr("LOG_LEVEL", "INFO"))
	if err != nil {
		return Settings{}, err
	}

	return Settings{
		TelegramBotToken:          token,
		AuthorizedTelegramUserIDs: authorized,
		GoogleClientID:            clientID,
		GoogleClientSecret:        clientSecret,
		AppBaseURL:                appBaseURL,
		DatabasePath:              databasePath,
		GmailPollIntervalSeconds:  pollInterval,
		WebHost:                   envOr("WEB_HOST", "0.0.0.0"),
		WebPort:                   webPort,
		LogLevel:                  logLevel,
	}, nil
}

// NormalizeLogLevel accepts Python logging level names and returns a canonical name.
func NormalizeLogLevel(raw string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DEBUG":
		return "DEBUG", nil
	case "INFO":
		return "INFO", nil
	case "WARN", "WARNING":
		return "WARNING", nil
	case "ERROR":
		return "ERROR", nil
	case "CRITICAL":
		return "CRITICAL", nil
	default:
		return "", &SettingsError{Message: fmt.Sprintf("Unknown level: %q", raw)}
	}
}

func requiredEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", &SettingsError{Message: name + " is required."}
	}
	return value, nil
}

func envOr(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func positiveIntEnv(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &SettingsError{Message: name + " must be an integer."}
	}
	if value <= 0 {
		return 0, &SettingsError{Message: name + " must be greater than zero."}
	}
	return value, nil
}
