package telegram

import "testing"

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
