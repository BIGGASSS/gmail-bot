# Repository Guidelines

## Project Structure & Module Organization
Application code lives under `internal/` with focused packages: `config` loads environment settings, `database` handles SQLite persistence, `gmail` and `oauth` wrap Google APIs, `telegram` manages bot behavior and Gmail polling, `formatting` renders Telegram HTML, and `web` exposes the OAuth callback server. Domain models live in `internal/models`. The executable entrypoint is `cmd/gmail-bot/main.go`. Package tests live beside the code they cover (`*_test.go`). Repository-level assets are minimal: `README.md`, `go.mod`, `go.sum`, and `.env.example`.

## Build, Test, and Development Commands
Use the Go toolchain for local development.

- `go test ./...`: run the full test suite.
- `go test -race ./...`: run tests with the race detector.
- `go run ./cmd/gmail-bot`: start the Telegram bot and HTTP callback server with the configured environment.
- `go build -o gmail-bot ./cmd/gmail-bot`: build a local binary.

## Coding Style & Naming Conventions
Target the current stable Go toolchain and follow the existing style: `gofmt`, explicit types, small focused functions, and package-level docs where helpful. Use `MixedCaps` for exported names, `mixedCaps` for unexported names, and uppercase names for environment variables. Prefer the standard library and existing dependencies over adding new packages. Keep changes consistent with surrounding code and imports grouped cleanly.

## Testing Guidelines
Tests use Go's `testing` package. Name files `*_test.go` and functions `Test*`. Add unit tests beside the behavior you change; database tests should use `t.TempDir()` for isolated SQLite files. Keep tests deterministic and avoid live Telegram or Google API calls. Prefer interfaces or `httptest` fakes for external services.

## Commit & Pull Request Guidelines
The current history uses short, imperative commit subjects like `Implement Gmail to Telegram bot`. Follow that pattern and keep each commit focused. Pull requests should describe the user-visible behavior change, list any new environment variables or OAuth setup changes, and include the commands you ran to verify the work. Add screenshots or Telegram message samples when changing notification formatting or bot interactions.

## Security & Configuration Tips
Do not commit real OAuth credentials, bot tokens, or populated database files. Use `.env.example` as the baseline for local configuration, and confirm `APP_BASE_URL` matches the Google OAuth redirect URI before testing login flows.
