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
	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

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
		parseResult, history, parseErr = b.llmParser.ParseImageWithHistory(tempFile, allAccounts, []string{}, nil, "")
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
		b.createPendingTxWithError(userID, transactionID, tempFile, message.MessageID, promptMessageID)
		b.sendRecognitionErrorKeyboard(userID, message.MessageID)
		return
	}

	if parseResult == nil {
		logger.Warnf("解析结果为空")
		b.createPendingTxWithError(userID, transactionID, tempFile, message.MessageID, promptMessageID)
		b.sendRecognitionErrorKeyboard(userID, message.MessageID)
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
		// 从 URL 中提取相对路径
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
		ConversationHistory:   parseHistory,
		BotPromptMessageIDs:   []int{},
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

// createPendingTxWithError 创建带错误的待确认交易（用于重新识别）
func (b *Bot) createPendingTxWithError(userID int, transactionID string, tempFile string, messageID int, promptMessageID int) {
	b.mu.Lock()
	if b.pendingTx[userID] == nil {
		b.pendingTx[userID] = make(map[string]*beancount.PendingTransaction)
	}
	b.pendingTx[userID][transactionID] = &beancount.PendingTransaction{
		TransactionID:        transactionID,
		UserID:               userID,
		OriginalTempFilePath: tempFile,
		LastMessageID:        messageID,
		OriginalMessageID:    messageID,
		BotPromptMessageIDs:  []int{},
	}
	if promptMessageID > 0 {
		b.pendingTx[userID][transactionID].BotPromptMessageIDs = append(b.pendingTx[userID][transactionID].BotPromptMessageIDs, promptMessageID)
	}
	b.mu.Unlock()
}

// sendRecognitionErrorKeyboard 发送识别错误的键盘
func (b *Bot) sendRecognitionErrorKeyboard(userID int, replyToMessageID int) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💬 引导重试", "error:guided_retry"),
			tgbotapi.NewInlineKeyboardButtonData("🔄 重新识别", "error:rerun_recognition"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ 取消", "error:cancel"),
		),
	)
	msg := tgbotapi.NewMessage(int64(userID), "❌ 无法识别图片中的交易信息\n\n请确保图片清晰，包含完整的交易信息（日期、金额、交易对象等）\n或尝试重新上传。")
	msg.ReplyMarkup = &keyboard
	msg.ReplyToMessageID = replyToMessageID
	if _, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	}
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

	// 下载文件
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

	// 下载图片
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

		// 使用清理函数
		b.cleanupTransactionMessages(userID, transactionID, data, false, true)

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
		sendMessage = b.config.Telegram.SendDirectiveConfirmationMessage
		logger.Infof("特殊指令提交，SendDirectiveConfirmationMessage 配置: %v", sendMessage)
	} else {
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

	logger.Infof("取消交易，开始清理: transactionID=%s, PreviousMessageIDs=%v, OriginalMessageID=%d, UserInputMessageIDs=%v, BotPromptMessageIDs=%v",
		transactionID, data.PreviousMessageIDs, data.OriginalMessageID, data.UserInputMessageIDs, data.BotPromptMessageIDs)

	// 使用清理函数（删除WebDAV临时文件，保留用户原始图片）
	b.cleanupTransactionMessages(userID, transactionID, data, true, false)

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
	allAccounts := append(append(append(accounts[beancount.AccountTypeAssets], accounts[beancount.AccountTypeLiabilities]...), accounts[beancount.AccountTypeExpenses]...), accounts[beancount.AccountTypeIncome]...)

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

// isBotPhotoMessage 检查消息是否为 Bot 发送的图片消息
func (b *Bot) isBotPhotoMessage(msg *tgbotapi.Message) bool {
	if msg == nil || msg.From == nil {
		return false
	}

	// 检查是否来自 Bot
	if !msg.From.IsBot {
		return false
	}

	// 检查是否包含图片（聊天图片）
	if len(msg.Photo) > 0 {
		return true
	}

	// 检查是否为图片类型的 Document
	if msg.Document != nil && strings.HasPrefix(msg.Document.MimeType, "image/") {
		return true
	}

	return false
}

// handleReplyToBotPhoto 处理用户回复 Bot 发送的图片
func (b *Bot) handleReplyToBotPhoto(message *tgbotapi.Message) {
	userID := int(message.From.ID)

	// 检查用户权限
	if !b.config.IsUserAllowed(userID) {
		b.sendReply(message, "❌ 您没有权限使用此机器人")
		return
	}

	// 获取被回复的消息
	replyToMsg := message.ReplyToMessage
	if replyToMsg == nil {
		b.sendReply(message, "❌ 未找到被回复的消息")
		return
	}

	logger.Infof("用户 %d 回复了 Bot 发送的图片，消息ID: %d", userID, replyToMsg.MessageID)

	// 处理聊天图片
	if len(replyToMsg.Photo) > 0 {
		b.downloadAndProcessBotPhoto(message, userID, replyToMsg)
		return
	}

	// 处理图片文件
	if replyToMsg.Document != nil && strings.HasPrefix(replyToMsg.Document.MimeType, "image/") {
		b.downloadAndProcessBotDocument(message, userID, replyToMsg)
		return
	}

	b.sendReply(message, "❌ 被回复的消息不包含图片")
}

// downloadAndProcessBotPhoto 下载并处理 Bot 发送的聊天图片
func (b *Bot) downloadAndProcessBotPhoto(message *tgbotapi.Message, userID int, replyToMsg *tgbotapi.Message) {
	photos := replyToMsg.Photo
	photo := photos[len(photos)-1] // 获取最大尺寸的图片

	fileConfig := tgbotapi.FileConfig{
		FileID: photo.FileID,
	}

	file, err := b.botAPI.GetFile(fileConfig)
	if err != nil {
		logger.Errorf("Failed to get bot photo file: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	// 创建临时文件
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("temp_bot_%d_%d.jpg", time.Now().Unix(), userID))

	// 下载图片
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.botAPI.Token, file.FilePath)
	resp, err := http.Get(fileURL)
	if err != nil {
		logger.Errorf("Failed to download bot photo: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Errorf("关闭响应体失败: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		logger.Errorf("Failed to download bot photo, status: %d", resp.StatusCode)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	// 保存到临时文件
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("Failed to read bot photo: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		logger.Errorf("Failed to write bot photo: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	logger.Infof("成功下载 Bot 发送的图片，临时文件: %s", tempFile)

	// 调用公共处理函数（使用被回复的消息作为原始消息引用）
	b.processImage(replyToMsg, userID, tempFile, ".jpg", "（Bot发送）")
}

// downloadAndProcessBotDocument 下载并处理 Bot 发送的图片文件
func (b *Bot) downloadAndProcessBotDocument(message *tgbotapi.Message, userID int, replyToMsg *tgbotapi.Message) {
	document := replyToMsg.Document

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
		logger.Errorf("Failed to get bot document file: %v", err)
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
			ext = ".jpg"
		}
	}

	// 创建临时文件
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("temp_bot_doc_%d_%d%s", time.Now().Unix(), userID, ext))

	// 下载文件
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.botAPI.Token, file.FilePath)
	resp, err := http.Get(fileURL)
	if err != nil {
		logger.Errorf("Failed to download bot document: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Errorf("关闭响应体失败: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		logger.Errorf("Failed to download bot document, status: %d", resp.StatusCode)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	// 保存到临时文件
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("Failed to read bot document: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		logger.Errorf("Failed to write bot document: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	logger.Infof("成功下载 Bot 发送的图片文件，临时文件: %s, 原始文件名: %s", tempFile, document.FileName)

	// 调用公共处理函数（使用被回复的消息作为原始消息引用）
	b.processImage(replyToMsg, userID, tempFile, ext, "文件（Bot发送）")
}
