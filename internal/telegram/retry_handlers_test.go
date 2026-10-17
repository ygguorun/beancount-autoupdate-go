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
