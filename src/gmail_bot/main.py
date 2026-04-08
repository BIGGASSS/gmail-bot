from __future__ import annotations

import asyncio
import logging
import sys

import httpx
import uvicorn
from aiogram import Bot

from gmail_bot.config import SettingsError, load_settings
from gmail_bot.db import Database
from gmail_bot.gmail_api import GmailService
from gmail_bot.oauth import GoogleOAuthClient
from gmail_bot.telegram_bot import GmailPoller, TelegramNotifier, build_dispatcher
from gmail_bot.web import create_web_app


def configure_logging(level: str) -> None:
    logging.basicConfig(
        level=level.upper(),
        format="%(asctime)s %(levelname)s [%(name)s] %(message)s",
    )


async def async_main() -> None:
    settings = load_settings()
    configure_logging(settings.log_level)

    database = Database(settings.database_path)
    await database.connect()
    await database.initialize()

    async with httpx.AsyncClient(timeout=30.0) as http_client:
        oauth_client = GoogleOAuthClient(settings, http_client)
        gmail_service = GmailService(
            http_client=http_client,
            oauth_client=oauth_client,
            database=database,
        )
        bot = Bot(settings.telegram_bot_token)
        notifier = TelegramNotifier(bot)
        dispatcher = build_dispatcher(
            settings=settings,
            database=database,
            oauth_client=oauth_client,
            gmail_service=gmail_service,
            notifier=notifier,
        )
        poller = GmailPoller(
            settings=settings,
            database=database,
            gmail_service=gmail_service,
            notifier=notifier,
        )
        web_app = create_web_app(
            database=database,
            oauth_client=oauth_client,
            gmail_service=gmail_service,
            bot=bot,
        )
        server = uvicorn.Server(
            uvicorn.Config(
                app=web_app,
                host=settings.web_host,
                port=settings.web_port,
                log_config=None,
                access_log=False,
            )
        )

        try:
            async with asyncio.TaskGroup() as task_group:
                task_group.create_task(dispatcher.start_polling(bot, handle_signals=False))
                task_group.create_task(poller.run())
                task_group.create_task(server.serve())
        finally:
            await bot.session.close()
            await database.close()


def run() -> None:
    try:
        asyncio.run(async_main())
    except SettingsError as exc:
        print(f"Configuration error: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    run()
