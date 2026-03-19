package telegram

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
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

	for accountType, typeNames := range typeNames {
		if accs, ok := accounts[accountType]; ok && len(accs) > 0 {
			fmt.Fprintf(&builder, "%s:\n", typeNames)
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
			// 返回预览页面
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

// processImage 处理图片识别的公共逻辑
func (b *Bot) processImage(message *tgbotapi.Message, userID int, tempFile string, fileExt string, sourceType string) {
	logger.Infof("开始处理用户 %d 的图片，文件: %s, 来源: %s", userID, tempFile, sourceType)

	// 先生成唯一的交易ID，包含随机数以避免冲突
	transactionID := fmt.Sprintf("%d_%s_%d", userID, time.Now().Format("20060102150405"), rand.Intn(10000))

	// 发送识别提示消息，并追踪消息ID
	msg := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("🔍 正在识别图片%s...", sourceType))
	msg.ReplyToMessageID = message.MessageID
	var promptMessageID int
	if sentMsg, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	} else {
		promptMessageID = sentMsg.MessageID
	}

	// 生成唯一的临时文件名，保留原始扩展名
	tempFilenameTemplate := fmt.Sprintf("temp_{datetime}_{uuid}%s", fileExt)
	tempWebDAVPath := filepath.Join(b.config.WebDAV.Path, tempFilenameTemplate)
	logger.Infof("生成的临时文件名模板: %s", tempFilenameTemplate)
	logger.Infof("临时 WebDAV 路径: %s", tempWebDAVPath)

	// 使用 goroutine 并发处理图片上传和解析
	var wg sync.WaitGroup
	var uploadResult string
	var parseResult *beancount.TransactionData
	var parseErr error
	var parseHistory []beancount.ConversationMessage

	// 并发上传图片到 WebDAV
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Infof("开始上传图片%s到 WebDAV...", sourceType)
		if b.webdavMgr != nil && b.config.WebDAV.Enabled {
			result, err := b.webdavMgr.UploadFile(tempFile, b.config.WebDAV.Path, tempFilenameTemplate, time.Now(), "")
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

		// 获取 LLM 信号量，控制并发
		b.llmSemaphore <- struct{}{}
		defer func() { <-b.llmSemaphore }()

		// 先执行 git pull，确保获取最新的资产账户信息
		logger.Infof("执行 git pull 获取最新账户信息...")
		if pulled, err := b.gitMgr.PullChanges(); err != nil {
			logger.Errorf("Git pull 失败: %v", err)
		} else if pulled {
			logger.Infof("Git pull 成功，已更新本地文件")
		} else {
			logger.Infof("没有需要拉取的更改")
		}

		logger.Infof("开始调用 LLM 解析图片%s...", sourceType)
		accounts := b.beancountMgr.GetAllCategories()
		allAccounts := append(append(append(accounts[beancount.AccountTypeAssets], accounts[beancount.AccountTypeLiabilities]...), accounts[beancount.AccountTypeExpenses]...), accounts[beancount.AccountTypeIncome]...)

		logger.Infof("获取到账户数量: 资产=%d, 负债=%d, 支出=%d, 收入=%d",
			len(accounts[beancount.AccountTypeAssets]),
			len(accounts[beancount.AccountTypeLiabilities]),
			len(accounts[beancount.AccountTypeExpenses]),
			len(accounts[beancount.AccountTypeIncome]))

		var history []beancount.ConversationMessage
		parseResult, history, parseErr = b.llmParser.ParseImageWithHistory(tempFile, allAccounts, []string{}, nil)
		if parseErr != nil {
			logger.Errorf("LLM 解析失败: %v", parseErr)
		} else if parseResult == nil {
			logger.Warnf("LLM 解析返回空结果")
		} else {
			logger.Infof("LLM 解析成功: payee=%s, narration=%s, postings=%d",
				parseResult.Payee, parseResult.Narration, len(parseResult.Postings))
			// 保存对话历史以便后续使用
			parseHistory = history
		}
	}()

	logger.Infof("等待并发任务完成...")
	wg.Wait()
	logger.Infof("并发任务完成")

	if parseErr != nil {
		logger.Errorf("解析图片%s失败: %v", sourceType, parseErr)
		// 创建临时 pendingTx 以支持重新识别
		b.mu.Lock()
		if b.pendingTx[userID] == nil {
			b.pendingTx[userID] = make(map[string]*beancount.PendingTransaction)
		}
		b.pendingTx[userID][transactionID] = &beancount.PendingTransaction{
			TransactionID:        transactionID,
			UserID:               userID,
			OriginalTempFilePath: tempFile,
			LastMessageID:        message.MessageID,
			OriginalMessageID:    message.MessageID,
			BotPromptMessageIDs:  []int{},
		}
		// 追踪识别提示消息
		if promptMessageID > 0 {
			b.pendingTx[userID][transactionID].BotPromptMessageIDs = append(b.pendingTx[userID][transactionID].BotPromptMessageIDs, promptMessageID)
		}
		b.mu.Unlock()
		// 发送带有重新识别选项的错误消息
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💬 引导重试", fmt.Sprintf("%s:guided_retry", transactionID)),
				tgbotapi.NewInlineKeyboardButtonData("🔄 重新识别", fmt.Sprintf("%s:rerun_recognition", transactionID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("❌ 取消", fmt.Sprintf("%s:cancel", transactionID)),
			),
		)
		msg := tgbotapi.NewMessage(int64(userID), "❌ 无法识别图片中的交易信息\n\n请确保图片清晰，包含完整的交易信息（日期、金额、交易对象等）\n或尝试重新上传。")
		msg.ReplyMarkup = &keyboard
		msg.ReplyToMessageID = message.MessageID
		if _, err := b.botAPI.Send(msg); err != nil {
			logger.Errorf("发送消息失败: %v", err)
		}
		return
	}

	if parseResult == nil {
		logger.Warnf("解析结果为空")
		// 创建临时 pendingTx 以支持重新识别
		b.mu.Lock()
		if b.pendingTx[userID] == nil {
			b.pendingTx[userID] = make(map[string]*beancount.PendingTransaction)
		}
		b.pendingTx[userID][transactionID] = &beancount.PendingTransaction{
			TransactionID:        transactionID,
			UserID:               userID,
			OriginalTempFilePath: tempFile,
			LastMessageID:        message.MessageID,
			OriginalMessageID:    message.MessageID,
			BotPromptMessageIDs:  []int{},
		}
		// 追踪识别提示消息
		if promptMessageID > 0 {
			b.pendingTx[userID][transactionID].BotPromptMessageIDs = append(b.pendingTx[userID][transactionID].BotPromptMessageIDs, promptMessageID)
		}
		b.mu.Unlock()
		// 发送带有重新识别选项的错误消息
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💬 引导重试", fmt.Sprintf("%s:guided_retry", transactionID)),
				tgbotapi.NewInlineKeyboardButtonData("🔄 重新识别", fmt.Sprintf("%s:rerun_recognition", transactionID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("❌ 取消", fmt.Sprintf("%s:cancel", transactionID)),
			),
		)
		msg := tgbotapi.NewMessage(int64(userID), "❌ 无法识别图片中的交易信息")
		msg.ReplyMarkup = &keyboard
		msg.ReplyToMessageID = message.MessageID
		if _, err := b.botAPI.Send(msg); err != nil {
			logger.Errorf("发送消息失败: %v", err)
		}
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
	// 从上传结果中提取实际的 WebDAV 路径
	actualTempWebDAVPath := tempWebDAVPath
	if uploadResult != "" && b.config.WebDAV.URL != "" {
		// 从 URL 中提取相对路径: https://dav.jianguoyun.com/dav/beancount/receipts/temp_xxx.jpg -> beancount/receipts/temp_xxx.jpg
		urlPrefix := strings.TrimSuffix(b.config.WebDAV.URL, "/") + "/"
		if strings.HasPrefix(uploadResult, urlPrefix) {
			actualTempWebDAVPath = strings.TrimPrefix(uploadResult, urlPrefix)
		}
	}

	b.mu.Lock()
	if b.pendingTx[userID] == nil {
		b.pendingTx[userID] = make(map[string]*beancount.PendingTransaction)
	}
	b.pendingTx[userID][transactionID] = &beancount.PendingTransaction{
		TransactionID:         transactionID,
		UserID:                userID,
		Date:                  transactionTime.Format("2006-01-02"),
		Time:                  transactionTime.Format("15:04:05"),
		Flag:                  parseResult.Flag,
		Payee:                 parseResult.Payee,
		Narration:             parseResult.Narration,
		Tags:                  parseResult.Tags,
		Postings:              parseResult.Postings,
		OrderID:               parseResult.OrderID,
		Extra:                 parseResult.Extra,
		OriginalTempFilePath:  tempFile,
		ImageURL:              "",
		TempImageURL:          uploadResult,
		TempWebDAVPath:        actualTempWebDAVPath,
		SpecialDirectives:     parseResult.SpecialDirectives,
		UserOriginalMessageID: message.MessageID,
		ConversationHistory:   parseHistory, // 保存对话历史
		BotPromptMessageIDs:   []int{},      // 初始化 Bot 提示消息 ID 列表
	}
	// 追踪识别提示消息
	if promptMessageID > 0 {
		b.pendingTx[userID][transactionID].BotPromptMessageIDs = append(b.pendingTx[userID][transactionID].BotPromptMessageIDs, promptMessageID)
	}
	b.mu.Unlock()

	logger.Infof("已存储待确认交易: userID=%d, transactionID=%s, payee=%s, narration=%s", userID, transactionID, parseResult.Payee, parseResult.Narration)
	logger.Infof("准备显示预览")

	// 显示预览（使用新的消息，不编辑原消息）
	b.showTransactionPreview(userID, transactionID, 0)
	logger.Infof("预览显示完成")
}

// handleDocument 处理文档（图片文件）
func (b *Bot) handleDocument(message *tgbotapi.Message) {
	document := message.Document
	userID := int(message.From.ID)

	// 检查用户权限
	if !b.config.IsUserAllowed(userID) {
		b.sendReply(message, "❌ 您没有权限使用此机器人")
		return
	}

	// 检查是否为图片文件
	if !strings.HasPrefix(document.MimeType, "image/") {
		b.sendReply(message, "❌ 仅支持图片文件")
		return
	}

	// 检查文件大小（限制 20MB）
	if document.FileSize > 20*1024*1024 {
		b.sendReply(message, "❌ 文件过大，请上传小于 20MB 的图片")
		return
	}

	// 获取文件
	fileConfig := tgbotapi.FileConfig{
		FileID: document.FileID,
	}

	file, err := b.botAPI.GetFile(fileConfig)
	if err != nil {
		logger.Errorf("Failed to get document file: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	// 确定文件扩展名
	ext := filepath.Ext(document.FileName)
	if ext == "" {
		// 从 MIME 类型推断扩展名
		switch document.MimeType {
		case "image/jpeg", "image/jpg":
			ext = ".jpg"
		case "image/png":
			ext = ".png"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		default:
			logger.Warnf("Unknown MIME type: %s", document.MimeType)
			return
		}
	}

	// 创建临时文件
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("temp_doc_%d_%d%s", time.Now().Unix(), userID, ext))

	// 下载文件 - 使用正确的 Telegram Bot API URL
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.botAPI.Token, file.FilePath)
	resp, err := http.Get(fileURL)
	if err != nil {
		logger.Errorf("Failed to download document: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Errorf("关闭响应体失败: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		logger.Errorf("Failed to download document, status: %d", resp.StatusCode)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	// 保存到临时文件
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("Failed to read document: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		logger.Errorf("Failed to write document: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}
	defer func() {
		if removeErr := os.Remove(tempFile); removeErr != nil {
			logger.Errorf("删除临时文件失败: %v", removeErr)
		}
	}()

	logger.Infof("开始处理用户 %d 的图片文件，文件: %s, 原始文件名: %s", userID, tempFile, document.FileName)

	// 调用公共处理函数
	b.processImage(message, userID, tempFile, ext, "文件")
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
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Errorf("关闭响应体失败: %v", closeErr)
		}
	}()

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

	logger.Infof("开始处理用户 %d 的图片，文件: %s", userID, tempFile)

	// 调用公共处理函数
	b.processImage(message, userID, tempFile, ".jpg", "")
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

		// 简化的键盘：包含确认、取消和重新识别
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

		// 简化的键盘：只包含引导重试、重新识别、确认、取消
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
			newMsg := tgbotapi.NewMessage(int64(userID), builder.String())
			newMsg.ReplyMarkup = &keyboard
			if sentMsg, sendErr := b.botAPI.Send(newMsg); sendErr == nil {
				b.mu.Lock()
				if d, ok := b.pendingTx[userID][transactionID]; ok {
					logger.Infof("编辑失败，发送新消息: transactionID=%s, oldMessageID=%d, newMessageID=%d", transactionID, messageID, sentMsg.MessageID)
					// 只删除当前交易相关的消息（不包括 messageID，因为编辑失败说明消息可能已不存在）
					if len(d.PreviousMessageIDs) > 0 {
						logger.Infof("删除当前交易 %s 的之前消息: %v", transactionID, d.PreviousMessageIDs)
						b.deleteMessages(userID, d.PreviousMessageIDs)
						d.PreviousMessageIDs = []int{}
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
			} else {
				logger.Errorf("发送新消息也失败: transactionID=%s, error=%v", transactionID, sendErr)
			}
		} else {
			logger.Infof("编辑消息成功: transactionID=%s, messageID=%d", transactionID, messageID)
		}
	} else {
		msg := tgbotapi.NewMessage(int64(userID), builder.String())
		msg.ReplyMarkup = &keyboard
		sentMsg, err := b.botAPI.Send(msg)
		if err == nil {
			b.mu.Lock()
			if d, ok := b.pendingTx[userID][transactionID]; ok {
				logger.Infof("发送新消息: transactionID=%s, messageID=%d", transactionID, sentMsg.MessageID)
				// 只删除当前交易相关的消息
				if len(d.PreviousMessageIDs) > 0 {
					logger.Infof("删除当前交易 %s 的之前消息: %v", transactionID, d.PreviousMessageIDs)
					b.deleteMessages(userID, d.PreviousMessageIDs)
					d.PreviousMessageIDs = []int{}
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
		} else {
			logger.Errorf("发送新消息失败: transactionID=%s, error=%v", transactionID, err)
		}
	}
}

// confirmTransaction 确认交易
func (b *Bot) confirmTransaction(userID int, transactionID string, messageID int) {
	logger.Infof("确认交易: userID=%d, transactionID=%s", userID, transactionID)

	b.mu.Lock()
	data, ok := b.pendingTx[userID][transactionID]
	if !ok {
		b.mu.Unlock()
		logger.Warnf("用户 %d 没有待确认的交易 %s（在 confirmTransaction 中）", userID, transactionID)
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

	var entry string
	var err error

	// 检查是否有特殊指令
	if len(data.SpecialDirectives) > 0 {
		// 使用包含特殊指令的方法
		transactionData := &beancount.TransactionData{
			DateTime:          data.Date + " " + data.Time,
			Flag:              data.Flag,
			Payee:             data.Payee,
			Narration:         data.Narration,
			Tags:              data.Tags,
			Postings:          data.Postings,
			OrderID:           data.OrderID,
			Extra:             data.Extra,
			SpecialDirectives: data.SpecialDirectives,
		}
		entry, err = b.beancountMgr.AddTransactionWithDirectives(transactionData)
	} else {
		// 使用标准交易记录方法
		entry, err = b.beancountMgr.AddTransactionFromPostings(
			transactionTime,
			data.Flag,
			data.Payee,
			data.Narration,
			data.Tags,
			data.Postings,
			data.OrderID,
			finalImageURL,
			data.Extra,
		)
	}

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
	data, ok = b.pendingTx[userID][transactionID]
	if ok {
		logger.Infof("确认交易，开始清理: transactionID=%s, PreviousMessageIDs=%v, OriginalMessageID=%d, UserOriginalMessageID=%d, UserInputMessageIDs=%v, BotPromptMessageIDs=%v",
			transactionID, data.PreviousMessageIDs, data.OriginalMessageID, data.UserOriginalMessageID, data.UserInputMessageIDs, data.BotPromptMessageIDs)

		// 删除所有预览消息（只删除当前交易的消息）
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

		// 也删除原始预览消息（如果存在且不在 PreviousMessageIDs 中）
		if data.OriginalMessageID > 0 {
			// 检查是否已经在 PreviousMessageIDs 中被删除
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

		// 根据配置删除用户发送的原始图片消息
		if b.config.Telegram.DeleteUserMessage && data.UserOriginalMessageID > 0 {
			logger.Infof("删除当前交易 %s 的用户原始图片消息: %d", transactionID, data.UserOriginalMessageID)
			deleteMsg := tgbotapi.NewDeleteMessage(int64(userID), data.UserOriginalMessageID)
			if _, err := b.botAPI.Request(deleteMsg); err != nil {
				logger.Errorf("删除消息失败: transactionID=%s, messageID=%d, error=%v", transactionID, data.UserOriginalMessageID, err)
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

		// 从 pendingTx 中删除当前交易
		delete(b.pendingTx[userID], transactionID)
		logger.Infof("已从 pendingTx 中删除交易: transactionID=%s", transactionID)

		// 如果用户没有其他待确认交易，删除用户的 map
		if len(b.pendingTx[userID]) == 0 {
			logger.Infof("用户 %d 没有其他待确认交易，删除用户 map", userID)
			delete(b.pendingTx, userID)
		}
	} else {
		logger.Warnf("交易不存在于 pendingTx 中: transactionID=%s", transactionID)
	}
	b.mu.Unlock()

	// 根据配置判断是否发送成功消息
	hasDirectives := len(data.SpecialDirectives) > 0
	sendMessage := false

	if hasDirectives {
		// 有特殊指令，使用 send_directive_confirmation_message 配置
		sendMessage = b.config.Telegram.SendDirectiveConfirmationMessage
		logger.Infof("特殊指令提交，SendDirectiveConfirmationMessage 配置: %v", sendMessage)
	} else {
		// 普通交易，使用 send_confirmation_message 配置
		sendMessage = b.config.Telegram.SendConfirmationMessage
		logger.Infof("普通交易提交，SendConfirmationMessage 配置: %v", sendMessage)
	}

	if sendMessage {
		response := fmt.Sprintf("✅ 交易已成功记录！\n\n📝 条目内容：\n%s", entry)
		b.sendMessageWithNilKeyboard(userID, response)
	} else {
		logger.Infof("根据配置跳过发送成功消息")
	}
}

// cancelTransaction 取消交易
func (b *Bot) cancelTransaction(userID int, transactionID string, messageID int) {
	logger.Infof("取消交易: userID=%d, transactionID=%s", userID, transactionID)

	b.mu.Lock()
	defer b.mu.Unlock()

	data, ok := b.pendingTx[userID][transactionID]
	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易 %s（在 cancelTransaction 中）", userID, transactionID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	logger.Infof("取消交易，开始清理: transactionID=%s, PreviousMessageIDs=%v, OriginalMessageID=%d, UserOriginalMessageID=%d",
		transactionID, data.PreviousMessageIDs, data.OriginalMessageID, data.UserOriginalMessageID)

	// 删除 WebDAV 临时文件
	if data.TempWebDAVPath != "" && b.webdavMgr != nil {
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

	// 删除所有预览消息（只删除当前交易的消息）
	if len(data.PreviousMessageIDs) > 0 {
		logger.Infof("删除当前交易 %s 的预览消息: %v", transactionID, data.PreviousMessageIDs)
		b.deleteMessages(userID, data.PreviousMessageIDs)
	}

	// 也删除原始预览消息（如果存在且不在 PreviousMessageIDs 中）
	if data.OriginalMessageID > 0 {
		// 检查是否已经在 PreviousMessageIDs 中被删除
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

	// 根据配置删除用户发送的原始图片消息
	if b.config.Telegram.DeleteUserMessage && data.UserOriginalMessageID > 0 {
		logger.Infof("删除当前交易 %s 的用户原始图片消息: %d", transactionID, data.UserOriginalMessageID)
		deleteMsg := tgbotapi.NewDeleteMessage(int64(userID), data.UserOriginalMessageID)
		if _, err := b.botAPI.Request(deleteMsg); err != nil {
			logger.Errorf("删除消息失败: transactionID=%s, messageID=%d, error=%v", transactionID, data.UserOriginalMessageID, err)
		}
	}

	// 从 pendingTx 中删除当前交易
	delete(b.pendingTx[userID], transactionID)
	logger.Infof("已从 pendingTx 中删除交易: transactionID=%s", transactionID)

	// 如果用户没有其他待确认交易，删除用户的 map
	if len(b.pendingTx[userID]) == 0 {
		logger.Infof("用户 %d 没有其他待确认交易，删除用户 map", userID)
		delete(b.pendingTx, userID)
	}

	b.sendMessageWithNilKeyboard(userID, "❌ 交易已取消")
}

// sendReply 发送回复消息
func (b *Bot) sendReply(message *tgbotapi.Message, text string) {
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ReplyToMessageID = message.MessageID
	if _, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	}
}

// sendMessage 发送消息
func (b *Bot) sendMessage(userID int, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(int64(userID), text)
	if keyboard.InlineKeyboard != nil {
		msg.ReplyMarkup = &keyboard
	}
	msg.ParseMode = ""
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

	// 执行 git pull，确保获取最新的资产账户信息
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
	allAccounts := append(append(append(accounts[beancount.AccountTypeAssets], accounts[beancount.AccountTypeLiabilities]...), accounts[beancount.AccountTypeExpenses]...), accounts[beancount.AccountTypeIncome]...)

	parseResult, newHistory, err := b.llmParser.ParseImageWithHistory(data.OriginalTempFilePath, allAccounts, []string{}, nil)
	if err != nil {
		logger.Errorf("LLM 重新解析失败: %v", err)
		// 发送带有重新识别选项的错误消息
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
		return
	}

	if parseResult == nil {
		logger.Warnf("LLM 重新解析返回空结果")
		// 发送带有重新识别选项的错误消息
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💬 引导重试", fmt.Sprintf("%s:guided_retry", transactionID)),
				tgbotapi.NewInlineKeyboardButtonData("🔄 重新识别", fmt.Sprintf("%s:rerun_recognition", transactionID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("❌ 取消", fmt.Sprintf("%s:cancel", transactionID)),
			),
		)
		msg := tgbotapi.NewMessage(int64(userID), "❌ 无法识别图片中的交易信息")
		msg.ReplyMarkup = &keyboard
		if _, err := b.botAPI.Send(msg); err != nil {
			logger.Errorf("发送消息失败: %v", err)
		}
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
		data.ConversationHistory = newHistory // 更新对话历史
		// 保留 OriginalMessageID 和 UserOriginalMessageID，重置其他消息ID
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
			historyInfo = fmt.Sprintf("已尝试 %d 次", int(len(data.ConversationHistory)/2))
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
		// 追踪引导提示消息
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

	// 检查临时文件
	if data.OriginalTempFilePath == "" {
		b.sendMessageWithNilKeyboard(userID, "❌ 无法重新识别：原始图片文件不存在")
		return
	}

	// 发送处理中提示，并追踪消息ID
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
	allAccounts := append(append(append(
		accounts[beancount.AccountTypeAssets],
		accounts[beancount.AccountTypeLiabilities]...),
		accounts[beancount.AccountTypeExpenses]...),
		accounts[beancount.AccountTypeIncome]...)

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
		// 保留历史，允许继续重试
		b.mu.Lock()
		if d, ok := b.pendingTx[userID][transactionID]; ok {
			// 添加用户引导到历史
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
		msg := tgbotapi.NewMessage(int64(userID),
			fmt.Sprintf("❌ 引导重试失败: %v\n\n您可以继续输入引导文字或重新识别。", err))
		msg.ReplyMarkup = &keyboard
		if _, sendErr := b.botAPI.Send(msg); sendErr != nil {
			logger.Errorf("发送消息失败: %v", sendErr)
		}
		return
	}

	if parseResult == nil {
		logger.Warnf("引导重试返回空结果")
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
		msg := tgbotapi.NewMessage(int64(userID), "❌ 无法识别图片中的交易信息\n\n您可以继续输入引导文字或重新识别。")
		msg.ReplyMarkup = &keyboard
		if _, sendErr := b.botAPI.Send(msg); sendErr != nil {
			logger.Errorf("发送消息失败: %v", sendErr)
		}
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
		d.ConversationHistory = newHistory // 保存新历史
	}
	b.mu.Unlock()

	logger.Infof("引导重试成功: userID=%d, transactionID=%s, payee=%s, narration=%s", userID, transactionID, parseResult.Payee, parseResult.Narration)

	// 显示新预览
	b.showTransactionPreview(userID, transactionID, 0)
}
