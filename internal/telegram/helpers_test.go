package telegram

import "testing"

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
