package telegram

import (
	"context"
	"fmt"

	"github.com/BIGGASSS/gmail-bot/internal/formatting"
	"github.com/BIGGASSS/gmail-bot/internal/models"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// BotAPI is the subset of the Telegram bot client used by the notifier.
type BotAPI interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
}

type SentMessage struct {
	ChatID    int64
	MessageID int
}

type Notifier struct {
	bot BotAPI
}

func NewNotifier(bot BotAPI) *Notifier {
	return &Notifier{bot: bot}
}

func (n *Notifier) SendText(ctx context.Context, chatID int64, text string) error {
	_ = ctx
	for _, chunk := range formatting.RenderAndChunk(text, formatting.SafeByteLimit) {
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = "HTML"
		if _, err := n.bot.Send(msg); err != nil {
			return NormalizeAPIError(err)
		}
	}
	return nil
}

func (n *Notifier) SendLoginSuccess(ctx context.Context, chatID int64, gmailEmail string) error {
	return n.SendText(ctx, chatID, fmt.Sprintf(
		"Gmail account connected: %s\nNew inbox messages will appear here automatically.",
		gmailEmail,
	))
}

func (n *Notifier) SendManualReloginPrompt(ctx context.Context, chatID int64, gmailEmail string, delayDays int) error {
	return n.SendText(ctx, chatID, fmt.Sprintf(
		"It has been %s since you connected Gmail: %s\nGoogle may revoke this connection after a week without warning.\nPlease refresh the connection now by sending /logout, then /login.",
		FormatDayCount(delayDays),
		gmailEmail,
	))
}

func (n *Notifier) SendLoginFailure(ctx context.Context, chatID int64, errorText string) error {
	return n.SendText(ctx, chatID, "Gmail login failed: "+errorText)
}

func (n *Notifier) SendReloginRequired(ctx context.Context, chatID int64, gmailEmail string) error {
	return n.SendText(ctx, chatID, fmt.Sprintf(
		"Gmail connection expired or was revoked: %s\nThe saved Google authorization is no longer valid, so automatic mail forwarding has stopped.\nUse /login to connect Gmail again.",
		gmailEmail,
	))
}

func (n *Notifier) SendMailNotification(ctx context.Context, chatID int64, mail models.IncomingMail) (SentMessage, error) {
	_ = ctx
	chunks := formatting.RenderAndChunk(formatting.FormatMailNotification(mail), formatting.SafeByteLimit)
	if len(chunks) == 0 {
		return SentMessage{}, nil
	}
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Expand", BuildExpandCallbackData(mail.GmailMessageID, 0)),
		),
	)
	msg := tgbotapi.NewMessage(chatID, chunks[0])
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = keyboard
	sent, err := n.bot.Send(msg)
	if err != nil {
		return SentMessage{}, NormalizeAPIError(err)
	}
	firstSent := SentMessage{ChatID: sent.Chat.ID, MessageID: sent.MessageID}
	for _, chunk := range chunks[1:] {
		extra := tgbotapi.NewMessage(chatID, chunk)
		extra.ParseMode = "HTML"
		if _, err := n.bot.Send(extra); err != nil {
			return firstSent, NormalizeAPIError(err)
		}
	}
	return firstSent, nil
}

func (n *Notifier) SendExpandedMail(ctx context.Context, chatID int64, mail models.ExpandedMail) error {
	_ = ctx
	for _, chunk := range formatting.RenderAndChunk(formatting.FormatExpandedMail(mail), formatting.SafeByteLimit) {
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = "HTML"
		if _, err := n.bot.Send(msg); err != nil {
			return NormalizeAPIError(err)
		}
	}
	return nil
}

func (n *Notifier) EditExpandedMail(ctx context.Context, chatID int64, messageID int, mail models.ExpandedMail, pageIndex int) error {
	_ = ctx
	chunks := formatting.RenderAndChunk(formatting.FormatExpandedMail(mail), formatting.SafeByteLimit)
	if pageIndex < 0 {
		pageIndex = 0
	}
	if pageIndex >= len(chunks) {
		pageIndex = len(chunks) - 1
	}
	edit := tgbotapi.NewEditMessageText(chatID, messageID, chunks[pageIndex])
	edit.ParseMode = "HTML"
	if keyboard := buildExpandedPageKeyboard(mail.GmailMessageID, len(chunks)); keyboard != nil {
		edit.ReplyMarkup = keyboard
	}
	if _, err := n.bot.Send(edit); err != nil {
		return NormalizeAPIError(err)
	}
	return nil
}

func buildExpandedPageKeyboard(gmailMessageID string, pageCount int) *tgbotapi.InlineKeyboardMarkup {
	if pageCount <= 1 {
		return nil
	}
	buttons := make([]tgbotapi.InlineKeyboardButton, 0, pageCount)
	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(
			fmt.Sprintf("%d", pageNumber),
			BuildExpandCallbackData(gmailMessageID, pageNumber-1),
		))
	}
	keyboard := tgbotapi.NewInlineKeyboardMarkup(buttons)
	return &keyboard
}

// BuildExpandedPageKeyboard is exported for tests.
func BuildExpandedPageKeyboard(gmailMessageID string, pageCount int) *tgbotapi.InlineKeyboardMarkup {
	return buildExpandedPageKeyboard(gmailMessageID, pageCount)
}
