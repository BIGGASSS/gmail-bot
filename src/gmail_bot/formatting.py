from __future__ import annotations

import base64
from datetime import UTC, datetime
from html import escape, unescape
from html.parser import HTMLParser
import re
from typing import Iterable

from gmail_bot.models import AttachmentMeta, ExpandedMail, IncomingMail


TELEGRAM_MESSAGE_LIMIT = 4096
SAFE_MESSAGE_CHUNK = 3500
LINK_TOKEN_START = "\ufff0"
LINK_TOKEN_SEPARATOR = "\ufff1"
LINK_TOKEN_END = "\ufff2"
URL_PATTERN = re.compile(r"<(?P<bracketed>https?://[^<>\s]+)>|(?P<plain>https?://[^\s<>]+)")
LINK_TOKEN_PATTERN = re.compile(
    rf"{LINK_TOKEN_START}(?P<label>[A-Za-z0-9_-]+){LINK_TOKEN_SEPARATOR}(?P<url>[A-Za-z0-9_-]+){LINK_TOKEN_END}"
)
BLOCK_TAGS = {
    "address",
    "article",
    "aside",
    "blockquote",
    "br",
    "div",
    "dd",
    "dl",
    "dt",
    "figcaption",
    "figure",
    "footer",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "header",
    "hr",
    "li",
    "main",
    "ol",
    "p",
    "pre",
    "section",
    "table",
    "td",
    "th",
    "tr",
    "ul",
}


class _HTMLTextExtractor(HTMLParser):
    def __init__(self, *, preserve_anchor_text_links: bool = False) -> None:
        super().__init__()
        self._parts: list[str] = []
        self._anchor_stack: list[tuple[str, int]] = []
        self._preserve_anchor_text_links = preserve_anchor_text_links

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag in BLOCK_TAGS:
            self._parts.append("\n")

        if tag == "a":
            href = dict(attrs).get("href") or ""
            self._anchor_stack.append((href.strip(), len(self._parts)))

    def handle_startendtag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        self.handle_starttag(tag, attrs)
        if tag == "a":
            self.handle_endtag(tag)

    def handle_endtag(self, tag: str) -> None:
        if tag == "a" and self._anchor_stack:
            href, start_index = self._anchor_stack.pop()
            if href:
                link_text = "".join(self._parts[start_index:]).strip()
                if self._preserve_anchor_text_links:
                    self._parts[start_index:] = [_encode_link_token(link_text or href, href)]
                elif not link_text:
                    self._parts.append(href)
                elif link_text != href:
                    self._parts.append(f" <{href}>")

        if tag in BLOCK_TAGS:
            self._parts.append("\n")

    def handle_data(self, data: str) -> None:
        self._parts.append(data)

    def get_text(self) -> str:
        return "".join(self._parts)


def normalize_whitespace(value: str) -> str:
    lines = [" ".join(line.split()) for line in value.splitlines()]
    collapsed_lines = [line for line in lines if line]
    return "\n".join(collapsed_lines)


def normalize_gmail_snippet(value: str) -> str:
    return normalize_whitespace(unescape(value))


def _encode_link_component(value: str) -> str:
    return base64.urlsafe_b64encode(value.encode("utf-8")).decode("ascii").rstrip("=")


def _decode_link_component(value: str) -> str:
    padding = "=" * (-len(value) % 4)
    return base64.urlsafe_b64decode(value + padding).decode("utf-8")


def _encode_link_token(label: str, url: str) -> str:
    return (
        f"{LINK_TOKEN_START}{_encode_link_component(label)}"
        f"{LINK_TOKEN_SEPARATOR}{_encode_link_component(url)}{LINK_TOKEN_END}"
    )


def _render_link(label: str, url: str) -> str:
    safe_url = escape(url, quote=True)
    safe_label = escape(label)
    return f'<a href="{safe_url}">{safe_label}</a>'


def _render_plain_segment(value: str) -> str:
    parts: list[str] = []
    last_index = 0

    for match in URL_PATTERN.finditer(value):
        start, end = match.span()
        parts.append(escape(value[last_index:start]))

        url = match.group("bracketed") or match.group("plain") or ""
        parts.append(_render_link(url, url))
        last_index = end

    parts.append(escape(value[last_index:]))
    return "".join(parts)


def render_telegram_html(value: str) -> str:
    parts: list[str] = []
    last_index = 0

    for match in LINK_TOKEN_PATTERN.finditer(value):
        parts.append(_render_plain_segment(value[last_index:match.start()]))
        parts.append(
            _render_link(
                _decode_link_component(match.group("label")),
                _decode_link_component(match.group("url")),
            )
        )
        last_index = match.end()

    parts.append(_render_plain_segment(value[last_index:]))
    return "".join(parts)


def strip_html(value: str) -> str:
    parser = _HTMLTextExtractor()
    parser.feed(value)
    return normalize_whitespace(parser.get_text())


def html_to_telegram_text(value: str) -> str:
    parser = _HTMLTextExtractor(preserve_anchor_text_links=True)
    parser.feed(value)
    return normalize_whitespace(parser.get_text())


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


def _safe_split_at(value: str, split_at: int) -> int:
    token_start = value.rfind(LINK_TOKEN_START, 0, split_at)
    if token_start == -1:
        return split_at

    token_end = value.rfind(LINK_TOKEN_END, 0, split_at)
    if token_end > token_start:
        return split_at

    return token_start or split_at


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

        split_at = _safe_split_at(remaining, split_at)
        chunks.append(remaining[:split_at].rstrip())
        remaining = remaining[split_at:].lstrip("\n")

    return chunks

