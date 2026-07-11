package telegram

import (
	"errors"
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type APIError struct {
	Message string
	Code    int
}

func (e *APIError) Error() string {
	return e.Message
}

func (e *APIError) IsForbidden() bool {
	return e.Code == 403
}

func NormalizeAPIError(err error) error {
	if err == nil {
		return nil
	}
	var tgErr *tgbotapi.Error
	if errors.As(err, &tgErr) {
		return &APIError{
			Message: fmt.Sprintf("Telegram API error %d: %s", tgErr.Code, tgErr.Message),
			Code:    tgErr.Code,
		}
	}
	return err
}

func IsForbidden(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.IsForbidden()
	}
	var tgErr *tgbotapi.Error
	if errors.As(err, &tgErr) {
		return tgErr.Code == 403
	}
	return false
}
