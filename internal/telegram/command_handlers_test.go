package telegram

import (
	"fmt"
	"strings"
	"testing"

	"beancount-autoupdate/internal/beancount"
)

func TestBuildPendingListEntriesSort(t *testing.T) {
	txMap := map[string]*beancount.PendingTransaction{
		"tx_old": {
			TransactionID: "tx_old",
			Date:          "2026-04-16",
			Time:          "10:00:00",
		},
		"tx_new": {
			TransactionID: "tx_new",
			Date:          "2026-04-18",
			Time:          "12:00:00",
		},
		"tx_nil": nil,
	}

	entries := buildPendingListEntries(txMap)
	if len(entries) != 2 {
		t.Fatalf("unexpected entries count: got %d want 2", len(entries))
	}

	if entries[0].transactionID != "tx_new" {
		t.Fatalf("unexpected first entry: got %q want %q", entries[0].transactionID, "tx_new")
	}

	if entries[1].transactionID != "tx_old" {
		t.Fatalf("unexpected second entry: got %q want %q", entries[1].transactionID, "tx_old")
	}
}

func TestBuildPendingPreviewKeyboardLimitAndCallback(t *testing.T) {
	entries := []pendingListEntry{
		{transactionID: "1001_20260418120000_1234", data: &beancount.PendingTransaction{}},
		{transactionID: "1001_20260417120000_5678", data: &beancount.PendingTransaction{}},
	}

	keyboard, count := buildPendingPreviewKeyboard(entries, 1)
	if keyboard == nil {
		t.Fatal("expected keyboard, got nil")
	}
	if count != 1 {
		t.Fatalf("unexpected button count: got %d want 1", count)
	}

	if len(keyboard.InlineKeyboard) != 1 {
		t.Fatalf("unexpected row count: got %d want 1", len(keyboard.InlineKeyboard))
	}
	if len(keyboard.InlineKeyboard[0]) != 1 {
		t.Fatalf("unexpected button count in row: got %d want 1", len(keyboard.InlineKeyboard[0]))
	}

	button := keyboard.InlineKeyboard[0][0]
	if button.CallbackData == nil {
		t.Fatal("expected callback data")
	}

	wantCallback := fmt.Sprintf("%s:open_preview", entries[0].transactionID)
	if *button.CallbackData != wantCallback {
		t.Fatalf("unexpected callback data: got %q want %q", *button.CallbackData, wantCallback)
	}

	wantShortID := strings.ToUpper(shortTransactionID(entries[0].transactionID))
	if !strings.Contains(button.Text, wantShortID) {
		t.Fatalf("button text does not contain short ID: text=%q shortID=%q", button.Text, wantShortID)
	}
}
