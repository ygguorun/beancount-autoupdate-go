package telegram

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleTextInput 处理文本输入
func (b *Bot) handleTextInput(message *tgbotapi.Message) {
	userID := int(message.From.ID)
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return
	}

	logger.Infof("收到文本输入: userID=%d, text=%s", userID, text)

	if transactionID, inputType, ok := b.resolveWaitingInputTarget(userID, message.ReplyToMessage); ok {
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
		return
	}

	transactionID, guidance, reply := b.resolveGuidanceTarget(userID, text, message.ReplyToMessage)
	if reply != "" {
		b.sendReply(message, reply)
		return
	}

	b.trackUserMessage(userID, transactionID, message.MessageID)
	b.handleGuidanceInput(userID, transactionID, guidance)
}

func (b *Bot) resolveWaitingInputTarget(userID int, replyTo *tgbotapi.Message) (string, string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	waitingMap, ok := b.waitingForInput[userID]
	if !ok || len(waitingMap) == 0 {
		return "", "", false
	}

	if replyTo != nil {
		if transactionID, found := b.findPendingTransactionByMessageIDLocked(userID, replyTo.MessageID); found {
			if inputType, exists := waitingMap[transactionID]; exists {
				return transactionID, inputType, true
			}
		}
	}

	for transactionID, inputType := range waitingMap {
		return transactionID, inputType, true
	}

	return "", "", false
}

func (b *Bot) resolveGuidanceTarget(userID int, rawText string, replyTo *tgbotapi.Message) (string, string, string) {
	if replyTo != nil {
		b.mu.RLock()
		transactionID, found := b.findPendingTransactionByMessageIDLocked(userID, replyTo.MessageID)
		b.mu.RUnlock()
		if found {
			return transactionID, rawText, ""
		}
	}

	if shortID, guidance, hasShortID := parseGuidanceWithShortID(rawText); hasShortID {
		if guidance == "" {
			return "", "", fmt.Sprintf("⚠️ 请在 #%s 后输入修改意见，例如：#%s 金额应为 35.00", strings.ToUpper(shortID), strings.ToUpper(shortID))
		}

		b.mu.RLock()
		transactionID, found, ambiguous := b.findPendingTransactionByShortIDLocked(userID, shortID)
		b.mu.RUnlock()

		if ambiguous {
			return "", "", "⚠️ 该短ID匹配到多笔交易，请直接回复目标预览消息进行修改"
		}
		if !found {
			return "", "", fmt.Sprintf("❌ 未找到交易 #%s，请使用 /pending 查看待处理交易", strings.ToUpper(shortID))
		}

		return transactionID, guidance, ""
	}

	b.mu.RLock()
	txMap := b.pendingTx[userID]
	count := len(txMap)
	if count == 1 {
		for transactionID := range txMap {
			b.mu.RUnlock()
			return transactionID, rawText, ""
		}
	}
	b.mu.RUnlock()

	if count == 0 {
		return "", "", "💡 请先发送账单图片开始识别，或使用 /help 查看用法"
	}

	return "", "", b.buildAmbiguousTargetHint(userID)
}

func parseGuidanceWithShortID(text string) (string, string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}

	trimmed = strings.TrimPrefix(trimmed, "#")
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return "", "", true
	}

	shortID := parts[0]
	if len(parts) == 1 {
		return shortID, "", true
	}

	guidance := strings.TrimSpace(strings.Join(parts[1:], " "))
	return shortID, guidance, true
}

func (b *Bot) findPendingTransactionByMessageIDLocked(userID int, messageID int) (string, bool) {
	if messageID <= 0 {
		return "", false
	}

	txMap, ok := b.pendingTx[userID]
	if !ok || len(txMap) == 0 {
		return "", false
	}

	for transactionID, data := range txMap {
		if data == nil {
			continue
		}

		if data.LastMessageID == messageID || data.OriginalMessageID == messageID || data.SourceImageMessageID == messageID {
			return transactionID, true
		}

		for _, id := range data.PreviousMessageIDs {
			if id == messageID {
				return transactionID, true
			}
		}

		for _, id := range data.ConversationContextMessageIDs {
			if id == messageID {
				return transactionID, true
			}
		}
	}

	return "", false
}

func (b *Bot) findPendingTransactionByShortIDLocked(userID int, shortID string) (string, bool, bool) {
	txMap, ok := b.pendingTx[userID]
	if !ok || len(txMap) == 0 {
		return "", false, false
	}

	normalized := strings.ToUpper(strings.TrimSpace(shortID))
	if normalized == "" {
		return "", false, false
	}

	matched := make([]string, 0, 2)
	for transactionID := range txMap {
		if strings.EqualFold(shortTransactionID(transactionID), normalized) {
			matched = append(matched, transactionID)
		}
	}

	if len(matched) == 0 {
		return "", false, false
	}
	if len(matched) > 1 {
		return "", false, true
	}

	return matched[0], true, false
}

func (b *Bot) buildAmbiguousTargetHint(userID int) string {
	b.mu.RLock()
	txMap := b.pendingTx[userID]
	items := make([]pendingHintItem, 0, len(txMap))
	for transactionID, data := range txMap {
		if data == nil {
			continue
		}
		items = append(items, pendingHintItem{
			shortID: shortTransactionID(transactionID),
			date:    data.Date,
			time:    data.Time,
			payee:   maskPayee(data.Payee),
		})
	}
	b.mu.RUnlock()

	sort.Slice(items, func(i, j int) bool {
		left := items[i].date + " " + items[i].time
		right := items[j].date + " " + items[j].time
		if left == right {
			return items[i].shortID < items[j].shortID
		}
		return left > right
	})

	var builder strings.Builder
	builder.WriteString("⚠️ 你有多笔待处理交易，请使用 #短ID 指定目标。\n")
	builder.WriteString("例如：#ABC123 金额应为 35.00\n\n")
	builder.WriteString("当前待处理：\n")

	for i, item := range items {
		if i >= 5 {
			break
		}
		fmt.Fprintf(&builder, "- #%s %s %s %s\n", strings.ToUpper(item.shortID), item.date, item.time, item.payee)
	}

	return strings.TrimSpace(builder.String())
}

type pendingHintItem struct {
	shortID string
	date    string
	time    string
	payee   string
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
	if sentMsg, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	} else {
		b.trackBotMessage(userID, transactionID, sentMsg.MessageID)
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

	if sentMsg, sendErr := b.botAPI.Send(msg); sendErr != nil {
		logger.Errorf("发送消息失败: %v", sendErr)
	} else {
		b.trackBotMessage(userID, transactionID, sentMsg.MessageID)
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
