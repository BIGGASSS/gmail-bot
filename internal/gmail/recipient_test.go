package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/models"
)

func TestGetMessageSummaryIncludesRecipients(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers []string
		wantTo  string
		wantCc  string
	}{
		{
			name:    "to and cc",
			headers: []string{"From", "sender@example.com", "To", "Alex <alex@example.com>", "Cc", "team@example.com", "Delivered-To", "alex@example.com"},
			wantTo:  "Alex <alex@example.com>",
			wantCc:  "team@example.com",
		},
		{
			name:    "falls back to delivered-to",
			headers: []string{"From", "sender@example.com", "Delivered-To", "hidden@example.com"},
			wantTo:  "hidden@example.com",
		},
		{
			name:    "no recipient headers",
			headers: []string{"From", "sender@example.com"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested := map[string]bool{}
				for _, name := range r.URL.Query()["metadataHeaders"] {
					requested[name] = true
				}
				for _, name := range []string{"To", "Cc", "Delivered-To"} {
					if !requested[name] {
						t.Errorf("metadata request missing header %q: %s", name, r.URL)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":       "msg-1",
					"labelIds": []string{"INBOX"},
					"payload":  withPartHeaders(map[string]any{}, tt.headers...),
				})
			}))
			defer server.Close()
			service := newTestService(t, server.URL, newMemoryStore())
			account := models.GoogleAccount{AccessToken: "access", TokenExpiry: time.Now().Add(time.Hour)}
			mail, err := service.GetMessageSummary(context.Background(), account, "msg-1")
			if err != nil {
				t.Fatal(err)
			}
			if mail.ToHeader != tt.wantTo || mail.CcHeader != tt.wantCc {
				t.Fatalf("got To=%q Cc=%q, want To=%q Cc=%q", mail.ToHeader, mail.CcHeader, tt.wantTo, tt.wantCc)
			}
		})
	}
}

func TestGetExpandedMessageIncludesRecipients(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg-1",
			"payload": withPartHeaders(textPart("text/plain", "hello"),
				"From", "sender@example.com", "To", "alex@example.com", "Cc", "team@example.com"),
		})
	}))
	defer server.Close()
	service := newTestService(t, server.URL, newMemoryStore())
	account := models.GoogleAccount{AccessToken: "access", TokenExpiry: time.Now().Add(time.Hour)}
	mail, err := service.GetExpandedMessage(context.Background(), account, "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if mail.ToHeader != "alex@example.com" || mail.CcHeader != "team@example.com" {
		t.Fatalf("got To=%q Cc=%q", mail.ToHeader, mail.CcHeader)
	}
}
