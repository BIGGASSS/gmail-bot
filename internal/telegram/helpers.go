package telegram

import (
	"strconv"
	"strings"
)

const ExpandPrefix = "expand:"

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
