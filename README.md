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
- `/relog`
- `/status`
- `/relogin_reminder on|off|days N`
- `/logout`
- `/help`

Unauthorized Telegram users are ignored and logged.

Login links are sent only in private chat. Start a private chat with the bot before using `/login` or `/relog` from a group. Starting a new `/login` or `/relog` invalidates earlier links (including callbacks already in flight); `/logout` invalidates pending links even if no account is connected.

`/logout` removes the local connection even if Google token revocation fails. In that case, the bot asks you to revoke access manually at https://myaccount.google.com/connections. Work already in progress may still complete.

Large mail backlogs are processed across polls without advancing past unfinished history. Expanded message bodies are capped at 50,000 internal-text runes, with a truncation notice; link tokens are never cut in half. Oversized links may be shown as plain text.

SQLite database files and existing WAL/shared-memory sidecars must allow owner-only permissions (`0600`); startup fails if these permissions cannot be enforced. Newly created database directories use `0700`. No new environment variables are required.

### Refresh authorization without disconnecting

Use `/relog` while Gmail is connected. Its private OAuth link expires after 15 minutes. Choose the **same Gmail account** and grant consent so Google returns a new, nonempty refresh token. Existing authorization and polling remain active until the replacement succeeds; no tokens are revoked by `/relog`. Cancelled, failed, expired, superseded, or wrong-account attempts do not replace credentials. If the old authorization expires during a pending `/relog`, the bot preserves the connection metadata and delivery records until the link's deadline, including while its callback is in flight. Forwarding cannot resume until valid credentials are installed. After the deadline, normal expired-authorization cleanup applies; `/logout` still disconnects immediately.

Success replaces only credentials, preserving the current inbox cursor, original connection date, delivered-message records, and reminder preferences. The reminder timer restarts at successful authorization. `/relog` does not recover mail already skipped before the refresh. If the connection is already gone, use `/login`. Logout invalidates in-flight callbacks; stale token refreshes and stale authorization failures cannot overwrite or delete a newly authorized account.
