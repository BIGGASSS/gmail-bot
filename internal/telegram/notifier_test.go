package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

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
