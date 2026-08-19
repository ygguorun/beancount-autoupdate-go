package telegram

import (
	"strings"

	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Run 运行 Bot
func (b *Bot) Run() error {
	logger.Info("Bot is running...")
	if err := b.registerSlashCommands(); err != nil {
		logger.Warnf("自动注册 Telegram slash commands 失败: %v", err)
	} else {
		logger.Info("已自动注册 Telegram slash commands（仅私聊）")
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.botAPI.GetUpdatesChan(u)

	// 使用 worker pool 处理消息
	workerCount := 5
	workerChan := make(chan tgbotapi.Update, 100)

	// 启动 workers
	for range workerCount {
		go b.worker(workerChan)
	}

	// 分发消息到 workers
	for update := range updates {
		if update.Message != nil {
			workerChan <- update
		} else if update.CallbackQuery != nil {
			workerChan <- update
		}
	}

	return nil
}

// worker 处理消息的 worker goroutine
func (b *Bot) worker(ch <-chan tgbotapi.Update) {
	for update := range ch {
		if update.Message != nil {
			b.handleMessage(update)
		} else if update.CallbackQuery != nil {
			b.handleCallback(update)
		}
	}
}

// handleMessage 处理消息
func (b *Bot) handleMessage(update tgbotapi.Update) {
	message := update.Message
	userID := int(message.From.ID)

	// 检查用户权限
	if !b.config.IsUserAllowed(userID) {
		b.sendReply(message, "❌ 您没有权限使用此机器人")
		return
	}

	// 处理命令
	if message.IsCommand() {
		b.handleCommand(message)
		return
	}

	// 处理回复 Bot 发送的图片
	if message.ReplyToMessage != nil && b.isBotPhotoMessage(message.ReplyToMessage) {
		b.handleReplyToBotPhoto(message)
		return
	}

	// 处理文本输入
	if message.Text != "" {
		b.handleTextInput(message)
		return
	}

	// 处理照片（聊天图片）
	if message.Photo != nil {
		b.handlePhoto(message)
		return
	}

	// 处理文档（图片文件）
	if message.Document != nil {
		b.handleDocument(message)
		return
	}
}

// handleCommand 处理命令
func (b *Bot) handleCommand(message *tgbotapi.Message) {
	switch message.Command() {
	case "start":
		b.handleStartCommand(message)
	case "help":
		b.handleHelpCommand(message)
	case "accounts":
		b.handleAccountsCommand(message)
	case "cancel":
		b.handleCancelCommand(message)
	case "pending":
		b.handlePendingCommand(message)
	default:
		b.sendReply(message, "未知命令，请使用 /help 查看可用命令")
	}
}

// handleCallback 处理回调查询
func (b *Bot) handleCallback(update tgbotapi.Update) {
	query := update.CallbackQuery
	if query == nil || query.From == nil {
		return
	}
	userID := int(query.From.ID)
	callbackData := query.Data
	if !b.config.IsUserAllowed(userID) {
		b.sendMessageWithNilKeyboard(userID, "❌ 您没有权限使用此机器人")
		return
	}

	logger.Infof("收到回调: userID=%d, callbackData=%s", userID, callbackData)

	// 回答回调
	callback := tgbotapi.NewCallback(query.ID, "")
	if _, err := b.botAPI.Request(callback); err != nil {
		logger.Errorf("回答回调失败: %v", err)
	}

	// 从回调数据中提取 transactionID
	var transactionID string
	var action string
	parts := strings.SplitN(callbackData, ":", 3)
	if len(parts) >= 2 {
		transactionID = parts[0]
		action = parts[1]
		// 如果还有更多部分，重新组合成 action
		if len(parts) == 3 {
			action = parts[1] + ":" + parts[2]
		}
	} else {
		// 兼容旧格式（没有 transactionID 的回调）
		action = callbackData
		// 尝试获取最新的交易ID
		b.mu.RLock()
		if txMap, ok := b.pendingTx[userID]; ok && len(txMap) > 0 {
			// 获取第一个（最新的）交易ID
			for tid := range txMap {
				transactionID = tid
				break
			}
		}
		b.mu.RUnlock()
	}

	if transactionID == "" {
		logger.Warnf("无法从回调数据中提取 transactionID: %s", callbackData)
		b.sendMessageWithNilKeyboard(userID, "❌ 无效的回调数据")
		return
	}

	b.mu.RLock()
	data, ok := b.pendingTx[userID][transactionID]
	if ok && data != nil {
		data = clonePendingTransaction(data)
	} else {
		ok = false
	}
	b.mu.RUnlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易 %s", userID, transactionID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	logger.Infof("找到待确认交易: transactionID=%s, payee=%s, narration=%s", transactionID, data.Payee, data.Narration)

	switch action {
	case "open_preview":
		b.openPendingPreview(userID, transactionID)
	case "confirm":
		b.confirmTransaction(userID, transactionID)
	case "cancel":
		b.cancelTransaction(userID, transactionID)
	case "rerun_recognition":
		messageID := 0
		if query.Message != nil {
			messageID = query.Message.MessageID
		}
		b.rerunRecognition(userID, transactionID, messageID)
	case "guided_retry":
		messageID := 0
		if query.Message != nil {
			messageID = query.Message.MessageID
		}
		b.startGuidedRetry(userID, transactionID, messageID)
	default:
		logger.Warnf("未知回调操作: userID=%d, transactionID=%s, action=%s", userID, transactionID, action)
		b.sendMessageWithNilKeyboard(userID, "⚠️ 该操作已失效，请重新发送图片或使用 /pending 查看待处理交易")
	}
}
