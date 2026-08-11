package llm

import (
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"

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
	schema := generateTransactionSchema()

	if schema["additionalProperties"] != false {
		t.Fatalf("root additionalProperties must be false")
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type assertion failed")
	}

	postings, ok := properties["postings"].(map[string]any)
	if !ok {
		t.Fatalf("postings schema missing")
	}
	postingsItems, ok := postings["items"].(map[string]any)
	if !ok {
		t.Fatalf("postings items schema missing")
	}
	if postingsItems["additionalProperties"] != false {
		t.Fatalf("postings item additionalProperties must be false")
	}

	extra, ok := properties["extra"].(map[string]any)
	if !ok {
		t.Fatalf("extra schema missing")
	}
	if extra["type"] != "array" {
		t.Fatalf("extra type must be array")
	}
	extraItems, ok := extra["items"].(map[string]any)
	if !ok {
		t.Fatalf("extra items schema missing")
	}
	if extraItems["additionalProperties"] != false {
		t.Fatalf("extra item additionalProperties must be false")
	}
	extraItemProps, ok := extraItems["properties"].(map[string]any)
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

func TestResolveBaseURLAndProtocol(t *testing.T) {
	tests := []struct {
		name         string
		baseURL      string
		wantBaseURL  string
		wantProtocol llmRequestProtocol
	}{
		{
			name:         "empty base url defaults to chat",
			baseURL:      "",
			wantBaseURL:  "",
			wantProtocol: llmRequestProtocolChatCompletions,
		},
		{
			name:         "base url only defaults to chat",
			baseURL:      "https://api.example.com/v1",
			wantBaseURL:  "https://api.example.com/v1",
			wantProtocol: llmRequestProtocolChatCompletions,
		},
		{
			name:         "chat endpoint switches to chat protocol",
			baseURL:      "https://api.example.com/v1/chat/completions/",
			wantBaseURL:  "https://api.example.com/v1",
			wantProtocol: llmRequestProtocolChatCompletions,
		},
		{
			name:         "responses endpoint switches to responses protocol",
			baseURL:      "https://api.example.com/v1/responses",
			wantBaseURL:  "https://api.example.com/v1",
			wantProtocol: llmRequestProtocolResponses,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotBaseURL, gotProtocol := resolveBaseURLAndProtocol(tc.baseURL)
			if gotBaseURL != tc.wantBaseURL {
				t.Fatalf("unexpected base url: got %q want %q", gotBaseURL, tc.wantBaseURL)
			}
			if gotProtocol != tc.wantProtocol {
				t.Fatalf("unexpected protocol: got %q want %q", gotProtocol, tc.wantProtocol)
			}
		})
	}
}

func TestBuildResponseInput(t *testing.T) {
	p := &Parser{}

	t.Run("first request with image", func(t *testing.T) {
		input := p.buildResponseInput("prompt", "abc123", nil, "")
		if len(input) != 1 {
			t.Fatalf("unexpected input length: got %d want 1", len(input))
		}

		msg := input[0].OfMessage
		if msg == nil {
			t.Fatalf("expected message item")
		}
		if msg.Role != responses.EasyInputMessageRoleUser {
			t.Fatalf("unexpected role: got %q", msg.Role)
		}

		parts := msg.Content.OfInputItemContentList
		if len(parts) != 2 {
			t.Fatalf("unexpected content parts length: got %d want 2", len(parts))
		}
		if parts[0].OfInputText == nil || parts[0].OfInputText.Text != "prompt" {
			t.Fatalf("unexpected text part")
		}
		if parts[1].OfInputImage == nil {
			t.Fatalf("expected image part")
		}
		if !parts[1].OfInputImage.ImageURL.Valid() {
			t.Fatalf("expected image url to be set")
		}
		if parts[1].OfInputImage.ImageURL.Value != "data:image/jpeg;base64,abc123" {
			t.Fatalf("unexpected image url: %q", parts[1].OfInputImage.ImageURL.Value)
		}
	})

	t.Run("history and guidance", func(t *testing.T) {
		history := []beancount.ConversationMessage{
			{Role: "user", Content: "first", ImageBase64: "img1"},
			{Role: "assistant", Content: "assistant reply"},
		}

		input := p.buildResponseInput("ignored", "ignored", history, "please retry")
		if len(input) != 3 {
			t.Fatalf("unexpected input length: got %d want 3", len(input))
		}

		assistant := input[1].OfMessage
		if assistant == nil {
			t.Fatalf("expected assistant message item")
		}
		if assistant.Role != responses.EasyInputMessageRoleAssistant {
			t.Fatalf("unexpected assistant role: got %q", assistant.Role)
		}
		if !assistant.Content.OfString.Valid() || assistant.Content.OfString.Value != "assistant reply" {
			t.Fatalf("unexpected assistant content")
		}

		guidance := input[2].OfMessage
		if guidance == nil {
			t.Fatalf("expected guidance message item")
		}
		if guidance.Role != responses.EasyInputMessageRoleUser {
			t.Fatalf("unexpected guidance role: got %q", guidance.Role)
		}
		if !guidance.Content.OfString.Valid() || guidance.Content.OfString.Value != "补充信息：please retry" {
			t.Fatalf("unexpected guidance content")
		}
	})

	t.Run("guidance prefix idempotent", func(t *testing.T) {
		history := []beancount.ConversationMessage{{Role: "assistant", Content: "assistant reply"}}

		input := p.buildResponseInput("ignored", "ignored", history, "补充信息：please retry")
		guidance := input[1].OfMessage
		if guidance == nil {
			t.Fatalf("expected guidance message item")
		}
		if !guidance.Content.OfString.Valid() || guidance.Content.OfString.Value != "补充信息：please retry" {
			t.Fatalf("unexpected guidance content")
		}
	})
}

type testErr struct {
	msg string
}

func (e *testErr) Error() string {
	return e.msg
}
