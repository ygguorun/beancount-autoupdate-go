package telegram

import (
	"fmt"
	"strings"

	"beancount-autoupdate/internal/logger"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// showTransactionPreview 显示交易预览
func (b *Bot) showTransactionPreview(userID int, transactionID string, messageID int) {
	logger.Infof("showTransactionPreview: userID=%d, transactionID=%s, messageID=%d", userID, transactionID, messageID)

	stateChanged := false
	b.mu.Lock()
	data, ok := b.pendingTx[userID][transactionID]
	if ok {
		data.LastMessageID = messageID
		if data.OriginalMessageID == 0 && messageID > 0 {
			data.OriginalMessageID = messageID
		}
		stateChanged = true
		logger.Infof("更新消息ID: LastMessageID=%d, OriginalMessageID=%d", data.LastMessageID, data.OriginalMessageID)
	}
	b.mu.Unlock()
	if stateChanged {
		b.persistSessionState()
	}

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易 %s（在 showTransactionPreview 中）", userID, transactionID)
		return
	}

	b.mu.RLock()
	data, ok = b.pendingTx[userID][transactionID]
	b.mu.RUnlock()

	if !ok {
		logger.Warnf("用户 %d 没有待确认的交易 %s（重新获取后）", userID, transactionID)
		return
	}

	var builder strings.Builder
	var keyboard tgbotapi.InlineKeyboardMarkup

	if len(data.SpecialDirectives) > 0 {
		builder.WriteString("📋 特殊指令预览\n")
		builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Fprintf(&builder, "交易ID: #%s\n", shortTransactionID(transactionID))
		fmt.Fprintf(&builder, "日期: %s\n", data.Date)
		fmt.Fprintf(&builder, "时间: %s\n", data.Time)
		fmt.Fprintf(&builder, "描述: %s\n", data.Narration)
		builder.WriteString("\n🔧 特殊指令:\n")
		for i, directive := range data.SpecialDirectives {
			fmt.Fprintf(&builder, "  %d. %s\n", i+1, directive)
		}
		builder.WriteString("\n💬 可直接回复本消息输入修改意见\n")
		builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

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
		builder.WriteString("📋 交易预览\n")
		builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Fprintf(&builder, "交易ID: #%s\n", shortTransactionID(transactionID))
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
		builder.WriteString("\n💬 可直接回复本消息输入修改意见\n")
		builder.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

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

	stateChanged := false
	b.mu.Lock()
	if d, ok := b.pendingTx[userID][transactionID]; ok {
		if oldMessageID > 0 {
			logger.Infof("编辑失败，发送新消息: transactionID=%s, oldMessageID=%d, newMessageID=%d", transactionID, oldMessageID, sentMsg.MessageID)
		} else {
			logger.Infof("发送新消息: transactionID=%s, messageID=%d", transactionID, sentMsg.MessageID)
		}

		if len(d.PreviousMessageIDs) > 0 {
			logger.Infof("删除当前交易 %s 的之前消息: %v", transactionID, d.PreviousMessageIDs)
			b.deleteMessages(userID, d.PreviousMessageIDs)
			d.PreviousMessageIDs = []int{}
			d.OriginalMessageID = 0
		}
		d.PreviousMessageIDs = append(d.PreviousMessageIDs, sentMsg.MessageID)

		d.LastMessageID = sentMsg.MessageID
		if d.OriginalMessageID == 0 {
			d.OriginalMessageID = sentMsg.MessageID
		}
		stateChanged = true
		logger.Infof("发送新消息成功: transactionID=%s, LastMessageID=%d, OriginalMessageID=%d, PreviousMessageIDs=%v", transactionID, d.LastMessageID, d.OriginalMessageID, d.PreviousMessageIDs)
	}
	b.mu.Unlock()
	if stateChanged {
		b.persistSessionState()
	}
}
