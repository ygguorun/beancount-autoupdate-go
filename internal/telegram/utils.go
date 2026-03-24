package telegram

import (
	"os"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendReply 发送回复消息
func (b *Bot) sendReply(message *tgbotapi.Message, text string) {
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ReplyToMessageID = message.MessageID
	if _, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	}
}

// sendMessageWithNilKeyboard 发送不带键盘的消息，返回消息ID
func (b *Bot) sendMessageWithNilKeyboard(userID int, text string) int {
	msg := tgbotapi.NewMessage(int64(userID), text)
	msg.ParseMode = ""
	if sentMsg, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
		return 0
	} else {
		return sentMsg.MessageID
	}
}

// deleteMessages 删除指定的消息列表
func (b *Bot) deleteMessages(userID int, messageIDs []int) {
	for _, messageID := range messageIDs {
		if messageID > 0 {
			deleteMsg := tgbotapi.NewDeleteMessage(int64(userID), messageID)
			if _, err := b.botAPI.Request(deleteMsg); err != nil {
				logger.Errorf("删除消息失败: %v", err)
			}
		}
	}
}

// trackUserMessage 追踪用户输入的消息ID（用于后续删除）
func (b *Bot) trackUserMessage(userID int, transactionID string, messageID int) {
	if messageID <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if txMap, ok := b.pendingTx[userID]; ok {
		if data, ok := txMap[transactionID]; ok {
			data.UserInputMessageIDs = append(data.UserInputMessageIDs, messageID)
			logger.Infof("追踪用户消息: userID=%d, transactionID=%s, messageID=%d, total=%d", userID, transactionID, messageID, len(data.UserInputMessageIDs))
		}
	}
}

// trackBotMessage 追踪Bot发送的提示消息ID（用于后续删除）
func (b *Bot) trackBotMessage(userID int, transactionID string, messageID int) {
	if messageID <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if txMap, ok := b.pendingTx[userID]; ok {
		if data, ok := txMap[transactionID]; ok {
			data.BotPromptMessageIDs = append(data.BotPromptMessageIDs, messageID)
			logger.Infof("追踪Bot消息: userID=%d, transactionID=%s, messageID=%d, total=%d", userID, transactionID, messageID, len(data.BotPromptMessageIDs))
		}
	}
}

// cleanupTransactionMessages 清理交易相关的所有消息和临时文件
// deleteWebDAV: 是否删除 WebDAV 临时文件（取消时删除）
// deleteUserImage: 是否删除用户原始图片消息（确认时根据配置决定）
func (b *Bot) cleanupTransactionMessages(userID int, transactionID string, data *beancount.PendingTransaction, deleteWebDAV bool, deleteUserImage bool) {
	logger.Infof("清理交易消息: transactionID=%s, deleteWebDAV=%v, deleteUserImage=%v", transactionID, deleteWebDAV, deleteUserImage)

	// 删除 WebDAV 临时文件
	if deleteWebDAV && data.TempWebDAVPath != "" && b.webdavMgr != nil {
		logger.Infof("删除 WebDAV 临时文件: transactionID=%s, path=%s", transactionID, data.TempWebDAVPath)
		if _, err := b.webdavMgr.DeleteFile(data.TempWebDAVPath); err != nil {
			logger.Errorf("删除 WebDAV 文件失败: transactionID=%s, path=%s, error=%v", transactionID, data.TempWebDAVPath, err)
		}
	}

	// 删除本地临时图片文件
	if data.OriginalTempFilePath != "" {
		if err := os.Remove(data.OriginalTempFilePath); err != nil {
			logger.Errorf("删除临时文件失败: transactionID=%s, path=%s, error=%v", transactionID, data.OriginalTempFilePath, err)
		} else {
			logger.Infof("已删除临时文件: transactionID=%s, path=%s", transactionID, data.OriginalTempFilePath)
		}
	}

	// 删除所有预览消息
	if len(data.PreviousMessageIDs) > 0 {
		logger.Infof("删除当前交易 %s 的预览消息: %v", transactionID, data.PreviousMessageIDs)
		b.deleteMessages(userID, data.PreviousMessageIDs)
	}

	// 删除用户输入的文本消息
	if len(data.UserInputMessageIDs) > 0 {
		logger.Infof("删除当前交易 %s 的用户输入消息: %v", transactionID, data.UserInputMessageIDs)
		b.deleteMessages(userID, data.UserInputMessageIDs)
	}

	// 删除 Bot 发送的提示消息
	if len(data.BotPromptMessageIDs) > 0 {
		logger.Infof("删除当前交易 %s 的 Bot 提示消息: %v", transactionID, data.BotPromptMessageIDs)
		b.deleteMessages(userID, data.BotPromptMessageIDs)
	}

	// 删除原始预览消息（如果存在且不在 PreviousMessageIDs 中）
	if data.OriginalMessageID > 0 {
		alreadyDeleted := false
		for _, id := range data.PreviousMessageIDs {
			if id == data.OriginalMessageID {
				alreadyDeleted = true
				break
			}
		}
		if !alreadyDeleted {
			logger.Infof("删除当前交易 %s 的原始预览消息: %d", transactionID, data.OriginalMessageID)
			deleteMsg := tgbotapi.NewDeleteMessage(int64(userID), data.OriginalMessageID)
			if _, err := b.botAPI.Request(deleteMsg); err != nil {
				logger.Errorf("删除消息失败: transactionID=%s, messageID=%d, error=%v", transactionID, data.OriginalMessageID, err)
			}
		} else {
			logger.Infof("当前交易 %s 的原始预览消息 %d 已在 PreviousMessageIDs 中删除，跳过", transactionID, data.OriginalMessageID)
		}
	}

	// 根据参数删除原始图片消息
	// 确认时根据 DeleteUserMessage 配置决定是否删除，取消时不删除
	if deleteUserImage && b.config.Telegram.DeleteUserMessage && data.UserOriginalMessageID > 0 {
		logger.Infof("删除当前交易 %s 的原始图片消息: %d", transactionID, data.UserOriginalMessageID)
		deleteMsg := tgbotapi.NewDeleteMessage(int64(userID), data.UserOriginalMessageID)
		if _, err := b.botAPI.Request(deleteMsg); err != nil {
			logger.Errorf("删除消息失败: transactionID=%s, messageID=%d, error=%v", transactionID, data.UserOriginalMessageID, err)
		}
	}
}
