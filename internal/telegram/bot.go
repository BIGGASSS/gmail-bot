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
	"github.com/BIGGASSS/gmail-bot/internal/models"
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
			applog.Warningf("Failed to get Telegram updates: %s", applog.RedactError(err))
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
	case "relog":
		b.reply(message.Chat.ID, "The /relog command has been removed. To refresh Gmail authorization, send /logout, then /login.")
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
	accounts, err := b.database.ListGoogleAccountsForUser(ctx, message.From.ID)
	if err != nil {
		applog.Errorf("Failed to load accounts for /start: %v", err)
		return
	}
	var text string
	switch len(accounts) {
	case 0:
		text = "Bot is active.\nUse /login to connect Gmail, then new inbox messages will be forwarded here."
	case 1:
		text = fmt.Sprintf(
			"Bot is active.\nConnected Gmail account: %s\nUse /status for connection details, /login to add another account, or /logout to disconnect.",
			accounts[0].GmailEmail,
		)
	default:
		text = fmt.Sprintf(
			"Bot is active.\nUse /status for connection details, /login to add another account, or /logout to disconnect.\nConnected Gmail accounts:\n%s",
			strings.Join(accountEmails(accounts), "\n"),
		)
	}
	b.reply(message.Chat.ID, text)
}

func (b *Bot) handleHelp(ctx context.Context, message *tgbotapi.Message) {
	_ = ctx
	b.reply(message.Chat.ID, strings.Join([]string{
		"Available commands:",
		"/start - basic status",
		"/login - connect a Gmail account (repeat to add more)",
		"/status - show Gmail connection status",
		"/relogin_reminder [on|off|days N] - configure the manual reconnect reminder",
		"/logout - disconnect one or all Gmail accounts",
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
	if err := b.database.DeleteOAuthStatesForUser(ctx, message.From.ID); err != nil {
		applog.Errorf("Failed to clear stale OAuth states: %v", err)
	}
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
	// Send the login link to the user's private chat
	msg := tgbotapi.NewMessage(message.From.ID, text)
	msg.ParseMode = "HTML"
	if _, err := b.api.Send(msg); err != nil {
		applog.Errorf("Failed to send login link: %v", err)
		// If private send fails and we're in a group, tell the user to start the bot privately
		if message.Chat.ID != message.From.ID {
			b.reply(message.Chat.ID, "I couldn't send you the login link in a private message. Please start a private chat with me first (send /start to me directly), then use /login again.")
		}
		return
	}
	// If invoked in a group, notify the group that the link was sent privately
	if message.Chat.ID != message.From.ID {
		b.reply(message.Chat.ID, "I've sent you the Gmail login link in a private message.")
	}
}

func (b *Bot) handleStatus(ctx context.Context, message *tgbotapi.Message) {
	accounts, err := b.database.ListGoogleAccountsForUser(ctx, message.From.ID)
	if err != nil {
		applog.Errorf("Failed to load accounts for /status: %v", err)
		return
	}
	if len(accounts) == 0 {
		b.reply(message.Chat.ID, "No Gmail account is connected. Use /login to connect one.")
		return
	}
	preferences, err := b.database.GetReloginPromptPreferences(ctx, message.From.ID)
	if err != nil {
		applog.Errorf("Failed to load preferences for /status: %v", err)
		return
	}
	statusLines := []string{
		fmt.Sprintf("Polling interval: %d seconds", b.settings.GmailPollIntervalSeconds),
		fmt.Sprintf("Manual reconnect reminder: %s", onOff(preferences.ReloginPromptEnabled)),
		fmt.Sprintf("Manual reconnect timer: %s", FormatDayCount(preferences.ReloginPromptDelayDays)),
	}
	for _, account := range accounts {
		statusLines = append(statusLines,
			"",
			fmt.Sprintf("Connected Gmail account: %s", account.GmailEmail),
			fmt.Sprintf("Watching for new mail after: %s", database.ToISO8601(account.ConnectedAt)),
		)
		if account.ReloginPromptEnabled && account.ReloginPromptDueAt != nil {
			statusLines = append(statusLines, fmt.Sprintf(
				"Manual reconnect reminder due: %s",
				database.ToISO8601(*account.ReloginPromptDueAt),
			))
		}
	}
	b.reply(message.Chat.ID, strings.Join(statusLines, "\n"))
}

func (b *Bot) handleReloginReminder(ctx context.Context, message *tgbotapi.Message) {
	telegramUserID := message.From.ID
	args := message.CommandArguments()

	if delayDays := ParseReloginReminderDelayDays(args); delayDays != nil {
		if err := b.database.SetReloginPromptDelayDays(ctx, telegramUserID, *delayDays); err != nil {
			applog.Errorf("Failed to set relogin delay: %v", err)
			return
		}
		accounts, err := b.database.ListGoogleAccountsForUser(ctx, telegramUserID)
		if err != nil {
			applog.Errorf("Failed to reload accounts after delay change: %v", err)
			return
		}
		responseLines := []string{
			fmt.Sprintf("Manual reconnect timer set to %s.", FormatDayCount(*delayDays)),
		}
		if len(accounts) == 0 {
			responseLines = append(responseLines, "This will apply to your next Gmail login.")
		}
		for _, account := range accounts {
			if account.ReloginPromptDueAt == nil {
				continue
			}
			due := database.ToISO8601(*account.ReloginPromptDueAt)
			if len(accounts) == 1 {
				responseLines = append(responseLines, fmt.Sprintf("Next reminder due: %s", due))
			} else {
				responseLines = append(responseLines, fmt.Sprintf("Next reminder for %s due: %s", account.GmailEmail, due))
			}
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

	preferences, err := b.database.GetReloginPromptPreferences(ctx, telegramUserID)
	if err != nil {
		applog.Errorf("Failed to load preferences for /relogin_reminder: %v", err)
		return
	}
	accounts, err := b.database.ListGoogleAccountsForUser(ctx, telegramUserID)
	if err != nil {
		applog.Errorf("Failed to load accounts for /relogin_reminder: %v", err)
		return
	}
	responseLines := []string{
		fmt.Sprintf("Manual reconnect reminder is currently %s.", onOff(preferences.ReloginPromptEnabled)),
		fmt.Sprintf("Manual reconnect timer: %s.", FormatDayCount(preferences.ReloginPromptDelayDays)),
		"Use /relogin_reminder on, /relogin_reminder off, or /relogin_reminder days 5.",
	}
	if len(accounts) == 0 {
		responseLines = append(responseLines, "No Gmail account is connected; settings will apply to your next /login.")
	}
	b.reply(message.Chat.ID, strings.Join(responseLines, "\n"))
}

func (b *Bot) handleLogout(ctx context.Context, message *tgbotapi.Message) {
	accounts, err := b.database.ListGoogleAccountsForUser(ctx, message.From.ID)
	if err != nil {
		applog.Errorf("Failed to load accounts for /logout: %v", err)
		b.reply(message.Chat.ID, "Couldn't disconnect safely. Please retry /logout.")
		return
	}
	if len(accounts) > 1 {
		b.sendLogoutPicker(message.Chat.ID, message.From.ID, accounts)
		return
	}
	// Invalidate in-flight login callbacks even when no account is connected.
	// With one account, remove exactly that account so a concurrent login is not swept up.
	var removed []models.GoogleAccount
	if len(accounts) == 1 {
		var account *models.GoogleAccount
		account, err = b.database.LogoutAccount(ctx, message.From.ID, accounts[0].ID)
		if account != nil {
			removed = []models.GoogleAccount{*account}
		}
	} else {
		removed, err = b.database.LogoutAll(ctx, message.From.ID)
	}
	if err != nil {
		applog.Errorf("Failed to disconnect account for /logout: %v", err)
		b.reply(message.Chat.ID, "Couldn't disconnect safely. Please retry /logout.")
		return
	}
	if len(removed) == 0 {
		b.reply(message.Chat.ID, "No Gmail account is currently connected.")
		return
	}
	b.reply(message.Chat.ID, b.revokeAndDescribe(ctx, removed))
}

func (b *Bot) sendLogoutPicker(chatID, telegramUserID int64, accounts []models.GoogleAccount) {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(accounts)+1)
	for _, account := range accounts {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(account.GmailEmail, BuildLogoutCallbackData(telegramUserID, account.ID)),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("All accounts", BuildLogoutCallbackData(telegramUserID, LogoutAllAccounts)),
	))
	msg := tgbotapi.NewMessage(chatID, "Which Gmail account do you want to disconnect?")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	if _, err := b.api.Send(msg); err != nil {
		applog.Errorf("Failed to send logout picker: %v", err)
	}
}

func (b *Bot) handleLogoutCallback(ctx context.Context, callback *tgbotapi.CallbackQuery) {
	owner, accountID, ok := ParseLogoutCallbackData(callback.Data)
	if !ok || owner != callback.From.ID {
		b.answerCallback(callback.ID, "This button belongs to someone else.", true)
		return
	}
	var (
		removed []models.GoogleAccount
		err     error
	)
	if accountID == LogoutAllAccounts {
		removed, err = b.database.LogoutAll(ctx, owner)
	} else {
		var account *models.GoogleAccount
		account, err = b.database.LogoutAccount(ctx, owner, accountID)
		if account != nil {
			removed = []models.GoogleAccount{*account}
		}
	}
	if err != nil {
		applog.Errorf("Failed to disconnect account from logout button: %v", err)
		b.answerCallback(callback.ID, "Couldn't disconnect safely. Please retry /logout.", true)
		return
	}
	text := "That Gmail account is no longer connected."
	if len(removed) > 0 {
		text = b.revokeAndDescribe(ctx, removed)
	}
	b.answerCallback(callback.ID, "", false)
	if callback.Message == nil {
		b.reply(owner, text)
		return
	}
	// Replacing the text also removes the picker's buttons. A result too long for
	// one message continues in follow-up messages.
	chunks := SplitMessageText(text, TelegramTextLimit)
	if _, err := b.api.Send(tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, chunks[0])); err != nil {
		applog.Errorf("Failed to update logout picker: %v", err)
		b.reply(callback.Message.Chat.ID, text)
		return
	}
	for _, chunk := range chunks[1:] {
		b.reply(callback.Message.Chat.ID, chunk)
	}
}

// revokeAndDescribe revokes Google tokens for accounts already removed locally
// and returns the reply describing the result.
func (b *Bot) revokeAndDescribe(ctx context.Context, removed []models.GoogleAccount) string {
	var failed []string
	for _, account := range removed {
		if err := b.oauthClient.RevokeToken(ctx, account.RefreshToken); err != nil {
			applog.Warningf("Failed to revoke Google token for account %d: %v", account.ID, err)
			failed = append(failed, account.GmailEmail)
		}
	}
	if len(removed) == 1 {
		if len(failed) > 0 {
			return fmt.Sprintf("Disconnected your Gmail account %s locally; automatic forwarding has stopped. Google token revocation failed. Manually revoke this bot's access at https://myaccount.google.com/connections . Use /login to connect again.", removed[0].GmailEmail)
		}
		return fmt.Sprintf("Disconnected your Gmail account %s. Use /login to connect again.", removed[0].GmailEmail)
	}
	lines := []string{"Disconnected your Gmail accounts. Use /login to connect again."}
	lines = append(lines, accountEmails(removed)...)
	if len(failed) > 0 {
		lines = append(lines, "", "Automatic forwarding has stopped, but Google token revocation failed for the accounts below. Manually revoke this bot's access at https://myaccount.google.com/connections .")
		lines = append(lines, failed...)
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) handleCallback(ctx context.Context, callback *tgbotapi.CallbackQuery) {
	if strings.HasPrefix(callback.Data, LogoutPrefix) {
		b.handleLogoutCallback(ctx, callback)
		return
	}
	if callback.Data == "" || !strings.HasPrefix(callback.Data, ExpandPrefix) {
		return
	}
	gmailMessageID, pageIndex := ParseExpandCallbackData(callback.Data)
	if callback.Message == nil {
		b.answerCallback(callback.ID, "Could not edit this message.", true)
		return
	}
	// The notification being expanded identifies which connected account received the mail.
	account, deliveredID, err := b.database.GetDeliveredMessageAccount(ctx, callback.Message.Chat.ID, callback.Message.MessageID)
	if err != nil {
		applog.Errorf("Failed to load account for expand: %v", err)
		b.answerCallback(callback.ID, "Failed to load the full message.", true)
		return
	}
	if account == nil || account.TelegramUserID != callback.From.ID || deliveredID != gmailMessageID {
		b.answerCallback(callback.ID, "This message is no longer available.", true)
		return
	}

	expanded, err := b.gmail.GetExpandedMessage(ctx, *account, gmailMessageID)
	if err != nil {
		applog.Errorf("Failed to expand Gmail message %s for account %d: %v", gmailMessageID, account.ID, err)
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

// reply sends plain text, split across messages at line breaks when it exceeds
// Telegram's message length limit (for example, /status with many accounts).
func (b *Bot) reply(chatID int64, text string) {
	for _, chunk := range SplitMessageText(text, TelegramTextLimit) {
		if _, err := b.api.Send(tgbotapi.NewMessage(chatID, chunk)); err != nil {
			applog.Errorf("Failed to send Telegram message: %v", err)
			return
		}
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

func accountEmails(accounts []models.GoogleAccount) []string {
	emails := make([]string, len(accounts))
	for i, account := range accounts {
		emails[i] = account.GmailEmail
	}
	return emails
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

// Ensure *oauth.Client satisfies TokenRevoker.
var _ TokenRevoker = (*oauth.Client)(nil)
