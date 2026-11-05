package telegram

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestParseExpandCallbackDataSupportsOptionalPage(t *testing.T) {
	id, page := ParseExpandCallbackData("expand:gmail-message-1")
	if id != "gmail-message-1" || page != 0 {
		t.Fatalf("got %q %d", id, page)
	}
	id, page = ParseExpandCallbackData("expand:gmail-message-1:2")
	if id != "gmail-message-1" || page != 2 {
		t.Fatalf("got %q %d", id, page)
	}
}

func TestParseReloginReminderSetting(t *testing.T) {
	if got := ParseReloginReminderSetting("on"); got == nil || !*got {
		t.Fatal("expected true for on")
	}
	if got := ParseReloginReminderSetting(" enable "); got == nil || !*got {
		t.Fatal("expected true for enable")
	}
	if got := ParseReloginReminderSetting("off"); got == nil || *got {
		t.Fatal("expected false for off")
	}
	if got := ParseReloginReminderSetting("DISABLE"); got == nil || *got {
		t.Fatal("expected false for DISABLE")
	}
	if got := ParseReloginReminderSetting(""); got != nil {
		t.Fatal("expected nil for empty")
	}
	if got := ParseReloginReminderSetting("maybe"); got != nil {
		t.Fatal("expected nil for maybe")
	}
}

func TestParseReloginReminderDelayDays(t *testing.T) {
	assertDays := func(args string, want int) {
		t.Helper()
		got := ParseReloginReminderDelayDays(args)
		if got == nil || *got != want {
			t.Fatalf("args %q: got %v want %d", args, got, want)
		}
	}
	assertDays("5", 5)
	assertDays("days 10", 10)
	assertDays("1 day", 1)
	if ParseReloginReminderDelayDays("0") != nil {
		t.Fatal("expected nil for 0")
	}
	if ParseReloginReminderDelayDays("off") != nil {
		t.Fatal("expected nil for off")
	}
	if ParseReloginReminderDelayDays("days nope") != nil {
		t.Fatal("expected nil for days nope")
	}
}

func TestLogoutCallbackDataRoundTrip(t *testing.T) {
	for _, accountID := range []int64{LogoutAllAccounts, 17} {
		user, got, ok := ParseLogoutCallbackData(BuildLogoutCallbackData(123456789, accountID))
		if !ok || user != 123456789 || got != accountID {
			t.Fatalf("account %d: user=%d got=%d ok=%v", accountID, user, got, ok)
		}
	}
	for _, bad := range []string{"logout:", "logout:1", "logout:x:all", "logout:1:-3", "logout:1:0", "logout:1:abc", "expand:1:all"} {
		if _, _, ok := ParseLogoutCallbackData(bad); ok {
			t.Fatalf("parsed malformed data %q", bad)
		}
	}
}

func TestSplitMessageText(t *testing.T) {
	if chunks := SplitMessageText("short", 10); len(chunks) != 1 || chunks[0] != "short" {
		t.Fatalf("chunks=%q", chunks)
	}
	chunks := SplitMessageText("aaaa\nbbbb\ncccc", 9)
	if strings.Join(chunks, "|") != "aaaa\nbbbb|cccc" {
		t.Fatalf("line split=%q", chunks)
	}
	// A single over-long line is split without breaking a surrogate pair.
	chunks = SplitMessageText("ab😀cd", 3)
	if strings.Join(chunks, "|") != "ab|😀c|d" {
		t.Fatalf("long line split=%q", chunks)
	}
	for _, chunk := range SplitMessageText(strings.Repeat("x\n", 5000), TelegramTextLimit) {
		if len(utf16.Encode([]rune(chunk))) > TelegramTextLimit {
			t.Fatalf("chunk too long: %d", len(chunk))
		}
	}
}
