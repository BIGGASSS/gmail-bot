package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettingsValidatesAndNormalizes(t *testing.T) {
	tmp := t.TempDir()
	envFile := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envFile, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure godotenv doesn't pick up a repo .env unexpectedly by chdir.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("AUTHORIZED_TELEGRAM_USER_IDS", "1, 2,2")
	t.Setenv("GOOGLE_CLIENT_ID", "client")
	t.Setenv("GOOGLE_CLIENT_SECRET", "secret")
	t.Setenv("APP_BASE_URL", "https://example.com/")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("GMAIL_POLL_INTERVAL_SECONDS", "30")
	t.Setenv("WEB_PORT", "9090")

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if settings.AppBaseURL != "https://example.com" {
		t.Fatalf("base url=%q", settings.AppBaseURL)
	}
	if settings.GoogleRedirectURI() != "https://example.com/oauth/google/callback" {
		t.Fatalf("redirect=%q", settings.GoogleRedirectURI())
	}
	if settings.LogLevel != "WARNING" {
		t.Fatalf("log level=%q", settings.LogLevel)
	}
	if settings.GmailPollIntervalSeconds != 30 || settings.WebPort != 9090 {
		t.Fatalf("poll/port=%d/%d", settings.GmailPollIntervalSeconds, settings.WebPort)
	}
	if len(settings.AuthorizedTelegramUserIDs) != 2 {
		t.Fatalf("ids=%v", settings.AuthorizedTelegramUserIDs)
	}
}

func TestLoadSettingsRejectsInvalidLogLevel(t *testing.T) {
	tmp := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("AUTHORIZED_TELEGRAM_USER_IDS", "1")
	t.Setenv("GOOGLE_CLIENT_ID", "client")
	t.Setenv("GOOGLE_CLIENT_SECRET", "secret")
	t.Setenv("APP_BASE_URL", "https://example.com")
	t.Setenv("LOG_LEVEL", "NOPE")

	_, err = LoadSettings()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*SettingsError); !ok {
		t.Fatalf("type=%T", err)
	}
}
