package telegram

import (
	"fmt"
	"os"
	"time"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleTextInput 处理文本输入
func (b *Bot) handleTextInput(message *tgbotapi.Message) {
	userID := int(message.From.ID)
	text := message.Text

	logger.Infof("收到文本输入: userID=%d, text=%s", userID, text)

	b.mu.RLock()
	var transactionID string
	var inputType string
	// 查找用户是否有等待输入的交易
	if txMap, ok := b.waitingForInput[userID]; ok && len(txMap) > 0 {
		// 获取第一个（最新的）交易ID
		for tid, itype := range txMap {
			transactionID = tid
			inputType = itype
			break
		}
	}
	b.mu.RUnlock()

	if transactionID == "" {
		logger.Infof("用户 %d 不在等待输入状态", userID)
		return
	}

	// 追踪用户输入的消息ID，用于后续删除
	b.trackUserMessage(userID, transactionID, message.MessageID)

	logger.Infof("用户 %d 在等待输入: transactionID=%s, inputType=%s", userID, transactionID, inputType)

	switch inputType {
	case "guidance":
		b.handleGuidanceInput(userID, transactionID, text)
	}

	b.mu.Lock()
	delete(b.waitingForInput[userID], transactionID)
	if len(b.waitingForInput[userID]) == 0 {
		delete(b.waitingForInput, userID)
	}
	b.mu.Unlock()
}

// rerunRecognition 重新识别图片
func (b *Bot) rerunRecognition(userID int, transactionID string, messageID int) {
	logger.Infof("重新识别: userID=%d, transactionID=%s, messageID=%d", userID, transactionID, messageID)

	b.mu.RLock()
	data, ok := b.pendingTx[userID][transactionID]
	b.mu.RUnlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易 %s（在 rerunRecognition 中）", userID, transactionID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	// 检查是否有临时文件路径
	if data.OriginalTempFilePath == "" {
		logger.Warnf("临时文件路径为空，无法重新识别")
		b.sendMessageWithNilKeyboard(userID, "❌ 无法重新识别：原始图片文件不存在")
		return
	}

	// 检查临时文件是否存在
	if _, err := os.Stat(data.OriginalTempFilePath); os.IsNotExist(err) {
		logger.Errorf("临时文件不存在: %s", data.OriginalTempFilePath)
		b.sendMessageWithNilKeyboard(userID, "❌ 无法重新识别：原始图片文件不存在")
		return
	}

	// 发送重新识别的提示消息，并追踪消息ID
	msgID := b.sendMessageWithNilKeyboard(userID, "🔄 正在重新识别图片...")
	b.trackBotMessage(userID, transactionID, msgID)

	// 获取 LLM 信号量，控制并发
	b.llmSemaphore <- struct{}{}
	defer func() { <-b.llmSemaphore }()

	// 执行 git pull
	logger.Infof("执行 git pull 获取最新账户信息...")
	if pulled, err := b.gitMgr.PullChanges(); err != nil {
		logger.Errorf("Git pull 失败: %v", err)
	} else if pulled {
		logger.Infof("Git pull 成功，已更新本地文件")
	} else {
		logger.Infof("没有需要拉取的更改")
	}

	// 调用 LLM 重新解析图片（清空历史，完全重新识别）
	logger.Infof("开始调用 LLM 重新解析图片...")
	accounts := b.beancountMgr.GetAllCategories()
	allAccounts := allAvailableAccounts(accounts)

	parseResult, newHistory, err := b.llmParser.ParseImageWithHistory(data.OriginalTempFilePath, allAccounts, []string{}, nil, "")
	if err != nil {
		logger.Errorf("LLM 重新解析失败: %v", err)
		b.sendRetryErrorKeyboard(userID, transactionID)
		return
	}

	if parseResult == nil {
		logger.Warnf("LLM 重新解析返回空结果")
		b.sendRetryErrorKeyboard(userID, transactionID)
		return
	}

	logger.Infof("LLM 重新解析成功: payee=%s, narration=%s, postings=%d",
		parseResult.Payee, parseResult.Narration, len(parseResult.Postings))

	// 解析日期
	transactionTime, err := b.llmParser.ParseTime(parseResult.DateTime)
	if err != nil {
		logger.Warnf("解析日期失败: %v，使用当前时间", err)
		transactionTime = time.Now()
	} else {
		logger.Infof("解析日期成功: %s", transactionTime.Format("2006-01-02 15:04:05"))
	}

	// 更新待确认的交易数据
	b.mu.Lock()
	if data, ok := b.pendingTx[userID][transactionID]; ok {
		data.Date = transactionTime.Format("2006-01-02")
		data.Time = transactionTime.Format("15:04:05")
		data.Flag = parseResult.Flag
		data.Payee = parseResult.Payee
		data.Narration = parseResult.Narration
		data.Tags = parseResult.Tags
		data.Postings = parseResult.Postings
		data.OrderID = parseResult.OrderID
		data.Extra = parseResult.Extra
		data.SpecialDirectives = parseResult.SpecialDirectives
		data.ConversationHistory = newHistory
		data.LastMessageID = messageID
		data.EditingPostingIndex = -1
		data.EditingPostingMessageID = 0
		data.PreviousMessageIDs = []int{}
	}
	b.mu.Unlock()

	logger.Infof("已更新待确认交易: userID=%d, transactionID=%s, payee=%s, narration=%s", userID, transactionID, parseResult.Payee, parseResult.Narration)

	// 显示新的预览
	b.showTransactionPreview(userID, transactionID, messageID)
	logger.Infof("重新识别完成，显示新预览")
}

// sendRetryErrorKeyboard 发送重试错误的键盘
func (b *Bot) sendRetryErrorKeyboard(userID int, transactionID string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💬 引导重试", fmt.Sprintf("%s:guided_retry", transactionID)),
			tgbotapi.NewInlineKeyboardButtonData("🔄 重新识别", fmt.Sprintf("%s:rerun_recognition", transactionID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ 取消", fmt.Sprintf("%s:cancel", transactionID)),
		),
	)
	msg := tgbotapi.NewMessage(int64(userID), "❌ 重新识别失败\n\n请确保图片清晰，包含完整的交易信息（日期、金额、交易对象等）\n或尝试重新上传。")
	msg.ReplyMarkup = &keyboard
	if _, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	}
}

// startGuidedRetry 开始引导重试流程
func (b *Bot) startGuidedRetry(userID int, transactionID string, messageID int) {
	logger.Infof("开始引导重试: userID=%d, transactionID=%s", userID, transactionID)

	b.mu.Lock()
	if b.waitingForInput[userID] == nil {
		b.waitingForInput[userID] = make(map[string]string)
	}
	b.waitingForInput[userID][transactionID] = "guidance"
	if data, ok := b.pendingTx[userID][transactionID]; ok {
		data.LastMessageID = messageID
	}
	b.mu.Unlock()

	var historyInfo string
	b.mu.RLock()
	if data, ok := b.pendingTx[userID][transactionID]; ok {
		if len(data.ConversationHistory) > 0 {
			historyInfo = fmt.Sprintf("已尝试 %d 次", len(data.ConversationHistory)/2)
		}
	}
	b.mu.RUnlock()

	prompt := "💬 请输入引导文字，描述您希望如何修正识别结果：\n\n" +
		"例如：\n" +
		"• 这是一笔打车支出，从微信支付\n" +
		"• 金额应该是 35.00 元，不是 3.50 元\n" +
		"• 这是超市购物，分类应该是 Expenses:FoodAndDrink\n" +
		"• 收款人应该是美团，不是美团外卖\n\n"

	if historyInfo != "" {
		prompt += fmt.Sprintf("(%s)\n\n", historyInfo)
	}

	prompt += "取消请输入 /cancel"

	msg := tgbotapi.NewMessage(int64(userID), prompt)
	if sentMsg, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	} else {
		b.trackBotMessage(userID, transactionID, sentMsg.MessageID)
	}
}

// handleGuidanceInput 处理引导输入
func (b *Bot) handleGuidanceInput(userID int, transactionID string, guidance string) {
	logger.Infof("处理引导输入: userID=%d, transactionID=%s, guidance=%s", userID, transactionID, guidance)

	b.mu.RLock()
	data, ok := b.pendingTx[userID][transactionID]
	b.mu.RUnlock()

	if !ok {
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	if data.OriginalTempFilePath == "" {
		b.sendMessageWithNilKeyboard(userID, "❌ 无法重新识别：原始图片文件不存在")
		return
	}

	// 发送处理中提示
	msgID := b.sendMessageWithNilKeyboard(userID, "🔄 正在根据您的引导重新识别...")
	b.trackBotMessage(userID, transactionID, msgID)

	// 获取 LLM 信号量
	b.llmSemaphore <- struct{}{}
	defer func() { <-b.llmSemaphore }()

	// Git pull
	if pulled, err := b.gitMgr.PullChanges(); err != nil {
		logger.Errorf("Git pull 失败: %v", err)
	} else if pulled {
		logger.Infof("Git pull 成功")
	}

	// 获取账户信息
	accounts := b.beancountMgr.GetAllCategories()
	allAccounts := allAvailableAccounts(accounts)

	// 使用带引导的解析方法
	parseResult, newHistory, err := b.llmParser.ParseWithGuidance(
		data.OriginalTempFilePath,
		allAccounts,
		[]string{},
		data.ConversationHistory,
		guidance,
	)
	if err != nil {
		logger.Errorf("引导重试失败: %v", err)
		b.handleGuidanceError(userID, transactionID, guidance, err)
		return
	}

	if parseResult == nil {
		logger.Warnf("引导重试返回空结果")
		b.handleGuidanceError(userID, transactionID, guidance, nil)
		return
	}

	// 解析日期时间
	transactionTime, err := b.llmParser.ParseTime(parseResult.DateTime)
	if err != nil {
		transactionTime = time.Now()
	}

	// 更新待确认交易
	b.mu.Lock()
	if d, ok := b.pendingTx[userID][transactionID]; ok {
		d.Date = transactionTime.Format("2006-01-02")
		d.Time = transactionTime.Format("15:04:05")
		d.Flag = parseResult.Flag
		d.Payee = parseResult.Payee
		d.Narration = parseResult.Narration
		d.Tags = parseResult.Tags
		d.Postings = parseResult.Postings
		d.OrderID = parseResult.OrderID
		d.Extra = parseResult.Extra
		d.SpecialDirectives = parseResult.SpecialDirectives
		d.ConversationHistory = newHistory
	}
	b.mu.Unlock()

	logger.Infof("引导重试成功: userID=%d, transactionID=%s, payee=%s, narration=%s", userID, transactionID, parseResult.Payee, parseResult.Narration)

	// 显示新预览
	b.showTransactionPreview(userID, transactionID, 0)
}

// handleGuidanceError 处理引导错误
func (b *Bot) handleGuidanceError(userID int, transactionID string, guidance string, err error) {
	// 保留历史，允许继续重试
	b.mu.Lock()
	if d, ok := b.pendingTx[userID][transactionID]; ok {
		d.ConversationHistory = append(d.ConversationHistory, beancount.ConversationMessage{
			Role:    "user",
			Content: guidance,
		})
	}
	b.mu.Unlock()

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💬 继续引导", fmt.Sprintf("%s:guided_retry", transactionID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 重新识别", fmt.Sprintf("%s:rerun_recognition", transactionID)),
			tgbotapi.NewInlineKeyboardButtonData("❌ 取消", fmt.Sprintf("%s:cancel", transactionID)),
		),
	)

	var msg tgbotapi.MessageConfig
	if err != nil {
		msg = tgbotapi.NewMessage(int64(userID),
			fmt.Sprintf("❌ 引导重试失败: %v\n\n您可以继续输入引导文字或重新识别。", err))
	} else {
		msg = tgbotapi.NewMessage(int64(userID), "❌ 无法识别图片中的交易信息\n\n您可以继续输入引导文字或重新识别。")
	}
	msg.ReplyMarkup = &keyboard

	if _, sendErr := b.botAPI.Send(msg); sendErr != nil {
		logger.Errorf("发送消息失败: %v", sendErr)
	}
}

func allAvailableAccounts(accounts map[beancount.AccountType][]string) []string {
	return append(
		append(
			append(accounts[beancount.AccountTypeAssets], accounts[beancount.AccountTypeLiabilities]...),
			accounts[beancount.AccountTypeExpenses]...,
		),
		accounts[beancount.AccountTypeIncome]...,
	)
}
