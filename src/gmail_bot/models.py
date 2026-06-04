from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta


MANUAL_RELOGIN_PROMPT_DELAY = timedelta(days=6)


@dataclass(slots=True, frozen=True)
class OAuthState:
    state: str
    telegram_user_id: int
    created_at: datetime
    expires_at: datetime


@dataclass(slots=True, frozen=True)
class GoogleAccount:
    telegram_user_id: int
    gmail_email: str
    access_token: str
    refresh_token: str
    token_expiry: datetime
    last_history_id: str
    connected_at: datetime
    relogin_prompt_due_at: datetime | None = None
    relogin_prompt_sent_at: datetime | None = None


@dataclass(slots=True, frozen=True)
class AttachmentMeta:
    filename: str
    mime_type: str
    size: int


@dataclass(slots=True, frozen=True)
class IncomingMail:
    gmail_message_id: str
    from_header: str
    subject: str
    snippet: str
    received_at: datetime


@dataclass(slots=True, frozen=True)
class ExpandedMail:
    gmail_message_id: str
    from_header: str
    subject: str
    received_at: datetime
    body_text: str
    attachments: tuple[AttachmentMeta, ...]
