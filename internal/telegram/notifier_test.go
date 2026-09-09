package telegram

import (
	"context"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/BIGGASSS/gmail-bot/internal/formatting"
	"github.com/BIGGASSS/gmail-bot/internal/models"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type fakeBot struct {
	sent []sentCall
}

type sentCall struct {
	chattable tgbotapi.Chattable
}

func (f *fakeBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.sent = append(f.sent, sentCall{chattable: c})
	// For edits, the library uses EditMessageTextConfig which is Chattable.
	return tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 456}}, nil
}

func (f *fakeBot) Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func TestSendMailNotificationTruncatesOversizedContent(t *testing.T) {
	for _, content := range []string{
		strings.Repeat("A", 5000),
		strings.Repeat("<&😀", 1500),
		strings.Repeat("https://example.com/path ", 300),
	} {
		t.Run(content[:8], func(t *testing.T) {
			testSingleNotification(t, content, true)
		})
	}
}

func TestSendMailNotificationPreservesShortPreview(t *testing.T) {
	testSingleNotification(t, "Hello <there> 😀", false)
}

func testSingleNotification(t *testing.T, content string, truncated bool) {
	t.Helper()
	bot := &fakeBot{}
	notifier := NewNotifier(bot)
	mail := models.IncomingMail{
		GmailMessageID: "gmail-message-1",
		FromHeader:     "sender@example.com",
		Subject:        content,
		Snippet:        "snippet",
		ReceivedAt:     time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	sent, err := notifier.SendMailNotification(context.Background(), 456, mail)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.ChatID != 456 || sent.MessageID != 1 {
		t.Fatalf("unexpected delivery reference: %+v", sent)
	}
	if len(bot.sent) != 1 {
		t.Fatalf("expected one preview message, got %d", len(bot.sent))
	}
	first, ok := bot.sent[0].chattable.(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected MessageConfig, got %T", bot.sent[0].chattable)
	}
	if first.ReplyMarkup == nil {
		t.Fatal("expected Expand keyboard on first chunk")
	}
	markup, ok := first.ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("unexpected markup type %T", first.ReplyMarkup)
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 || markup.InlineKeyboard[0][0].Text != "Expand" {
		t.Fatalf("unexpected keyboard: %+v", markup.InlineKeyboard)
	}
	if first.ParseMode != "HTML" || len(first.Text) > formatting.SafeByteLimit || !utf8.ValidString(first.Text) {
		t.Fatalf("invalid preview: mode=%q bytes=%d", first.ParseMode, len(first.Text))
	}
	if truncated {
		if !strings.HasSuffix(first.Text, "\n… (preview truncated; tap Expand)") {
			t.Fatal("missing truncation notice")
		}
	} else if want := formatting.RenderTelegramHTML(formatting.FormatMailNotification(mail)); first.Text != want {
		t.Fatalf("short preview changed: got %q, want %q", first.Text, want)
	}
	if data := markup.InlineKeyboard[0][0].CallbackData; data == nil || *data != "expand:gmail-message-1" {
		t.Fatalf("unexpected Expand callback: %v", data)
	}
	decoder := xml.NewDecoder(strings.NewReader("<root>" + first.Text + "</root>"))
	for {
		if _, err := decoder.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("preview contains broken HTML: %v", err)
		}
	}
}

func TestEditExpandedMailEditsOriginalMessageAndRemovesButton(t *testing.T) {
	bot := &fakeBot{}
	notifier := NewNotifier(bot)
	mail := models.ExpandedMail{
		GmailMessageID: "gmail-message-1",
		FromHeader:     "sender@example.com",
		Subject:        "Hello <there>",
		ReceivedAt:     time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		BodyText:       "Full body",
		Attachments:    nil,
	}

	if err := notifier.EditExpandedMail(context.Background(), 456, 10, mail, 0); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if len(bot.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(bot.sent))
	}
	edit, ok := bot.sent[0].chattable.(tgbotapi.EditMessageTextConfig)
	if !ok {
		t.Fatalf("expected EditMessageTextConfig, got %T", bot.sent[0].chattable)
	}
	if !strings.Contains(edit.Text, "Expanded Gmail message") {
		t.Fatalf("missing expanded heading: %s", edit.Text)
	}
	if !strings.Contains(edit.Text, "Subject: Hello &lt;there&gt;") {
		t.Fatalf("expected escaped subject: %s", edit.Text)
	}
	if edit.ParseMode != "HTML" {
		t.Fatalf("parse mode=%q", edit.ParseMode)
	}
	if edit.ReplyMarkup != nil {
		t.Fatal("expected no reply markup for single page")
	}
}

func TestEditExpandedMailAddsPageButtonsForLongMessages(t *testing.T) {
	bot := &fakeBot{}
	notifier := NewNotifier(bot)
	mail := models.ExpandedMail{
		GmailMessageID: "gmail-message-1",
		FromHeader:     "sender@example.com",
		Subject:        "Long body",
		ReceivedAt:     time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		BodyText:       strings.Repeat("Line\n", 1000),
		Attachments:    nil,
	}

	if err := notifier.EditExpandedMail(context.Background(), 456, 10, mail, 0); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if len(bot.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(bot.sent))
	}
	edit := bot.sent[0].chattable.(tgbotapi.EditMessageTextConfig)
	if edit.ReplyMarkup == nil {
		t.Fatal("expected page keyboard")
	}
	buttons := edit.ReplyMarkup.InlineKeyboard[0]
	if len(buttons) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(buttons))
	}
	if buttons[0].Text != "1" || buttons[1].Text != "2" {
		t.Fatalf("unexpected button labels: %+v", buttons)
	}
	if buttons[0].CallbackData == nil || *buttons[0].CallbackData != "expand:gmail-message-1" {
		t.Fatalf("unexpected first callback: %v", buttons[0].CallbackData)
	}
	if buttons[1].CallbackData == nil || *buttons[1].CallbackData != "expand:gmail-message-1:1" {
		t.Fatalf("unexpected second callback: %v", buttons[1].CallbackData)
	}
}

func TestEditExpandedMailEditsRequestedPage(t *testing.T) {
	bot := &fakeBot{}
	notifier := NewNotifier(bot)
	mail := models.ExpandedMail{
		GmailMessageID: "gmail-message-1",
		FromHeader:     "sender@example.com",
		Subject:        "Long body",
		ReceivedAt:     time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		BodyText:       strings.Repeat("Line\n", 1000),
		Attachments:    nil,
	}

	if err := notifier.EditExpandedMail(context.Background(), 456, 10, mail, 1); err != nil {
		t.Fatalf("edit: %v", err)
	}
	edit := bot.sent[0].chattable.(tgbotapi.EditMessageTextConfig)
	if strings.Contains(edit.Text, "Body:") {
		t.Fatalf("page 1 should not contain Body heading: %s", edit.Text)
	}
}
