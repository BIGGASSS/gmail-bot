from __future__ import annotations

import base64
import logging
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any

import httpx

from gmail_bot.db import Database
from gmail_bot.formatting import strip_html
from gmail_bot.models import AttachmentMeta, ExpandedMail, GoogleAccount, IncomingMail
from gmail_bot.oauth import GoogleOAuthClient


logger = logging.getLogger(__name__)

GMAIL_API_BASE = "https://gmail.googleapis.com/gmail/v1/users/me"


@dataclass(slots=True, frozen=True)
class GmailProfile:
    email_address: str
    history_id: str


class GmailAPIError(RuntimeError):
    def __init__(self, message: str, *, status_code: int | None = None) -> None:
        super().__init__(message)
        self.status_code = status_code


class GmailHistoryExpiredError(GmailAPIError):
    def __init__(self, current_history_id: str) -> None:
        super().__init__("Stored Gmail history cursor is no longer valid.", status_code=404)
        self.current_history_id = current_history_id


class GmailService:
    def __init__(
        self,
        *,
        http_client: httpx.AsyncClient,
        oauth_client: GoogleOAuthClient,
        database: Database,
    ) -> None:
        self._http_client = http_client
        self._oauth_client = oauth_client
        self._database = database

    async def get_profile_for_access_token(self, *, access_token: str) -> GmailProfile:
        payload = await self._raw_json_request(
            "GET",
            f"{GMAIL_API_BASE}/profile",
            access_token=access_token,
        )
        return GmailProfile(
            email_address=payload["emailAddress"],
            history_id=str(payload["historyId"]),
        )

    async def list_new_inbox_messages(self, account: GoogleAccount) -> tuple[list[IncomingMail], str]:
        latest_history_id = account.last_history_id
        page_token: str | None = None
        message_ids: set[str] = set()

        while True:
            try:
                payload = await self._request_json(
                    account,
                    "GET",
                    f"{GMAIL_API_BASE}/history",
                    params={
                        "startHistoryId": account.last_history_id,
                        "historyTypes": "messageAdded",
                        "maxResults": "100",
                        **({"pageToken": page_token} if page_token else {}),
                    },
                )
            except GmailAPIError as exc:
                if exc.status_code == 404:
                    profile = await self.get_profile(account)
                    raise GmailHistoryExpiredError(profile.history_id) from exc
                raise

            latest_history_id = str(payload.get("historyId", latest_history_id))

            for history_entry in payload.get("history", []):
                latest_history_id = str(history_entry.get("id", latest_history_id))
                for item in history_entry.get("messagesAdded", []):
                    message_id = item.get("message", {}).get("id")
                    if message_id:
                        message_ids.add(message_id)

            page_token = payload.get("nextPageToken")
            if not page_token:
                break

        incoming_messages: list[IncomingMail] = []
        for message_id in message_ids:
            if await self._database.was_message_delivered(
                telegram_user_id=account.telegram_user_id,
                gmail_message_id=message_id,
            ):
                continue

            summary = await self.get_message_summary(account, message_id)
            if summary is None:
                continue
            if summary.received_at <= account.connected_at:
                continue
            incoming_messages.append(summary)

        incoming_messages.sort(key=lambda item: item.received_at)
        return incoming_messages, latest_history_id

    async def get_profile(self, account: GoogleAccount) -> GmailProfile:
        payload = await self._request_json(account, "GET", f"{GMAIL_API_BASE}/profile")
        return GmailProfile(
            email_address=payload["emailAddress"],
            history_id=str(payload["historyId"]),
        )

    async def get_message_summary(self, account: GoogleAccount, message_id: str) -> IncomingMail | None:
        payload = await self._request_json(
            account,
            "GET",
            f"{GMAIL_API_BASE}/messages/{message_id}",
            params={
                "format": "metadata",
                "metadataHeaders": ["From", "Subject", "Date"],
            },
        )
        label_ids = set(payload.get("labelIds", []))
        if "INBOX" not in label_ids:
            return None

        headers = payload.get("payload", {}).get("headers", [])
        return IncomingMail(
            gmail_message_id=payload["id"],
            from_header=self._header_value(headers, "From") or "Unknown sender",
            subject=self._header_value(headers, "Subject") or "(no subject)",
            snippet=payload.get("snippet", "") or "(no preview available)",
            received_at=self._internal_date_to_datetime(payload.get("internalDate")),
        )

    async def get_expanded_message(self, account: GoogleAccount, message_id: str) -> ExpandedMail:
        payload = await self._request_json(
            account,
            "GET",
            f"{GMAIL_API_BASE}/messages/{message_id}",
            params={"format": "full"},
        )
        headers = payload.get("payload", {}).get("headers", [])
        body_text, attachments = self._extract_body_and_attachments(payload.get("payload", {}))

        return ExpandedMail(
            gmail_message_id=payload["id"],
            from_header=self._header_value(headers, "From") or "Unknown sender",
            subject=self._header_value(headers, "Subject") or "(no subject)",
            received_at=self._internal_date_to_datetime(payload.get("internalDate")),
            body_text=body_text or "(no body text available)",
            attachments=tuple(attachments),
        )

    async def _request_json(
        self,
        account: GoogleAccount,
        method: str,
        url: str,
        *,
        params: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        current_account = await self._ensure_valid_access_token(account)
        response = await self._http_client.request(
            method,
            url,
            params=params,
            headers={"Authorization": f"Bearer {current_account.access_token}"},
        )
        if response.status_code == 401:
            refreshed_account = await self._force_refresh(current_account)
            response = await self._http_client.request(
                method,
                url,
                params=params,
                headers={"Authorization": f"Bearer {refreshed_account.access_token}"},
            )

        return self._decode_json_response(response)

    async def _raw_json_request(
        self,
        method: str,
        url: str,
        *,
        access_token: str,
        params: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        response = await self._http_client.request(
            method,
            url,
            params=params,
            headers={"Authorization": f"Bearer {access_token}"},
        )
        return self._decode_json_response(response)

    async def _ensure_valid_access_token(self, account: GoogleAccount) -> GoogleAccount:
        if account.token_expiry > datetime.now(tz=UTC):
            return account
        return await self._force_refresh(account)

    async def _force_refresh(self, account: GoogleAccount) -> GoogleAccount:
        tokens = await self._oauth_client.refresh_access_token(refresh_token=account.refresh_token)
        refresh_token = tokens.refresh_token or account.refresh_token
        await self._database.update_tokens(
            telegram_user_id=account.telegram_user_id,
            access_token=tokens.access_token,
            refresh_token=refresh_token,
            token_expiry=tokens.expires_at,
        )
        return GoogleAccount(
            telegram_user_id=account.telegram_user_id,
            gmail_email=account.gmail_email,
            access_token=tokens.access_token,
            refresh_token=refresh_token,
            token_expiry=tokens.expires_at,
            last_history_id=account.last_history_id,
            connected_at=account.connected_at,
        )

    def _decode_json_response(self, response: httpx.Response) -> dict[str, Any]:
        if response.status_code >= 400:
            message = response.text.strip() or response.reason_phrase
            raise GmailAPIError(
                f"Gmail API request failed: {response.status_code} {message}",
                status_code=response.status_code,
            )
        return response.json()

    def _header_value(self, headers: list[dict[str, Any]], name: str) -> str | None:
        target = name.casefold()
        for header in headers:
            if header.get("name", "").casefold() == target:
                return header.get("value")
        return None

    def _internal_date_to_datetime(self, internal_date: str | int | None) -> datetime:
        timestamp_ms = int(internal_date or 0)
        return datetime.fromtimestamp(timestamp_ms / 1000, tz=UTC)

    def _extract_body_and_attachments(self, payload: dict[str, Any]) -> tuple[str, list[AttachmentMeta]]:
        plain_parts: list[str] = []
        html_parts: list[str] = []
        attachments: list[AttachmentMeta] = []

        def visit(part: dict[str, Any]) -> None:
            mime_type = part.get("mimeType", "")
            filename = part.get("filename", "") or ""
            body = part.get("body", {}) or {}
            data = body.get("data")

            if filename or body.get("attachmentId"):
                attachments.append(
                    AttachmentMeta(
                        filename=filename or "(unnamed attachment)",
                        mime_type=mime_type or "application/octet-stream",
                        size=int(body.get("size") or 0),
                    )
                )
                return

            if mime_type == "text/plain" and data:
                plain_parts.append(self._decode_body_data(data))
            elif mime_type == "text/html" and data:
                html_parts.append(strip_html(self._decode_body_data(data)))

            for child in part.get("parts", []) or []:
                visit(child)

        visit(payload)

        body_text = "\n\n".join(part.strip() for part in plain_parts if part.strip())
        if not body_text:
            body_text = "\n\n".join(part.strip() for part in html_parts if part.strip())
        return body_text, attachments

    def _decode_body_data(self, data: str) -> str:
        padding = "=" * (-len(data) % 4)
        raw = base64.urlsafe_b64decode(data + padding)
        try:
            return raw.decode("utf-8")
        except UnicodeDecodeError:
            logger.warning("Falling back to latin-1 while decoding Gmail message body.")
            return raw.decode("latin-1", errors="replace")
