package applog

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"testing"
)

func TestCentralLogRedaction(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	err := fmt.Errorf("startup: %w", &url.Error{Op: "Post", URL: "https://api.telegram.org/bot123:SECRET/getMe?token=QUERY", Err: errors.New("transport failed")})
	Errorf("startup: %v", err)
	Errorf("callback %s", "https://example.com/callback?code=CODE&state=STATE")
	Errorf("body %s", `{"refresh_token":"REFRESH","client_secret":"CLIENT"}`)
	Errorf("raw %s", "https://api.telegram.org/bot123:SECRET/getUpdates")
	for _, secret := range []string{"SECRET", "QUERY", "CODE", "STATE", "REFRESH", "CLIENT"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("leaked %s: %s", secret, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "transport failed") {
		t.Fatal("lost error context")
	}
	if strings.Contains(RedactError(err), "SECRET") {
		t.Fatal("startup redaction leaked token")
	}
}
