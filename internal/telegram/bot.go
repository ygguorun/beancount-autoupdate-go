package telegram

import (
	"fmt"
	"strings"
	"sync"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/config"
	"beancount-autoupdate/internal/git"
	"beancount-autoupdate/internal/llm"
	"beancount-autoupdate/internal/logger"
	"beancount-autoupdate/internal/webdav"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot Telegram Bot
type Bot struct {
	config          *config.Config
	beancountMgr    *beancount.Manager
	llmParser       *llm.Parser
	gitMgr          *git.Manager
	webdavMgr       *webdav.Manager
	botAPI          *tgbotapi.BotAPI
	pendingTx       map[int]map[string]*beancount.PendingTransaction // userID -> transactionID -> PendingTransaction
	waitingForInput map[int]map[string]string                        // userID -> transactionID -> inputType
	mu              sync.RWMutex
	llmSemaphore    chan struct{} // LLM 信号量，控制并发调用
}

// NewBot 创建 Telegram Bot
func NewBot(
	cfg *config.Config,
	beancountMgr *beancount.Manager,
	llmParser *llm.Parser,
	gitMgr *git.Manager,
	webdavMgr *webdav.Manager,
) (*Bot, error) {
	botAPI, err := tgbotapi.NewBotAPI(cfg.Telegram.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot API: %w", err)
	}

	botAPI.Debug = false

	return &Bot{
		config:          cfg,
		beancountMgr:    beancountMgr,
		llmParser:       llmParser,
		gitMgr:          gitMgr,
		webdavMgr:       webdavMgr,
		botAPI:          botAPI,
		pendingTx:       make(map[int]map[string]*beancount.PendingTransaction),
		waitingForInput: make(map[int]map[string]string),
		llmSemaphore:    make(chan struct{}, 1), // 限制 LLM 并发为1
	}, nil
}

// Run 运行 Bot
func (b *Bot) Run() error {
	logger.Info("Bot is running...")

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

	// 处理回复 Bot 发送的图片
	if message.ReplyToMessage != nil && b.isBotPhotoMessage(message.ReplyToMessage) {
		b.handleReplyToBotPhoto(message)
		return
	}

	// 处理命令
	if message.IsCommand() {
		b.handleCommand(message)
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

// handleStartCommand 处理 /start 命令
func (b *Bot) handleStartCommand(message *tgbotapi.Message) {
	welcomeMessage := `👋 欢迎使用 Beancount 自动记账 Bot！

📸 发送账单截图或图片文件，我将自动识别并生成记账条目

🔧 可用命令：
/start - 显示欢迎信息
/help - 显示帮助信息
/accounts - 查看所有账户和分类
/pending - 查看待处理的交易列表
/cancel - 取消当前输入

💡 使用流程：
1. 发送账单截图或图片文件（可连续发送多张）
2. 查看识别结果
3. 如需修改，点击相应按钮
4. 确认后自动记账并同步到 Git

📌 提示：使用 /pending 查看所有待处理的交易
`
	b.sendReply(message, welcomeMessage)
}

// handleHelpCommand 处理 /help 命令
func (b *Bot) handleHelpCommand(message *tgbotapi.Message) {
	helpMessage := `📖 使用帮助

📸 发送账单截图或图片文件
- 支持聊天图片（直接发送图片）
- 支持图片文件（JPG、PNG、GIF、WebP 等）
- 文件大小限制：20MB
- 系统将自动识别日期、金额、交易对象等信息

🔧 交互修改
- 识别结果会显示预览界面
- 可以修改金额、账户、分类、备注等字段
- 支持确认提交或取消操作

🔄 多交易处理
- 支持同时处理多张图片
- 使用 /pending 查看待处理的交易列表
- 每个交易独立管理，不会互相影响

📝 记账规则
- 支出：从资产账户到支出账户
- 收入：从收入账户到资产账户
- 转账：从源账户到目标账户

💾 自动同步
- 确认后自动写入 Beancount 文件
- 自动提交并推送到 Git 仓库

❓ 问题反馈
如有问题请联系管理员
`
	b.sendReply(message, helpMessage)
}

// handleAccountsCommand 处理 /accounts 命令
func (b *Bot) handleAccountsCommand(message *tgbotapi.Message) {
	accounts := b.beancountMgr.GetAllCategories()

	var builder strings.Builder
	builder.WriteString("📊 账户和分类列表\n\n")

	typeNames := map[beancount.AccountType]string{
		beancount.AccountTypeAssets:      "💰 资产账户",
		beancount.AccountTypeExpenses:    "💸 支出分类",
		beancount.AccountTypeIncome:      "💵 收入分类",
		beancount.AccountTypeLiabilities: "💳 负债账户",
		beancount.AccountTypeEquity:      "📊 权益账户",
	}

	for accountType, typeName := range typeNames {
		if accs, ok := accounts[accountType]; ok && len(accs) > 0 {
			fmt.Fprintf(&builder, "%s:\n", typeName)
			for _, acc := range accs {
				fmt.Fprintf(&builder, "  • %s\n", acc)
			}
			builder.WriteString("\n")
		}
	}

	b.sendReply(message, builder.String())
}

// handleCancelCommand 处理 /cancel 命令
func (b *Bot) handleCancelCommand(message *tgbotapi.Message) {
	userID := int(message.From.ID)

	logger.Infof("处理 /cancel 命令: userID=%d", userID)

	b.mu.Lock()
	var transactionID string
	var inputType string
	if txMap, ok := b.waitingForInput[userID]; ok && len(txMap) > 0 {
		for tid, itype := range txMap {
			transactionID = tid
			inputType = itype
			break
		}
	}

	if transactionID != "" {
		delete(b.waitingForInput[userID], transactionID)
		if len(b.waitingForInput[userID]) == 0 {
			delete(b.waitingForInput, userID)
		}
		b.mu.Unlock()
		logger.Infof("已取消用户 %d 的输入等待: transactionID=%s, inputType=%s", userID, transactionID, inputType)

		// 检查是否有待确认的交易，如果有则返回预览页面
		b.mu.RLock()
		data, hasPendingTx := b.pendingTx[userID][transactionID]
		b.mu.RUnlock()

		if hasPendingTx && data != nil {
			messageID := data.OriginalMessageID
			if messageID == 0 {
				messageID = data.LastMessageID
			}
			logger.Infof("返回预览页面，使用 messageID=%d", messageID)
			b.showTransactionPreview(userID, transactionID, messageID)
		} else {
			b.sendReply(message, "❌ 已取消输入")
		}
		return
	}
	b.mu.Unlock()

	b.sendReply(message, "没有正在进行的操作")
}

// handlePendingCommand 处理 /pending 命令，显示待处理的交易列表
func (b *Bot) handlePendingCommand(message *tgbotapi.Message) {
	userID := int(message.From.ID)

	logger.Infof("处理 /pending 命令: userID=%d", userID)

	b.mu.RLock()
	txMap, hasPending := b.pendingTx[userID]
	b.mu.RUnlock()

	if !hasPending || len(txMap) == 0 {
		b.sendReply(message, "📋 当前没有待处理的交易")
		return
	}

	var builder strings.Builder
	builder.WriteString("📋 待处理的交易列表\n")
	builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// 遍历所有待处理的交易
	i := 1
	for transactionID, data := range txMap {
		fmt.Fprintf(&builder, "%d. %s %s\n", i, data.Date, data.Time)
		fmt.Fprintf(&builder, "   收款人: %s\n", data.Payee)
		fmt.Fprintf(&builder, "   描述: %s\n", data.Narration)

		// 计算总金额
		var totalAmount float64
		for _, posting := range data.Postings {
			if posting.Amount != "" {
				var amount float64
				if _, err := fmt.Sscanf(posting.Amount, "%f", &amount); err == nil {
					if amount > 0 {
						totalAmount += amount
					}
				}
			}
		}

		if totalAmount > 0 {
			fmt.Fprintf(&builder, "   金额: %.2f CNY\n", totalAmount)
		}

		fmt.Fprintf(&builder, "   ID: %s\n", transactionID)
		builder.WriteString("\n")
		i++
	}

	builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&builder, "共 %d 个待处理交易", len(txMap))

	b.sendReply(message, builder.String())
}

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

// handleCallback 处理回调查询
func (b *Bot) handleCallback(update tgbotapi.Update) {
	query := update.CallbackQuery
	userID := int(query.From.ID)
	callbackData := query.Data

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
	b.mu.RUnlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易 %s", userID, transactionID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	logger.Infof("找到待确认交易: transactionID=%s, payee=%s, narration=%s", transactionID, data.Payee, data.Narration)

	switch action {
	case "confirm":
		b.confirmTransaction(userID, transactionID, query.Message.MessageID)
	case "cancel":
		b.cancelTransaction(userID, transactionID, query.Message.MessageID)
	case "rerun_recognition":
		b.rerunRecognition(userID, transactionID, query.Message.MessageID)
	case "guided_retry":
		b.startGuidedRetry(userID, transactionID, query.Message.MessageID)
	}
}

// showTransactionPreview 显示交易预览
func (b *Bot) showTransactionPreview(userID int, transactionID string, messageID int) {
	logger.Infof("showTransactionPreview: userID=%d, transactionID=%s, messageID=%d", userID, transactionID, messageID)

	b.mu.Lock()
	data, ok := b.pendingTx[userID][transactionID]
	if ok {
		data.LastMessageID = messageID
		// 如果 OriginalMessageID 为 0，则设置为当前 messageID
		if data.OriginalMessageID == 0 && messageID > 0 {
			data.OriginalMessageID = messageID
		}
		logger.Infof("更新消息ID: LastMessageID=%d, OriginalMessageID=%d", data.LastMessageID, data.OriginalMessageID)
	}
	b.mu.Unlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易 %s（在 showTransactionPreview 中）", userID, transactionID)
		return
	}

	// 重新获取数据，确保数据是最新的
	b.mu.RLock()
	data, ok = b.pendingTx[userID][transactionID]
	b.mu.RUnlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易 %s（重新获取后）", userID, transactionID)
		return
	}

	var builder strings.Builder
	var keyboard tgbotapi.InlineKeyboardMarkup

	// 检查是否有特殊指令
	if len(data.SpecialDirectives) > 0 {
		// 有特殊指令，只显示特殊指令
		builder.WriteString("📋 特殊指令预览\n")
		builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Fprintf(&builder, "日期: %s\n", data.Date)
		fmt.Fprintf(&builder, "时间: %s\n", data.Time)
		fmt.Fprintf(&builder, "描述: %s\n", data.Narration)
		builder.WriteString("\n🔧 特殊指令:\n")
		for i, directive := range data.SpecialDirectives {
			fmt.Fprintf(&builder, "  %d. %s\n", i+1, directive)
		}
		builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		// 简化的键盘
		keyboard = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💬 引导重试", fmt.Sprintf("%s:guided_retry", transactionID)),
				tgbotapi.NewInlineKeyboardButtonData("🔄 重新识别", fmt.Sprintf("%s:rerun_recognition", transactionID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ 确认提交", fmt.Sprintf("%s:confirm", transactionID)),
				tgbotapi.NewInlineKeyboardButtonData("❌ 取消", fmt.Sprintf("%s:cancel", transactionID)),
			),
		)
	} else {
		// 没有特殊指令，显示标准交易预览
		builder.WriteString("📋 交易预览\n")
		builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Fprintf(&builder, "日期: %s\n", data.Date)
		fmt.Fprintf(&builder, "时间: %s\n", data.Time)
		fmt.Fprintf(&builder, "标志: %s\n", data.Flag)
		fmt.Fprintf(&builder, "收款人: %s\n", data.Payee)
		fmt.Fprintf(&builder, "描述: %s\n", data.Narration)

		if data.OrderID != "" {
			fmt.Fprintf(&builder, "订单号: %s\n", data.OrderID)
		}

		builder.WriteString("\n💰 金额信息:\n")
		if data.Extra != nil {
			if data.Extra["original_amount"] != "" {
				fmt.Fprintf(&builder, "  原始金额: %s CNY\n", data.Extra["original_amount"])
			}
			if data.Extra["discount"] != "" {
				fmt.Fprintf(&builder, "  优惠总额: %s CNY\n", data.Extra["discount"])
			}
		}

		if len(data.Tags) > 0 {
			fmt.Fprintf(&builder, "\n标签: %s\n", strings.Join(data.Tags, ", "))
		}

		builder.WriteString("\n分录:\n")
		for i, posting := range data.Postings {
			amount := posting.Amount
			if amount == "" {
				amount = "0.00"
			}
			currency := posting.Currency
			if currency == "" {
				currency = "CNY"
			}
			fmt.Fprintf(&builder, "  %d. %s: %s %s\n", i+1, posting.Account, amount, currency)
		}
		builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		// 简化的键盘
		keyboard = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💬 引导重试", fmt.Sprintf("%s:guided_retry", transactionID)),
				tgbotapi.NewInlineKeyboardButtonData("🔄 重新识别", fmt.Sprintf("%s:rerun_recognition", transactionID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ 确认提交", fmt.Sprintf("%s:confirm", transactionID)),
				tgbotapi.NewInlineKeyboardButtonData("❌ 取消", fmt.Sprintf("%s:cancel", transactionID)),
			),
		)
	}

	if messageID > 0 {
		logger.Infof("编辑消息: transactionID=%s, messageID=%d", transactionID, messageID)
		msg := tgbotapi.NewEditMessageText(int64(userID), messageID, builder.String())
		msg.ReplyMarkup = &keyboard
		if _, err := b.botAPI.Request(msg); err != nil {
			logger.Errorf("编辑消息失败: transactionID=%s, messageID=%d, error=%v", transactionID, messageID, err)
			// 如果编辑失败，尝试发送新消息
			b.sendNewTransactionPreview(userID, transactionID, builder.String(), &keyboard, messageID)
		} else {
			logger.Infof("编辑消息成功: transactionID=%s, messageID=%d", transactionID, messageID)
		}
	} else {
		b.sendNewTransactionPreview(userID, transactionID, builder.String(), &keyboard, 0)
	}
}

// sendNewTransactionPreview 发送新的交易预览消息
func (b *Bot) sendNewTransactionPreview(userID int, transactionID string, text string, keyboard *tgbotapi.InlineKeyboardMarkup, oldMessageID int) {
	msg := tgbotapi.NewMessage(int64(userID), text)
	msg.ReplyMarkup = keyboard
	sentMsg, err := b.botAPI.Send(msg)
	if err != nil {
		logger.Errorf("发送新消息失败: transactionID=%s, error=%v", transactionID, err)
		return
	}

	b.mu.Lock()
	if d, ok := b.pendingTx[userID][transactionID]; ok {
		if oldMessageID > 0 {
			logger.Infof("编辑失败，发送新消息: transactionID=%s, oldMessageID=%d, newMessageID=%d", transactionID, oldMessageID, sentMsg.MessageID)
		} else {
			logger.Infof("发送新消息: transactionID=%s, messageID=%d", transactionID, sentMsg.MessageID)
		}

		// 只删除当前交易相关的消息
		if len(d.PreviousMessageIDs) > 0 {
			logger.Infof("删除当前交易 %s 的之前消息: %v", transactionID, d.PreviousMessageIDs)
			b.deleteMessages(userID, d.PreviousMessageIDs)
			d.PreviousMessageIDs = []int{}
			d.OriginalMessageID = 0 // 原始消息已被删除
		}
		// 添加当前消息ID到PreviousMessageIDs
		d.PreviousMessageIDs = append(d.PreviousMessageIDs, sentMsg.MessageID)

		d.LastMessageID = sentMsg.MessageID
		if d.OriginalMessageID == 0 {
			d.OriginalMessageID = sentMsg.MessageID
		}
		logger.Infof("发送新消息成功: transactionID=%s, LastMessageID=%d, OriginalMessageID=%d, PreviousMessageIDs=%v", transactionID, d.LastMessageID, d.OriginalMessageID, d.PreviousMessageIDs)
	}
	b.mu.Unlock()
}