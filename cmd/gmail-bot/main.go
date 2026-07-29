package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BIGGASSS/gmail-bot/internal/applog"
	"github.com/BIGGASSS/gmail-bot/internal/config"
	"github.com/BIGGASSS/gmail-bot/internal/database"
	"github.com/BIGGASSS/gmail-bot/internal/gmail"
	"github.com/BIGGASSS/gmail-bot/internal/oauth"
	"github.com/BIGGASSS/gmail-bot/internal/telegram"
	"github.com/BIGGASSS/gmail-bot/internal/web"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/sync/errgroup"
)

func main() {
	if err := run(); err != nil {
		if settingsErr, ok := err.(*config.SettingsError); ok {
			fmt.Fprintf(os.Stderr, "Configuration error: %s\n", settingsErr.Error())
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Fatal error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}
	if err := applog.Configure(settings.LogLevel); err != nil {
		return &config.SettingsError{Message: err.Error()}
	}
	// Route telegram-bot-api package logs through applog so LOG_LEVEL applies.
	if err := tgbotapi.SetLogger(applog.TelegramBotLogger{}); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(settings.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Initialize(ctx); err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	oauthClient := oauth.NewClient(settings, httpClient)
	gmailService := gmail.NewService(httpClient, oauthClient, db)

	api, err := tgbotapi.NewBotAPIWithClient(settings.TelegramBotToken, tgbotapi.APIEndpoint, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("create telegram bot: %w", err)
	}
	notifier := telegram.NewNotifier(api)
	bot := telegram.NewBot(settings, api, db, oauthClient, gmailService, notifier)
	poller := telegram.NewGmailPoller(
		telegram.StaticPollerSettings{Interval: time.Duration(settings.GmailPollIntervalSeconds) * time.Second},
		db,
		gmailService,
		notifier,
	)
	server := web.NewServer(
		fmt.Sprintf("%s:%d", settings.WebHost, settings.WebPort),
		db,
		oauthClient,
		gmailService,
		notifier,
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return bot.Run(groupCtx)
	})
	group.Go(func() error {
		return poller.Run(groupCtx)
	})
	group.Go(func() error {
		return server.Run(groupCtx)
	})

	if err := group.Wait(); err != nil && groupCtx.Err() == nil {
		return err
	}
	return nil
}
