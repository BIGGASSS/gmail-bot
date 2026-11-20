package telegram

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const ExpandPrefix = "expand:"

// LogoutPrefix starts callback data for the /logout account picker:
// logout:<telegram user ID>:<account ID or "all">. The user ID stops someone else
// in a group chat from pressing another user's button.
const LogoutPrefix = "logout:"

// LogoutAllAccounts is the account ID that stands for every connected account.
const LogoutAllAccounts int64 = 0

func BuildLogoutCallbackData(telegramUserID, accountID int64) string {
	target := "all"
	if accountID != LogoutAllAccounts {
		target = strconv.FormatInt(accountID, 10)
	}
	return LogoutPrefix + strconv.FormatInt(telegramUserID, 10) + ":" + target
}

// LogoutPagePrefix starts callback data that shows another page of the logout
// picker: logoutpage:<telegram user ID>:<zero-based page>.
const LogoutPagePrefix = "logoutpage:"

func BuildLogoutPageCallbackData(telegramUserID int64, page int) string {
	return LogoutPagePrefix + strconv.FormatInt(telegramUserID, 10) + ":" + strconv.Itoa(page)
}

func ParseLogoutPageCallbackData(callbackData string) (int64, int, bool) {
	if !strings.HasPrefix(callbackData, LogoutPagePrefix) {
		return 0, 0, false
	}
	owner, rawPage, found := strings.Cut(strings.TrimPrefix(callbackData, LogoutPagePrefix), ":")
	if !found {
		return 0, 0, false
	}
	user, err := strconv.ParseInt(owner, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	page, err := strconv.Atoi(rawPage)
	if err != nil || page < 0 {
		return 0, 0, false
	}
	return user, page, true
}

// ParseLogoutCallbackData returns the owning user and the account ID, which is
// LogoutAllAccounts for the "All accounts" button.
func ParseLogoutCallbackData(callbackData string) (int64, int64, bool) {
	owner, target, found := strings.Cut(strings.TrimPrefix(callbackData, LogoutPrefix), ":")
	if !found || !strings.HasPrefix(callbackData, LogoutPrefix) {
		return 0, 0, false
	}
	user, err := strconv.ParseInt(owner, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if target == "all" {
		return user, LogoutAllAccounts, true
	}
	accountID, err := strconv.ParseInt(target, 10, 64)
	if err != nil || accountID <= 0 {
		return 0, 0, false
	}
	return user, accountID, true
}

func BuildExpandCallbackData(gmailMessageID string, pageIndex int) string {
	if pageIndex == 0 {
		return ExpandPrefix + gmailMessageID
	}
	return ExpandPrefix + gmailMessageID + ":" + strconv.Itoa(pageIndex)
}

func ParseExpandCallbackData(callbackData string) (string, int) {
	value := strings.TrimPrefix(callbackData, ExpandPrefix)
	idx := strings.LastIndex(value, ":")
	if idx < 0 {
		return value, 0
	}
	page, err := strconv.Atoi(value[idx+1:])
	if err != nil {
		return value, 0
	}
	return value[:idx], page
}

// ParseReloginReminderSetting returns a pointer to bool when args are recognized.
func ParseReloginReminderSetting(args string) *bool {
	value := strings.ToLower(strings.TrimSpace(args))
	switch value {
	case "on", "enable", "enabled", "yes", "true", "1":
		v := true
		return &v
	case "off", "disable", "disabled", "no", "false", "0":
		v := false
		return &v
	default:
		return nil
	}
}

func ParseReloginReminderDelayDays(args string) *int {
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(args)))
	if len(tokens) == 0 {
		return nil
	}

	var candidate string
	switch {
	case len(tokens) == 1:
		candidate = tokens[0]
	case len(tokens) == 2 && (tokens[0] == "day" || tokens[0] == "days" || tokens[0] == "delay" || tokens[0] == "timer"):
		candidate = tokens[1]
	case len(tokens) == 2 && (tokens[1] == "day" || tokens[1] == "days"):
		candidate = tokens[0]
	default:
		return nil
	}

	if candidate == "" || !isDecimal(candidate) {
		return nil
	}
	days, err := strconv.Atoi(candidate)
	if err != nil || days <= 0 {
		return nil
	}
	return &days
}

func FormatDayCount(days int) string {
	if days == 1 {
		return "1 day"
	}
	return strconv.Itoa(days) + " days"
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// TelegramTextLimit is the maximum length of a Telegram message, which Telegram
// counts in UTF-16 code units.
const TelegramTextLimit = 4096

// SplitMessageText splits plain text into chunks no longer than limit UTF-16
// code units. It breaks at line boundaries and splits a line only when that
// line alone exceeds the limit. It always returns at least one chunk.
func SplitMessageText(text string, limit int) []string {
	if utf16Len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	var current strings.Builder
	currentLen := 0
	flush := func() {
		if currentLen > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
			currentLen = 0
		}
	}
	for _, line := range strings.Split(text, "\n") {
		for _, piece := range splitLongLine(line, limit) {
			pieceLen := utf16Len(piece)
			if currentLen > 0 && currentLen+1+pieceLen > limit {
				flush()
			}
			if currentLen > 0 {
				current.WriteByte('\n')
				currentLen++
			}
			current.WriteString(piece)
			currentLen += pieceLen
		}
	}
	flush()
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

func splitLongLine(line string, limit int) []string {
	if utf16Len(line) <= limit {
		return []string{line}
	}
	var pieces []string
	start, length := 0, 0
	for i, r := range line {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if length+width > limit {
			pieces = append(pieces, line[start:i])
			start, length = i, 0
		}
		length += width
	}
	return append(pieces, line[start:])
}

func utf16Len(text string) int {
	n := 0
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}
