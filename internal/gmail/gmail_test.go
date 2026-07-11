package gmail

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/BIGGASSS/gmail-bot/internal/formatting"
)

func encodeBody(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func TestExtractBodyAndAttachmentsPrefersPlainText(t *testing.T) {
	body, attachments := ExtractBodyAndAttachments(map[string]any{
		"mimeType": "multipart/mixed",
		"parts": []any{
			map[string]any{
				"mimeType": "multipart/alternative",
				"parts": []any{
					map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": encodeBody("Plain body")}},
					map[string]any{"mimeType": "text/html", "body": map[string]any{"data": encodeBody("<p>HTML body</p>")}},
				},
			},
			map[string]any{
				"mimeType": "application/pdf",
				"filename": "report.pdf",
				"body":     map[string]any{"attachmentId": "attachment-1", "size": 128},
			},
		},
	})
	if body != "Plain body" {
		t.Fatalf("got body %q", body)
	}
	if len(attachments) != 1 || attachments[0].Filename != "report.pdf" {
		t.Fatalf("unexpected attachments: %+v", attachments)
	}
}

func TestExtractBodyAndAttachmentsKeepsHTMLAnchorTextClickable(t *testing.T) {
	body, attachments := ExtractBodyAndAttachments(map[string]any{
		"mimeType": "text/html",
		"body": map[string]any{
			"data": encodeBody(`<p><a href="https://example.com/deal?plan=pro&discount=50">Join Pro now</a></p>`),
		},
	})
	if len(attachments) != 0 {
		t.Fatalf("expected no attachments, got %+v", attachments)
	}
	rendered := formatting.RenderTelegramHTML(body)
	if !strings.Contains(rendered, `href="https://example.com/deal?plan=pro&amp;discount=50"`) {
		t.Fatalf("missing href: %s", rendered)
	}
	if !strings.Contains(rendered, `>Join Pro now<`) {
		t.Fatalf("missing label: %s", rendered)
	}
}
