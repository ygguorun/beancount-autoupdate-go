package telegram

import (
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func slashCommands() []tgbotapi.BotCommand {
	return []tgbotapi.BotCommand{
		{Command: "start", Description: "显示欢迎信息"},
		{Command: "help", Description: "显示使用帮助"},
		{Command: "accounts", Description: "查看账户和分类"},
		{Command: "pending", Description: "查看待处理交易"},
		{Command: "cancel", Description: "取消当前输入"},
	}
}

func (b *Bot) registerSlashCommands() error {
	scope := tgbotapi.NewBotCommandScopeAllPrivateChats()
	config := tgbotapi.NewSetMyCommandsWithScope(scope, slashCommands()...)

	if _, err := b.botAPI.Request(config); err != nil {
		return fmt.Errorf("failed to register slash commands: %w", err)
	}

	return nil
}
