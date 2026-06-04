from __future__ import annotations

from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import aiosqlite

from gmail_bot.models import (
    DEFAULT_RELOGIN_PROMPT_DELAY_DAYS,
    GoogleAccount,
    OAuthState,
    ReloginPromptPreferences,
)


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
            f"""
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
                relogin_prompt_enabled INTEGER NOT NULL DEFAULT 1,
                relogin_prompt_delay_days INTEGER NOT NULL DEFAULT {DEFAULT_RELOGIN_PROMPT_DELAY_DAYS},
                relogin_prompt_base_at TEXT,
                relogin_prompt_due_at TEXT,
                relogin_prompt_sent_at TEXT,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS relogin_prompt_preferences (
                telegram_user_id INTEGER PRIMARY KEY,
                relogin_prompt_enabled INTEGER NOT NULL DEFAULT 1,
                relogin_prompt_delay_days INTEGER NOT NULL DEFAULT {DEFAULT_RELOGIN_PROMPT_DELAY_DAYS},
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
        await self._ensure_google_account_relogin_prompt_columns()
        await self._backfill_google_account_relogin_prompt_due_at()
        await self._backfill_relogin_prompt_preferences()
        await self.connection.commit()

    async def _ensure_google_account_relogin_prompt_columns(self) -> None:
        cursor = await self.connection.execute("PRAGMA table_info(google_accounts)")
        rows = await cursor.fetchall()
        await cursor.close()
        existing_columns = {row["name"] for row in rows}

        if "relogin_prompt_enabled" not in existing_columns:
            await self.connection.execute(
                "ALTER TABLE google_accounts ADD COLUMN relogin_prompt_enabled INTEGER NOT NULL DEFAULT 1"
            )
        if "relogin_prompt_delay_days" not in existing_columns:
            await self.connection.execute(
                f"ALTER TABLE google_accounts ADD COLUMN relogin_prompt_delay_days INTEGER NOT NULL DEFAULT {DEFAULT_RELOGIN_PROMPT_DELAY_DAYS}"
            )
        if "relogin_prompt_base_at" not in existing_columns:
            await self.connection.execute(
                "ALTER TABLE google_accounts ADD COLUMN relogin_prompt_base_at TEXT"
            )
        if "relogin_prompt_due_at" not in existing_columns:
            await self.connection.execute(
                "ALTER TABLE google_accounts ADD COLUMN relogin_prompt_due_at TEXT"
            )
        if "relogin_prompt_sent_at" not in existing_columns:
            await self.connection.execute(
                "ALTER TABLE google_accounts ADD COLUMN relogin_prompt_sent_at TEXT"
            )

    async def _backfill_google_account_relogin_prompt_due_at(self) -> None:
        cursor = await self.connection.execute(
            """
            SELECT
                telegram_user_id,
                connected_at,
                relogin_prompt_delay_days,
                relogin_prompt_base_at,
                relogin_prompt_due_at
            FROM google_accounts
            WHERE relogin_prompt_base_at IS NULL OR relogin_prompt_due_at IS NULL
            """
        )
        rows = await cursor.fetchall()
        await cursor.close()

        for row in rows:
            delay_days = int(row["relogin_prompt_delay_days"] or DEFAULT_RELOGIN_PROMPT_DELAY_DAYS)
            if row["relogin_prompt_base_at"]:
                base_at = from_iso8601(row["relogin_prompt_base_at"])
            elif row["relogin_prompt_due_at"]:
                base_at = from_iso8601(row["relogin_prompt_due_at"]) - timedelta(days=delay_days)
            else:
                base_at = from_iso8601(row["connected_at"])
            await self.connection.execute(
                """
                UPDATE google_accounts
                SET
                    relogin_prompt_base_at = COALESCE(relogin_prompt_base_at, ?),
                    relogin_prompt_due_at = COALESCE(relogin_prompt_due_at, ?),
                    updated_at = ?
                WHERE telegram_user_id = ?
                """,
                (
                    to_iso8601(base_at),
                    to_iso8601(base_at + timedelta(days=delay_days)),
                    to_iso8601(utcnow()),
                    row["telegram_user_id"],
                ),
            )

    async def _backfill_relogin_prompt_preferences(self) -> None:
        cursor = await self.connection.execute(
            """
            SELECT telegram_user_id, relogin_prompt_enabled, relogin_prompt_delay_days
            FROM google_accounts
            """
        )
        rows = await cursor.fetchall()
        await cursor.close()

        for row in rows:
            now = to_iso8601(utcnow())
            await self.connection.execute(
                """
                INSERT INTO relogin_prompt_preferences (
                    telegram_user_id,
                    relogin_prompt_enabled,
                    relogin_prompt_delay_days,
                    created_at,
                    updated_at
                )
                VALUES (?, ?, ?, ?, ?)
                ON CONFLICT(telegram_user_id) DO NOTHING
                """,
                (
                    row["telegram_user_id"],
                    row["relogin_prompt_enabled"],
                    row["relogin_prompt_delay_days"],
                    now,
                    now,
                ),
            )

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
        relogin_prompt_enabled: bool = True,
        relogin_prompt_delay_days: int = DEFAULT_RELOGIN_PROMPT_DELAY_DAYS,
        relogin_prompt_base_at: datetime | None = None,
        relogin_prompt_due_at: datetime | None = None,
    ) -> None:
        now = to_iso8601(utcnow())
        relogin_prompt_base_at = relogin_prompt_base_at or connected_at
        relogin_prompt_due_at = relogin_prompt_due_at or (
            relogin_prompt_base_at + timedelta(days=relogin_prompt_delay_days)
        )
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
                relogin_prompt_enabled,
                relogin_prompt_delay_days,
                relogin_prompt_base_at,
                relogin_prompt_due_at,
                relogin_prompt_sent_at,
                created_at,
                updated_at
            )
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(telegram_user_id) DO UPDATE SET
                gmail_email = excluded.gmail_email,
                access_token = excluded.access_token,
                refresh_token = excluded.refresh_token,
                token_expiry = excluded.token_expiry,
                last_history_id = excluded.last_history_id,
                connected_at = excluded.connected_at,
                relogin_prompt_enabled = excluded.relogin_prompt_enabled,
                relogin_prompt_delay_days = excluded.relogin_prompt_delay_days,
                relogin_prompt_base_at = excluded.relogin_prompt_base_at,
                relogin_prompt_due_at = excluded.relogin_prompt_due_at,
                relogin_prompt_sent_at = excluded.relogin_prompt_sent_at,
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
                int(relogin_prompt_enabled),
                relogin_prompt_delay_days,
                to_iso8601(relogin_prompt_base_at),
                to_iso8601(relogin_prompt_due_at),
                None,
                now,
                now,
            ),
        )
        await self.connection.commit()

    async def get_google_account(self, telegram_user_id: int) -> GoogleAccount | None:
        cursor = await self.connection.execute(
            """
            SELECT
                telegram_user_id,
                gmail_email,
                access_token,
                refresh_token,
                token_expiry,
                last_history_id,
                connected_at,
                relogin_prompt_enabled,
                relogin_prompt_delay_days,
                relogin_prompt_base_at,
                relogin_prompt_due_at,
                relogin_prompt_sent_at
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
            SELECT
                telegram_user_id,
                gmail_email,
                access_token,
                refresh_token,
                token_expiry,
                last_history_id,
                connected_at,
                relogin_prompt_enabled,
                relogin_prompt_delay_days,
                relogin_prompt_base_at,
                relogin_prompt_due_at,
                relogin_prompt_sent_at
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

    async def get_relogin_prompt_preferences(self, telegram_user_id: int) -> ReloginPromptPreferences:
        cursor = await self.connection.execute(
            """
            SELECT telegram_user_id, relogin_prompt_enabled, relogin_prompt_delay_days
            FROM relogin_prompt_preferences
            WHERE telegram_user_id = ?
            """,
            (telegram_user_id,),
        )
        row = await cursor.fetchone()
        await cursor.close()
        if row is None:
            return ReloginPromptPreferences(telegram_user_id=telegram_user_id)
        return self._relogin_prompt_preferences_from_row(row)

    async def _upsert_relogin_prompt_preferences(
        self,
        *,
        preferences: ReloginPromptPreferences,
        now: datetime,
    ) -> None:
        await self.connection.execute(
            """
            INSERT INTO relogin_prompt_preferences (
                telegram_user_id,
                relogin_prompt_enabled,
                relogin_prompt_delay_days,
                created_at,
                updated_at
            )
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(telegram_user_id) DO UPDATE SET
                relogin_prompt_enabled = excluded.relogin_prompt_enabled,
                relogin_prompt_delay_days = excluded.relogin_prompt_delay_days,
                updated_at = excluded.updated_at
            """,
            (
                preferences.telegram_user_id,
                int(preferences.relogin_prompt_enabled),
                preferences.relogin_prompt_delay_days,
                to_iso8601(now),
                to_iso8601(now),
            ),
        )

    async def mark_relogin_prompt_sent(
        self,
        *,
        telegram_user_id: int,
        sent_at: datetime | None = None,
    ) -> None:
        sent_at = sent_at or utcnow()
        await self.connection.execute(
            """
            UPDATE google_accounts
            SET relogin_prompt_sent_at = ?, updated_at = ?
            WHERE telegram_user_id = ?
            """,
            (to_iso8601(sent_at), to_iso8601(utcnow()), telegram_user_id),
        )
        await self.connection.commit()

    async def set_relogin_prompt_enabled(self, *, telegram_user_id: int, enabled: bool) -> None:
        current_preferences = await self.get_relogin_prompt_preferences(telegram_user_id)
        now = utcnow()
        await self._upsert_relogin_prompt_preferences(
            preferences=ReloginPromptPreferences(
                telegram_user_id=telegram_user_id,
                relogin_prompt_enabled=enabled,
                relogin_prompt_delay_days=current_preferences.relogin_prompt_delay_days,
            ),
            now=now,
        )
        await self.connection.execute(
            """
            UPDATE google_accounts
            SET relogin_prompt_enabled = ?, updated_at = ?
            WHERE telegram_user_id = ?
            """,
            (int(enabled), to_iso8601(now), telegram_user_id),
        )
        await self.connection.commit()

    async def set_relogin_prompt_delay_days(self, *, telegram_user_id: int, delay_days: int) -> None:
        current_preferences = await self.get_relogin_prompt_preferences(telegram_user_id)
        now = utcnow()
        await self._upsert_relogin_prompt_preferences(
            preferences=ReloginPromptPreferences(
                telegram_user_id=telegram_user_id,
                relogin_prompt_enabled=current_preferences.relogin_prompt_enabled,
                relogin_prompt_delay_days=delay_days,
            ),
            now=now,
        )

        account = await self.get_google_account(telegram_user_id)
        if account is not None:
            base_at = account.relogin_prompt_base_at or account.connected_at
            due_at = base_at + timedelta(days=delay_days)
            await self.connection.execute(
                """
                UPDATE google_accounts
                SET
                    relogin_prompt_delay_days = ?,
                    relogin_prompt_base_at = ?,
                    relogin_prompt_due_at = ?,
                    relogin_prompt_sent_at = NULL,
                    updated_at = ?
                WHERE telegram_user_id = ?
                """,
                (
                    delay_days,
                    to_iso8601(base_at),
                    to_iso8601(due_at),
                    to_iso8601(now),
                    telegram_user_id,
                ),
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
        relogin_prompt_base_at = row["relogin_prompt_base_at"]
        relogin_prompt_due_at = row["relogin_prompt_due_at"]
        relogin_prompt_sent_at = row["relogin_prompt_sent_at"]
        return GoogleAccount(
            telegram_user_id=row["telegram_user_id"],
            gmail_email=row["gmail_email"],
            access_token=row["access_token"],
            refresh_token=row["refresh_token"],
            token_expiry=from_iso8601(row["token_expiry"]),
            last_history_id=row["last_history_id"],
            connected_at=from_iso8601(row["connected_at"]),
            relogin_prompt_enabled=bool(row["relogin_prompt_enabled"]),
            relogin_prompt_delay_days=int(row["relogin_prompt_delay_days"]),
            relogin_prompt_base_at=(
                from_iso8601(relogin_prompt_base_at) if relogin_prompt_base_at else None
            ),
            relogin_prompt_due_at=(
                from_iso8601(relogin_prompt_due_at) if relogin_prompt_due_at else None
            ),
            relogin_prompt_sent_at=(
                from_iso8601(relogin_prompt_sent_at) if relogin_prompt_sent_at else None
            ),
        )

    def _relogin_prompt_preferences_from_row(
        self,
        row: aiosqlite.Row | dict[str, Any],
    ) -> ReloginPromptPreferences:
        return ReloginPromptPreferences(
            telegram_user_id=row["telegram_user_id"],
            relogin_prompt_enabled=bool(row["relogin_prompt_enabled"]),
            relogin_prompt_delay_days=int(row["relogin_prompt_delay_days"]),
        )

