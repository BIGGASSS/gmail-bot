from __future__ import annotations

import base64
from datetime import UTC, datetime

from gmail_bot.formatting import (
    chunk_text,
    format_expanded_mail,
    html_to_telegram_text,
    normalize_gmail_snippet,
    render_telegram_html,
    strip_html,
)
from gmail_bot.gmail_api import GmailService
from gmail_bot.models import AttachmentMeta, ExpandedMail


def encode_body(value: str) -> str:
    return base64.urlsafe_b64encode(value.encode("utf-8")).decode("ascii").rstrip("=")


def test_strip_html_collapses_markup() -> None:
    assert strip_html("<p>Hello <strong>world</strong></p>") == "Hello world"


def test_strip_html_preserves_link_targets() -> None:
    assert strip_html('<p><a href="https://example.com/deal">Join Pro now</a></p>') == "Join Pro now <https://example.com/deal>"


def test_normalize_gmail_snippet_unescapes_entities() -> None:
    assert normalize_gmail_snippet("Don&#39;t miss out") == "Don't miss out"


def test_render_telegram_html_linkifies_urls() -> None:
    rendered = render_telegram_html("Join Pro now\n<https://example.com/deal?plan=pro&discount=50>")
    assert 'href="https://example.com/deal?plan=pro&amp;discount=50"' in rendered
    assert ">https://example.com/deal?plan=pro&amp;discount=50<" in rendered


def test_render_telegram_html_preserves_anchor_text() -> None:
    rendered = render_telegram_html(
        html_to_telegram_text('<p><a href="https://example.com/deal?plan=pro&discount=50">Join Pro now</a></p>')
    )
    assert 'href="https://example.com/deal?plan=pro&amp;discount=50"' in rendered
    assert ">Join Pro now<" in rendered
    assert "https://example.com/deal?plan=pro&amp;discount=50</a>" not in rendered


def test_chunk_text_splits_long_messages() -> None:
    text = "line\n" * 1500
    chunks = chunk_text(text, limit=1000)
    assert len(chunks) > 1
    assert all(len(chunk) <= 1000 for chunk in chunks)


def test_format_expanded_mail_includes_attachments() -> None:
    mail = ExpandedMail(
        gmail_message_id="gmail-1",
        from_header="sender@example.com",
        subject="Subject",
        received_at=datetime(2026, 4, 8, 12, 0, tzinfo=UTC),
        body_text="Hello from Gmail",
        attachments=(AttachmentMeta(filename="report.pdf", mime_type="application/pdf", size=42),),
    )
    rendered = format_expanded_mail(mail)
    assert "Attachments:" in rendered
    assert "report.pdf" in rendered


def test_extract_body_and_attachments_prefers_plain_text() -> None:
    service = GmailService(http_client=None, oauth_client=None, database=None)  # type: ignore[arg-type]
    body, attachments = service._extract_body_and_attachments(  # noqa: SLF001
        {
            "mimeType": "multipart/mixed",
            "parts": [
                {
                    "mimeType": "multipart/alternative",
                    "parts": [
                        {"mimeType": "text/plain", "body": {"data": encode_body("Plain body")}},
                        {"mimeType": "text/html", "body": {"data": encode_body("<p>HTML body</p>")}},
                    ],
                },
                {
                    "mimeType": "application/pdf",
                    "filename": "report.pdf",
                    "body": {"attachmentId": "attachment-1", "size": 128},
                },
            ],
        }
    )
    assert body == "Plain body"
    assert attachments[0].filename == "report.pdf"


def test_extract_body_and_attachments_keeps_html_anchor_text_clickable() -> None:
    service = GmailService(http_client=None, oauth_client=None, database=None)  # type: ignore[arg-type]
    body, attachments = service._extract_body_and_attachments(  # noqa: SLF001
        {
            "mimeType": "text/html",
            "body": {
                "data": encode_body(
                    '<p><a href="https://example.com/deal?plan=pro&discount=50">Join Pro now</a></p>'
                )
            },
        }
    )
    rendered = render_telegram_html(body)
    assert not attachments
    assert 'href="https://example.com/deal?plan=pro&amp;discount=50"' in rendered
    assert ">Join Pro now<" in rendered

