from __future__ import annotations

from datetime import UTC, datetime
from html.parser import HTMLParser
from typing import Iterable

from gmail_bot.models import AttachmentMeta, ExpandedMail, IncomingMail


TELEGRAM_MESSAGE_LIMIT = 4096
SAFE_MESSAGE_CHUNK = 3500


class _HTMLTextExtractor(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self._parts: list[str] = []

    def handle_data(self, data: str) -> None:
        self._parts.append(data)

    def get_text(self) -> str:
        return "".join(self._parts)


def strip_html(value: str) -> str:
    parser = _HTMLTextExtractor()
    parser.feed(value)
    return " ".join(parser.get_text().split())


def format_timestamp(value: datetime) -> str:
    return value.astimezone(UTC).strftime("%Y-%m-%d %H:%M:%S UTC")


def format_mail_notification(mail: IncomingMail) -> str:
    lines = [
        "New Gmail message",
        f"From: {mail.from_header or 'Unknown sender'}",
        f"Subject: {mail.subject or '(no subject)'}",
        f"Received: {format_timestamp(mail.received_at)}",
        "",
        f"Snippet: {mail.snippet or '(no preview available)'}",
    ]
    return "\n".join(lines)


def format_expanded_mail(mail: ExpandedMail) -> str:
    body_text = mail.body_text.strip() or "(no body text available)"
    lines = [
        "Expanded Gmail message",
        f"From: {mail.from_header or 'Unknown sender'}",
        f"Subject: {mail.subject or '(no subject)'}",
        f"Received: {format_timestamp(mail.received_at)}",
        "",
        "Body:",
        body_text,
    ]

    if mail.attachments:
        lines.extend(["", "Attachments:"])
        lines.extend(format_attachment_lines(mail.attachments))

    return "\n".join(lines)


def format_attachment_lines(attachments: Iterable[AttachmentMeta]) -> list[str]:
    lines: list[str] = []
    for attachment in attachments:
        size_suffix = f"{attachment.size} bytes" if attachment.size else "size unknown"
        name = attachment.filename or "(unnamed attachment)"
        lines.append(f"- {name} [{attachment.mime_type or 'application/octet-stream'}, {size_suffix}]")
    return lines


def chunk_text(value: str, *, limit: int = SAFE_MESSAGE_CHUNK) -> list[str]:
    if len(value) <= limit:
        return [value]

    chunks: list[str] = []
    remaining = value
    while remaining:
        if len(remaining) <= limit:
            chunks.append(remaining)
            break

        split_at = remaining.rfind("\n", 0, limit)
        if split_at <= 0:
            split_at = limit

        chunks.append(remaining[:split_at].rstrip())
        remaining = remaining[split_at:].lstrip("\n")

    return chunks

