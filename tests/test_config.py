from gmail_bot.config import SettingsError, parse_authorized_user_ids


def test_parse_authorized_user_ids_accepts_csv_integers() -> None:
    assert parse_authorized_user_ids("123, 456,789") == frozenset({123, 456, 789})


def test_parse_authorized_user_ids_rejects_invalid_values() -> None:
    try:
        parse_authorized_user_ids("123,abc")
    except SettingsError as exc:
        assert "invalid integer value" in str(exc)
    else:
        raise AssertionError("Expected SettingsError for invalid authorized user ids.")

