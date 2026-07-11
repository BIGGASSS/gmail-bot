# Gmail Telegram Bot

Self-hosted Telegram bot that lets a whitelisted Telegram user connect their Gmail account via Google OAuth and receive notifications for newly arriving inbox mail.

## Features

- Static whitelist of Telegram user IDs
- Per-user Gmail OAuth connection
- Long-polling Telegram bot
- HTTP callback endpoint for Google OAuth
- SQLite persistence for tokens, OAuth state, and delivery dedupe
- Inline `Expand` button to fetch full body text and attachment metadata

## Configuration

Set these environment variables before running:

- `TELEGRAM_BOT_TOKEN`
- `AUTHORIZED_TELEGRAM_USER_IDS`
- `GOOGLE_CLIENT_ID`
- `GOOGLE_CLIENT_SECRET`
- `APP_BASE_URL`
- `DATABASE_PATH` (optional, default `data/gmail_bot.db`)
- `GMAIL_POLL_INTERVAL_SECONDS` (optional, default `45`)
- `WEB_HOST` (optional, default `0.0.0.0`)
- `WEB_PORT` (optional, default `8080`)
- `LOG_LEVEL` (optional, default `INFO`)

Google OAuth redirect URI must be configured as:

```text
{APP_BASE_URL}/oauth/google/callback
```

## Google setup

1. Create a Google Cloud project.
2. Enable the Gmail API.
3. Create an OAuth client credential for a web application.
4. Add `{APP_BASE_URL}/oauth/google/callback` to the authorized redirect URIs.

The bot requests `https://www.googleapis.com/auth/gmail.readonly`.

## Local run

Requires Go 1.19 or newer.

```bash
cp .env.example .env
go run ./cmd/gmail-bot
```

Build a binary:

```bash
go build -o gmail-bot ./cmd/gmail-bot
./gmail-bot
```

## Tests

```bash
go test ./...
```

## Telegram commands

- `/start`
- `/login`
- `/status`
- `/relogin_reminder on|off|days N`
- `/logout`
- `/help`

Unauthorized Telegram users are ignored and logged.
