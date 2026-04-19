from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest

from gmail_bot.models import GoogleAccount
from gmail_bot.oauth import OAuthError
from gmail_bot.telegram_bot import GmailPoller


class DummyDatabase:
    def __init__(self) -> None:
        self.deleted_user_ids: list[int] = []

    async def delete_google_account(self, telegram_user_id: int) -> None:
        self.deleted_user_ids.append(telegram_user_id)


class DummyNotifier:
    def __init__(self) -> None:
        self.relogin_requests: list[tuple[int, str]] = []

    async def send_relogin_required(self, chat_id: int, gmail_email: str) -> None:
        self.relogin_requests.append((chat_id, gmail_email))


class DummySettings:
    gmail_poll_interval_seconds = 45


class DummyGmailService:
    pass


@pytest.mark.asyncio
async def test_handle_invalid_grant_notifies_user_and_disconnects_account() -> None:
    database = DummyDatabase()
    notifier = DummyNotifier()
    poller = GmailPoller(
        settings=DummySettings(),
        database=database,  # type: ignore[arg-type]
        gmail_service=DummyGmailService(),  # type: ignore[arg-type]
        notifier=notifier,  # type: ignore[arg-type]
    )
    account = GoogleAccount(
        telegram_user_id=123,
        gmail_email="user@example.com",
        access_token="access-token",
        refresh_token="refresh-token",
        token_expiry=datetime.now(tz=UTC) + timedelta(hours=1),
        last_history_id="100",
        connected_at=datetime.now(tz=UTC),
    )
    error = OAuthError(
        "Google OAuth request failed: 400 invalid_grant",
        status_code=400,
        error="invalid_grant",
        error_description="Token has been expired or revoked.",
    )

    await poller._handle_invalid_grant(account, error)  # noqa: SLF001

    assert notifier.relogin_requests == [(123, "user@example.com")]
    assert database.deleted_user_ids == [123]
