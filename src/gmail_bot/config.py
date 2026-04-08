from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

from dotenv import load_dotenv


class SettingsError(RuntimeError):
    """Raised when required configuration is missing or invalid."""


def parse_authorized_user_ids(raw_value: str) -> frozenset[int]:
    entries = [part.strip() for part in raw_value.split(",") if part.strip()]
    if not entries:
        raise SettingsError("AUTHORIZED_TELEGRAM_USER_IDS must contain at least one user id.")

    parsed_ids: set[int] = set()
    for entry in entries:
        try:
            parsed_ids.add(int(entry))
        except ValueError as exc:
            raise SettingsError(
                f"AUTHORIZED_TELEGRAM_USER_IDS contains an invalid integer value: {entry!r}"
            ) from exc

    return frozenset(parsed_ids)


@dataclass(frozen=True, slots=True)
class Settings:
    telegram_bot_token: str
    authorized_telegram_user_ids: frozenset[int]
    google_client_id: str
    google_client_secret: str
    app_base_url: str
    database_path: Path
    gmail_poll_interval_seconds: int
    web_host: str
    web_port: int
    log_level: str

    @property
    def google_redirect_uri(self) -> str:
        return f"{self.app_base_url}/oauth/google/callback"


def _get_required_env(name: str) -> str:
    value = os.getenv(name, "").strip()
    if not value:
        raise SettingsError(f"{name} is required.")
    return value


def load_settings() -> Settings:
    load_dotenv()
    app_base_url = _get_required_env("APP_BASE_URL").rstrip("/")
    try:
        poll_interval = int(os.getenv("GMAIL_POLL_INTERVAL_SECONDS", "45"))
    except ValueError as exc:
        raise SettingsError("GMAIL_POLL_INTERVAL_SECONDS must be an integer.") from exc
    if poll_interval <= 0:
        raise SettingsError("GMAIL_POLL_INTERVAL_SECONDS must be greater than zero.")

    try:
        web_port = int(os.getenv("WEB_PORT", "8080"))
    except ValueError as exc:
        raise SettingsError("WEB_PORT must be an integer.") from exc
    if web_port <= 0:
        raise SettingsError("WEB_PORT must be greater than zero.")

    database_path = Path(os.getenv("DATABASE_PATH", "data/gmail_bot.db"))

    return Settings(
        telegram_bot_token=_get_required_env("TELEGRAM_BOT_TOKEN"),
        authorized_telegram_user_ids=parse_authorized_user_ids(
            _get_required_env("AUTHORIZED_TELEGRAM_USER_IDS")
        ),
        google_client_id=_get_required_env("GOOGLE_CLIENT_ID"),
        google_client_secret=_get_required_env("GOOGLE_CLIENT_SECRET"),
        app_base_url=app_base_url,
        database_path=database_path,
        gmail_poll_interval_seconds=poll_interval,
        web_host=os.getenv("WEB_HOST", "0.0.0.0"),
        web_port=web_port,
        log_level=os.getenv("LOG_LEVEL", "INFO"),
    )
