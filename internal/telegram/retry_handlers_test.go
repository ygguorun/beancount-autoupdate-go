package telegram

import (
	"testing"

	"beancount-autoupdate/internal/beancount"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func newTestRoutingBot(waiting map[string]string) *Bot {
	b := &Bot{
		pendingTx: map[int]map[string]*beancount.PendingTransaction{
			1001: {
				"tx_a": {TransactionID: "tx_a", UserID: 1001, LastMessageID: 100},
				"tx_b": {TransactionID: "tx_b", UserID: 1001, LastMessageID: 200},
			},
		},
		waitingForInput: make(map[int]map[string]string),
	}
	if len(waiting) > 0 {
		b.waitingForInput[1001] = waiting
	}
	return b
}

func TestSelectWaitingTargetLocked(t *testing.T) {
	tests := []struct {
		name           string
		waiting        map[string]string
		replyMessageID int
		wantTxID       string
		wantResult     waitingTargetResult
	}{
		{
			name:           "no waiting transactions",
			replyMessageID: 0,
			wantTxID:       "",
			wantResult:     waitingTargetNone,
		},
		{
			name:       "single waiting routes automatically",
			waiting:    map[string]string{"tx_a": "guidance"},
			wantTxID:   "tx_a",
			wantResult: waitingTargetFound,
		},
		{
			name:       "multiple waiting without reply is ambiguous",
			waiting:    map[string]string{"tx_a": "guidance", "tx_b": "guidance"},
			wantTxID:   "",
			wantResult: waitingTargetAmbiguous,
		},
		{
			name:           "reply matches waiting transaction",
			waiting:        map[string]string{"tx_a": "guidance", "tx_b": "guidance"},
			replyMessageID: 200,
			wantTxID:       "tx_b",
			wantResult:     waitingTargetFound,
		},
		{
			name:           "reply to non-waiting transaction yields none",
			waiting:        map[string]string{"tx_b": "guidance"},
			replyMessageID: 100,
			wantTxID:       "",
			wantResult:     waitingTargetNone,
		},
		{
			name:           "reply to unrelated message keeps single auto route",
			waiting:        map[string]string{"tx_a": "guidance"},
			replyMessageID: 999,
			wantTxID:       "tx_a",
			wantResult:     waitingTargetFound,
		},
		{
			name:           "reply to unrelated message with multiple waiting is ambiguous",
			waiting:        map[string]string{"tx_a": "guidance", "tx_b": "guidance"},
			replyMessageID: 999,
			wantTxID:       "",
			wantResult:     waitingTargetAmbiguous,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestRoutingBot(tt.waiting)

			b.mu.RLock()
			defer b.mu.RUnlock()

			txID, inputType, result := b.selectWaitingTargetLocked(1001, tt.replyMessageID)
			if txID != tt.wantTxID {
				t.Fatalf("transactionID: got %q want %q", txID, tt.wantTxID)
			}
			if result != tt.wantResult {
				t.Fatalf("result: got %v want %v", result, tt.wantResult)
			}
			if result == waitingTargetFound && inputType != "guidance" {
				t.Fatalf("inputType: got %q want %q", inputType, "guidance")
			}
		})
	}
}

func TestResolveWaitingInputTarget(t *testing.T) {
	tests := []struct {
		name     string
		waiting  map[string]string
		replyTo  *tgbotapi.Message
		wantTxID string
		wantOK   bool
	}{
		{
			name:     "plain text with single waiting routes to it",
			waiting:  map[string]string{"tx_b": "guidance"},
			wantTxID: "tx_b",
			wantOK:   true,
		},
		{
			name:    "plain text with multiple waiting does not guess",
			waiting: map[string]string{"tx_a": "guidance", "tx_b": "guidance"},
			wantOK:  false,
		},
		{
			name:     "reply to waiting transaction routes to it",
			waiting:  map[string]string{"tx_a": "guidance", "tx_b": "guidance"},
			replyTo:  &tgbotapi.Message{MessageID: 100},
			wantTxID: "tx_a",
			wantOK:   true,
		},
		{
			name:    "reply to non-waiting transaction does not fall back",
			waiting: map[string]string{"tx_b": "guidance"},
			replyTo: &tgbotapi.Message{MessageID: 100},
			wantOK:  false,
		},
		{
			name:   "no waiting transactions",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestRoutingBot(tt.waiting)

			txID, inputType, ok := b.resolveWaitingInputTarget(1001, tt.replyTo)
			if ok != tt.wantOK {
				t.Fatalf("ok: got %v want %v", ok, tt.wantOK)
			}
			if txID != tt.wantTxID {
				t.Fatalf("transactionID: got %q want %q", txID, tt.wantTxID)
			}
			if ok && inputType != "guidance" {
				t.Fatalf("inputType: got %q want %q", inputType, "guidance")
			}
		})
	}
}

func TestMergeFailedGuidanceHistory(t *testing.T) {
	current := []beancount.ConversationMessage{
		{Role: "user", Content: "prompt", ImageBase64: "img"},
		{Role: "assistant", Content: `{}`},
	}
	failed := []beancount.ConversationMessage{
		{Role: "user", Content: "prompt", ImageBase64: "img"},
		{Role: "assistant", Content: `{}`},
		{Role: "user", Content: "副业收入"},
		{Role: "assistant", Content: `[{"datetime":"2026-09-10 14:49:26"}]`},
		{Role: "user", Content: "你上一次的回复无法解析为交易数据: cannot unmarshal array"},
	}

	tests := []struct {
		name     string
		current  []beancount.ConversationMessage
		failed   []beancount.ConversationMessage
		guidance string
		wantLen  int
		check    func(t *testing.T, got []beancount.ConversationMessage)
	}{
		{
			name:     "failed history with error output replaces current",
			current:  current,
			failed:   failed,
			guidance: "副业收入",
			wantLen:  5,
			check: func(t *testing.T, got []beancount.ConversationMessage) {
				if got[3].Role != "assistant" || got[3].Content != `[{"datetime":"2026-09-10 14:49:26"}]` {
					t.Fatalf("failed assistant output missing: %#v", got[3])
				}
				if got[4].Role != "user" || got[4].Content == "" {
					t.Fatalf("error hint message missing: %#v", got[4])
				}
			},
		},
		{
			name:     "empty failed history falls back to appending guidance",
			current:  current,
			failed:   nil,
			guidance: "金额应为 35.00",
			wantLen:  3,
			check: func(t *testing.T, got []beancount.ConversationMessage) {
				last := got[2]
				if last.Role != "user" || last.Content != "金额应为 35.00" {
					t.Fatalf("unexpected appended guidance: %#v", last)
				}
			},
		},
		{
			name:     "equal length failed history falls back to appending guidance",
			current:  current,
			failed:   current,
			guidance: "继续",
			wantLen:  3,
			check: func(t *testing.T, got []beancount.ConversationMessage) {
				if got[2].Content != "继续" {
					t.Fatalf("unexpected fallback result: %#v", got[2])
				}
			},
		},
		{
			name:     "nil current with nil failed appends guidance",
			current:  nil,
			failed:   nil,
			guidance: "副业收入",
			wantLen:  1,
			check: func(t *testing.T, got []beancount.ConversationMessage) {
				if got[0].Role != "user" || got[0].Content != "副业收入" {
					t.Fatalf("unexpected result: %#v", got[0])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeFailedGuidanceHistory(tt.current, tt.failed, tt.guidance)
			if len(got) != tt.wantLen {
				t.Fatalf("length: got %d want %d (%#v)", len(got), tt.wantLen, got)
			}
			tt.check(t, got)
		})
	}
}
