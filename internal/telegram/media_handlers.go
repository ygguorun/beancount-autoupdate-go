package telegram

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleDocument 处理文档（图片文件）
func (b *Bot) handleDocument(message *tgbotapi.Message) {
	document := message.Document
	userID := int(message.From.ID)

	if !b.config.IsUserAllowed(userID) {
		b.sendReply(message, "❌ 您没有权限使用此机器人")
		return
	}

	if !strings.HasPrefix(document.MimeType, "image/") {
		b.sendReply(message, "❌ 仅支持图片文件")
		return
	}

	if document.FileSize > 20*1024*1024 {
		b.sendReply(message, "❌ 文件过大，请上传小于 20MB 的图片")
		return
	}

	file, err := b.botAPI.GetFile(tgbotapi.FileConfig{FileID: document.FileID})
	if err != nil {
		logger.Errorf("Failed to get document file: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	ext := inferImageExtension(document.FileName, document.MimeType, "")
	if ext == "" {
		logger.Warnf("Unknown MIME type: %s", document.MimeType)
		b.sendReply(message, "❌ 不支持的图片格式")
		return
	}

	tempFile, err := b.downloadToTempFile(file.FilePath, "temp_doc", userID, ext)
	if err != nil {
		logger.Errorf("Failed to download document: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	logger.Infof("开始处理用户 %d 的图片文件，文件: %s, 原始文件名: %s", userID, tempFile, document.FileName)
	b.processImage(message, userID, tempFile, ext, "文件", nil)
}

// handlePhoto 处理照片
func (b *Bot) handlePhoto(message *tgbotapi.Message) {
	userID := int(message.From.ID)

	if !b.config.IsUserAllowed(userID) {
		b.sendReply(message, "❌ 您没有权限使用此机器人")
		return
	}

	photos := message.Photo
	if len(photos) == 0 {
		b.sendReply(message, "❌ 未找到图片")
		return
	}
	photo := photos[len(photos)-1]

	file, err := b.botAPI.GetFile(tgbotapi.FileConfig{FileID: photo.FileID})
	if err != nil {
		logger.Errorf("Failed to get file: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	tempFile, err := b.downloadToTempFile(file.FilePath, "temp", userID, ".jpg")
	if err != nil {
		logger.Errorf("Failed to download file: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	logger.Infof("开始处理用户 %d 的图片，文件: %s", userID, tempFile)
	b.processImage(message, userID, tempFile, ".jpg", "", nil)
}

// isBotPhotoMessage 检查消息是否为 Bot 发送的图片消息
func (b *Bot) isBotPhotoMessage(msg *tgbotapi.Message) bool {
	if msg == nil || msg.From == nil {
		return false
	}

	if !msg.From.IsBot {
		return false
	}

	if len(msg.Photo) > 0 {
		return true
	}

	if msg.Document != nil && strings.HasPrefix(msg.Document.MimeType, "image/") {
		return true
	}

	return false
}

// handleReplyToBotPhoto 处理用户回复 Bot 发送的图片
func (b *Bot) handleReplyToBotPhoto(message *tgbotapi.Message) {
	userID := int(message.From.ID)

	if !b.config.IsUserAllowed(userID) {
		b.sendReply(message, "❌ 您没有权限使用此机器人")
		return
	}

	replyToMsg := message.ReplyToMessage
	if replyToMsg == nil {
		b.sendReply(message, "❌ 未找到被回复的消息")
		return
	}

	logger.Infof("用户 %d 回复了 Bot 发送的图片，消息ID: %d", userID, replyToMsg.MessageID)

	if len(replyToMsg.Photo) > 0 {
		b.downloadAndProcessBotPhoto(message, userID, replyToMsg)
		return
	}

	if replyToMsg.Document != nil && strings.HasPrefix(replyToMsg.Document.MimeType, "image/") {
		b.downloadAndProcessBotDocument(message, userID, replyToMsg)
		return
	}

	b.sendReply(message, "❌ 被回复的消息不包含图片")
}

// downloadAndProcessBotPhoto 下载并处理 Bot 发送的聊天图片
func (b *Bot) downloadAndProcessBotPhoto(message *tgbotapi.Message, userID int, replyToMsg *tgbotapi.Message) {
	photos := replyToMsg.Photo
	photo := photos[len(photos)-1]

	file, err := b.botAPI.GetFile(tgbotapi.FileConfig{FileID: photo.FileID})
	if err != nil {
		logger.Errorf("Failed to get bot photo file: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	tempFile, err := b.downloadToTempFile(file.FilePath, "temp_bot", userID, ".jpg")
	if err != nil {
		logger.Errorf("Failed to download bot photo: %v", err)
		b.sendReply(message, "❌ 下载图片失败")
		return
	}

	logger.Infof("成功下载 Bot 发送的图片，临时文件: %s", tempFile)
	b.processImage(replyToMsg, userID, tempFile, ".jpg", "（Bot发送）", []int{message.MessageID})
}

// downloadAndProcessBotDocument 下载并处理 Bot 发送的图片文件
func (b *Bot) downloadAndProcessBotDocument(message *tgbotapi.Message, userID int, replyToMsg *tgbotapi.Message) {
	document := replyToMsg.Document

	if document.FileSize > 20*1024*1024 {
		b.sendReply(message, "❌ 文件过大，请上传小于 20MB 的图片")
		return
	}

	file, err := b.botAPI.GetFile(tgbotapi.FileConfig{FileID: document.FileID})
	if err != nil {
		logger.Errorf("Failed to get bot document file: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	ext := inferImageExtension(document.FileName, document.MimeType, ".jpg")
	tempFile, err := b.downloadToTempFile(file.FilePath, "temp_bot_doc", userID, ext)
	if err != nil {
		logger.Errorf("Failed to download bot document: %v", err)
		b.sendReply(message, "❌ 下载文件失败")
		return
	}

	logger.Infof("成功下载 Bot 发送的图片文件，临时文件: %s, 原始文件名: %s", tempFile, document.FileName)
	b.processImage(replyToMsg, userID, tempFile, ext, "文件（Bot发送）", []int{message.MessageID})
}

func inferImageExtension(fileName, mimeType, fallback string) string {
	ext := filepath.Ext(fileName)
	if ext != "" {
		return ext
	}

	switch mimeType {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return fallback
	}
}

func (b *Bot) downloadToTempFile(filePath string, prefix string, userID int, ext string) (string, error) {
	data, err := b.downloadTelegramFile(filePath)
	if err != nil {
		return "", err
	}

	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("%s_%d_%d%s", prefix, time.Now().Unix(), userID, ext))
	if err := os.WriteFile(tempFile, data, 0o644); err != nil {
		return "", err
	}

	return tempFile, nil
}

func (b *Bot) downloadTelegramFile(filePath string) ([]byte, error) {
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.botAPI.Token, filePath)
	resp, err := http.Get(fileURL)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Errorf("关闭响应体失败: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download file, status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return data, nil
}
