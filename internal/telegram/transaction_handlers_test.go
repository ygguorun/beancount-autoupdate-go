package telegram

import (
	"testing"

	"beancount-autoupdate/internal/beancount"
)

func TestConfirmationReservationIsExclusive(t *testing.T) {
	b := &Bot{
		pendingTx: map[int]map[string]*beancount.PendingTransaction{
			1001: {
				"tx_1": {TransactionID: "tx_1", Tags: []string{"food"}},
			},
		},
	}

	first, ok := b.beginConfirmation(1001, "tx_1")
	if !ok || first == nil {
		t.Fatal("first confirmation reservation should succeed")
	}
	first.Tags[0] = "changed"

	if _, ok := b.beginConfirmation(1001, "tx_1"); ok {
		t.Fatal("second confirmation reservation should be rejected")
	}
	b.endConfirmation(1001, "tx_1")

	third, ok := b.beginConfirmation(1001, "tx_1")
	if !ok || third.Tags[0] != "food" {
		t.Fatalf("reservation should be reusable with an unchanged snapshot: %#v", third)
	}
	b.endConfirmation(1001, "tx_1")
}

func TestBuildRecordedWebDAVURL(t *testing.T) {
	tests := []struct {
		name            string
		uploadURL       string
		publicURL       string
		webdavPath      string
		finalWebDAVPath string
		want            string
	}{
		{
			name:            "keeps legacy URL when public url missing",
			uploadURL:       "https://dav.example.com/dav",
			webdavPath:      "beancount/receipts",
			finalWebDAVPath: "beancount/receipts/2026/06_receipt.jpg",
			want:            "https://dav.example.com/dav/beancount/receipts/2026/06_receipt.jpg",
		},
		{
			name:            "uses public URL without webdav path prefix",
			uploadURL:       "https://dav.example.com/dav",
			publicURL:       "https://receipts.example.com",
			webdavPath:      "beancount/receipts",
			finalWebDAVPath: "beancount/receipts/2026/06_receipt.jpg",
			want:            "https://receipts.example.com/2026/06_receipt.jpg",
		},
		{
			name:            "supports empty remaining path",
			uploadURL:       "https://dav.example.com/dav",
			publicURL:       "https://receipts.example.com/",
			webdavPath:      "beancount/receipts",
			finalWebDAVPath: "beancount/receipts",
			want:            "https://receipts.example.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRecordedWebDAVURL(tc.uploadURL, tc.publicURL, tc.webdavPath, tc.finalWebDAVPath)
			if got != tc.want {
				t.Fatalf("unexpected url: got %q want %q", got, tc.want)
			}
		})
	}
}
