from __future__ import annotations

import logging
import secrets
import asyncio
from datetime import timedelta

from aiogram import Bot, Dispatcher, F, Router
from aiogram.dispatcher.middlewares.base import BaseMiddleware
from aiogram.exceptions import TelegramAPIError, TelegramForbiddenError
from aiogram.filters import Command
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from gmail_bot.config import Settings
from gmail_bot.db import Database, utcnow
from gmail_bot.formatting import chunk_text, format_expanded_mail, format_mail_notification, render_telegram_html
from gmail_bot.gmail_api import GmailAPIError, GmailHistoryExpiredError, GmailService
from gmail_bot.models import ExpandedMail, IncomingMail
from gmail_bot.oauth import GoogleOAuthClient, OAuthError


logger = logging.getLogger(__name__)

EXPAND_PREFIX = "expand:"


class WhitelistMiddleware(BaseMiddleware):
    def __init__(self, authorized_user_ids: frozenset[int]) -> None:
        self._authorized_user_ids = authorized_user_ids

    async def __call__(self, handler, event, data):
        from_user = getattr(event, "from_user", None)
        if from_user is None:
            return await handler(event, data)

        if from_user.id not in self._authorized_user_ids:
            logger.warning("Ignored update from unauthorized Telegram user %s.", from_user.id)
            return None

        return await handler(event, data)


class TelegramNotifier:
    def __init__(self, bot: Bot) -> None:
        self._bot = bot

    async def send_text(self, chat_id: int, text: str) -> None:
        for chunk in chunk_text(text):
            await self._bot.send_message(chat_id, render_telegram_html(chunk), parse_mode="HTML")

    async def send_login_success(self, chat_id: int, gmail_email: str) -> None:
        await self.send_text(
            chat_id,
            f"Gmail account connected: {gmail_email}\nNew inbox messages will appear here automatically.",
        )

    async def send_login_failure(self, chat_id: int, error: str) -> None:
        await self.send_text(chat_id, f"Gmail login failed: {error}")

    async def send_mail_notification(self, chat_id: int, mail: IncomingMail) -> Message:
        keyboard = InlineKeyboardMarkup(
            inline_keyboard=[
                [InlineKeyboardButton(text="Expand", callback_data=f"{EXPAND_PREFIX}{mail.gmail_message_id}")]
            ]
        )
        return await self._bot.send_message(
            chat_id,
            render_telegram_html(format_mail_notification(mail)),
            parse_mode="HTML",
            reply_markup=keyboard,
        )

    async def send_expanded_mail(self, chat_id: int, mail: ExpandedMail) -> None:
        for chunk in chunk_text(format_expanded_mail(mail)):
            await self._bot.send_message(chat_id, render_telegram_html(chunk), parse_mode="HTML")


def build_dispatcher(
    *,
    settings: Settings,
    database: Database,
    oauth_client: GoogleOAuthClient,
    gmail_service: GmailService,
    notifier: TelegramNotifier,
) -> Dispatcher:
    router = Router()
    router.message.outer_middleware(WhitelistMiddleware(settings.authorized_telegram_user_ids))
    router.callback_query.outer_middleware(WhitelistMiddleware(settings.authorized_telegram_user_ids))

    @router.message(Command("start"))
    async def handle_start(message: Message) -> None:
        if message.from_user is None:
            return

        account = await database.get_google_account(message.from_user.id)
        if account:
            text = (
                "Bot is active.\n"
                f"Connected Gmail account: {account.gmail_email}\n"
                "Use /status for connection details or /logout to disconnect."
            )
        else:
            text = (
                "Bot is active.\n"
                "Use /login to connect Gmail, then new inbox messages will be forwarded here."
            )
        await message.answer(text)

    @router.message(Command("help"))
    async def handle_help(message: Message) -> None:
        await message.answer(
            "\n".join(
                [
                    "Available commands:",
                    "/start - basic status",
                    "/login - connect your Gmail account",
                    "/status - show Gmail connection status",
                    "/logout - disconnect Gmail",
                    "/help - show this help",
                ]
            )
        )

    @router.message(Command("login"))
    async def handle_login(message: Message) -> None:
        if message.from_user is None:
            return

        state = secrets.token_urlsafe(32)
        await database.cleanup_expired_oauth_states()
        await database.store_oauth_state(
            state=state,
            telegram_user_id=message.from_user.id,
            expires_at=utcnow() + timedelta(minutes=15),
        )
        url = oauth_client.build_authorization_url(state=state)
        await message.answer(
            render_telegram_html(
                "\n".join(
                    [
                        "Open this link to connect Gmail:",
                        url,
                        "",
                        "The login link expires in 15 minutes.",
                    ]
                )
            ),
            parse_mode="HTML",
        )

    @router.message(Command("status"))
    async def handle_status(message: Message) -> None:
        if message.from_user is None:
            return

        account = await database.get_google_account(message.from_user.id)
        if account is None:
            await message.answer("No Gmail account is connected. Use /login to connect one.")
            return

        await message.answer(
            "\n".join(
                [
                    f"Connected Gmail account: {account.gmail_email}",
                    f"Polling interval: {settings.gmail_poll_interval_seconds} seconds",
                    f"Watching for new mail after: {account.connected_at.isoformat()}",
                ]
            )
        )

    @router.message(Command("logout"))
    async def handle_logout(message: Message) -> None:
        if message.from_user is None:
            return

        account = await database.get_google_account(message.from_user.id)
        if account is None:
            await message.answer("No Gmail account is currently connected.")
            return

        try:
            await oauth_client.revoke_token(token=account.refresh_token)
        except OAuthError as exc:
            logger.warning("Failed to revoke Google token for %s: %s", account.gmail_email, exc)

        await database.delete_google_account(message.from_user.id)
        await message.answer("Disconnected your Gmail account. Use /login to connect again.")

    @router.callback_query(F.data.startswith(EXPAND_PREFIX))
    async def handle_expand(callback: CallbackQuery) -> None:
        if callback.from_user is None or callback.data is None:
            return

        gmail_message_id = callback.data.removeprefix(EXPAND_PREFIX)
        account = await database.get_google_account(callback.from_user.id)
        if account is None:
            await callback.answer("Connect Gmail first.", show_alert=True)
            return

        was_delivered = await database.was_message_delivered(
            telegram_user_id=callback.from_user.id,
            gmail_message_id=gmail_message_id,
        )
        if not was_delivered:
            await callback.answer("This message is no longer available.", show_alert=True)
            return

        try:
            expanded = await gmail_service.get_expanded_message(account, gmail_message_id)
        except GmailAPIError as exc:
            logger.exception("Failed to expand Gmail message %s for user %s", gmail_message_id, callback.from_user.id)
            await callback.answer("Failed to load the full message.", show_alert=True)
            await notifier.send_text(callback.from_user.id, f"Could not expand the message: {exc}")
            return

        await callback.answer("Loading full message...")
        await notifier.send_expanded_mail(callback.from_user.id, expanded)

    dispatcher = Dispatcher()
    dispatcher.include_router(router)
    return dispatcher


class GmailPoller:
    def __init__(
        self,
        *,
        settings: Settings,
        database: Database,
        gmail_service: GmailService,
        notifier: TelegramNotifier,
    ) -> None:
        self._settings = settings
        self._database = database
        self._gmail_service = gmail_service
        self._notifier = notifier

    async def run(self) -> None:
        while True:
            accounts = await self._database.list_google_accounts()
            for account in accounts:
                try:
                    await self._process_account(account)
                except TelegramForbiddenError:
                    logger.warning("Telegram user %s blocked the bot.", account.telegram_user_id)
                except (GmailAPIError, TelegramAPIError, OAuthError):
                    logger.exception("Failed while polling Gmail for Telegram user %s.", account.telegram_user_id)
            await asyncio.sleep(self._settings.gmail_poll_interval_seconds)

    async def _process_account(self, account) -> None:
        try:
            new_messages, latest_history_id = await self._gmail_service.list_new_inbox_messages(account)
        except GmailHistoryExpiredError as exc:
            await self._database.update_last_history_id(
                telegram_user_id=account.telegram_user_id,
                last_history_id=exc.current_history_id,
            )
            logger.info(
                "Reset Gmail history cursor for user %s to %s.",
                account.telegram_user_id,
                exc.current_history_id,
            )
            return

        if latest_history_id != account.last_history_id:
            await self._database.update_last_history_id(
                telegram_user_id=account.telegram_user_id,
                last_history_id=latest_history_id,
            )

        for mail in new_messages:
            telegram_message = await self._notifier.send_mail_notification(account.telegram_user_id, mail)
            await self._database.mark_message_delivered(
                telegram_user_id=account.telegram_user_id,
                gmail_message_id=mail.gmail_message_id,
                telegram_chat_id=telegram_message.chat.id,
                telegram_message_id=telegram_message.message_id,
            )
