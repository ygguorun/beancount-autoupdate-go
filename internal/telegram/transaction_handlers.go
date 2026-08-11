package telegram

import (
	"fmt"
	"math/rand/v2"
	"path"
	"strings"
	"sync"
	"time"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// processImage 处理图片识别的公共逻辑
// message: 图片来源消息（用户发送的图片消息或Bot发送的图片消息）
// extraContextMessageIDs: 额外的对话上下文消息ID（如用户回复Bot图片的消息ID）
func (b *Bot) processImage(message *tgbotapi.Message, userID int, tempFile string, fileExt string, sourceType string, extraContextMessageIDs []int) {
	b.processImageCore(userID, tempFile, fileExt, sourceType, extraContextMessageIDs, message.MessageID, message.Chat.ID)
}

// ProcessExternalImage 处理外部 HTTP 上传图片并接入 Bot 识别流程。
func (b *Bot) ProcessExternalImage(userID int, tempFile string, fileExt string) {
	b.processImageCore(userID, tempFile, fileExt, "（HTTP上传）", nil, 0, int64(userID))
}

func (b *Bot) processImageCore(userID int, tempFile string, fileExt string, sourceType string, extraContextMessageIDs []int, sourceMessageID int, chatID int64) {
	logger.Infof("开始处理用户 %d 的图片，文件: %s, 来源: %s", userID, tempFile, sourceType)

	transactionID := fmt.Sprintf("%d_%s_%d", userID, time.Now().Format("20060102150405"), rand.IntN(10000))

	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("🔍 正在识别图片%s...", sourceType))
	if sourceMessageID > 0 {
		msg.ReplyToMessageID = sourceMessageID
	}
	var promptMessageID int
	if sentMsg, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	} else {
		promptMessageID = sentMsg.MessageID
	}

	tempFilenameTemplate := fmt.Sprintf("temp_{datetime}_{uuid}%s", fileExt)
	tempWebDAVPath := path.Join(b.config.WebDAV.Path, tempFilenameTemplate)
	logger.Infof("生成的临时文件名模板: %s", tempFilenameTemplate)
	logger.Infof("临时 WebDAV 路径: %s", tempWebDAVPath)

	var wg sync.WaitGroup
	var uploadResult string
	var parseResult *beancount.TransactionData
	var parseErr error
	var parseHistory []beancount.ConversationMessage

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

	wg.Add(1)
	go func() {
		defer wg.Done()

		b.llmSemaphore <- struct{}{}
		defer func() { <-b.llmSemaphore }()

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
		allAccounts := allAvailableAccounts(accounts)

		logger.Infof("获取到账户数量: 资产=%d, 负债=%d, 支出=%d, 收入=%d",
			len(accounts[beancount.AccountTypeAssets]),
			len(accounts[beancount.AccountTypeLiabilities]),
			len(accounts[beancount.AccountTypeExpenses]),
			len(accounts[beancount.AccountTypeIncome]))

		var history []beancount.ConversationMessage
		parseResult, history, parseErr = b.llmParser.ParseImageWithHistory(tempFile, allAccounts, []string{}, nil, "")
		switch {
		case parseErr != nil:
			logger.Errorf("LLM 解析失败: %v", parseErr)
		case parseResult == nil:
			logger.Warnf("LLM 解析返回空结果")
		default:
			logger.Infof("LLM 解析成功: payee=%s, narration=%s, postings=%d",
				parseResult.Payee, parseResult.Narration, len(parseResult.Postings))
			parseHistory = history
		}
	}()

	logger.Infof("等待并发任务完成...")
	wg.Wait()
	logger.Infof("并发任务完成")

	if parseErr != nil {
		logger.Errorf("解析图片%s失败: %v", sourceType, parseErr)
		b.createPendingTxWithError(userID, transactionID, tempFile, sourceMessageID, promptMessageID, extraContextMessageIDs)
		b.sendRecognitionErrorKeyboard(userID, transactionID, sourceMessageID)
		return
	}

	if parseResult == nil {
		logger.Warnf("解析结果为空")
		b.createPendingTxWithError(userID, transactionID, tempFile, sourceMessageID, promptMessageID, extraContextMessageIDs)
		b.sendRecognitionErrorKeyboard(userID, transactionID, sourceMessageID)
		return
	}

	logger.Infof("解析成功，准备处理交易数据")

	transactionTime, err := b.llmParser.ParseTime(parseResult.DateTime)
	if err != nil {
		logger.Warnf("解析日期失败: %v，使用当前时间", err)
		transactionTime = time.Now()
	} else {
		logger.Infof("解析日期成功: %s", transactionTime.Format("2006-01-02 15:04:05"))
	}

	actualTempWebDAVPath := tempWebDAVPath
	if uploadResult != "" && b.config.WebDAV.URL != "" {
		urlPrefix := strings.TrimSuffix(b.config.WebDAV.URL, "/") + "/"
		if path, ok := strings.CutPrefix(uploadResult, urlPrefix); ok {
			actualTempWebDAVPath = path
		}
	}

	b.mu.Lock()
	if b.pendingTx[userID] == nil {
		b.pendingTx[userID] = make(map[string]*beancount.PendingTransaction)
	}
	contextMessageIDs := make([]int, 0, len(extraContextMessageIDs)+1)
	contextMessageIDs = append(contextMessageIDs, extraContextMessageIDs...)
	b.pendingTx[userID][transactionID] = &beancount.PendingTransaction{
		TransactionID:                 transactionID,
		UserID:                        userID,
		Date:                          transactionTime.Format("2006-01-02"),
		Time:                          transactionTime.Format("15:04:05"),
		Flag:                          parseResult.Flag,
		Payee:                         parseResult.Payee,
		Narration:                     parseResult.Narration,
		Tags:                          parseResult.Tags,
		Postings:                      parseResult.Postings,
		OrderID:                       parseResult.OrderID,
		Extra:                         parseResult.Extra,
		OriginalTempFilePath:          tempFile,
		ImageURL:                      "",
		TempImageURL:                  uploadResult,
		TempWebDAVPath:                actualTempWebDAVPath,
		SpecialDirectives:             parseResult.SpecialDirectives,
		SourceImageMessageID:          sourceMessageID,
		ConversationContextMessageIDs: contextMessageIDs,
		ConversationHistory:           parseHistory,
	}
	if promptMessageID > 0 {
		b.pendingTx[userID][transactionID].ConversationContextMessageIDs = append(b.pendingTx[userID][transactionID].ConversationContextMessageIDs, promptMessageID)
	}
	b.mu.Unlock()
	b.persistSessionState()

	logger.Infof("已存储待确认交易: userID=%d, transactionID=%s, payee=%s, narration=%s", userID, transactionID, parseResult.Payee, parseResult.Narration)
	logger.Infof("准备显示预览")

	b.showTransactionPreview(userID, transactionID, 0)
	logger.Infof("预览显示完成")
}

// createPendingTxWithError 创建带错误的待确认交易（用于重新识别）
func (b *Bot) createPendingTxWithError(userID int, transactionID string, tempFile string, messageID int, promptMessageID int, extraContextMessageIDs []int) {
	b.mu.Lock()
	if b.pendingTx[userID] == nil {
		b.pendingTx[userID] = make(map[string]*beancount.PendingTransaction)
	}
	contextMessageIDs := make([]int, 0, len(extraContextMessageIDs)+1)
	contextMessageIDs = append(contextMessageIDs, extraContextMessageIDs...)
	b.pendingTx[userID][transactionID] = &beancount.PendingTransaction{
		TransactionID:                 transactionID,
		UserID:                        userID,
		OriginalTempFilePath:          tempFile,
		LastMessageID:                 messageID,
		OriginalMessageID:             messageID,
		ConversationContextMessageIDs: contextMessageIDs,
	}
	if promptMessageID > 0 {
		b.pendingTx[userID][transactionID].ConversationContextMessageIDs = append(b.pendingTx[userID][transactionID].ConversationContextMessageIDs, promptMessageID)
	}
	b.mu.Unlock()
	b.persistSessionState()
}

// sendRecognitionErrorKeyboard 发送识别错误的键盘
func (b *Bot) sendRecognitionErrorKeyboard(userID int, transactionID string, replyToMessageID int) {
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
	msg.ReplyToMessageID = replyToMessageID
	if sentMsg, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	} else {
		b.trackBotMessage(userID, transactionID, sentMsg.MessageID)
	}
}

// confirmTransaction 确认交易
func (b *Bot) confirmTransaction(userID int, transactionID string) {
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

	finalImageURL := data.TempImageURL
	if data.TempImageURL != "" && b.webdavMgr != nil {
		logger.Infof("准备重命名 WebDAV 文件")
		logger.Infof("临时文件路径: %s", data.TempWebDAVPath)
		logger.Infof("临时文件 URL: %s", data.TempImageURL)

		transactionTime, _ := time.Parse("2006-01-02 15:04:05", data.Date+" "+data.Time)
		logger.Infof("交易时间: %s", transactionTime)

		finalWebDAVPath := b.webdavMgr.GenerateReceiptPath(b.config.WebDAV.Path, transactionTime, data.OrderID, path.Ext(data.TempWebDAVPath))

		logger.Infof("目标路径: %s", finalWebDAVPath)
		logger.Infof("WebDAV URL: %s", b.config.WebDAV.URL)
		logger.Infof("WebDAV Path: %s", b.config.WebDAV.Path)

		logger.Infof("开始执行 WebDAV Move 操作...")
		success, err := b.webdavMgr.MoveFile(data.TempWebDAVPath, finalWebDAVPath)
		logger.Infof("WebDAV Move 结果: success=%v, err=%v", success, err)

		if success {
			logger.Infof("WebDAV 文件重命名成功")
			finalImageURL = buildRecordedWebDAVURL(b.config.WebDAV.URL, b.config.WebDAV.PublicURL, b.config.WebDAV.Path, finalWebDAVPath)
			logger.Infof("最终图片 URL: %s", finalImageURL)
		} else {
			logger.Errorf("WebDAV 文件重命名失败: %v", err)
			logger.Errorf("源路径: %s", data.TempWebDAVPath)
			logger.Errorf("目标路径: %s", finalWebDAVPath)
		}
	} else {
		logger.Infof("跳过 WebDAV 重命名: TempImageURL=%s, webdavMgr=%v", data.TempImageURL, b.webdavMgr != nil)
	}

	transactionTime, _ := time.Parse("2006-01-02 15:04:05", data.Date+" "+data.Time)

	var err error

	if len(data.SpecialDirectives) > 0 {
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
		_, err = b.beancountMgr.AddTransactionWithDirectives(transactionData)
	} else {
		_, err = b.beancountMgr.AddTransactionFromPostings(
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

	b.mu.Lock()
	data, ok = b.pendingTx[userID][transactionID]
	if ok {
		logger.Infof("确认交易，开始清理: transactionID=%s, PreviousMessageIDs=%v, OriginalMessageID=%d, SourceImageMessageID=%d, ConversationContextMessageIDs=%v",
			transactionID, data.PreviousMessageIDs, data.OriginalMessageID, data.SourceImageMessageID, data.ConversationContextMessageIDs)

		b.cleanupTransactionMessages(userID, transactionID, data, false, true)

		delete(b.pendingTx[userID], transactionID)
		logger.Infof("已从 pendingTx 中删除交易: transactionID=%s", transactionID)

		if len(b.pendingTx[userID]) == 0 {
			logger.Infof("用户 %d 没有其他待确认交易，删除用户 map", userID)
			delete(b.pendingTx, userID)
		}
	} else {
		logger.Warnf("交易不存在于 pendingTx 中: transactionID=%s", transactionID)
	}
	b.mu.Unlock()
	b.persistSessionState()

	response := fmt.Sprintf(
		"✅ 交易已记录\n\n🆔 交易ID: %s\n📅 日期: %s\n🏷️ 对象: %s\n\n🔒 详情已脱敏，如需核对请在账本或 Git 记录中查看",
		shortTransactionID(transactionID),
		data.Date,
		maskPayee(data.Payee),
	)
	b.sendMessageWithNilKeyboard(userID, response)
}

func buildRecordedWebDAVURL(uploadURL string, publicURL string, webdavPath string, finalWebDAVPath string) string {
	finalWebDAVPath = strings.TrimLeft(finalWebDAVPath, "/")
	if finalWebDAVPath == "" {
		if strings.TrimSpace(publicURL) != "" {
			return strings.TrimRight(publicURL, "/")
		}
		return strings.TrimRight(uploadURL, "/")
	}

	baseURL := strings.TrimRight(uploadURL, "/")
	pathForRecord := finalWebDAVPath

	if strings.TrimSpace(publicURL) != "" {
		baseURL = strings.TrimRight(publicURL, "/")
		trimmedWebDAVPath := strings.Trim(strings.TrimSpace(webdavPath), "/")
		if trimmedWebDAVPath != "" {
			prefix := trimmedWebDAVPath + "/"
			if path, ok := strings.CutPrefix(pathForRecord, prefix); ok {
				pathForRecord = path
			} else if pathForRecord == trimmedWebDAVPath {
				pathForRecord = ""
			}
		}
	}

	if pathForRecord == "" {
		return baseURL
	}

	return baseURL + "/" + pathForRecord
}

// cancelTransaction 取消交易
func (b *Bot) cancelTransaction(userID int, transactionID string) {
	logger.Infof("取消交易: userID=%d, transactionID=%s", userID, transactionID)

	b.mu.Lock()
	data, ok := b.pendingTx[userID][transactionID]
	if !ok {
		b.mu.Unlock()
		logger.Warnf("用户 %d 没有待确认的交易 %s（在 cancelTransaction 中）", userID, transactionID)
		b.sendMessageWithNilKeyboard(userID, "❌ 没有待确认的交易")
		return
	}

	logger.Infof("取消交易，开始清理: transactionID=%s, PreviousMessageIDs=%v, OriginalMessageID=%d, ConversationContextMessageIDs=%v",
		transactionID, data.PreviousMessageIDs, data.OriginalMessageID, data.ConversationContextMessageIDs)

	b.cleanupTransactionMessages(userID, transactionID, data, true, false)

	delete(b.pendingTx[userID], transactionID)
	logger.Infof("已从 pendingTx 中删除交易: transactionID=%s", transactionID)

	if len(b.pendingTx[userID]) == 0 {
		logger.Infof("用户 %d 没有其他待确认交易，删除用户 map", userID)
		delete(b.pendingTx, userID)
	}
	b.mu.Unlock()
	b.persistSessionState()

	b.sendMessageWithNilKeyboard(userID, "❌ 交易已取消")
}

func shortTransactionID(transactionID string) string {
	normalized := make([]rune, 0, len(transactionID))
	for _, r := range transactionID {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			normalized = append(normalized, r)
		}
	}

	if len(normalized) == 0 {
		return "NA"
	}

	if len(normalized) > 6 {
		normalized = normalized[len(normalized)-6:]
	}

	return strings.ToUpper(string(normalized))
}

func maskPayee(payee string) string {
	trimmed := strings.TrimSpace(payee)
	if trimmed == "" {
		return "未识别"
	}

	runes := []rune(trimmed)
	if len(runes) <= 2 {
		return string(runes[0]) + "*"
	}

	return string(runes[0]) + "**" + string(runes[len(runes)-1])
}
