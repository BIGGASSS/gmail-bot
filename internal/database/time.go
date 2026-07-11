package database

import (
	"fmt"
	"strings"
	"time"
)

func UTCNow() time.Time {
	return time.Now().UTC()
}

// ToISO8601 formats timestamps like Python's datetime.isoformat() for UTC values,
// always using a +00:00 offset so SQLite lexical comparisons stay compatible.
func ToISO8601(value time.Time) string {
	value = value.UTC()
	micro := value.Nanosecond() / 1000
	if micro == 0 {
		return value.Format("2006-01-02T15:04:05") + "+00:00"
	}
	return fmt.Sprintf("%s.%06d+00:00", value.Format("2006-01-02T15:04:05"), micro)
}

func FromISO8601(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}

	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999+00:00",
		"2006-01-02T15:04:05+00:00",
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
	}
	var lastErr error
	for _, layout := range formats {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, lastErr)
}
