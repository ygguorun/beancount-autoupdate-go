package beancount

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetTransactionFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	m := &Manager{beansDir: filepath.Join(tmpDir, "beans")}

	date := time.Date(2026, time.March, 5, 0, 0, 0, 0, time.Local)
	got := m.GetTransactionFilePath(date)

	wantSuffix := filepath.Join("beans", "2026", "03.bean")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("unexpected path: got %q, want suffix %q", got, wantSuffix)
	}

	if _, err := os.Stat(filepath.Dir(got)); err != nil {
		t.Fatalf("expected year directory to exist: %v", err)
	}
}

func TestAddTransactionFromPostings(t *testing.T) {
	tmpDir := t.TempDir()
	m := &Manager{
		dataDir:           tmpDir,
		beansDir:          filepath.Join(tmpDir, "beans"),
		operatingCurrency: "CNY",
	}

	date := time.Date(2026, time.March, 5, 14, 30, 40, 0, time.Local)
	entry, err := m.AddTransactionFromPostings(
		date,
		"*",
		"CityMart",
		"Grocery",
		[]string{"food", "weekly"},
		[]PostingData{
			{Account: "Expenses:Food", Amount: "88.00", Currency: "CNY"},
			{Account: "Assets:Cash", Amount: "-88.00", Currency: ""},
		},
		"ORDER-123",
		"https://example.com/receipt.jpg",
		map[string]string{
			"discount":        "2.00",
			"original_amount": "90.00",
			"merchant_name":   "CityMart",
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		`2026-03-05 * "CityMart" "Grocery" #food #weekly`,
		`time: "14:30:40"`,
		`order-id: "ORDER-123"`,
		`image-url: "https://example.com/receipt.jpg"`,
		`discount: "2.00"`,
		`original-amount: "90.00"`,
		`merchant-name: "CityMart"`,
		`Expenses:Food 88.00 CNY`,
		`Assets:Cash -88.00 CNY`,
	}

	for _, check := range checks {
		if !strings.Contains(entry, check) {
			t.Fatalf("entry missing %q\nentry:\n%s", check, entry)
		}
	}

	filePath := m.GetTransactionFilePath(date)
	content, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatalf("failed to read transaction file: %v", readErr)
	}
	if !strings.Contains(string(content), entry) {
		t.Fatalf("file does not contain generated entry")
	}
}

func TestAddTransactionFromPostingsDiscountPairRule(t *testing.T) {
	tests := []struct {
		name            string
		extra           map[string]string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name: "discount only skipped",
			extra: map[string]string{
				"discount": "1.00",
			},
			wantNotContains: []string{`discount: "1.00"`, `original-amount:`},
		},
		{
			name: "original amount only skipped",
			extra: map[string]string{
				"original_amount": "11.00",
			},
			wantNotContains: []string{`original-amount: "11.00"`, `discount:`},
		},
		{
			name: "both included",
			extra: map[string]string{
				"discount":        "1.00",
				"original_amount": "11.00",
			},
			wantContains: []string{`discount: "1.00"`, `original-amount: "11.00"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			m := &Manager{
				dataDir:           tmpDir,
				beansDir:          filepath.Join(tmpDir, "beans"),
				operatingCurrency: "CNY",
			}

			entry, err := m.AddTransactionFromPostings(
				time.Date(2026, time.March, 1, 10, 0, 0, 0, time.Local),
				"*",
				"Payee",
				"Narration",
				nil,
				[]PostingData{{Account: "Assets:Cash", Amount: "-10.00"}},
				"",
				"",
				tc.extra,
			)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, want := range tc.wantContains {
				if !strings.Contains(entry, want) {
					t.Fatalf("entry missing %q\nentry:\n%s", want, entry)
				}
			}

			for _, notWant := range tc.wantNotContains {
				if strings.Contains(entry, notWant) {
					t.Fatalf("entry should not contain %q\nentry:\n%s", notWant, entry)
				}
			}
		})
	}
}

func TestAddTransactionEscapesTextAndRejectsInjection(t *testing.T) {
	m := &Manager{beansDir: filepath.Join(t.TempDir(), "beans"), operatingCurrency: "CNY"}
	date := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.Local)

	entry, err := m.AddTransactionFromPostings(date, "*", `Shop "A"`, `Line \ note`, nil,
		[]PostingData{{Account: "Assets:Cash", Amount: "-10.00"}}, "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(entry, `"Shop \"A\"" "Line \\ note"`) {
		t.Fatalf("transaction text was not escaped: %s", entry)
	}

	if _, err := m.AddTransactionFromPostings(date, "*", "Shop\n2026-03-01 *", "note", nil,
		[]PostingData{{Account: "Assets:Cash", Amount: "-10.00"}}, "", "", nil); err == nil {
		t.Fatal("expected newline injection to be rejected")
	}
}

func TestAddTransactionWithDirectivesWithoutDirectivesDoesNotDeadlock(t *testing.T) {
	m := &Manager{beansDir: filepath.Join(t.TempDir(), "beans"), operatingCurrency: "CNY"}
	result := make(chan error, 1)
	go func() {
		_, err := m.AddTransactionWithDirectives(&TransactionData{
			DateTime:  "2026-03-01 10:00:00",
			Flag:      "*",
			Narration: "Test",
			Postings:  []PostingData{{Account: "Assets:Cash", Amount: "-10.00"}},
		})
		result <- err
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AddTransactionWithDirectives deadlocked")
	}
}
