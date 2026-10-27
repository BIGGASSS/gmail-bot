package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/formatting"
	"github.com/BIGGASSS/gmail-bot/internal/models"
)

func TestGetExpandedMessageWithSignatureOnlyPlainAlternative(t *testing.T) {
	// Synthetic version of the reported maintenance notice, without user data.
	html, err := os.ReadFile("testdata/maintenance-notice.html")
	if err != nil {
		t.Fatal(err)
	}
	body := multipart("multipart/alternative",
		textPart("text/plain", " \u00a0\r\n\r\n---\r\nExampleCloud Control Panel"),
		textPart("text/html", string(html)))
	attachment := textPart("application/pdf", "attachment contents")
	attachment["filename"] = "maintenance.pdf"
	attachment["body"].(map[string]any)["size"] = 42
	payload := map[string]any{
		"id":           "maintenance-1",
		"internalDate": "1792569614000",
		"payload": withPartHeaders(multipart("multipart/mixed", body, attachment),
			"From", "ExampleCloud Control Panel <noreply@example.invalid>",
			"Subject", "[ExampleCloud] Emergency Maintenance – Kernel Update"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/messages/maintenance-1" || r.URL.Query().Get("format") != "full" {
			t.Errorf("unexpected expansion request: %s", r.URL)
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	service := newTestService(t, server.URL, newMemoryStore())
	account := models.GoogleAccount{AccessToken: "access", TokenExpiry: time.Now().Add(time.Hour)}
	mail, err := service.GetExpandedMessage(context.Background(), account, "maintenance-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Dear Alex,",
		"Maintenance Details:",
		"Start Time: Wednesday, October 21, 2026, at 09:00 PM (GMT+8)",
		"Expected Downtime: Approximately 15–40 minutes",
		"Affected VPS IP: example-vps - 192.0.2.10",
		"No action is required on your part.",
		"---\nExampleCloud Control Panel",
	} {
		if !strings.Contains(mail.BodyText, want) {
			t.Errorf("expanded body missing %q:\n%s", want, mail.BodyText)
		}
	}
	if len(mail.Attachments) != 1 || mail.Attachments[0].Filename != "maintenance.pdf" || mail.Attachments[0].Size != 42 {
		t.Fatalf("lost attachment metadata: %+v", mail.Attachments)
	}
	chunks := formatting.RenderAndChunk(formatting.FormatExpandedMail(mail), formatting.SafeByteLimit)
	if len(chunks) != 1 || !strings.Contains(chunks[0], "Expected Downtime: Approximately 15–40 minutes") {
		t.Fatalf("expanded Telegram message lost the notice: %q", chunks)
	}
	for _, noise := range []string{"@media", "OfficeDocumentSettings", "tracking", "<table", "\ufff0", "\ufff1", "\ufff2"} {
		if strings.Contains(chunks[0], noise) {
			t.Errorf("expanded Telegram message contains unwanted markup %q", noise)
		}
	}
}

func TestGetExpandedMessagePropagatesHTMLResourceLimit(t *testing.T) {
	payload := map[string]any{
		"id": "message-1",
		"payload": textPart("text/html", strings.Repeat("<div>", 20000)+
			"Body"+strings.Repeat("</div>", 20000)),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "full" {
			t.Error("expected full-message request")
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	service := newTestService(t, server.URL, newMemoryStore())
	account := models.GoogleAccount{AccessToken: "access", TokenExpiry: time.Now().Add(time.Hour)}
	mail, err := service.GetExpandedMessage(context.Background(), account, "message-1")
	if !errors.Is(err, formatting.ErrHTMLResourceLimit) {
		t.Fatalf("got error %v, want ErrHTMLResourceLimit", err)
	}
	if mail.BodyText != "" || mail.GmailMessageID != "" || len(mail.Attachments) != 0 {
		t.Fatalf("failed expansion returned misleading partial mail: %+v", mail)
	}
}
