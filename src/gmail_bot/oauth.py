from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from urllib.parse import urlencode

import httpx

from gmail_bot.config import Settings


GOOGLE_AUTH_URL = "https://accounts.google.com/o/oauth2/v2/auth"
GOOGLE_TOKEN_URL = "https://oauth2.googleapis.com/token"
GOOGLE_REVOKE_URL = "https://oauth2.googleapis.com/revoke"
GMAIL_SCOPE = "https://www.googleapis.com/auth/gmail.readonly"


@dataclass(slots=True, frozen=True)
class TokenResponse:
    access_token: str
    refresh_token: str | None
    expires_at: datetime


class OAuthError(RuntimeError):
    """Raised when OAuth operations fail."""

    def __init__(
        self,
        message: str,
        *,
        status_code: int | None = None,
        error: str | None = None,
        error_description: str | None = None,
    ) -> None:
        super().__init__(message)
        self.status_code = status_code
        self.error = error
        self.error_description = error_description

    @property
    def is_invalid_grant(self) -> bool:
        return self.error == "invalid_grant"


class GoogleOAuthClient:
    def __init__(self, settings: Settings, http_client: httpx.AsyncClient) -> None:
        self._settings = settings
        self._http_client = http_client

    def build_authorization_url(self, *, state: str) -> str:
        query = urlencode(
            {
                "client_id": self._settings.google_client_id,
                "redirect_uri": self._settings.google_redirect_uri,
                "response_type": "code",
                "scope": GMAIL_SCOPE,
                "access_type": "offline",
                "include_granted_scopes": "true",
                "prompt": "consent",
                "state": state,
            }
        )
        return f"{GOOGLE_AUTH_URL}?{query}"

    async def exchange_code(self, *, code: str) -> TokenResponse:
        response = await self._http_client.post(
            GOOGLE_TOKEN_URL,
            data={
                "client_id": self._settings.google_client_id,
                "client_secret": self._settings.google_client_secret,
                "code": code,
                "grant_type": "authorization_code",
                "redirect_uri": self._settings.google_redirect_uri,
            },
            headers={"Accept": "application/json"},
        )
        payload = await self._decode_json_response(response)
        return self._token_response_from_payload(payload)

    async def refresh_access_token(self, *, refresh_token: str) -> TokenResponse:
        response = await self._http_client.post(
            GOOGLE_TOKEN_URL,
            data={
                "client_id": self._settings.google_client_id,
                "client_secret": self._settings.google_client_secret,
                "refresh_token": refresh_token,
                "grant_type": "refresh_token",
            },
            headers={"Accept": "application/json"},
        )
        payload = await self._decode_json_response(response)
        return self._token_response_from_payload(payload)

    async def revoke_token(self, *, token: str) -> None:
        response = await self._http_client.post(
            GOOGLE_REVOKE_URL,
            params={"token": token},
            headers={"Content-Type": "application/x-www-form-urlencoded"},
        )
        if response.status_code not in {200, 400}:
            message = response.text.strip() or response.reason_phrase
            raise OAuthError(f"Google token revocation failed: {response.status_code} {message}")

    async def _decode_json_response(self, response: httpx.Response) -> dict:
        if response.status_code >= 400:
            try:
                payload = response.json()
            except ValueError:
                payload = None

            message = response.text.strip() or response.reason_phrase
            raise OAuthError(
                f"Google OAuth request failed: {response.status_code} {message}",
                status_code=response.status_code,
                error=payload.get("error") if isinstance(payload, dict) else None,
                error_description=payload.get("error_description") if isinstance(payload, dict) else None,
            )
        payload = response.json()
        if "access_token" not in payload:
            raise OAuthError("Google OAuth response did not include an access token.")
        return payload

    def _token_response_from_payload(self, payload: dict) -> TokenResponse:
        expires_in = int(payload.get("expires_in", 3600))
        expires_at = datetime.now(tz=UTC) + timedelta(seconds=max(expires_in - 60, 60))
        return TokenResponse(
            access_token=payload["access_token"],
            refresh_token=payload.get("refresh_token"),
            expires_at=expires_at,
        )

