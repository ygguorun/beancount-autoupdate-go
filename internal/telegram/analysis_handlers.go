package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"beancount-autoupdate/internal/analysis"
	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const telegramMessageLimit = 3800

// handleAnalyzeCommand 处理 /analyze 命令。
func (b *Bot) handleAnalyzeCommand(message *tgbotapi.Message) {
	if b.analysisSvc == nil || !b.analysisSvc.Enabled() {
		b.sendReply(message, "📊 分析功能未启用，请先在配置中开启 [analysis].enabled")
		return
	}

	userID := int(message.From.ID)
	query := strings.TrimSpace(message.CommandArguments())
	logger.Debugf("收到 /analyze 命令: userID=%d query=%q", userID, query)
	if strings.EqualFold(query, "reset") || query == "重置" {
		b.analysisSvc.ResetSession(userID)
		b.sendReply(message, "🧹 已重置分析会话。可继续使用 /analyze <问题> 开始新会话。")
		return
	}

	if query == "" {
		helpText := "请在 /analyze 后输入分析问题，例如：\n" +
			"- /analyze 本月餐饮支出最多的是哪些？\n" +
			"- /analyze 最近三个月储蓄率趋势\n" +
			"- /analyze 检查账本是否有错误\n\n" +
			"会话中可直接继续追问；使用 /analyze reset 结束会话。"
		b.sendReply(message, helpText)
		return
	}

	b.runAnalysisAndReply(message.Chat.ID, userID, message.MessageID, query, true)
}

func (b *Bot) tryHandleAnalysisText(message *tgbotapi.Message, text string) bool {
	if b.analysisSvc == nil || !b.analysisSvc.Enabled() {
		return false
	}

	userID := int(message.From.ID)
	if b.hasPendingOrWaiting(userID) {
		return false
	}

	if !b.analysisSvc.IsSessionActive(userID) {
		return false
	}
	logger.Debugf("分析会话追问: userID=%d text=%q", userID, text)

	b.runAnalysisAndReply(message.Chat.ID, userID, message.MessageID, text, false)
	return true
}

func (b *Bot) runAnalysisAndReply(chatID int64, userID int, replyToMessageID int, query string, fromCommand bool) {
	logger.Debugf("开始执行分析请求: userID=%d fromCommand=%v query=%q", userID, fromCommand, query)
	if fromCommand {
		b.sendReplyToChat(chatID, replyToMessageID, "📊 正在分析账单问题，请稍候...")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result, err := b.analysisSvc.AnalyzeAgent(ctx, userID, query, time.Now())

	if err != nil {
		if errors.Is(err, analysis.ErrAgentDisabled) {
			helpText := "分析 Agent 未启用或缺少 LLM 配置。请检查 [analysis].agent_enabled、LLM API Key 与模型设置。"
			b.sendReplyToChat(chatID, replyToMessageID, helpText)
			return
		}

		logger.Errorf("报表分析失败: userID=%d, query=%q, err=%v", userID, query, err)
		b.sendReplyToChat(chatID, replyToMessageID, fmt.Sprintf("❌ 分析失败: %v", err))
		return
	}

	logger.Debugf("分析请求完成: userID=%d sections=%d summary_len=%d", userID, len(result.Sections), len(strings.TrimSpace(result.Summary)))

	msg := formatAnalysisMessage(result)
	b.sendLongText(chatID, replyToMessageID, msg)
}

func formatAnalysisMessage(result *analysis.Result) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "📊 %s\n\n", result.Title)
	builder.WriteString("【总结】\n")
	builder.WriteString(strings.TrimSpace(result.Summary))
	builder.WriteString("\n\n")

	for _, section := range result.Sections {
		fmt.Fprintf(&builder, "【%s】\n", section.Title)
		fmt.Fprintf(&builder, "命令: %s\n", section.Command)
		builder.WriteString("输出:\n")
		builder.WriteString(section.Output)
		builder.WriteString("\n\n")
	}

	return strings.TrimSpace(builder.String())
}

func (b *Bot) sendLongText(chatID int64, replyToMessageID int, text string) {
	chunks := splitMessageChunks(text, telegramMessageLimit)
	for idx, chunk := range chunks {
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = ""
		if idx == 0 && replyToMessageID > 0 {
			msg.ReplyToMessageID = replyToMessageID
		}
		if _, err := b.botAPI.Send(msg); err != nil {
			logger.Errorf("发送分析消息失败: %v", err)
		}
	}
}

func splitMessageChunks(text string, limit int) []string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= limit {
		return []string{trimmed}
	}

	parts := strings.Split(trimmed, "\n")
	chunks := make([]string, 0, len(parts)/6+1)
	var builder strings.Builder

	for _, line := range parts {
		candidate := line
		if builder.Len() > 0 {
			candidate = "\n" + line
		}
		if builder.Len()+len(candidate) > limit {
			chunks = append(chunks, builder.String())
			builder.Reset()
			builder.WriteString(line)
			continue
		}
		builder.WriteString(candidate)
	}

	if builder.Len() > 0 {
		chunks = append(chunks, builder.String())
	}

	return chunks
}

func (b *Bot) hasPendingOrWaiting(userID int) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if waiting, ok := b.waitingForInput[userID]; ok && len(waiting) > 0 {
		return true
	}

	if pending, ok := b.pendingTx[userID]; ok && len(pending) > 0 {
		return true
	}

	return false
}

func (b *Bot) sendReplyToChat(chatID int64, replyToMessageID int, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if replyToMessageID > 0 {
		msg.ReplyToMessageID = replyToMessageID
	}
	if _, err := b.botAPI.Send(msg); err != nil {
		logger.Errorf("发送消息失败: %v", err)
	}
}
