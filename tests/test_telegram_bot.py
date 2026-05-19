from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest

from gmail_bot.models import ExpandedMail, GoogleAccount
from gmail_bot.oauth import OAuthError
from gmail_bot.telegram_bot import GmailPoller, TelegramNotifier


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


class FakeChat:
    id = 456


class FakeMessage:
    def __init__(self) -> None:
        self.chat = FakeChat()
        self.edits: list[dict[str, object]] = []

    async def edit_text(self, text: str, **kwargs: object) -> None:
        self.edits.append({"text": text, **kwargs})


class FakeBot:
    def __init__(self) -> None:
        self.sent_messages: list[tuple[int, str, dict[str, object]]] = []

    async def send_message(self, chat_id: int, text: str, **kwargs: object) -> None:
        self.sent_messages.append((chat_id, text, kwargs))


@pytest.mark.asyncio
async def test_edit_expanded_mail_edits_original_message_and_removes_button() -> None:
    bot = FakeBot()
    notifier = TelegramNotifier(bot)  # type: ignore[arg-type]
    message = FakeMessage()
    mail = ExpandedMail(
        gmail_message_id="gmail-message-1",
        from_header="sender@example.com",
        subject="Hello <there>",
        received_at=datetime(2024, 1, 2, 3, 4, 5, tzinfo=UTC),
        body_text="Full body",
        attachments=(),
    )

    await notifier.edit_expanded_mail(message, mail)  # type: ignore[arg-type]

    assert len(message.edits) == 1
    edit = message.edits[0]
    assert "Expanded Gmail message" in edit["text"]
    assert "Subject: Hello &lt;there&gt;" in edit["text"]
    assert edit["parse_mode"] == "HTML"
    assert edit["reply_markup"] is None
    assert bot.sent_messages == []


@pytest.mark.asyncio
async def test_edit_expanded_mail_sends_remaining_chunks_as_continuations() -> None:
    bot = FakeBot()
    notifier = TelegramNotifier(bot)  # type: ignore[arg-type]
    message = FakeMessage()
    mail = ExpandedMail(
        gmail_message_id="gmail-message-1",
        from_header="sender@example.com",
        subject="Long body",
        received_at=datetime(2024, 1, 2, 3, 4, 5, tzinfo=UTC),
        body_text="Line\n" * 1000,
        attachments=(),
    )

    await notifier.edit_expanded_mail(message, mail)  # type: ignore[arg-type]

    assert len(message.edits) == 1
    assert bot.sent_messages
    assert bot.sent_messages[0][0] == 456
    assert bot.sent_messages[0][2]["parse_mode"] == "HTML"


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
