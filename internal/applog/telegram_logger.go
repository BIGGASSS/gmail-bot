package applog

import (
	"fmt"
)

// TelegramBotLogger adapts applog to telegram-bot-api's BotLogger interface.
// Library debug/error messages are treated as warnings so they honor LOG_LEVEL.
type TelegramBotLogger struct{}

func (TelegramBotLogger) Println(v ...any) {
	Warningf("%s", RedactString(fmt.Sprint(v...)))
}

func (TelegramBotLogger) Printf(format string, v ...any) {
	Warningf("%s", RedactString(fmt.Sprintf(format, v...)))
}
