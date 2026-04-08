from __future__ import annotations

import logging

from aiogram import Bot
from fastapi import FastAPI, Request
from fastapi.responses import HTMLResponse, JSONResponse

from gmail_bot.db import Database, utcnow
from gmail_bot.gmail_api import GmailService
from gmail_bot.oauth import GoogleOAuthClient, OAuthError
from gmail_bot.telegram_bot import TelegramNotifier


logger = logging.getLogger(__name__)


def create_web_app(
    *,
    database: Database,
    oauth_client: GoogleOAuthClient,
    gmail_service: GmailService,
    bot: Bot,
) -> FastAPI:
    app = FastAPI(title="Gmail Telegram Bot")
    notifier = TelegramNotifier(bot)

    @app.get("/", response_class=HTMLResponse)
    async def root() -> str:
        return "<html><body><p>Gmail Telegram Bot is running.</p></body></html>"

    @app.get("/healthz")
    async def healthz() -> JSONResponse:
        return JSONResponse({"status": "ok"})

    @app.get("/oauth/google/callback", response_class=HTMLResponse)
    async def google_callback(request: Request) -> HTMLResponse:
        params = request.query_params
        error = params.get("error")
        state = params.get("state")
        code = params.get("code")

        if error:
            return HTMLResponse(
                f"<html><body><h1>Google login failed</h1><p>{error}</p></body></html>",
                status_code=400,
            )
        if not state or not code:
            return HTMLResponse(
                "<html><body><h1>Missing OAuth parameters.</h1></body></html>",
                status_code=400,
            )

        oauth_state = await database.consume_oauth_state(state)
        if oauth_state is None:
            return HTMLResponse(
                "<html><body><h1>Login link expired or invalid.</h1></body></html>",
                status_code=400,
            )

        try:
            existing_account = await database.get_google_account(oauth_state.telegram_user_id)
            token_response = await oauth_client.exchange_code(code=code)
            refresh_token = token_response.refresh_token or (
                existing_account.refresh_token if existing_account else None
            )
            if not refresh_token:
                raise OAuthError("Google did not return a refresh token.")

            profile = await gmail_service.get_profile_for_access_token(
                access_token=token_response.access_token
            )
            connected_at = utcnow()
            await database.upsert_google_account(
                telegram_user_id=oauth_state.telegram_user_id,
                gmail_email=profile.email_address,
                access_token=token_response.access_token,
                refresh_token=refresh_token,
                token_expiry=token_response.expires_at,
                last_history_id=profile.history_id,
                connected_at=connected_at,
            )
            await notifier.send_login_success(oauth_state.telegram_user_id, profile.email_address)
        except Exception as exc:
            logger.exception(
                "Failed to complete Gmail OAuth callback for Telegram user %s.",
                oauth_state.telegram_user_id,
            )
            await notifier.send_login_failure(oauth_state.telegram_user_id, str(exc))
            return HTMLResponse(
                "<html><body><h1>Gmail connection failed.</h1><p>Return to Telegram for details.</p></body></html>",
                status_code=500,
            )

        return HTMLResponse(
            "<html><body><h1>Gmail connected.</h1><p>You can return to Telegram.</p></body></html>"
        )

    return app

