package applog

import (
	"bytes"
	"log"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestTelegramBotLoggerHonorsLogLevel(t *testing.T) {
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		_ = Configure("INFO")
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		_ = tgbotapi.SetLogger(log.Default())
	})

	if err := Configure("ERROR"); err != nil {
		t.Fatalf("configure: %v", err)
	}
	log.SetOutput(&buf)
	log.SetFlags(0)
	if err := tgbotapi.SetLogger(TelegramBotLogger{}); err != nil {
		t.Fatalf("set logger: %v", err)
	}

	// Simulate library logging through the package logger interface.
	TelegramBotLogger{}.Println("Failed to get updates, retrying in 3 seconds...")
	if out := buf.String(); out != "" {
		t.Fatalf("expected filtered library log at ERROR level, got %q", out)
	}

	if err := Configure("WARNING"); err != nil {
		t.Fatalf("configure warning: %v", err)
	}
	log.SetOutput(&buf)
	log.SetFlags(0)
	buf.Reset()
	TelegramBotLogger{}.Printf("network error: %s", "timeout")
	if out := buf.String(); !strings.Contains(out, "WARNING network error: timeout") {
		t.Fatalf("expected warning log, got %q", out)
	}
}
