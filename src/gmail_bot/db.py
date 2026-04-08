from __future__ import annotations

from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import aiosqlite

from gmail_bot.models import GoogleAccount, OAuthState


def utcnow() -> datetime:
    return datetime.now(tz=UTC)


def to_iso8601(value: datetime) -> str:
    return value.astimezone(UTC).isoformat()


def from_iso8601(value: str) -> datetime:
    return datetime.fromisoformat(value).astimezone(UTC)


class Database:
    def __init__(self, database_path: Path) -> None:
        self._database_path = database_path
        self._connection: aiosqlite.Connection | None = None

    async def connect(self) -> None:
        self._database_path.parent.mkdir(parents=True, exist_ok=True)
        self._connection = await aiosqlite.connect(self._database_path)
        self._connection.row_factory = aiosqlite.Row
        await self._connection.execute("PRAGMA journal_mode=WAL;")
        await self._connection.execute("PRAGMA foreign_keys=ON;")
        await self._connection.commit()

    async def close(self) -> None:
        if self._connection is not None:
            await self._connection.close()
            self._connection = None

    @property
    def connection(self) -> aiosqlite.Connection:
        if self._connection is None:
            raise RuntimeError("Database connection has not been initialized.")
        return self._connection

    async def initialize(self) -> None:
        await self.connection.executescript(
            """
            CREATE TABLE IF NOT EXISTS oauth_states (
                state TEXT PRIMARY KEY,
                telegram_user_id INTEGER NOT NULL,
                created_at TEXT NOT NULL,
                expires_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS google_accounts (
                telegram_user_id INTEGER PRIMARY KEY,
                gmail_email TEXT NOT NULL,
                access_token TEXT NOT NULL,
                refresh_token TEXT NOT NULL,
                token_expiry TEXT NOT NULL,
                last_history_id TEXT NOT NULL,
                connected_at TEXT NOT NULL,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS delivered_messages (
                telegram_user_id INTEGER NOT NULL,
                gmail_message_id TEXT NOT NULL,
                telegram_chat_id INTEGER NOT NULL,
                telegram_message_id INTEGER NOT NULL,
                delivered_at TEXT NOT NULL,
                PRIMARY KEY (telegram_user_id, gmail_message_id),
                FOREIGN KEY (telegram_user_id) REFERENCES google_accounts (telegram_user_id) ON DELETE CASCADE
            );
            """
        )
        await self.connection.commit()

    async def store_oauth_state(
        self,
        *,
        state: str,
        telegram_user_id: int,
        expires_at: datetime,
    ) -> None:
        now = utcnow()
        await self.connection.execute(
            """
            INSERT INTO oauth_states (state, telegram_user_id, created_at, expires_at)
            VALUES (?, ?, ?, ?)
            ON CONFLICT(state) DO UPDATE SET
                telegram_user_id = excluded.telegram_user_id,
                created_at = excluded.created_at,
                expires_at = excluded.expires_at
            """,
            (state, telegram_user_id, to_iso8601(now), to_iso8601(expires_at)),
        )
        await self.connection.commit()

    async def consume_oauth_state(self, state: str) -> OAuthState | None:
        cursor = await self.connection.execute(
            "SELECT state, telegram_user_id, created_at, expires_at FROM oauth_states WHERE state = ?",
            (state,),
        )
        row = await cursor.fetchone()
        await cursor.close()
        if row is None:
            return None

        await self.connection.execute("DELETE FROM oauth_states WHERE state = ?", (state,))
        await self.connection.commit()

        oauth_state = self._oauth_state_from_row(row)
        if oauth_state.expires_at <= utcnow():
            return None
        return oauth_state

    async def cleanup_expired_oauth_states(self) -> None:
        await self.connection.execute(
            "DELETE FROM oauth_states WHERE expires_at <= ?",
            (to_iso8601(utcnow()),),
        )
        await self.connection.commit()

    async def upsert_google_account(
        self,
        *,
        telegram_user_id: int,
        gmail_email: str,
        access_token: str,
        refresh_token: str,
        token_expiry: datetime,
        last_history_id: str,
        connected_at: datetime,
    ) -> None:
        now = to_iso8601(utcnow())
        await self.connection.execute(
            """
            INSERT INTO google_accounts (
                telegram_user_id,
                gmail_email,
                access_token,
                refresh_token,
                token_expiry,
                last_history_id,
                connected_at,
                created_at,
                updated_at
            )
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(telegram_user_id) DO UPDATE SET
                gmail_email = excluded.gmail_email,
                access_token = excluded.access_token,
                refresh_token = excluded.refresh_token,
                token_expiry = excluded.token_expiry,
                last_history_id = excluded.last_history_id,
                connected_at = excluded.connected_at,
                updated_at = excluded.updated_at
            """,
            (
                telegram_user_id,
                gmail_email,
                access_token,
                refresh_token,
                to_iso8601(token_expiry),
                last_history_id,
                to_iso8601(connected_at),
                now,
                now,
            ),
        )
        await self.connection.commit()

    async def get_google_account(self, telegram_user_id: int) -> GoogleAccount | None:
        cursor = await self.connection.execute(
            """
            SELECT telegram_user_id, gmail_email, access_token, refresh_token, token_expiry, last_history_id, connected_at
            FROM google_accounts
            WHERE telegram_user_id = ?
            """,
            (telegram_user_id,),
        )
        row = await cursor.fetchone()
        await cursor.close()
        return self._account_from_row(row) if row else None

    async def list_google_accounts(self) -> list[GoogleAccount]:
        cursor = await self.connection.execute(
            """
            SELECT telegram_user_id, gmail_email, access_token, refresh_token, token_expiry, last_history_id, connected_at
            FROM google_accounts
            ORDER BY telegram_user_id ASC
            """
        )
        rows = await cursor.fetchall()
        await cursor.close()
        return [self._account_from_row(row) for row in rows]

    async def update_tokens(
        self,
        *,
        telegram_user_id: int,
        access_token: str,
        token_expiry: datetime,
        refresh_token: str | None = None,
    ) -> None:
        if refresh_token is None:
            await self.connection.execute(
                """
                UPDATE google_accounts
                SET access_token = ?, token_expiry = ?, updated_at = ?
                WHERE telegram_user_id = ?
                """,
                (
                    access_token,
                    to_iso8601(token_expiry),
                    to_iso8601(utcnow()),
                    telegram_user_id,
                ),
            )
        else:
            await self.connection.execute(
                """
                UPDATE google_accounts
                SET access_token = ?, refresh_token = ?, token_expiry = ?, updated_at = ?
                WHERE telegram_user_id = ?
                """,
                (
                    access_token,
                    refresh_token,
                    to_iso8601(token_expiry),
                    to_iso8601(utcnow()),
                    telegram_user_id,
                ),
            )
        await self.connection.commit()

    async def update_last_history_id(self, *, telegram_user_id: int, last_history_id: str) -> None:
        await self.connection.execute(
            """
            UPDATE google_accounts
            SET last_history_id = ?, updated_at = ?
            WHERE telegram_user_id = ?
            """,
            (last_history_id, to_iso8601(utcnow()), telegram_user_id),
        )
        await self.connection.commit()

    async def delete_google_account(self, telegram_user_id: int) -> None:
        await self.connection.execute(
            "DELETE FROM google_accounts WHERE telegram_user_id = ?",
            (telegram_user_id,),
        )
        await self.connection.commit()

    async def was_message_delivered(self, *, telegram_user_id: int, gmail_message_id: str) -> bool:
        cursor = await self.connection.execute(
            """
            SELECT 1
            FROM delivered_messages
            WHERE telegram_user_id = ? AND gmail_message_id = ?
            """,
            (telegram_user_id, gmail_message_id),
        )
        row = await cursor.fetchone()
        await cursor.close()
        return row is not None

    async def mark_message_delivered(
        self,
        *,
        telegram_user_id: int,
        gmail_message_id: str,
        telegram_chat_id: int,
        telegram_message_id: int,
    ) -> None:
        await self.connection.execute(
            """
            INSERT OR IGNORE INTO delivered_messages (
                telegram_user_id,
                gmail_message_id,
                telegram_chat_id,
                telegram_message_id,
                delivered_at
            )
            VALUES (?, ?, ?, ?, ?)
            """,
            (
                telegram_user_id,
                gmail_message_id,
                telegram_chat_id,
                telegram_message_id,
                to_iso8601(utcnow()),
            ),
        )
        await self.connection.commit()

    def _oauth_state_from_row(self, row: aiosqlite.Row | dict[str, Any]) -> OAuthState:
        return OAuthState(
            state=row["state"],
            telegram_user_id=row["telegram_user_id"],
            created_at=from_iso8601(row["created_at"]),
            expires_at=from_iso8601(row["expires_at"]),
        )

    def _account_from_row(self, row: aiosqlite.Row | dict[str, Any]) -> GoogleAccount:
        return GoogleAccount(
            telegram_user_id=row["telegram_user_id"],
            gmail_email=row["gmail_email"],
            access_token=row["access_token"],
            refresh_token=row["refresh_token"],
            token_expiry=from_iso8601(row["token_expiry"]),
            last_history_id=row["last_history_id"],
            connected_at=from_iso8601(row["connected_at"]),
        )

