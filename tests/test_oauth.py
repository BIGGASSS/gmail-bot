from __future__ import annotations

from pathlib import Path

import httpx
import pytest

from gmail_bot.config import Settings
from gmail_bot.oauth import GoogleOAuthClient, OAuthError


@pytest.mark.asyncio
async def test_decode_json_response_exposes_invalid_grant_details() -> None:
    settings = Settings(
        telegram_bot_token="token",
        authorized_telegram_user_ids=frozenset({1}),
        google_client_id="client-id",
        google_client_secret="client-secret",
        app_base_url="https://example.com",
        database_path=Path("bot.db"),
        gmail_poll_interval_seconds=45,
        web_host="127.0.0.1",
        web_port=8080,
        log_level="INFO",
    )
    client = GoogleOAuthClient(settings, http_client=None)  # type: ignore[arg-type]
    response = httpx.Response(
        400,
        json={
            "error": "invalid_grant",
            "error_description": "Token has been expired or revoked.",
        },
    )

    with pytest.raises(OAuthError) as exc_info:
        await client._decode_json_response(response)  # noqa: SLF001

    error = exc_info.value
    assert error.status_code == 400
    assert error.error == "invalid_grant"
    assert error.error_description == "Token has been expired or revoked."
    assert error.is_invalid_grant is True
