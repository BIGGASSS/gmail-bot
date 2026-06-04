from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest

from gmail_bot.db import Database, utcnow
from gmail_bot.models import DEFAULT_RELOGIN_PROMPT_DELAY_DAYS, MANUAL_RELOGIN_PROMPT_DELAY


@pytest.mark.asyncio
async def test_database_stores_and_consumes_oauth_state(tmp_path) -> None:
    database = Database(tmp_path / "bot.db")
    await database.connect()
    await database.initialize()

    expires_at = utcnow() + timedelta(minutes=5)
    await database.store_oauth_state(state="state-1", telegram_user_id=123, expires_at=expires_at)

    state = await database.consume_oauth_state("state-1")
    assert state is not None
    assert state.telegram_user_id == 123

    second_read = await database.consume_oauth_state("state-1")
    assert second_read is None

    await database.close()


@pytest.mark.asyncio
async def test_database_tracks_delivered_messages(tmp_path) -> None:
    database = Database(tmp_path / "bot.db")
    await database.connect()
    await database.initialize()

    now = utcnow()
    await database.upsert_google_account(
        telegram_user_id=321,
        gmail_email="user@example.com",
        access_token="access-token",
        refresh_token="refresh-token",
        token_expiry=now + timedelta(hours=1),
        last_history_id="100",
        connected_at=now,
    )
    await database.mark_message_delivered(
        telegram_user_id=321,
        gmail_message_id="gmail-message-1",
        telegram_chat_id=321,
        telegram_message_id=10,
    )

    assert await database.was_message_delivered(
        telegram_user_id=321,
        gmail_message_id="gmail-message-1",
    )

    await database.close()


@pytest.mark.asyncio
async def test_database_tracks_manual_relogin_prompt_schedule(tmp_path) -> None:
    database = Database(tmp_path / "bot.db")
    await database.connect()
    await database.initialize()

    connected_at = datetime(2024, 1, 1, 12, 0, 0, tzinfo=UTC)
    await database.upsert_google_account(
        telegram_user_id=654,
        gmail_email="user@example.com",
        access_token="access-token",
        refresh_token="refresh-token",
        token_expiry=connected_at + timedelta(hours=1),
        last_history_id="100",
        connected_at=connected_at,
    )

    account = await database.get_google_account(654)
    assert account is not None
    assert account.relogin_prompt_enabled is True
    assert account.relogin_prompt_delay_days == DEFAULT_RELOGIN_PROMPT_DELAY_DAYS
    assert account.relogin_prompt_base_at == connected_at
    assert account.relogin_prompt_due_at == connected_at + MANUAL_RELOGIN_PROMPT_DELAY
    assert account.relogin_prompt_sent_at is None

    await database.set_relogin_prompt_enabled(telegram_user_id=654, enabled=False)
    account = await database.get_google_account(654)
    assert account is not None
    assert account.relogin_prompt_enabled is False
    preferences = await database.get_relogin_prompt_preferences(654)
    assert preferences.relogin_prompt_enabled is False

    await database.set_relogin_prompt_delay_days(telegram_user_id=654, delay_days=3)
    account = await database.get_google_account(654)
    assert account is not None
    assert account.relogin_prompt_delay_days == 3
    assert account.relogin_prompt_due_at == connected_at + timedelta(days=3)
    preferences = await database.get_relogin_prompt_preferences(654)
    assert preferences.relogin_prompt_delay_days == 3

    sent_at = connected_at + timedelta(days=3, minutes=1)
    await database.mark_relogin_prompt_sent(telegram_user_id=654, sent_at=sent_at)

    account = await database.get_google_account(654)
    assert account is not None
    assert account.relogin_prompt_sent_at == sent_at

    await database.close()
