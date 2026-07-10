package config

import (
	"strings"
	"testing"
)

func TestParseAuthorizedUserIDsAcceptsCSVIntegers(t *testing.T) {
	ids, err := ParseAuthorizedUserIDs("123, 456,789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []int64{123, 456, 789} {
		if _, ok := ids[want]; !ok {
			t.Fatalf("missing id %d", want)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 ids, got %d", len(ids))
	}
}

func TestParseAuthorizedUserIDsRejectsInvalidValues(t *testing.T) {
	_, err := ParseAuthorizedUserIDs("123,abc")
	if err == nil {
		t.Fatal("expected SettingsError for invalid authorized user ids")
	}
	if _, ok := err.(*SettingsError); !ok {
		t.Fatalf("expected SettingsError, got %T", err)
	}
	if got := err.Error(); !strings.Contains(got, "invalid integer value") {
		t.Fatalf("unexpected error message: %s", got)
	}
}

func TestNormalizeLogLevelAcceptsPythonNames(t *testing.T) {
	cases := map[string]string{
		"debug":    "DEBUG",
		"INFO":     "INFO",
		"warn":     "WARNING",
		"WARNING":  "WARNING",
		"error":    "ERROR",
		"CRITICAL": "CRITICAL",
	}
	for raw, want := range cases {
		got, err := NormalizeLogLevel(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%s: got %q want %q", raw, got, want)
		}
	}
}

func TestNormalizeLogLevelRejectsUnknown(t *testing.T) {
	_, err := NormalizeLogLevel("FOO")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Unknown level") {
		t.Fatalf("unexpected error: %v", err)
	}
}
