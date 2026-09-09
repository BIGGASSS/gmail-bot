package main

import (
	"testing"
	"time"
)

func TestTelegramClientTimeoutAllowsLongPoll(t *testing.T) {
	timeout := newTelegramHTTPClient().Timeout
	if timeout <= 30*time.Second || timeout > 2*time.Minute {
		t.Fatalf("Telegram timeout %s must exceed long poll and remain bounded", timeout)
	}
}
