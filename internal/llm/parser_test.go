package llm

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"beancount-autoupdate/internal/beancount"
)

func TestParseResponse(t *testing.T) {
	p := &Parser{}

	tests := []struct {
		name      string
		input     string
		wantErr   bool
		wantPayee string
		wantExtra map[string]string
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
				"extra":{"discount":"2.00","original_amount":"20.00"},
				"special_directives":[]
			}`,
			wantErr:   false,
			wantPayee: "Coffee Shop",
			wantExtra: map[string]string{"discount": "2.00", "original_amount": "20.00"},
		},
		{
			name: "extra kv array",
			input: `{
				"datetime":"2026-03-01 12:31:00",
				"flag":"*",
				"payee":"Takeout",
				"narration":"Lunch",
				"tags":[],
				"postings":[{"account":"Expenses:Food","amount":"16.00","currency":"CNY"}],
				"order_id":"abc",
				"extra":[{"k":"sub_merchant","v":"Foo"},{"k":"payment_method","v":"花呗"}],
				"special_directives":[]
			}`,
			wantErr:   false,
			wantPayee: "Takeout",
			wantExtra: map[string]string{"sub_merchant": "Foo", "payment_method": "花呗"},
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
			wantExtra: map[string]string{},
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
			wantExtra: map[string]string{},
		},
		{
			name: "invalid extra format",
			input: `{
				"datetime":"2026-03-01 09:00:00",
				"flag":"*",
				"payee":"Metro",
				"narration":"Commute",
				"tags":[],
				"postings":[{"account":"Expenses:Transport","amount":"4.00","currency":"CNY"}],
				"order_id":"",
				"extra":"bad-format",
				"special_directives":[]
			}`,
			wantErr: true,
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
			if len(got.Extra) != len(tc.wantExtra) {
				t.Fatalf("unexpected extra length: got %d want %d", len(got.Extra), len(tc.wantExtra))
			}
			for k, wantV := range tc.wantExtra {
				if got.Extra[k] != wantV {
					t.Fatalf("unexpected extra[%q]: got %q want %q", k, got.Extra[k], wantV)
				}
			}
		})
	}
}

func TestGenerateTransactionSchema(t *testing.T) {
	schema, ok := generateTransactionSchema().(map[string]interface{})
	if !ok {
		t.Fatalf("schema type assertion failed")
	}

	if schema["additionalProperties"] != false {
		t.Fatalf("root additionalProperties must be false")
	}

	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties type assertion failed")
	}

	postings, ok := properties["postings"].(map[string]interface{})
	if !ok {
		t.Fatalf("postings schema missing")
	}
	postingsItems, ok := postings["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("postings items schema missing")
	}
	if postingsItems["additionalProperties"] != false {
		t.Fatalf("postings item additionalProperties must be false")
	}

	extra, ok := properties["extra"].(map[string]interface{})
	if !ok {
		t.Fatalf("extra schema missing")
	}
	if extra["type"] != "array" {
		t.Fatalf("extra type must be array")
	}
	extraItems, ok := extra["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("extra items schema missing")
	}
	if extraItems["additionalProperties"] != false {
		t.Fatalf("extra item additionalProperties must be false")
	}
	extraItemProps, ok := extraItems["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("extra item properties missing")
	}
	if _, ok := extraItemProps["k"]; !ok {
		t.Fatalf("extra item missing key 'k'")
	}
	if _, ok := extraItemProps["v"]; !ok {
		t.Fatalf("extra item missing key 'v'")
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

func TestIsResponsesProtocolUnsupported(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		expect bool
	}{
		{name: "responses endpoint 404", err: &testErr{msg: `POST "https://example.com/v1/responses": 404 Not Found`}, expect: true},
		{name: "responses unknown endpoint", err: &testErr{msg: "unknown endpoint /v1/responses"}, expect: true},
		{name: "wrapped responses unsupported", err: fmt.Errorf("wrapped: %w", &testErr{msg: "responses endpoint unsupported"}), expect: true},
		{name: "schema error", err: &testErr{msg: "invalid schema for response_format"}, expect: false},
		{name: "network timeout", err: &testErr{msg: "network timeout"}, expect: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isResponsesProtocolUnsupported(tc.err)
			if got != tc.expect {
				t.Fatalf("unexpected result: got %v want %v", got, tc.expect)
			}
		})
	}
}

func TestBuildResponseInput(t *testing.T) {
	p := &Parser{}

	t.Run("first round with image", func(t *testing.T) {
		input := p.buildResponseInput("prompt-text", "ZmFrZS1pbWFnZQ==", nil, "")
		if len(input) != 1 {
			t.Fatalf("unexpected input length: got %d want 1", len(input))
		}

		msg := input[0].OfMessage
		if msg == nil {
			t.Fatalf("expected message variant")
		}
		if msg.Role != "user" {
			t.Fatalf("unexpected role: %s", msg.Role)
		}

		parts := msg.Content.OfInputItemContentList
		if len(parts) != 2 {
			t.Fatalf("unexpected content parts length: got %d want 2", len(parts))
		}

		text := parts[0].GetText()
		if text == nil || *text != "prompt-text" {
			t.Fatalf("unexpected text part: %v", text)
		}

		imageURL := parts[1].GetImageURL()
		if imageURL == nil || !strings.Contains(*imageURL, "ZmFrZS1pbWFnZQ==") {
			t.Fatalf("unexpected image url: %v", imageURL)
		}
	})

	t.Run("history with guidance", func(t *testing.T) {
		history := []beancount.ConversationMessage{
			{Role: "user", Content: "first-question", ImageBase64: "aGVsbG8="},
			{Role: "assistant", Content: "first-answer"},
		}

		input := p.buildResponseInput("", "", history, "retry-with-guidance")
		if len(input) != 3 {
			t.Fatalf("unexpected input length: got %d want 3", len(input))
		}

		if role := input[0].GetRole(); role == nil || *role != "user" {
			t.Fatalf("unexpected first role: %v", role)
		}
		if role := input[1].GetRole(); role == nil || *role != "assistant" {
			t.Fatalf("unexpected second role: %v", role)
		}
		if role := input[2].GetRole(); role == nil || *role != "user" {
			t.Fatalf("unexpected third role: %v", role)
		}

		if !input[2].OfMessage.Content.OfString.Valid() || input[2].OfMessage.Content.OfString.Value != "retry-with-guidance" {
			t.Fatalf("unexpected third message content: %v", input[2].OfMessage.Content.OfString)
		}
	})
}

type testErr struct {
	msg string
}

func (e *testErr) Error() string {
	return e.msg
}
