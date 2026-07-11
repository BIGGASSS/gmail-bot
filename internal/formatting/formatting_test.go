package formatting

import (
	"strings"
	"testing"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/models"
)

func TestStripHTMLCollapsesMarkup(t *testing.T) {
	if got := StripHTML("<p>Hello <strong>world</strong></p>"); got != "Hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestStripHTMLPreservesLinkTargets(t *testing.T) {
	got := StripHTML(`<p><a href="https://example.com/deal">Join Pro now</a></p>`)
	want := "Join Pro now <https://example.com/deal>"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNormalizeGmailSnippetUnescapesEntities(t *testing.T) {
	if got := NormalizeGmailSnippet("Don&#39;t miss out"); got != "Don't miss out" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderTelegramHTMLLinkifiesURLs(t *testing.T) {
	rendered := RenderTelegramHTML("Join Pro now\n<https://example.com/deal?plan=pro&discount=50>")
	if !strings.Contains(rendered, `href="https://example.com/deal?plan=pro&amp;discount=50"`) {
		t.Fatalf("missing href: %s", rendered)
	}
	if !strings.Contains(rendered, `>https://example.com/deal?plan=pro&amp;discount=50<`) {
		t.Fatalf("missing label: %s", rendered)
	}
}

func TestRenderTelegramHTMLPreservesAnchorText(t *testing.T) {
	rendered := RenderTelegramHTML(
		HTMLToTelegramText(`<p><a href="https://example.com/deal?plan=pro&discount=50">Join Pro now</a></p>`),
	)
	if !strings.Contains(rendered, `href="https://example.com/deal?plan=pro&amp;discount=50"`) {
		t.Fatalf("missing href: %s", rendered)
	}
	if !strings.Contains(rendered, `>Join Pro now<`) {
		t.Fatalf("missing anchor text: %s", rendered)
	}
	if strings.Contains(rendered, "https://example.com/deal?plan=pro&amp;discount=50</a>") {
		t.Fatalf("unexpected bare URL label: %s", rendered)
	}
}

func TestChunkTextSplitsLongMessages(t *testing.T) {
	text := strings.Repeat("line\n", 1500)
	chunks := ChunkText(text, 1000)
	if len(chunks) <= 1 {
		t.Fatal("expected multiple chunks")
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 1000 {
			t.Fatalf("chunk too long: %d", len([]rune(chunk)))
		}
	}
}

func TestFormatExpandedMailIncludesAttachments(t *testing.T) {
	mail := models.ExpandedMail{
		GmailMessageID: "gmail-1",
		FromHeader:     "sender@example.com",
		Subject:        "Subject",
		ReceivedAt:     time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC),
		BodyText:       "Hello from Gmail",
		Attachments: []models.AttachmentMeta{
			{Filename: "report.pdf", MimeType: "application/pdf", Size: 42},
		},
	}
	rendered := FormatExpandedMail(mail)
	if !strings.Contains(rendered, "Attachments:") {
		t.Fatal("missing attachments heading")
	}
	if !strings.Contains(rendered, "report.pdf") {
		t.Fatal("missing attachment name")
	}
}
