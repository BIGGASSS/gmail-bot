package telegram

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/applog"
	"github.com/BIGGASSS/gmail-bot/internal/config"
	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/formatting"
	"github.com/BIGGASSS/gmail-bot/internal/gmail"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// UpdateClient is the Telegram Bot API surface used by the command bot.
// *tgbotapi.BotAPI implements this interface.
type UpdateClient interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
	GetUpdates(config tgbotapi.UpdateConfig) ([]tgbotapi.Update, error)
}

// TokenRevoker is the OAuth subset needed by bot commands.
type TokenRevoker interface {
	BuildAuthorizationURL(state string) string
	RevokeToken(ctx context.Context, token string) error
}

// Bot handles Telegram updates.
type Bot struct {
	settings    config.Settings
	api         UpdateClient
	database    *database.Database
	oauthClient TokenRevoker
	gmail       *gmail.Service
	notifier    *Notifier
}

func NewBot(
	settings config.Settings,
	api UpdateClient,
	db *database.Database,
	oauthClient TokenRevoker,
	gmailService *gmail.Service,
	notifier *Notifier,
) *Bot {
	return &Bot{
		settings:    settings,
		api:         api,
		database:    db,
		oauthClient: oauthClient,
		gmail:       gmailService,
		notifier:    notifier,
	}
}

func (b *Bot) Run(ctx context.Context) error {
	// Poll with GetUpdates directly so failure logging goes through applog
	// instead of telegram-bot-api's package-level logger.
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 30

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		updates, err := b.api.GetUpdates(updateConfig)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			applog.Warningf("Failed to get Telegram updates: %v", err)
			applog.Warningf("Retrying Telegram updates in 3 seconds...")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= updateConfig.Offset {
				updateConfig.Offset = update.UpdateID + 1
			}
			b.handleUpdate(ctx, update)
		}
	}
}

// HandleUpdate exposes update dispatch for tests.
func (b *Bot) HandleUpdate(ctx context.Context, update tgbotapi.Update) {
	b.handleUpdate(ctx, update)
}

func (b *Bot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	if update.Message != nil {
		if update.Message.From == nil {
			return
		}
		if !b.settings.IsAuthorized(update.Message.From.ID) {
			applog.Warningf("Ignored update from unauthorized Telegram user %d.", update.Message.From.ID)
			return
		}
		b.handleMessage(ctx, update.Message)
		return
	}
	if update.CallbackQuery != nil {
		if update.CallbackQuery.From.ID == 0 {
			return
		}
		if !b.settings.IsAuthorized(update.CallbackQuery.From.ID) {
			applog.Warningf("Ignored update from unauthorized Telegram user %d.", update.CallbackQuery.From.ID)
			return
		}
		b.handleCallback(ctx, update.CallbackQuery)
	}
}

func (b *Bot) handleMessage(ctx context.Context, message *tgbotapi.Message) {
	if message == nil || !message.IsCommand() {
		return
	}
	switch message.Command() {
	case "start":
		b.handleStart(ctx, message)
	case "help":
		b.handleHelp(ctx, message)
	case "login":
		b.handleLogin(ctx, message)
	case "status":
		b.handleStatus(ctx, message)
	case "relogin_reminder":
		b.handleReloginReminder(ctx, message)
	case "logout":
		b.handleLogout(ctx, message)
	}
}

func (b *Bot) handleStart(ctx context.Context, message *tgbotapi.Message) {
	account, err := b.database.GetGoogleAccount(ctx, message.From.ID)
	if err != nil {
		applog.Errorf("Failed to load account for /start: %v", err)
		return
	}
	var text string
	if account != nil {
		text = fmt.Sprintf(
			"Bot is active.\nConnected Gmail account: %s\nUse /status for connection details or /logout to disconnect.",
			account.GmailEmail,
		)
	} else {
		text = "Bot is active.\nUse /login to connect Gmail, then new inbox messages will be forwarded here."
	}
	b.reply(message.Chat.ID, text)
}

func (b *Bot) handleHelp(ctx context.Context, message *tgbotapi.Message) {
	_ = ctx
	b.reply(message.Chat.ID, strings.Join([]string{
		"Available commands:",
		"/start - basic status",
		"/login - connect your Gmail account",
		"/status - show Gmail connection status",
		"/relogin_reminder [on|off|days N] - configure the manual reconnect reminder",
		"/logout - disconnect Gmail",
		"/help - show this help",
	}, "\n"))
}

func (b *Bot) handleLogin(ctx context.Context, message *tgbotapi.Message) {
	state, err := newOAuthState()
	if err != nil {
		applog.Errorf("Failed to generate OAuth state: %v", err)
		return
	}
	if err := b.database.CleanupExpiredOAuthStates(ctx); err != nil {
		applog.Errorf("Failed to cleanup OAuth states: %v", err)
	}
	expiresAt := database.UTCNow().Add(15 * time.Minute)
	if err := b.database.StoreOAuthState(ctx, state, message.From.ID, expiresAt); err != nil {
		applog.Errorf("Failed to store OAuth state: %v", err)
		return
	}
	authURL := b.oauthClient.BuildAuthorizationURL(state)
	text := formatting.RenderTelegramHTML(strings.Join([]string{
		"Open this link to connect Gmail:",
		authURL,
		"",
		"The login link expires in 15 minutes.",
	}, "\n"))
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ParseMode = "HTML"
	if _, err := b.api.Send(msg); err != nil {
		applog.Errorf("Failed to send login link: %v", err)
	}
}

func (b *Bot) handleStatus(ctx context.Context, message *tgbotapi.Message) {
	account, err := b.database.GetGoogleAccount(ctx, message.From.ID)
	if err != nil {
		applog.Errorf("Failed to load account for /status: %v", err)
		return
	}
	if account == nil {
		b.reply(message.Chat.ID, "No Gmail account is connected. Use /login to connect one.")
		return
	}
	statusLines := []string{
		fmt.Sprintf("Connected Gmail account: %s", account.GmailEmail),
		fmt.Sprintf("Polling interval: %d seconds", b.settings.GmailPollIntervalSeconds),
		fmt.Sprintf("Watching for new mail after: %s", database.ToISO8601(account.ConnectedAt)),
		fmt.Sprintf("Manual reconnect reminder: %s", onOff(account.ReloginPromptEnabled)),
		fmt.Sprintf("Manual reconnect timer: %s", FormatDayCount(account.ReloginPromptDelayDays)),
	}
	if account.ReloginPromptEnabled && account.ReloginPromptDueAt != nil {
		statusLines = append(statusLines, fmt.Sprintf(
			"Manual reconnect reminder due: %s",
			database.ToISO8601(*account.ReloginPromptDueAt),
		))
	}
	b.reply(message.Chat.ID, strings.Join(statusLines, "\n"))
}

func (b *Bot) handleReloginReminder(ctx context.Context, message *tgbotapi.Message) {
	telegramUserID := message.From.ID
	args := message.CommandArguments()

	account, err := b.database.GetGoogleAccount(ctx, telegramUserID)
	if err != nil {
		applog.Errorf("Failed to load account for /relogin_reminder: %v", err)
		return
	}
	preferences, err := b.database.GetReloginPromptPreferences(ctx, telegramUserID)
	if err != nil {
		applog.Errorf("Failed to load preferences for /relogin_reminder: %v", err)
		return
	}

	if delayDays := ParseReloginReminderDelayDays(args); delayDays != nil {
		if err := b.database.SetReloginPromptDelayDays(ctx, telegramUserID, *delayDays); err != nil {
			applog.Errorf("Failed to set relogin delay: %v", err)
			return
		}
		account, err = b.database.GetGoogleAccount(ctx, telegramUserID)
		if err != nil {
			applog.Errorf("Failed to reload account after delay change: %v", err)
			return
		}
		responseLines := []string{
			fmt.Sprintf("Manual reconnect timer set to %s.", FormatDayCount(*delayDays)),
		}
		if account == nil {
			responseLines = append(responseLines, "This will apply to your next Gmail login.")
		} else if account.ReloginPromptDueAt != nil {
			responseLines = append(responseLines, fmt.Sprintf(
				"Next reminder due: %s",
				database.ToISO8601(*account.ReloginPromptDueAt),
			))
		}
		b.reply(message.Chat.ID, strings.Join(responseLines, "\n"))
		return
	}

	if enabled := ParseReloginReminderSetting(args); enabled != nil {
		if err := b.database.SetReloginPromptEnabled(ctx, telegramUserID, *enabled); err != nil {
			applog.Errorf("Failed to set relogin enabled: %v", err)
			return
		}
		b.reply(message.Chat.ID, fmt.Sprintf("Manual reconnect reminder switched %s.", onOff(*enabled)))
		return
	}

	currentEnabled := preferences.ReloginPromptEnabled
	currentDelayDays := preferences.ReloginPromptDelayDays
	if account != nil {
		currentEnabled = account.ReloginPromptEnabled
		currentDelayDays = account.ReloginPromptDelayDays
	}
	responseLines := []string{
		fmt.Sprintf("Manual reconnect reminder is currently %s.", onOff(currentEnabled)),
		fmt.Sprintf("Manual reconnect timer: %s.", FormatDayCount(currentDelayDays)),
		"Use /relogin_reminder on, /relogin_reminder off, or /relogin_reminder days 5.",
	}
	if account == nil {
		responseLines = append(responseLines, "No Gmail account is connected; settings will apply to your next /login.")
	}
	b.reply(message.Chat.ID, strings.Join(responseLines, "\n"))
}

func (b *Bot) handleLogout(ctx context.Context, message *tgbotapi.Message) {
	account, err := b.database.GetGoogleAccount(ctx, message.From.ID)
	if err != nil {
		applog.Errorf("Failed to load account for /logout: %v", err)
		return
	}
	if account == nil {
		b.reply(message.Chat.ID, "No Gmail account is currently connected.")
		return
	}
	if err := b.oauthClient.RevokeToken(ctx, account.RefreshToken); err != nil {
		applog.Warningf("Failed to revoke Google token for %s: %v", account.GmailEmail, err)
	}
	if err := b.database.DeleteGoogleAccount(ctx, message.From.ID); err != nil {
		applog.Errorf("Failed to delete Google account: %v", err)
		return
	}
	b.reply(message.Chat.ID, "Disconnected your Gmail account. Use /login to connect again.")
}

func (b *Bot) handleCallback(ctx context.Context, callback *tgbotapi.CallbackQuery) {
	if callback.Data == "" || !strings.HasPrefix(callback.Data, ExpandPrefix) {
		return
	}
	gmailMessageID, pageIndex := ParseExpandCallbackData(callback.Data)
	account, err := b.database.GetGoogleAccount(ctx, callback.From.ID)
	if err != nil {
		applog.Errorf("Failed to load account for expand: %v", err)
		b.answerCallback(callback.ID, "Failed to load account.", true)
		return
	}
	if account == nil {
		b.answerCallback(callback.ID, "Connect Gmail first.", true)
		return
	}
	wasDelivered, err := b.database.WasMessageDelivered(ctx, callback.From.ID, gmailMessageID)
	if err != nil {
		applog.Errorf("Failed to check delivery state: %v", err)
		b.answerCallback(callback.ID, "Failed to load the full message.", true)
		return
	}
	if !wasDelivered {
		b.answerCallback(callback.ID, "This message is no longer available.", true)
		return
	}
	if callback.Message == nil {
		b.answerCallback(callback.ID, "Could not edit this message.", true)
		return
	}

	expanded, err := b.gmail.GetExpandedMessage(ctx, *account, gmailMessageID)
	if err != nil {
		applog.Errorf("Failed to expand Gmail message %s for user %d: %v", gmailMessageID, callback.From.ID, err)
		b.answerCallback(callback.ID, "Failed to load the full message.", true)
		_ = b.notifier.SendText(ctx, callback.From.ID, fmt.Sprintf("Could not expand the message: %v", err))
		return
	}

	if err := b.notifier.EditExpandedMail(ctx, callback.Message.Chat.ID, callback.Message.MessageID, expanded, pageIndex); err != nil {
		applog.Errorf("Failed to edit Telegram message %d for user %d: %v", callback.Message.MessageID, callback.From.ID, err)
		b.answerCallback(callback.ID, "Failed to update the message.", true)
		return
	}
	b.answerCallback(callback.ID, "", false)
}

func (b *Bot) reply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := b.api.Send(msg); err != nil {
		applog.Errorf("Failed to send Telegram message: %v", err)
	}
}

func (b *Bot) answerCallback(callbackID, text string, showAlert bool) {
	cfg := tgbotapi.NewCallback(callbackID, text)
	cfg.ShowAlert = showAlert
	if _, err := b.api.Request(cfg); err != nil {
		applog.Errorf("Failed to answer callback: %v", err)
	}
}

func newOAuthState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

// Ensure *oauth.Client satisfies TokenRevoker.
var _ TokenRevoker = (*oauth.Client)(nil)
