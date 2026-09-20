package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/formatting"
	"github.com/BIGGASSS/gmail-bot/internal/models"
)

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
