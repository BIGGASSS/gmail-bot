package applog

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarning
	LevelError
	LevelCritical
)

var (
	mu           sync.RWMutex
	currentLevel = LevelInfo
)

// ParseLevel accepts Python logging level names.
func ParseLevel(raw string) (Level, string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(raw))
	switch normalized {
	case "DEBUG":
		return LevelDebug, "DEBUG", nil
	case "INFO":
		return LevelInfo, "INFO", nil
	case "WARN", "WARNING":
		return LevelWarning, "WARNING", nil
	case "ERROR":
		return LevelError, "ERROR", nil
	case "CRITICAL":
		return LevelCritical, "CRITICAL", nil
	default:
		return 0, "", fmt.Errorf("Unknown level: %q", raw)
	}
}

func Configure(raw string) error {
	level, _, err := ParseLevel(raw)
	if err != nil {
		return err
	}
	mu.Lock()
	currentLevel = level
	mu.Unlock()
	log.SetFlags(log.LstdFlags)
	return nil
}

func CurrentLevel() Level {
	mu.RLock()
	defer mu.RUnlock()
	return currentLevel
}

func Debugf(format string, args ...any) { logf(LevelDebug, "DEBUG", format, args...) }
func Infof(format string, args ...any)  { logf(LevelInfo, "INFO", format, args...) }
func Warningf(format string, args ...any) {
	logf(LevelWarning, "WARNING", format, args...)
}
func Errorf(format string, args ...any) { logf(LevelError, "ERROR", format, args...) }

var botTokenPattern = regexp.MustCompile(`bot\d+:[A-Za-z0-9_-]+`)

// RedactString replaces Telegram bot tokens ("bot<id>:<secret>") in s.
func RedactString(s string) string {
	return botTokenPattern.ReplaceAllString(s, "bot***")
}

// RedactError renders err for logging without leaking credentials: URLs in
// *url.Error (which may embed bot or OAuth tokens) are dropped, and any
// remaining bot-token patterns are redacted.
func RedactError(err error) string {
	if err == nil {
		return ""
	}
	var urlErr *url.Error
	msg := err.Error()
	if errors.As(err, &urlErr) {
		msg = urlErr.Op + ": " + urlErr.Err.Error()
	}
	return RedactString(msg)
}

func logf(level Level, name, format string, args ...any) {
	if level < CurrentLevel() {
		return
	}
	log.Printf("%s %s", name, fmt.Sprintf(format, args...))
}
