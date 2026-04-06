package telegram

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"beancount-autoupdate/internal/beancount"
)

func TestPersistAndLoadSessionState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "telegram_sessions.json")

	original := newTestStateBot(statePath)
	original.pendingTx[1001] = map[string]*beancount.PendingTransaction{
		"tx_1": {
			TransactionID:        "tx_1",
			UserID:               1001,
			Date:                 "2026-04-06",
			Time:                 "09:30:00",
			Payee:                "Test Store",
			Narration:            "Breakfast",
			OriginalTempFilePath: "/tmp/receipt_1.jpg",
			ConversationHistory: []beancount.ConversationMessage{
				{Role: "user", Content: "first prompt", ImageBase64: "aGVsbG8="},
				{Role: "assistant", Content: `{"datetime":"2026-04-06 09:30:00"}`},
			},
		},
	}
	original.waitingForInput[1001] = map[string]string{"tx_1": "guidance"}

	original.persistSessionState()

	restored := newTestStateBot(statePath)
	if err := restored.loadSessionState(); err != nil {
		t.Fatalf("loadSessionState returned error: %v", err)
	}

	txMap, ok := restored.pendingTx[1001]
	if !ok || len(txMap) != 1 {
		t.Fatalf("unexpected pending map: %#v", restored.pendingTx)
	}

	tx := txMap["tx_1"]
	if tx == nil {
		t.Fatalf("transaction tx_1 missing after load")
	}
	if tx.Payee != "Test Store" || tx.Narration != "Breakfast" {
		t.Fatalf("transaction fields not restored: %+v", tx)
	}
	if len(tx.ConversationHistory) != 2 {
		t.Fatalf("conversation history not restored: %+v", tx.ConversationHistory)
	}
	if tx.ConversationHistory[0].ImageBase64 != "aGVsbG8=" {
		t.Fatalf("image base64 not restored: %+v", tx.ConversationHistory[0])
	}

	if restored.waitingForInput[1001]["tx_1"] != "guidance" {
		t.Fatalf("waitingForInput not restored: %#v", restored.waitingForInput)
	}
}

func TestLoadSessionStateSanitizeWaitingTargets(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "telegram_sessions.json")

	state := sessionState{
		PendingTx: map[int]map[string]*beancount.PendingTransaction{
			1001: {
				"tx_keep": {TransactionID: "tx_keep", UserID: 1001},
				"tx_nil":  nil,
			},
		},
		WaitingForInput: map[int]map[string]string{
			1001: {
				"tx_keep":    "guidance",
				"tx_missing": "guidance",
			},
			2002: {
				"tx_other": "guidance",
			},
		},
	}

	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state failed: %v", err)
	}
	if err := os.WriteFile(statePath, payload, 0o644); err != nil {
		t.Fatalf("write state file failed: %v", err)
	}

	b := newTestStateBot(statePath)
	if err := b.loadSessionState(); err != nil {
		t.Fatalf("loadSessionState returned error: %v", err)
	}

	if len(b.pendingTx) != 1 || len(b.pendingTx[1001]) != 1 {
		t.Fatalf("pendingTx not sanitized: %#v", b.pendingTx)
	}
	if _, ok := b.pendingTx[1001]["tx_keep"]; !ok {
		t.Fatalf("expected tx_keep to exist: %#v", b.pendingTx)
	}

	if len(b.waitingForInput) != 1 || len(b.waitingForInput[1001]) != 1 {
		t.Fatalf("waitingForInput not sanitized: %#v", b.waitingForInput)
	}
	if b.waitingForInput[1001]["tx_keep"] != "guidance" {
		t.Fatalf("expected tx_keep waiting input to remain: %#v", b.waitingForInput)
	}
}

func TestLoadSessionStateInvalidJSON(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "telegram_sessions.json")
	if err := os.WriteFile(statePath, []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write invalid json failed: %v", err)
	}

	b := newTestStateBot(statePath)
	b.pendingTx[1] = map[string]*beancount.PendingTransaction{
		"tx_existing": {TransactionID: "tx_existing", UserID: 1},
	}

	if err := b.loadSessionState(); err != nil {
		t.Fatalf("loadSessionState should not return error for invalid json: %v", err)
	}

	if _, ok := b.pendingTx[1]["tx_existing"]; !ok {
		t.Fatalf("existing in-memory state should remain unchanged on invalid json")
	}
}

func TestPersistSessionStateRemovesFileWhenEmpty(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "telegram_sessions.json")
	if err := os.WriteFile(statePath, []byte(`{"pending_tx":{"1":{"tx_1":{"transaction_id":"tx_1"}}}}`), 0o644); err != nil {
		t.Fatalf("write initial state file failed: %v", err)
	}

	b := newTestStateBot(statePath)
	b.persistSessionState()

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected state file to be removed when empty, got err=%v", err)
	}
}

func newTestStateBot(statePath string) *Bot {
	return &Bot{
		pendingTx:       make(map[int]map[string]*beancount.PendingTransaction),
		waitingForInput: make(map[int]map[string]string),
		stateFilePath:   statePath,
	}
}
