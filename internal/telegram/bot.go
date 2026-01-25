package telegram

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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
	pendingTx       map[int]*beancount.PendingTransaction
	waitingForInput map[int]string
	mu              sync.RWMutex
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
		pendingTx:       make(map[int]*beancount.PendingTransaction),
		waitingForInput: make(map[int]string),
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
	for i := 0; i < workerCount; i++ {
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

	// 处理文本输入
	if message.Text != "" {
		b.handleTextInput(message)
		return
	}

	// 处理照片
	if message.Photo != nil {
		b.handlePhoto(message)
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
	default:
		b.sendReply(message, "未知命令，请使用 /help 查看可用命令")
	}
}

// handleStartCommand 处理 /start 命令
func (b *Bot) handleStartCommand(message *tgbotapi.Message) {
	welcomeMessage := `👋 欢迎使用 Beancount 自动记账 Bot！

📸 发送账单截图，我将自动识别并生成记账条目

🔧 可用命令：
/start - 显示欢迎信息
/help - 显示帮助信息
/accounts - 查看所有账户和分类

💡 使用流程：
1. 发送账单截图
2. 查看识别结果
3. 如需修改，点击相应按钮
4. 确认后自动记账并同步到 Git
`
	b.sendReply(message, welcomeMessage)
}

// handleHelpCommand 处理 /help 命令
func (b *Bot) handleHelpCommand(message *tgbotapi.Message) {
	helpMessage := `📖 使用帮助

📸 发送账单截图
- 支持支付凭证、消费小票等图片
- 系统将自动识别日期、金额、交易对象等信息

🔧 交互修改
- 识别结果会显示预览界面
- 可以修改金额、账户、分类、备注等字段
- 支持确认提交或取消操作

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

	for accountType, typeNames := range typeNames {
		if accs, ok := accounts[accountType]; ok && len(accs) > 0 {
			builder.WriteString(fmt.Sprintf("%s:\n", typeNames))
			for _, acc := range accs {
				builder.WriteString(fmt.Sprintf("  • %s\n", acc))
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
	_, ok := b.waitingForInput[userID]
	if ok {
		delete(b.waitingForInput, userID)
		b.mu.Unlock()
		logger.Infof("已取消用户 %d 的输入等待", userID)

		// 检查是否有待确认的交易，如果有则返回预览页面
		b.mu.RLock()
		data, hasPendingTx := b.pendingTx[userID]
		b.mu.RUnlock()

		if hasPendingTx && data != nil {
			// 返回预览页面
			messageID := data.OriginalMessageID
			if messageID == 0 {
				messageID = data.LastMessageID
			}
			logger.Infof("返回预览页面，使用 messageID=%d", messageID)
			b.showTransactionPreview(userID, messageID)
		} else {
			b.sendReply(message, "❌ 已取消输入")
		}
		return
	}
	b.mu.Unlock()

	b.sendReply(message, "没有正在进行的操作")
}

// handlePhoto 处理照片
func (b *Bot) handlePhoto(message *tgbotapi.Message) {
	userID := int(message.From.ID)

	// 检查用户权限
	if !b.config.IsUserAllowed(userID) {
		b.sendReply(message, "❌ 您没有权限使用此机器人")
		return
	}

	// 下载图片
	photos := message.Photo
	if len(photos) == 0 {
		b.sendReply(message, "❌ 未找到图片")
		return
	}
	photo := photos[len(photos)-1] // 获取最大尺寸的图片

	fileConfig := tgbotapi.FileConfig{
		FileID: photo.FileID,
	}

	file, err := b.botAPI.GetFile(fileConfig)
	if err != nil {
		logger.Errorf("Failed to get file: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	// 创建临时文件
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("temp_%d_%d.jpg", time.Now().Unix(), userID))

	// 下载图片 - 使用正确的 Telegram Bot API URL
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.botAPI.Token, file.FilePath)
	resp, err := http.Get(fileURL)
	if err != nil {
		logger.Errorf("Failed to download file: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Errorf("Failed to download file, status: %d", resp.StatusCode)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	// 保存到临时文件
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("Failed to read file: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		logger.Errorf("Failed to write file: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}
	defer os.Remove(tempFile)

	b.sendReply(message, "🔍 正在识别图片...")

	logger.Infof("开始处理用户 %d 的图片，文件: %s", userID, tempFile)

	// 生成唯一的临时文件名，确保上传和重命名使用同一个文件名
	tempFilename := fmt.Sprintf("temp_%s_%d.jpg", time.Now().Format("20060102_150405"), userID)
	tempWebDAVPath := filepath.Join(b.config.WebDAV.Path, tempFilename)
	logger.Infof("生成的临时文件名: %s", tempFilename)
	logger.Infof("临时 WebDAV 路径: %s", tempWebDAVPath)

	// 使用 goroutine 并发处理图片上传和解析
	var wg sync.WaitGroup
	var uploadResult string
	var parseResult *beancount.TransactionData
	var parseErr error

	// 并发上传图片到 WebDAV
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Infof("开始上传图片到 WebDAV...")
		if b.webdavMgr != nil && b.config.WebDAV.Enabled {
			result, err := b.webdavMgr.UploadFile(tempFile, b.config.WebDAV.Path, tempFilename, time.Now(), "")
			if err != nil {
				logger.Errorf("WebDAV 上传失败: %v", err)
			} else {
				logger.Infof("WebDAV 上传成功: %s", result)
				uploadResult = result
			}
		} else {
			logger.Infof("WebDAV 未启用或未配置")
		}
	}()

	// 并发解析图片
	wg.Add(1)
	go func() {
		defer wg.Done()

		// 先执行 git pull，确保获取最新的资产账户信息
		logger.Infof("执行 git pull 获取最新账户信息...")
		if pulled, err := b.gitMgr.PullChanges(); err != nil {
			logger.Errorf("Git pull 失败: %v", err)
		} else if pulled {
			logger.Infof("Git pull 成功，已更新本地文件")
		} else {
			logger.Infof("没有需要拉取的更改")
		}

		logger.Infof("开始调用 LLM 解析图片...")
		accounts := b.beancountMgr.GetAllCategories()
		allAccounts := append(append(append(accounts[beancount.AccountTypeAssets], accounts[beancount.AccountTypeLiabilities]...), accounts[beancount.AccountTypeExpenses]...), accounts[beancount.AccountTypeIncome]...)

		logger.Infof("获取到账户数量: 资产=%d, 负债=%d, 支出=%d, 收入=%d",
			len(accounts[beancount.AccountTypeAssets]),
			len(accounts[beancount.AccountTypeLiabilities]),
			len(accounts[beancount.AccountTypeExpenses]),
			len(accounts[beancount.AccountTypeIncome]))

		parseResult, parseErr = b.llmParser.ParseImage(tempFile, accounts[beancount.AccountTypeExpenses], accounts[beancount.AccountTypeIncome], accounts[beancount.AccountTypeLiabilities], allAccounts, []string{})
		if parseErr != nil {
			logger.Errorf("LLM 解析失败: %v", parseErr)
		} else if parseResult == nil {
			logger.Warnf("LLM 解析返回空结果")
		} else {
			logger.Infof("LLM 解析成功: payee=%s, narration=%s, postings=%d",
				parseResult.Payee, parseResult.Narration, len(parseResult.Postings))
		}
	}()

	logger.Infof("等待并发任务完成...")
	wg.Wait()
	logger.Infof("并发任务完成")

	if parseErr != nil {
		logger.Errorf("解析图片失败: %v", parseErr)
		b.sendReply(message, "❌ 无法识别图片中的交易信息\n\n请确保图片清晰，包含完整的交易信息（日期、金额、交易对象等）\n或尝试重新上传。")
		return
	}

	if parseResult == nil {
		logger.Warnf("解析结果为空")
		b.sendReply(message, "❌ 无法识别图片中的交易信息")
		return
	}

	logger.Infof("解析成功，准备处理交易数据")

	// 解析日期
	transactionTime, err := b.llmParser.ParseTime(parseResult.DateTime)
	if err != nil {
		logger.Warnf("解析日期失败: %v，使用当前时间", err)
		transactionTime = time.Now()
	} else {
		logger.Infof("解析日期成功: %s", transactionTime.Format("2006-01-02 15:04:05"))
	}

	// 存储待确认的交易
	b.mu.Lock()
	b.pendingTx[userID] = &beancount.PendingTransaction{
		UserID:         userID,
		Date:           transactionTime.Format("2006-01-02"),
		Time:           transactionTime.Format("15:04:05"),
		Flag:           parseResult.Flag,
		Payee:          parseResult.Payee,
		Narration:      parseResult.Narration,
		Tags:           parseResult.Tags,
		Postings:       parseResult.Postings,
		OrderID:        parseResult.OrderID,
		Discount:       parseResult.Discount,
		OriginalAmount: parseResult.OriginalAmount,
		ImageURL:       "",
		TempImageURL:   uploadResult,
		TempWebDAVPath: tempWebDAVPath,
	}
	b.mu.Unlock()

	logger.Infof("已存储待确认交易: userID=%d, payee=%s, narration=%s", userID, parseResult.Payee, parseResult.Narration)
	logger.Infof("准备显示预览")

	// 显示预览（使用新的消息，不编辑原消息）
	b.showTransactionPreview(userID, 0)
	logger.Infof("预览显示完成")
}

// handleTextInput 处理文本输入
func (b *Bot) handleTextInput(message *tgbotapi.Message) {
	userID := int(message.From.ID)
	text := message.Text

	logger.Infof("收到文本输入: userID=%d, text=%s", userID, text)

	b.mu.RLock()
	inputType, ok := b.waitingForInput[userID]
	b.mu.RUnlock()

	if !ok {
		logger.Infof("用户 %d 不在等待输入状态", userID)
		return
	}

	logger.Infof("用户 %d 在等待输入: %s", userID, inputType)

	switch inputType {
	case "posting_amount":
		b.handlePostingAmountInput(userID, text)
	case "narration":
		b.handleNarrationInput(userID, text)
	case "payee":
		b.handlePayeeInput(userID, text)
	}

	b.mu.Lock()
	delete(b.waitingForInput, userID)
	b.mu.Unlock()
}

// handlePostingAmountInput 处理分录金额输入
func (b *Bot) handlePostingAmountInput(userID int, text string) {
	var messageID int
	var posting beancount.PostingData
	var editingPostingIndex int

	b.mu.Lock()
	data, ok := b.pendingTx[userID]
	if ok {
		if data.EditingPostingIndex >= 0 && data.EditingPostingIndex < len(data.Postings) {
			// 更新金额
			data.Postings[data.EditingPostingIndex].Amount = text
			posting = data.Postings[data.EditingPostingIndex]
			editingPostingIndex = data.EditingPostingIndex

			// 使用 EditingPostingMessageID 来编辑分录编辑界面
			messageID = data.EditingPostingMessageID
		}
	}
	b.mu.Unlock()

	if !ok || editingPostingIndex < 0 {
		return
	}

	// 返回分录编辑界面
	message := fmt.Sprintf("编辑分录 %d:\n\n账户: %s\n金额: %s %s\n\n请选择要修改的字段：",
		editingPostingIndex+1, posting.Account, posting.Amount, posting.Currency)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🏦 修改账户", "edit_posting_account"),
			tgbotapi.NewInlineKeyboardButtonData("💰 修改金额", "edit_posting_amount"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 返回", "back_to_preview"),
		),
	)

	if messageID > 0 {
		msg := tgbotapi.NewEditMessageText(int64(userID), messageID, message)
		msg.ReplyMarkup = &keyboard
		b.botAPI.Request(msg)
	} else {
		b.sendMessage(userID, message, keyboard)
	}
}

// handleNarrationInput 处理描述输入
func (b *Bot) handleNarrationInput(userID int, text string) {
	var messageID int

	b.mu.Lock()
	data, ok := b.pendingTx[userID]
	if ok {
		data.Narration = text
		// 使用 OriginalMessageID 来编辑预览消息
		messageID = data.OriginalMessageID
	}
	b.mu.Unlock()

	if !ok {
		return
	}

	b.showTransactionPreview(userID, messageID)
}

// handlePayeeInput 处理收款人输入
func (b *Bot) handlePayeeInput(userID int, text string) {
	logger.Infof("处理收款人输入: userID=%d, text=%s", userID, text)

	var messageID int

	b.mu.Lock()
	data, ok := b.pendingTx[userID]
	if ok {
		data.Payee = text
		logger.Infof("更新收款人: %s", text)
		// 使用 OriginalMessageID 来编辑预览消息
		messageID = data.OriginalMessageID
		logger.Infof("编辑预览消息，使用 messageID=%d (OriginalMessageID=%d, LastMessageID=%d)", messageID, data.OriginalMessageID, data.LastMessageID)
	}
	b.mu.Unlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易（在 handlePayeeInput 中）", userID)
		return
	}

	b.showTransactionPreview(userID, messageID)
}

// handleCallback 处理回调查询
func (b *Bot) handleCallback(update tgbotapi.Update) {
	query := update.CallbackQuery
	userID := int(query.From.ID)
	callbackData := query.Data

	logger.Infof("收到回调: userID=%d, callbackData=%s", userID, callbackData)

	// 回答回调
	callback := tgbotapi.NewCallback(query.ID, "")
	b.botAPI.Request(callback)

	b.mu.RLock()
	data, ok := b.pendingTx[userID]
	b.mu.RUnlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易", userID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	logger.Infof("找到待确认交易: payee=%s, narration=%s", data.Payee, data.Narration)

	switch callbackData {
	case "confirm":
		b.confirmTransaction(userID, query.Message.MessageID)
	case "cancel":
		b.cancelTransaction(userID, query.Message.MessageID)
	case "back_to_preview":
		b.showTransactionPreview(userID, query.Message.MessageID)
	case "back_to_edit_posting":
		b.mu.RLock()
		data := b.pendingTx[userID]
		index := data.EditingPostingIndex
		b.mu.RUnlock()
		b.editPosting(userID, query.Message.MessageID, fmt.Sprintf("edit_posting:%d", index))
	case "edit_postings":
		b.showPostingsEdit(userID, query.Message.MessageID)
	case "edit_posting_account":
		b.showPostingAccountSelection(userID, query.Message.MessageID)
	case "edit_posting_amount":
		logger.Infof("处理 edit_posting_amount 回调: userID=%d, messageID=%d", userID, query.Message.MessageID)
		b.mu.Lock()
		b.waitingForInput[userID] = "posting_amount"
		data := b.pendingTx[userID]
		data.LastMessageID = query.Message.MessageID
		logger.Infof("设置 waitingForInput=posting_amount, LastMessageID=%d, OriginalMessageID=%d", data.LastMessageID, data.OriginalMessageID)
		b.mu.Unlock()
		// 发送新消息而不是编辑当前消息
		msg := tgbotapi.NewMessage(int64(userID), "请输入新的金额：\n取消请输入 /cancel")
		b.botAPI.Send(msg)
	case "edit_narration":
		logger.Infof("处理 edit_narration 回调: userID=%d, messageID=%d", userID, query.Message.MessageID)
		b.mu.Lock()
		b.waitingForInput[userID] = "narration"
		data := b.pendingTx[userID]
		data.LastMessageID = query.Message.MessageID
		logger.Infof("设置 waitingForInput=narration, LastMessageID=%d, OriginalMessageID=%d", data.LastMessageID, data.OriginalMessageID)
		b.mu.Unlock()
		// 发送新消息而不是编辑当前消息
		msg := tgbotapi.NewMessage(int64(userID), "请输入新的描述：\n取消请输入 /cancel")
		b.botAPI.Send(msg)
	case "edit_payee":
		logger.Infof("处理 edit_payee 回调: userID=%d, messageID=%d", userID, query.Message.MessageID)
		b.mu.Lock()
		b.waitingForInput[userID] = "payee"
		data := b.pendingTx[userID]
		data.LastMessageID = query.Message.MessageID
		logger.Infof("设置 waitingForInput=payee, LastMessageID=%d, OriginalMessageID=%d", data.LastMessageID, data.OriginalMessageID)
		b.mu.Unlock()
		// 发送新消息而不是编辑当前消息
		msg := tgbotapi.NewMessage(int64(userID), "请输入新的收款人：\n取消请输入 /cancel")
		b.botAPI.Send(msg)
	default:
		if strings.HasPrefix(callbackData, "edit_posting:") {
			b.editPosting(userID, query.Message.MessageID, callbackData)
		} else if strings.HasPrefix(callbackData, "select_posting_account:") {
			b.selectPostingAccount(userID, query.Message.MessageID, callbackData)
		} else if strings.HasPrefix(callbackData, "account_page:") {
			parts := strings.Split(callbackData, ":")
			if len(parts) == 2 {
				if page, err := strconv.Atoi(parts[1]); err == nil {
					b.showAccountPage(userID, query.Message.MessageID, page)
				}
			}
		}
	}
}

// showTransactionPreview 显示交易预览
func (b *Bot) showTransactionPreview(userID, messageID int) {
	logger.Infof("showTransactionPreview: userID=%d, messageID=%d", userID, messageID)

	b.mu.Lock()
	data, ok := b.pendingTx[userID]
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
		logger.Warnf("用户 %d 没有待确认的交易（在 showTransactionPreview 中）", userID)
		return
	}

	// 重新获取数据，确保数据是最新的
	b.mu.RLock()
	data, ok = b.pendingTx[userID]
	b.mu.RUnlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易（重新获取后）", userID)
		return
	}

	var builder strings.Builder
	builder.WriteString("📋 交易预览\n")
	builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	builder.WriteString(fmt.Sprintf("日期: %s\n", data.Date))
	builder.WriteString(fmt.Sprintf("时间: %s\n", data.Time))
	builder.WriteString(fmt.Sprintf("标志: %s\n", data.Flag))
	builder.WriteString(fmt.Sprintf("收款人: %s\n", data.Payee))
	builder.WriteString(fmt.Sprintf("描述: %s\n", data.Narration))

	if data.OrderID != "" {
		builder.WriteString(fmt.Sprintf("订单号: %s\n", data.OrderID))
	}

	builder.WriteString("\n💰 金额信息:\n")
	if data.OriginalAmount != "" {
		builder.WriteString(fmt.Sprintf("  原始金额: %s CNY\n", data.OriginalAmount))
	}
	if data.Discount != "" {
		builder.WriteString(fmt.Sprintf("  优惠总额: %s CNY\n", data.Discount))
	}

	if len(data.Tags) > 0 {
		builder.WriteString(fmt.Sprintf("\n标签: %s\n", strings.Join(data.Tags, ", ")))
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
		builder.WriteString(fmt.Sprintf("  %d. %s: %s %s\n", i+1, posting.Account, amount, currency))
	}
	builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📝 修改描述", "edit_narration"),
			tgbotapi.NewInlineKeyboardButtonData("🏪 修改收款人", "edit_payee"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✏️ 编辑分录", "edit_postings"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ 确认提交", "confirm"),
			tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "cancel"),
		),
	)

	if messageID > 0 {
		logger.Infof("编辑消息: messageID=%d", messageID)
		msg := tgbotapi.NewEditMessageText(int64(userID), messageID, builder.String())
		msg.ReplyMarkup = &keyboard
		_, err := b.botAPI.Request(msg)
		if err != nil {
			logger.Errorf("编辑消息失败: %v", err)
			// 如果编辑失败，尝试发送新消息
			logger.Infof("尝试发送新消息")
			newMsg := tgbotapi.NewMessage(int64(userID), builder.String())
			newMsg.ReplyMarkup = &keyboard
			if sentMsg, sendErr := b.botAPI.Send(newMsg); sendErr == nil {
				b.mu.Lock()
				if d, ok := b.pendingTx[userID]; ok {
					// 删除之前的消息
					if len(d.PreviousMessageIDs) > 0 {
						logger.Infof("删除之前的消息: %v", d.PreviousMessageIDs)
						b.deleteMessages(userID, d.PreviousMessageIDs)
						d.PreviousMessageIDs = []int{}
					}
					// 添加当前消息ID到PreviousMessageIDs
					d.PreviousMessageIDs = append(d.PreviousMessageIDs, sentMsg.MessageID)

					d.LastMessageID = sentMsg.MessageID
					if d.OriginalMessageID == 0 {
						d.OriginalMessageID = sentMsg.MessageID
					}
					logger.Infof("发送新消息成功，更新 LastMessageID=%d, OriginalMessageID=%d", d.LastMessageID, d.OriginalMessageID)
				}
				b.mu.Unlock()
			} else {
				logger.Errorf("发送新消息也失败: %v", sendErr)
			}
		} else {
			logger.Infof("编辑消息成功")
		}
	} else {
		msg := tgbotapi.NewMessage(int64(userID), builder.String())
		msg.ReplyMarkup = &keyboard
		sentMsg, err := b.botAPI.Send(msg)
		if err == nil {
			b.mu.Lock()
			if d, ok := b.pendingTx[userID]; ok {
				// 删除之前的消息
				if len(d.PreviousMessageIDs) > 0 {
					logger.Infof("删除之前的消息: %v", d.PreviousMessageIDs)
					b.deleteMessages(userID, d.PreviousMessageIDs)
					d.PreviousMessageIDs = []int{}
				}
				// 添加当前消息ID到PreviousMessageIDs
				d.PreviousMessageIDs = append(d.PreviousMessageIDs, sentMsg.MessageID)

				d.LastMessageID = sentMsg.MessageID
				if d.OriginalMessageID == 0 {
					d.OriginalMessageID = sentMsg.MessageID
				}
				logger.Infof("发送新消息成功，更新 LastMessageID=%d, OriginalMessageID=%d", d.LastMessageID, d.OriginalMessageID)
			}
			b.mu.Unlock()
		}
	}
}

// showPostingsEdit 显示分录编辑界面
func (b *Bot) showPostingsEdit(userID, messageID int) {
	b.mu.Lock()
	data, ok := b.pendingTx[userID]
	if ok {
		data.LastMessageID = messageID
	}
	b.mu.Unlock()

	if !ok {
		return
	}

	var builder strings.Builder
	builder.WriteString("✏️ 编辑分录\n\n")

	for i, posting := range data.Postings {
		amount := posting.Amount
		if amount == "" {
			amount = "0.00"
		}
		currency := posting.Currency
		if currency == "" {
			currency = "CNY"
		}
		builder.WriteString(fmt.Sprintf("%d. %s: %s %s\n", i+1, posting.Account, amount, currency))
	}

	// 构建按钮
	var rows []tgbotapi.InlineKeyboardButton
	for i := range data.Postings {
		rows = append(rows, tgbotapi.NewInlineKeyboardButtonData(
			fmt.Sprintf("分录 %d", i+1),
			fmt.Sprintf("edit_posting:%d", i),
		))
	}

	// 每行 2 个按钮
	var keyboardRows [][]tgbotapi.InlineKeyboardButton
	for i := 0; i < len(rows); i += 2 {
		if i+1 < len(rows) {
			keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(rows[i], rows[i+1]))
		} else {
			keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(rows[i]))
		}
	}

	keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🔙 返回", "back_to_preview"),
	))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)

	if messageID > 0 {
		msg := tgbotapi.NewEditMessageText(int64(userID), messageID, builder.String())
		msg.ReplyMarkup = &keyboard
		b.botAPI.Request(msg)
	} else {
		msg := tgbotapi.NewMessage(int64(userID), builder.String())
		msg.ReplyMarkup = &keyboard
		sentMsg, err := b.botAPI.Send(msg)
		if err == nil {
			b.mu.Lock()
			if d, ok := b.pendingTx[userID]; ok {
				d.LastMessageID = sentMsg.MessageID
			}
			b.mu.Unlock()
		}
	}
}

// editPosting 编辑分录
func (b *Bot) editPosting(userID, messageID int, callbackData string) {
	parts := strings.Split(callbackData, ":")
	if len(parts) != 2 {
		return
	}

	index, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}

	b.mu.Lock()
	data, ok := b.pendingTx[userID]
	if ok {
		data.EditingPostingIndex = index
		data.LastMessageID = messageID
	}
	b.mu.Unlock()

	if !ok || index < 0 || index >= len(data.Postings) {
		return
	}

	posting := data.Postings[index]
	message := fmt.Sprintf("编辑分录 %d:\n\n账户: %s\n金额: %s %s\n\n请选择要修改的字段：",
		index+1, posting.Account, posting.Amount, posting.Currency)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🏦 修改账户", "edit_posting_account"),
			tgbotapi.NewInlineKeyboardButtonData("💰 修改金额", "edit_posting_amount"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 返回", "back_to_preview"),
		),
	)

	if messageID > 0 {
		msg := tgbotapi.NewEditMessageText(int64(userID), messageID, message)
		msg.ReplyMarkup = &keyboard
		b.botAPI.Request(msg)
		b.mu.Lock()
		if d, ok := b.pendingTx[userID]; ok {
			d.EditingPostingMessageID = messageID
		}
		b.mu.Unlock()
	} else {
		msg := tgbotapi.NewMessage(int64(userID), message)
		msg.ReplyMarkup = &keyboard
		sentMsg, err := b.botAPI.Send(msg)
		if err == nil {
			b.mu.Lock()
			if d, ok := b.pendingTx[userID]; ok {
				d.LastMessageID = sentMsg.MessageID
				d.EditingPostingMessageID = sentMsg.MessageID
			}
			b.mu.Unlock()
		}
	}
}

// showPostingAccountSelection 显示账户选择列表
func (b *Bot) showPostingAccountSelection(userID, messageID int) {
	b.mu.RLock()
	data := b.pendingTx[userID]
	b.mu.RUnlock()

	if data.EditingPostingIndex < 0 || data.EditingPostingIndex >= len(data.Postings) {
		return
	}

	accounts := b.beancountMgr.GetAllCategories()
	allAccounts := append(append(append(accounts[beancount.AccountTypeAssets], accounts[beancount.AccountTypeLiabilities]...), accounts[beancount.AccountTypeExpenses]...), accounts[beancount.AccountTypeIncome]...)

	data.AvailableAccounts = allAccounts
	data.AccountPage = 0

	b.showAccountPage(userID, messageID, 0)
}

// showAccountPage 显示账户分页
func (b *Bot) showAccountPage(userID, messageID int, page int) {
	b.mu.Lock()
	data, ok := b.pendingTx[userID]
	if ok {
		data.AccountPage = page
		data.LastMessageID = messageID
	}
	b.mu.Unlock()

	if !ok {
		return
	}

	pageSize := 8
	start := page * pageSize
	end := start + pageSize

	if end > len(data.AvailableAccounts) {
		end = len(data.AvailableAccounts)
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("🏦 选择账户 (第 %d 页)\n\n", page+1))

	for i := start; i < end; i++ {
		builder.WriteString(fmt.Sprintf("%d. %s\n", i+1, data.AvailableAccounts[i]))
	}

	var keyboardRows [][]tgbotapi.InlineKeyboardButton

	// 账户按钮
	for i := start; i < end; i++ {
		keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(
				data.AvailableAccounts[i],
				fmt.Sprintf("select_posting_account:%d", i),
			),
		))
	}

	// 翻页按钮
	var navRow []tgbotapi.InlineKeyboardButton
	if page > 0 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("⬅️ 上一页", fmt.Sprintf("account_page:%d", page-1)))
	}
	if end < len(data.AvailableAccounts) {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("下一页 ➡️", fmt.Sprintf("account_page:%d", page+1)))
	}
	if len(navRow) > 0 {
		keyboardRows = append(keyboardRows, navRow)
	}

	// 返回按钮
	keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🔙 返回", "back_to_edit_posting"),
	))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)

	if messageID > 0 {
		msg := tgbotapi.NewEditMessageText(int64(userID), messageID, builder.String())
		msg.ReplyMarkup = &keyboard
		b.botAPI.Request(msg)
	} else {
		msg := tgbotapi.NewMessage(int64(userID), builder.String())
		msg.ReplyMarkup = &keyboard
		sentMsg, err := b.botAPI.Send(msg)
		if err == nil {
			b.mu.Lock()
			if d, ok := b.pendingTx[userID]; ok {
				d.LastMessageID = sentMsg.MessageID
			}
			b.mu.Unlock()
		}
	}
}

// selectPostingAccount 选择账户
func (b *Bot) selectPostingAccount(userID, messageID int, callbackData string) {
	parts := strings.Split(callbackData, ":")
	if len(parts) != 2 {
		return
	}

	index, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}

	b.mu.Lock()
	data := b.pendingTx[userID]
	if index >= 0 && index < len(data.AvailableAccounts) {
		if data.EditingPostingIndex >= 0 && data.EditingPostingIndex < len(data.Postings) {
			data.Postings[data.EditingPostingIndex].Account = data.AvailableAccounts[index]
		}
	}
	b.mu.Unlock()

	b.editPosting(userID, messageID, fmt.Sprintf("edit_posting:%d", data.EditingPostingIndex))
}

// confirmTransaction 确认交易
func (b *Bot) confirmTransaction(userID, messageID int) {
	logger.Infof("确认交易: userID=%d", userID)

	b.mu.Lock()
	data, ok := b.pendingTx[userID]
	if !ok {
		b.mu.Unlock()
		logger.Warnf("用户 %d 没有待确认的交易（在 confirmTransaction 中）", userID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}
	b.mu.Unlock()

	// 重命名已上传的临时文件
	finalImageURL := data.TempImageURL
	if data.TempImageURL != "" && b.webdavMgr != nil {
		logger.Infof("准备重命名 WebDAV 文件")
		logger.Infof("临时文件路径: %s", data.TempWebDAVPath)
		logger.Infof("临时文件 URL: %s", data.TempImageURL)

		// 解析日期时间
		transactionTime, _ := time.Parse("2006-01-02 15:04:05", data.Date+" "+data.Time)
		logger.Infof("交易时间: %s", transactionTime)

		// 使用配置中的 filename_template 生成最终文件名
		finalFilename := b.webdavMgr.GenerateFilename(b.config.WebDAV.FilenameTemplate, transactionTime, data.OrderID)
		finalWebDAVPath := filepath.Join(b.config.WebDAV.Path, finalFilename)

		logger.Infof("目标路径: %s", finalWebDAVPath)
		logger.Infof("WebDAV URL: %s", b.config.WebDAV.URL)
		logger.Infof("WebDAV Path: %s", b.config.WebDAV.Path)
		logger.Infof("Filename Template: %s", b.config.WebDAV.FilenameTemplate)

		logger.Infof("开始执行 WebDAV Move 操作...")
		success, err := b.webdavMgr.MoveFile(data.TempWebDAVPath, finalWebDAVPath)
		logger.Infof("WebDAV Move 结果: success=%v, err=%v", success, err)

		if success {
			logger.Infof("WebDAV 文件重命名成功")
			// 构建最终 URL
			finalWebDAVPath = strings.TrimLeft(finalWebDAVPath, "/")
			if finalWebDAVPath == "" {
				finalImageURL = b.config.WebDAV.URL
			} else {
				finalImageURL = b.config.WebDAV.URL + "/" + finalWebDAVPath
			}
			logger.Infof("最终图片 URL: %s", finalImageURL)
		} else {
			logger.Errorf("WebDAV 文件重命名失败: %v", err)
			logger.Errorf("源路径: %s", data.TempWebDAVPath)
			logger.Errorf("目标路径: %s", finalWebDAVPath)
		}
	} else {
		logger.Infof("跳过 WebDAV 重命名: TempImageURL=%s, webdavMgr=%v", data.TempImageURL, b.webdavMgr != nil)
	}

	// 解析日期
	transactionTime, _ := time.Parse("2006-01-02 15:04:05", data.Date+" "+data.Time)

	// 添加交易到 Beancount
	entry, err := b.beancountMgr.AddTransactionFromPostings(
		transactionTime,
		data.Flag,
		data.Payee,
		data.Narration,
		data.Tags,
		data.Postings,
		data.OrderID,
		data.Discount,
		data.OriginalAmount,
		finalImageURL,
		nil,
	)

	if err != nil {
		logger.Errorf("Failed to add transaction: %v", err)
		b.sendMessageWithNilKeyboard(userID, fmt.Sprintf("❌ 提交失败: %v", err))
		return
	}

	// Git 提交和推送（使用 goroutine 异步执行）
	go func() {
		logger.Infof("开始 Git 操作...")
		logger.Infof("AutoCommit: %v, AutoPush: %v", b.config.Git.AutoCommit, b.config.Git.AutoPush)

		if b.config.Git.AutoCommit {
			commitMessage := fmt.Sprintf("%s %s", b.config.Git.CommitMessagePrefix, data.Narration)
			logger.Infof("执行 Git 提交: %s", commitMessage)

			if committed, err := b.gitMgr.CommitChanges(commitMessage); err != nil {
				logger.Errorf("Git 提交失败: %v", err)
			} else if committed {
				logger.Infof("Git 提交成功")
			} else {
				logger.Infof("没有更改需要提交")
			}

			if b.config.Git.AutoPush {
				logger.Infof("执行 Git 推送...")
				if pushed, err := b.gitMgr.PushChanges(); err != nil {
					logger.Errorf("Git 推送失败: %v", err)
				} else if pushed {
					logger.Infof("Git 推送成功")
				} else {
					logger.Infof("没有更改需要推送")
				}
			}
		} else {
			logger.Infof("Git 自动提交已禁用")
		}

		logger.Infof("Git 操作完成")
	}()

	// 清除待确认交易并删除所有预览消息
	b.mu.Lock()
	if data, ok := b.pendingTx[userID]; ok {
		// 删除所有预览消息
		if len(data.PreviousMessageIDs) > 0 {
			logger.Infof("删除所有预览消息: %v", data.PreviousMessageIDs)
			b.deleteMessages(userID, data.PreviousMessageIDs)
		}
		// 也删除原始预览消息
		if data.OriginalMessageID > 0 {
			logger.Infof("删除原始预览消息: %d", data.OriginalMessageID)
			deleteMsg := tgbotapi.NewDeleteMessage(int64(userID), data.OriginalMessageID)
			b.botAPI.Request(deleteMsg)
		}
	}
	delete(b.pendingTx, userID)
	b.mu.Unlock()

	response := fmt.Sprintf("✅ 交易已成功记录！\n\n📝 条目内容：\n%s", entry)
	b.sendMessageWithNilKeyboard(userID, response)
}

// cancelTransaction 取消交易
func (b *Bot) cancelTransaction(userID, messageID int) {
	logger.Infof("取消交易: userID=%d", userID)

	b.mu.Lock()
	defer b.mu.Unlock()

	data, ok := b.pendingTx[userID]
	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易（在 cancelTransaction 中）", userID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	// 删除临时文件
	if data.TempWebDAVPath != "" && b.webdavMgr != nil {
		b.webdavMgr.DeleteFile(data.TempWebDAVPath)
	}

	// 删除所有预览消息
	if len(data.PreviousMessageIDs) > 0 {
		logger.Infof("删除所有预览消息: %v", data.PreviousMessageIDs)
		b.deleteMessages(userID, data.PreviousMessageIDs)
	}
	// 也删除原始预览消息
	if data.OriginalMessageID > 0 {
		logger.Infof("删除原始预览消息: %d", data.OriginalMessageID)
		deleteMsg := tgbotapi.NewDeleteMessage(int64(userID), data.OriginalMessageID)
		b.botAPI.Request(deleteMsg)
	}

	delete(b.pendingTx, userID)
	logger.Infof("已删除待确认交易: userID=%d", userID)
	b.sendMessageWithNilKeyboard(userID, "❌ 交易已取消")
}

// sendReply 发送回复消息
func (b *Bot) sendReply(message *tgbotapi.Message, text string) {
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ReplyToMessageID = message.MessageID
	b.botAPI.Send(msg)
}

// sendMessage 发送消息
func (b *Bot) sendMessage(userID int, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(int64(userID), text)
	if keyboard.InlineKeyboard != nil {
		msg.ReplyMarkup = &keyboard
	}
	msg.ParseMode = ""
	b.botAPI.Send(msg)
}

// sendMessageWithNilKeyboard 发送不带键盘的消息
func (b *Bot) sendMessageWithNilKeyboard(userID int, text string) {
	msg := tgbotapi.NewMessage(int64(userID), text)
	msg.ParseMode = ""
	b.botAPI.Send(msg)
}

// deleteMessages 删除指定的消息列表
func (b *Bot) deleteMessages(userID int, messageIDs []int) {
	for _, messageID := range messageIDs {
		if messageID > 0 {
			deleteMsg := tgbotapi.NewDeleteMessage(int64(userID), messageID)
			b.botAPI.Request(deleteMsg)
		}
	}
}

// addMessageID 添加消息ID到PreviousMessageIDs列表
func (b *Bot) addMessageID(userID int, messageID int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if data, ok := b.pendingTx[userID]; ok {
		// 避免重复添加
		for _, id := range data.PreviousMessageIDs {
			if id == messageID {
				return
			}
		}
		data.PreviousMessageIDs = append(data.PreviousMessageIDs, messageID)
		logger.Infof("添加消息ID到PreviousMessageIDs: %d, 当前列表: %v", messageID, data.PreviousMessageIDs)
	}
}
