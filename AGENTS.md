# Repository Guidelines

## Project Structure & Module Organization
Application code lives in `src/gmail_bot/`. Keep service boundaries clear: `config.py` loads environment settings, `db.py` handles SQLite persistence, `gmail_api.py` and `oauth.py` wrap Google APIs, `telegram_bot.py` manages bot behavior and polling, and `web.py` exposes the FastAPI OAuth callback. The entrypoint is `main.py`. Tests live in `tests/` and currently cover config parsing, database behavior, and message formatting. Repository-level assets are minimal: `README.md`, `pyproject.toml`, and `.env.example`.

## Build, Test, and Development Commands
Use `uv` for local development.

- `uv sync --extra dev`: install runtime and dev dependencies into the local environment.
- `uv run gmail-bot`: start the Telegram bot and FastAPI callback server with the configured environment.
- `uv run pytest`: run the full test suite.

## Coding Style & Naming Conventions
Target Python 3.12 and follow the existing style: 4-space indentation, explicit type hints, `dataclass` models where appropriate, and small focused functions. Use `snake_case` for modules, functions, and variables, `CapWords` for classes, and uppercase names for environment variables. Prefer standard-library and existing dependencies over adding new packages. No formatter or linter is configured yet, so keep changes consistent with surrounding code and imports grouped cleanly.

## Testing Guidelines
Tests use `pytest` with `pytest-asyncio` in auto mode. Name files `test_*.py` and test functions `test_*`. Add unit tests beside the behavior you change; async database tests should use `@pytest.mark.asyncio` and `tmp_path` for isolated SQLite files. Keep tests deterministic and avoid live Telegram or Google API calls.

## Commit & Pull Request Guidelines
The current history uses short, imperative commit subjects like `Implement Gmail to Telegram bot`. Follow that pattern and keep each commit focused. Pull requests should describe the user-visible behavior change, list any new environment variables or OAuth setup changes, and include the commands you ran to verify the work. Add screenshots or Telegram message samples when changing notification formatting or bot interactions.

## Security & Configuration Tips
Do not commit real OAuth credentials, bot tokens, or populated database files. Use `.env.example` as the baseline for local configuration, and confirm `APP_BASE_URL` matches the Google OAuth redirect URI before testing login flows.
