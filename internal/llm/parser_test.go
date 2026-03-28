package llm

import (
	"strings"
	"testing"
	"time"
)

func TestParseResponse(t *testing.T) {
	p := &Parser{}

	tests := []struct {
		name      string
		input     string
		wantErr   bool
		wantPayee string
	}{
		{
			name: "plain json",
			input: `{
				"datetime":"2026-03-01 12:30:00",
				"flag":"*",
				"payee":"Coffee Shop",
				"narration":"Latte",
				"tags":[],
				"postings":[{"account":"Expenses:Food","amount":"18.00","currency":"CNY"}],
				"order_id":"",
				"extra":{},
				"special_directives":[]
			}`,
			wantErr:   false,
			wantPayee: "Coffee Shop",
		},
		{
			name: "json markdown block",
			input: "```json\n" + `{
				"datetime":"2026-03-01 08:00:00",
				"flag":"*",
				"payee":"Bakery",
				"narration":"Bread",
				"tags":[],
				"postings":[{"account":"Expenses:Food","amount":"9.90","currency":"CNY"}],
				"order_id":"",
				"extra":{},
				"special_directives":[]
			}` + "\n```",
			wantErr:   false,
			wantPayee: "Bakery",
		},
		{
			name: "generic markdown block",
			input: "```\n" + `{
				"datetime":"2026-03-01 09:00:00",
				"flag":"*",
				"payee":"Metro",
				"narration":"Commute",
				"tags":[],
				"postings":[{"account":"Expenses:Transport","amount":"4.00","currency":"CNY"}],
				"order_id":"",
				"extra":{},
				"special_directives":[]
			}` + "\n```",
			wantErr:   false,
			wantPayee: "Metro",
		},
		{
			name:    "empty object",
			input:   `{}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   `{not-json}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.parseResponse(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatalf("expected parsed transaction, got nil")
			}
			if got.Payee != tc.wantPayee {
				t.Fatalf("unexpected payee: got %q want %q", got.Payee, tc.wantPayee)
			}
		})
	}
}

func TestParseTime(t *testing.T) {
	p := &Parser{}

	t.Run("full datetime", func(t *testing.T) {
		got, err := p.ParseTime("2026-03-01 11:22:33")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Format("2006-01-02 15:04:05") != "2026-03-01 11:22:33" {
			t.Fatalf("unexpected datetime: %s", got.Format("2006-01-02 15:04:05"))
		}
	})

	t.Run("date only", func(t *testing.T) {
		got, err := p.ParseTime("2026-03-01")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Format("2006-01-02") != "2026-03-01" {
			t.Fatalf("unexpected date: %s", got.Format("2006-01-02"))
		}
	})

	t.Run("empty uses now", func(t *testing.T) {
		before := time.Now().Add(-2 * time.Second)
		got, err := p.ParseTime("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		after := time.Now().Add(2 * time.Second)
		if got.Before(before) || got.After(after) {
			t.Fatalf("expected now-like time, got: %v", got)
		}
	})

	t.Run("invalid format", func(t *testing.T) {
		got, err := p.ParseTime("03/01/2026")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "invalid datetime format") {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.IsZero() {
			t.Fatalf("expected non-zero fallback time")
		}
	})
}

func TestIsStructuredOutputUnsupported(t *testing.T) {
	tests := []struct {
		name   string
		errMsg string
		expect bool
	}{
		{name: "unsupported keyword", errMsg: "feature unsupported by model", expect: true},
		{name: "schema keyword", errMsg: "invalid schema for response_format", expect: true},
		{name: "generic error", errMsg: "network timeout", expect: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := &testErr{msg: tc.errMsg}
			got := isStructuredOutputUnsupported(err)
			if got != tc.expect {
				t.Fatalf("unexpected result: got %v want %v", got, tc.expect)
			}
		})
	}
}

type testErr struct {
	msg string
}

func (e *testErr) Error() string {
	return e.msg
}
