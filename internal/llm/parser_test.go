package llm

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"

	"beancount-autoupdate/internal/beancount"
)

func TestBuildFailedParseHistory(t *testing.T) {
	p := &Parser{}

	baseHistory := []beancount.ConversationMessage{
		{Role: "user", Content: "initial prompt", ImageBase64: "img"},
		{Role: "assistant", Content: `{"datetime":"2026-01-01 00:00:00"}`},
	}

	parseErr := errors.New("failed to unmarshal JSON: json: cannot unmarshal array into Go value of type llm.transactionDataRaw")
	got := p.buildFailedParseHistory(baseHistory, "副业收入", "", "", `[{"datetime":"2026-09-10 14:49:26"}]`, parseErr)

	if len(got) != 5 {
		t.Fatalf("expected 5 messages, got %d: %#v", len(got), got)
	}
	if got[0].Content != "initial prompt" || got[1].Content != `{"datetime":"2026-01-01 00:00:00"}` {
		t.Fatalf("original history altered: %#v", got[:2])
	}
	if got[2].Role != "user" || got[2].Content != "副业收入" {
		t.Fatalf("unexpected guidance message: %#v", got[2])
	}
	if got[3].Role != "assistant" || !strings.Contains(got[3].Content, `[{"datetime"`) {
		t.Fatalf("failed assistant output not kept: %#v", got[3])
	}
	if got[4].Role != "user" ||
		!strings.Contains(got[4].Content, "无法解析") ||
		!strings.Contains(got[4].Content, "cannot unmarshal array") {
		t.Fatalf("error hint missing or wrong: %#v", got[4])
	}
	if len(baseHistory) != 2 {
		t.Fatalf("input history mutated: %#v", baseHistory)
	}
}

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

func TestImageDataURLPreservesMIMEType(t *testing.T) {
	got := imageDataURL("image/png", "abc123")
	if got != "data:image/png;base64,abc123" {
		t.Fatalf("unexpected image data URL: %q", got)
	}

	if got := detectImageMIMEType([]byte("not an image")); got != "image/jpeg" {
		t.Fatalf("unexpected fallback MIME type: %q", got)
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
		input := p.buildResponseInput("prompt", "abc123", nil, "", "image/jpeg")
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

		input := p.buildResponseInput("ignored", "ignored", history, "please retry", "")
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

		input := p.buildResponseInput("ignored", "ignored", history, "补充信息：please retry", "")
		guidance := input[1].OfMessage
		if guidance == nil {
			t.Fatalf("expected guidance message item")
		}
		if !guidance.Content.OfString.Valid() || guidance.Content.OfString.Value != "补充信息：please retry" {
			t.Fatalf("unexpected guidance content")
		}
	})
}

func TestParseResponseNormalization(t *testing.T) {
	p := &Parser{}
	const body = `{"datetime":"2026-10-01 18:00:00","flag":"auto","payee":"a","narration":"b",` +
		`"tags":[],"postings":[{"account":"X","amount":"1.00","currency":"CNY","flag":"auto"}],` +
		`"order_id":"","extra":[{"k":"收单机构","v":"招商银行"},{"k":"order_note","v":"ok"}],"special_directives":[]}`

	txn, err := p.parseResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if txn.Flag != "*" {
		t.Errorf("txn.Flag = %q, want *", txn.Flag)
	}
	if txn.Postings[0].Flag != "" {
		t.Errorf("posting.Flag = %q, want empty", txn.Postings[0].Flag)
	}
	if _, ok := txn.Extra["收单机构"]; ok {
		t.Error("非法 extra 键未被剔除")
	}
	if txn.Extra["order_note"] != "ok" {
		t.Error("合法 extra 键被误删")
	}

	// 单元素数组包裹：宽容收编
	wrapped := `[` + body + `]`
	if _, err := p.parseResponse(wrapped); err != nil {
		t.Errorf("单元素数组应可解析: %v", err)
	}
	// 空数组与 schema 回显：仍须拒绝
	if _, err := p.parseResponse(`[]`); err == nil {
		t.Error("空数组应解析失败")
	}
	if _, err := p.parseResponse(`[{"properties":{},"required":[]}]`); err == nil {
		t.Error("schema 回显应解析失败")
	}
}
