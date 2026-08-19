package telegram

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/logger"
)

type sessionState struct {
	PendingTx       map[int]map[string]*beancount.PendingTransaction `json:"pending_tx"`
	WaitingForInput map[int]map[string]string                        `json:"waiting_for_input"`
}

func (b *Bot) loadSessionState() error {
	if b.stateFilePath == "" {
		return nil
	}

	payload, err := os.ReadFile(b.stateFilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	if len(payload) == 0 {
		return nil
	}

	var state sessionState
	if err := json.Unmarshal(payload, &state); err != nil {
		logger.Warnf("加载会话持久化文件失败，已跳过恢复: path=%s, err=%v", b.stateFilePath, err)
		return nil
	}

	pending := sanitizePendingState(state.PendingTx)
	waiting := sanitizeWaitingState(pending, state.WaitingForInput)

	b.mu.Lock()
	b.pendingTx = pending
	b.waitingForInput = waiting
	b.mu.Unlock()

	logger.Infof(
		"已恢复 Telegram 会话: users=%d, pending=%d, waiting=%d",
		len(pending),
		countPendingTransactions(pending),
		countWaitingInputs(waiting),
	)

	return nil
}

func (b *Bot) persistSessionState() {
	if b.stateFilePath == "" {
		return
	}

	b.mu.RLock()
	state := sessionState{
		PendingTx:       b.pendingTx,
		WaitingForInput: b.waitingForInput,
	}
	pendingCount := countPendingTransactions(state.PendingTx)
	waitingCount := countWaitingInputs(state.WaitingForInput)
	payload, err := json.Marshal(state)
	b.mu.RUnlock()
	if err != nil {
		logger.Warnf("序列化会话状态失败: %v", err)
		return
	}

	b.stateMu.Lock()
	defer b.stateMu.Unlock()

	if pendingCount == 0 && waitingCount == 0 {
		if err := os.Remove(b.stateFilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warnf("删除空会话状态文件失败: path=%s, err=%v", b.stateFilePath, err)
		}
		return
	}

	if err := os.MkdirAll(filepath.Dir(b.stateFilePath), 0o700); err != nil {
		logger.Warnf("创建会话状态目录失败: path=%s, err=%v", b.stateFilePath, err)
		return
	}

	tempPath := b.stateFilePath + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o600); err != nil {
		logger.Warnf("写入会话状态临时文件失败: path=%s, err=%v", tempPath, err)
		return
	}

	if err := os.Rename(tempPath, b.stateFilePath); err != nil {
		if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			logger.Warnf("清理会话状态临时文件失败: path=%s, err=%v", tempPath, removeErr)
		}
		logger.Warnf("落盘会话状态失败: path=%s, err=%v", b.stateFilePath, err)
	}
}

func sanitizePendingState(source map[int]map[string]*beancount.PendingTransaction) map[int]map[string]*beancount.PendingTransaction {
	if len(source) == 0 {
		return make(map[int]map[string]*beancount.PendingTransaction)
	}

	result := make(map[int]map[string]*beancount.PendingTransaction, len(source))
	for userID, txMap := range source {
		if len(txMap) == 0 {
			continue
		}

		cleaned := make(map[string]*beancount.PendingTransaction, len(txMap))
		for txID, data := range txMap {
			if txID == "" || data == nil {
				continue
			}
			cleaned[txID] = data
		}

		if len(cleaned) > 0 {
			result[userID] = cleaned
		}
	}

	return result
}

func sanitizeWaitingState(
	pending map[int]map[string]*beancount.PendingTransaction,
	source map[int]map[string]string,
) map[int]map[string]string {
	if len(source) == 0 {
		return make(map[int]map[string]string)
	}

	result := make(map[int]map[string]string, len(source))
	for userID, waitingMap := range source {
		if len(waitingMap) == 0 {
			continue
		}

		pendingMap, ok := pending[userID]
		if !ok || len(pendingMap) == 0 {
			continue
		}

		cleaned := make(map[string]string, len(waitingMap))
		for txID, inputType := range waitingMap {
			if txID == "" || inputType == "" {
				continue
			}
			if _, exists := pendingMap[txID]; !exists {
				continue
			}
			cleaned[txID] = inputType
		}

		if len(cleaned) > 0 {
			result[userID] = cleaned
		}
	}

	return result
}

func countPendingTransactions(pending map[int]map[string]*beancount.PendingTransaction) int {
	total := 0
	for _, txMap := range pending {
		total += len(txMap)
	}
	return total
}

func countWaitingInputs(waiting map[int]map[string]string) int {
	total := 0
	for _, waitingMap := range waiting {
		total += len(waitingMap)
	}
	return total
}
