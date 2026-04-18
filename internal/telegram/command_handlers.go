package telegram

import (
	"fmt"
	"sort"
	"strings"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

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
- 可以通过“💬 引导重试”告诉 Bot 如何修正识别结果
- 也可以使用“🔄 重新识别”让 Bot 重新解析图片
- 支持确认提交或取消操作

🔄 多交易处理
- 支持同时处理多张图片
- 使用 /pending 查看待处理的交易列表
- 每个交易独立管理，不会互相影响
- 多笔待处理时，可用 #短ID 指定要修改的交易

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
		b.persistSessionState()
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

	entries := buildPendingListEntries(txMap)
	if len(entries) == 0 {
		b.sendReply(message, "📋 当前没有待处理的交易")
		return
	}

	var builder strings.Builder
	builder.WriteString("📋 待处理的交易列表\n")
	builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	for i, item := range entries {
		transactionID := item.transactionID
		data := item.data
		fmt.Fprintf(&builder, "%d. %s %s\n", i+1, data.Date, data.Time)
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

		fmt.Fprintf(&builder, "   短ID: #%s\n", strings.ToUpper(shortTransactionID(transactionID)))
		builder.WriteString("\n")
	}

	builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&builder, "共 %d 个待处理交易\n", len(entries))
	builder.WriteString("💡 可直接回复预览消息输入修改意见，或使用：#短ID 修改内容\n")
	builder.WriteString("👉 也可点击下方按钮重新打开确认卡片")

	pendingMsg := tgbotapi.NewMessage(message.Chat.ID, builder.String())
	pendingMsg.ReplyToMessageID = message.MessageID
	if keyboard, buttonCount := buildPendingPreviewKeyboard(entries, 12); keyboard != nil {
		pendingMsg.ReplyMarkup = keyboard
		if buttonCount < len(entries) {
			pendingMsg.Text += fmt.Sprintf("\n\n⚠️ 仅显示前 %d 条快捷按钮，请用 #短ID 访问其余交易。", buttonCount)
		}
	}

	if _, err := b.botAPI.Send(pendingMsg); err != nil {
		logger.Errorf("发送 pending 列表失败: userID=%d, error=%v", userID, err)
	}
}

func (b *Bot) openPendingPreview(userID int, transactionID string) {
	b.mu.RLock()
	data, ok := b.pendingTx[userID][transactionID]
	b.mu.RUnlock()

	if !ok || data == nil {
		logger.Warnf("用户 %d 没有待确认的交易 %s（在 openPendingPreview 中）", userID, transactionID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	messageID := data.LastMessageID
	if messageID == 0 {
		messageID = data.OriginalMessageID
	}

	logger.Infof("从 pending 打开交易预览: userID=%d, transactionID=%s, messageID=%d", userID, transactionID, messageID)
	b.showTransactionPreview(userID, transactionID, messageID)
}

type pendingListEntry struct {
	transactionID string
	data          *beancount.PendingTransaction
}

func buildPendingListEntries(txMap map[string]*beancount.PendingTransaction) []pendingListEntry {
	entries := make([]pendingListEntry, 0, len(txMap))
	for transactionID, data := range txMap {
		if data == nil {
			continue
		}
		entries = append(entries, pendingListEntry{transactionID: transactionID, data: data})
	}

	sort.Slice(entries, func(i, j int) bool {
		left := strings.TrimSpace(entries[i].data.Date + " " + entries[i].data.Time)
		right := strings.TrimSpace(entries[j].data.Date + " " + entries[j].data.Time)
		if left == right {
			return entries[i].transactionID < entries[j].transactionID
		}
		return left > right
	})

	return entries
}

func buildPendingPreviewKeyboard(entries []pendingListEntry, maxButtons int) (*tgbotapi.InlineKeyboardMarkup, int) {
	if len(entries) == 0 || maxButtons <= 0 {
		return nil, 0
	}

	if maxButtons > len(entries) {
		maxButtons = len(entries)
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, maxButtons)
	for i := 0; i < maxButtons; i++ {
		transactionID := entries[i].transactionID
		shortID := strings.ToUpper(shortTransactionID(transactionID))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("打开确认 #%s", shortID), fmt.Sprintf("%s:open_preview", transactionID)),
		))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return &keyboard, maxButtons
}
