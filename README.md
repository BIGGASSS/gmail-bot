# Gmail Telegram Bot

Self-hosted Telegram bot that lets a whitelisted Telegram user connect one or more Gmail accounts via Google OAuth and receive notifications for newly arriving inbox mail.

## Features

- Static whitelist of Telegram user IDs
- Multiple Gmail accounts per Telegram user, each connected through Google OAuth
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
- `/login` — connect a Gmail account; repeat to add more
- `/status` — list connected accounts
- `/relogin_reminder on|off|days N` — applies to all of your accounts; each account keeps its own timer
- `/logout` — with several accounts, choose one account or all of them
- `/help`

Unauthorized Telegram users are ignored and logged.

Login links are sent only in private chat. Start a private chat with the bot before using `/login` from a group. The link opens Google's account chooser, so you can connect an account other than the one your browser is signed in to. Each `/login` adds the chosen account; an address you have already connected is rejected and its existing connection is left unchanged. Starting a new login invalidates earlier links, including callbacks already in flight; disconnecting any account also invalidates pending links, even if no account is connected. To refresh an existing Gmail connection, send `/logout` and choose that account, then `/login`. Forwarding from that account stops until the login succeeds; other accounts keep forwarding.

Notifications from all of your accounts arrive in the same chat. The `Expand` button always reads from the account that received the message.

`/logout` removes the local connection even if Google token revocation fails. In that case, the bot asks you to revoke access manually at https://myaccount.google.com/connections. Work already in progress may still complete.

Large mail backlogs are processed across polls without advancing past unfinished history. Expanded message bodies are capped at 50,000 internal-text runes, with a truncation notice; link tokens are never cut in half. Oversized links may be shown as plain text.

Expanded messages prefer clean plain text within each MIME alternative group. If the plain alternative contains leaked Outlook/HTML markup, the bot uses the HTML alternative instead, excluding comments, scripts, styles, clearly hidden content, and Outlook-only fallbacks. Paragraphs, lists, links, and preformatted indentation are preserved; empty padding and excessive blank lines are removed. Independent message sections and intentional repeated text are retained. This is a text conversion, not a browser: external stylesheets and media queries are not evaluated. If no readable HTML alternative exists, the plain text is retained rather than losing the message. HTML exceeding the source-size limits (2 MiB or 4096 tags) is never partially rendered: the bot falls back to another alternative of the same message, such as its plain text, and reports an expansion error only if none is usable.

SQLite database files and existing WAL/shared-memory sidecars must allow owner-only permissions (`0600`); startup fails if these permissions cannot be enforced. Newly created database directories use `0700`. No new environment variables are required.

On first start after upgrading, single-account databases are migrated in place: each existing connection becomes the user's first account and keeps its delivery history. Back up the database file before upgrading.

`auth_versions`, `oauth_attempts`, and `google_accounts.generation` let logout and a newer `/login` reject a callback that already consumed its link, and they keep background token refreshes from writing over a replacement account. `pending_relogs` stays unused so existing databases are not altered destructively. An in-flight `/relog` link is rejected and does not replace credentials; send `/logout`, then `/login`.
