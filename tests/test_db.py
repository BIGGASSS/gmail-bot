from __future__ import annotations

from datetime import UTC, timedelta

import pytest

from gmail_bot.db import Database, utcnow


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
