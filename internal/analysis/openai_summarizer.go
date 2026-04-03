package analysis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAISummarizer 使用 OpenAI 兼容接口生成文本总结。
type OpenAISummarizer struct {
	client  openai.Client
	model   string
	timeout time.Duration
}

// OpenAIOptions OpenAI 总结器配置。
type OpenAIOptions struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// NewOpenAISummarizer 创建 OpenAI 总结器。
func NewOpenAISummarizer(opts OpenAIOptions) *OpenAISummarizer {
	if strings.TrimSpace(opts.APIKey) == "" || strings.TrimSpace(opts.Model) == "" {
		return nil
	}

	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}

	requestOpts := []option.RequestOption{
		option.WithAPIKey(opts.APIKey),
	}
	if strings.TrimSpace(opts.BaseURL) != "" {
		requestOpts = append(requestOpts, option.WithBaseURL(opts.BaseURL))
	}

	return &OpenAISummarizer{
		client:  openai.NewClient(requestOpts...),
		model:   opts.Model,
		timeout: opts.Timeout,
	}
}

// Summarize 生成报表总结。
func (s *OpenAISummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("summarizer is nil")
	}

	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	completion, err := s.client.Chat.Completions.New(callCtx, openai.ChatCompletionNewParams{
		Model: s.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("你是严格的中文财务助手，只基于输入内容给出结论，不得编造数据。"),
			openai.UserMessage(prompt),
		},
	})
	if err != nil {
		return "", fmt.Errorf("openai summarize failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("openai summarize returned empty choices")
	}

	content := strings.TrimSpace(completion.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("openai summarize returned empty content")
	}

	return content, nil
}
