package applog

import (
	"bytes"
	"errors"
	"log"
	"net/url"
	"strings"
	"testing"
)

func TestConfigureFiltersByLevel(t *testing.T) {
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		_ = Configure("INFO")
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	if err := Configure("ERROR"); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Re-apply test writer because Configure may adjust flags/output for production defaults.
	log.SetOutput(&buf)
	log.SetFlags(0)

	Infof("should be hidden")
	Warningf("also hidden")
	Errorf("visible error")

	out := buf.String()
	if strings.Contains(out, "should be hidden") || strings.Contains(out, "also hidden") {
		t.Fatalf("lower-level logs leaked: %q", out)
	}
	if !strings.Contains(out, "ERROR visible error") {
		t.Fatalf("missing error log: %q", out)
	}
	if strings.Contains(out, "INFO visible") {
		t.Fatalf("error log mislabeled: %q", out)
	}
}

func TestRedactErrorDropsURL(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://api.telegram.org/bot123:abc/getUpdates",
		Err: errors.New("connection refused"),
	}
	msg := RedactError(err)
	if strings.Contains(msg, "123:abc") || strings.Contains(msg, "api.telegram.org") {
		t.Fatalf("URL leaked into message: %q", msg)
	}
	if !strings.Contains(msg, "connection refused") {
		t.Fatalf("missing underlying error: %q", msg)
	}
}

func TestRedactStringRedactsBotToken(t *testing.T) {
	msg := RedactString("error calling https://api.telegram.org/bot999:xyz/getMe")
	if strings.Contains(msg, "999:xyz") {
		t.Fatalf("bot token leaked: %q", msg)
	}
}

func TestRedactErrorPreservesNonURLErrors(t *testing.T) {
	if got := RedactError(errors.New("plain error")); got != "plain error" {
		t.Fatalf("got %q", got)
	}
}

func TestParseLevelRejectsUnknown(t *testing.T) {
	_, _, err := ParseLevel("FOO")
	if err == nil || !strings.Contains(err.Error(), "Unknown level") {
		t.Fatalf("unexpected error: %v", err)
	}
}
