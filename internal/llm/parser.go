package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/embed"
	"beancount-autoupdate/internal/logger"
)

// Parser LLM 解析器
type Parser struct {
	client       openai.Client
	model        string
	protocol     llmRequestProtocol
	timeout      time.Duration
	extendPrompt string
	maxImageSize int
}

type llmRequestProtocol string

const (
	llmRequestProtocolChatCompletions llmRequestProtocol = "chat/completions"
	llmRequestProtocolResponses       llmRequestProtocol = "responses"
	llmGuidancePrefix                                    = "补充信息："
)

// NewParser 创建 LLM 解析器
func NewParser(baseURL, model, apiKey string, timeout int, extendPrompt string, maxImageSize int) *Parser {
	resolvedBaseURL, protocol := resolveBaseURLAndProtocol(baseURL)

	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if resolvedBaseURL != "" {
		opts = append(opts, option.WithBaseURL(resolvedBaseURL))
	}

	logger.Infof("LLM request protocol: %s", protocol)

	return &Parser{
		client:       openai.NewClient(opts...),
		model:        model,
		protocol:     protocol,
		timeout:      time.Duration(timeout) * time.Second,
		extendPrompt: extendPrompt,
		maxImageSize: maxImageSize,
	}
}

func resolveBaseURLAndProtocol(baseURL string) (string, llmRequestProtocol) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", llmRequestProtocolChatCompletions
	}

	trimmed = strings.TrimSuffix(trimmed, "/")
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return trimmed, llmRequestProtocolChatCompletions
	}

	pathWithoutTrailingSlash := strings.TrimSuffix(u.Path, "/")
	if path, ok := strings.CutSuffix(pathWithoutTrailingSlash, "/responses"); ok {
		u.Path = path
		u.RawPath = ""
		return strings.TrimSuffix(u.String(), "/"), llmRequestProtocolResponses
	}

	if path, ok := strings.CutSuffix(pathWithoutTrailingSlash, "/chat/completions"); ok {
		u.Path = path
		u.RawPath = ""
		return strings.TrimSuffix(u.String(), "/"), llmRequestProtocolChatCompletions
	}

	return trimmed, llmRequestProtocolChatCompletions
}

// ParseImage 解析图片中的交易信息
func (p *Parser) ParseImage(imagePath string, expenseCategories, incomeCategories, transferCategories, accountNames, tagNames []string) (*beancount.TransactionData, error) {
	result, _, err := p.ParseImageWithHistory(imagePath, accountNames, tagNames, nil, "")
	return result, err
}

// ParseImageWithHistory 带对话历史的图片解析（支持重试）
// userPromptOverride 用于引导重试时传入用户引导文字，为空时使用默认提示词
func (p *Parser) ParseImageWithHistory(
	imagePath string,
	accountNames, tagNames []string,
	history []beancount.ConversationMessage,
	userPromptOverride string,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	// 编码图片
	base64Image, imageMIMEType, err := p.encodeImage(imagePath)
	if err != nil {
		return nil, history, fmt.Errorf("failed to encode image: %w", err)
	}

	// 构建提示词
	prompt := p.buildPrompt(accountNames, tagNames)

	// 构建请求适配层负载
	// userPromptOverride 作为 currentPrompt 传入，用于引导重试时添加当前用户消息
	payload := p.buildRequestPayload(prompt, base64Image, imageMIMEType, history, userPromptOverride)

	// 记录 LLM 输入日志
	logger.Debugf("LLM Input: history_count=%d, has_image=%v, current_prompt_len=%d", len(history), base64Image != "", len(userPromptOverride))
	for i, msg := range history {
		hasImage := msg.ImageBase64 != ""
		contentPreview := msg.Content
		if len(contentPreview) > 100 {
			contentPreview = contentPreview[:100] + "..."
		}
		logger.Debugf("  History[%d] %s: %s (image=%v)", i, msg.Role, contentPreview, hasImage)
	}
	if userPromptOverride != "" {
		logger.Debugf("  Current user: %s", userPromptOverride)
	}

	// 确定需要保存的用户消息内容
	// 第一次识别（历史为空）：保存提示词 + 图片
	// 引导重试（历史不为空）：保存用户引导文字（userPromptOverride）
	var userPromptForHistory string
	var imageBase64ForHistory string
	if len(history) == 0 {
		userPromptForHistory = prompt
		imageBase64ForHistory = base64Image
	} else {
		userPromptForHistory = userPromptOverride
		// 引导重试时不保存图片
	}

	var transactionData *beancount.TransactionData
	var newHistory []beancount.ConversationMessage

	switch p.protocol {
	case llmRequestProtocolResponses:
		transactionData, newHistory, err = p.callWithResponsesStructuredOutput(
			payload.responseInput,
			history,
			userPromptForHistory,
			imageBase64ForHistory,
			imageMIMEType,
		)
	default:
		// chat/completions 保持现有行为：先 Structured Outputs，再降级 JSON mode
		transactionData, newHistory, err = p.callWithStructuredOutput(
			payload.chatMessages,
			history,
			userPromptForHistory,
			imageBase64ForHistory,
			imageMIMEType,
		)
		if err != nil && isStructuredOutputUnsupported(err) {
			logger.Warnf("Structured Outputs failed: %v, falling back to JSON mode", err)
			transactionData, newHistory, err = p.callWithJSONMode(
				payload.chatMessages,
				history,
				userPromptForHistory,
				imageBase64ForHistory,
				imageMIMEType,
			)
		}
	}
	if err != nil {
		return nil, history, fmt.Errorf("failed to parse response: %w", err)
	}

	return transactionData, newHistory, nil
}

// ParseWithGuidance 带引导文字的重新解析
func (p *Parser) ParseWithGuidance(
	imagePath string,
	accountNames, tagNames []string,
	history []beancount.ConversationMessage,
	userGuidance string,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	// 不再直接修改历史，让 ParseImageWithHistory 通过 updateHistory 保存用户引导
	return p.ParseImageWithHistory(imagePath, accountNames, tagNames, history, userGuidance)
}

// ParseImageFromBytes 从字节数组解析图片
func (p *Parser) ParseImageFromBytes(imageData []byte, expenseCategories, incomeCategories, transferCategories, accountNames, tagNames []string) (*beancount.TransactionData, error) {
	// 创建临时文件
	tempFileHandle, err := os.CreateTemp("", "beancount_temp_*.jpg")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tempFile := tempFileHandle.Name()
	if err := tempFileHandle.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.WriteFile(tempFile, imageData, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write temp file: %w", err)
	}
	defer func() {
		if err := os.Remove(tempFile); err != nil {
			logger.Warnf("删除临时文件失败: %v", err)
		}
	}()

	return p.ParseImage(tempFile, expenseCategories, incomeCategories, transferCategories, accountNames, tagNames)
}

// encodeImage 将图片编码为 base64，如果超过尺寸限制则进行压缩
func (p *Parser) encodeImage(imagePath string) (string, string, error) {
	// 读取原始图片数据
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return "", "", err
	}
	imageMIMEType := detectImageMIMEType(data)

	// 如果没有尺寸限制，直接返回原始数据
	if p.maxImageSize <= 0 {
		return base64.StdEncoding.EncodeToString(data), imageMIMEType, nil
	}

	// 解码图片获取尺寸
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// 如果解码失败，可能是格式不支持，直接返回原始数据
		logger.Warnf("无法解码图片 %s: %v，将使用原始图片", imagePath, err)
		return base64.StdEncoding.EncodeToString(data), imageMIMEType, nil
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// 检查是否需要压缩
	if width <= p.maxImageSize && height <= p.maxImageSize {
		logger.Debugf("图片尺寸 %dx%d 在限制范围内，无需压缩", width, height)
		return base64.StdEncoding.EncodeToString(data), imageMIMEType, nil
	}

	// 计算缩放后的尺寸（保持宽高比）
	var newWidth, newHeight int
	if width > height {
		newWidth = p.maxImageSize
		newHeight = height * p.maxImageSize / width
	} else {
		newHeight = p.maxImageSize
		newWidth = width * p.maxImageSize / height
	}

	logger.Infof("图片尺寸 %dx%d 超过限制 %d，压缩为 %dx%d", width, height, p.maxImageSize, newWidth, newHeight)

	// 使用 imaging 库进行缩放
	resized := imaging.Resize(img, newWidth, newHeight, imaging.Lanczos)

	// 编码为 JPEG 格式
	var buf bytes.Buffer
	if format == "png" {
		if err := imaging.Encode(&buf, resized, imaging.PNG); err != nil {
			return "", "", fmt.Errorf("failed to encode resized image as PNG: %w", err)
		}
	} else {
		if err := imaging.Encode(&buf, resized, imaging.JPEG, imaging.JPEGQuality(90)); err != nil {
			return "", "", fmt.Errorf("failed to encode resized image as JPEG: %w", err)
		}
	}

	if format == "png" {
		imageMIMEType = "image/png"
	} else {
		imageMIMEType = "image/jpeg"
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), imageMIMEType, nil
}

func detectImageMIMEType(data []byte) string {
	detected := http.DetectContentType(data)
	switch detected {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return detected
	default:
		return "image/jpeg"
	}
}

// buildPrompt 构建 LLM 提示词
func (p *Parser) buildPrompt(accountNames, tagNames []string) string {
	// 从嵌入的模板文件读取
	templateContent, err := embed.GetTemplate("receipt_image_recognition.txt")
	if err != nil {
		logger.Fatalf("Failed to load receipt template: %v", err)
	}

	currentTime := time.Now().Format("2006-01-02 15:04:05")

	// 准备账户列表
	accountList := ""
	if len(accountNames) > 0 {
		accountList = strings.Join(accountNames, "\n")
	} else {
		accountList = "None"
	}

	// 准备标签列表
	tagList := ""
	if len(tagNames) > 0 {
		tagList = strings.Join(tagNames, "\n")
	} else {
		tagList = "None"
	}

	// 替换占位符
	prompt := templateContent
	prompt = strings.ReplaceAll(prompt, "{current_time}", currentTime)
	prompt = strings.ReplaceAll(prompt, "{account_names}", accountList)
	prompt = strings.ReplaceAll(prompt, "{tags}", tagList)

	// 处理扩展 prompt
	if p.extendPrompt != "" {
		if mode, content, ok := strings.Cut(p.extendPrompt, ":"); ok {
			mode = strings.TrimSpace(mode)
			content = strings.TrimSpace(content)

			// 处理 \n 转义字符，转换为实际的换行符
			content = strings.ReplaceAll(content, "\\n", "\n")
			// 处理 \\n，保留为字面量的 \n
			content = strings.ReplaceAll(content, "\\\\n", "\\n")

			switch mode {
			case "append":
				// 追加模式：在原有 prompt 基础上追加
				prompt = prompt + "\n\n" + content
			case "replace":
				// 替换模式：完全替换原有 prompt
				prompt = content
				// 仍然替换占位符（如果用户在自定义 prompt 中使用了占位符）
				prompt = strings.ReplaceAll(prompt, "{current_time}", currentTime)
				prompt = strings.ReplaceAll(prompt, "{account_names}", accountList)
				prompt = strings.ReplaceAll(prompt, "{tags}", tagList)
			default:
				logger.Warnf("Unknown extend_prompt mode: %s, ignoring. Supported modes: append, replace", mode)
			}
		} else {
			logger.Warnf("Invalid extend_prompt format. Expected 'mode:content', got: %s", p.extendPrompt)
		}
	}

	return prompt
}

// buildMessages 构建 OpenAI API 消息列表
// currentPrompt 用于引导重试时添加当前用户消息
func (p *Parser) buildMessages(prompt, base64Image string, history []beancount.ConversationMessage, currentPrompt string, imageMIMETypes ...string) []openai.ChatCompletionMessageParamUnion {
	imageMIMEType := firstImageMIMEType(imageMIMETypes...)
	var messages []openai.ChatCompletionMessageParamUnion

	// 如果历史为空，使用 prompt 和 base64Image 构建新消息（第一次识别）
	if len(history) == 0 {
		if base64Image != "" {
			// 包含图片的消息
			content := []openai.ChatCompletionContentPartUnionParam{
				openai.TextContentPart(prompt),
				openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
					URL: imageDataURL(imageMIMEType, base64Image),
				}),
			}
			messages = append(messages, openai.UserMessage(content))
		} else {
			messages = append(messages, openai.UserMessage(prompt))
		}
		return messages
	}

	// 历史不为空，从历史构建消息（引导重试）
	for _, msg := range history {
		switch msg.Role {
		case "user":
			content := formatGuidanceForLLM(msg.Content)
			messageMIMEType := msg.ImageMIMEType
			// 如果历史消息包含图片，构建包含图片的用户消息
			if msg.ImageBase64 != "" {
				content := []openai.ChatCompletionContentPartUnionParam{
					openai.TextContentPart(content),
					openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
						URL: imageDataURL(messageMIMEType, msg.ImageBase64),
					}),
				}
				messages = append(messages, openai.UserMessage(content))
			} else {
				messages = append(messages, openai.UserMessage(content))
			}
		case "assistant":
			messages = append(messages, openai.AssistantMessage(msg.Content))
		}
	}

	// 添加当前用户消息（引导重试时的用户引导）
	if currentPrompt != "" {
		messages = append(messages, openai.UserMessage(formatGuidanceForLLM(currentPrompt)))
	}

	return messages
}

type parserRequestPayload struct {
	chatMessages  []openai.ChatCompletionMessageParamUnion
	responseInput responses.ResponseInputParam
}

func (p *Parser) buildRequestPayload(prompt, base64Image, imageMIMEType string, history []beancount.ConversationMessage, currentPrompt string) parserRequestPayload {
	return parserRequestPayload{
		chatMessages:  p.buildMessages(prompt, base64Image, history, currentPrompt, imageMIMEType),
		responseInput: p.buildResponseInput(prompt, base64Image, history, currentPrompt, imageMIMEType),
	}
}

func (p *Parser) buildResponseInput(prompt, base64Image string, history []beancount.ConversationMessage, currentPrompt string, imageMIMETypes ...string) responses.ResponseInputParam {
	input := responses.ResponseInputParam{}

	// 如果历史为空，使用 prompt 和 base64Image 构建新消息（第一次识别）
	if len(history) == 0 {
		input = append(input, buildResponsesUserMessage(prompt, base64Image, firstImageMIMEType(imageMIMETypes...)))
		return input
	}

	// 历史不为空，从历史构建消息（引导重试）
	for _, msg := range history {
		switch msg.Role {
		case "user":
			input = append(input, buildResponsesUserMessage(formatGuidanceForLLM(msg.Content), msg.ImageBase64, msg.ImageMIMEType))
		case "assistant":
			input = append(input, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleAssistant))
		}
	}

	// 添加当前用户消息（引导重试时的用户引导）
	if currentPrompt != "" {
		input = append(input, responses.ResponseInputItemParamOfMessage(formatGuidanceForLLM(currentPrompt), responses.EasyInputMessageRoleUser))
	}

	return input
}

func buildResponsesUserMessage(content, imageBase64 string, imageMIMETypes ...string) responses.ResponseInputItemUnionParam {
	if imageBase64 == "" {
		return responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleUser)
	}

	messageContent := responses.ResponseInputMessageContentListParam{
		responses.ResponseInputContentParamOfInputText(content),
		{
			OfInputImage: &responses.ResponseInputImageParam{
				Detail:   responses.ResponseInputImageDetailAuto,
				ImageURL: openai.String(imageDataURL(firstImageMIMEType(imageMIMETypes...), imageBase64)),
			},
		},
	}

	return responses.ResponseInputItemParamOfMessage(messageContent, responses.EasyInputMessageRoleUser)
}

func formatGuidanceForLLM(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || strings.HasPrefix(trimmed, llmGuidancePrefix) {
		return content
	}

	return llmGuidancePrefix + content
}

func firstImageMIMEType(types ...string) string {
	if len(types) > 0 && types[0] != "" {
		return types[0]
	}
	return "image/jpeg"
}

func imageDataURL(mimeType, base64Image string) string {
	return fmt.Sprintf("data:%s;base64,%s", firstImageMIMEType(mimeType), base64Image)
}

func (p *Parser) callWithResponsesStructuredOutput(
	input responses.ResponseInputParam,
	history []beancount.ConversationMessage,
	prompt, imageBase64 string,
	imageMIMEType string,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	resp, err := p.client.Responses.New(ctx, responses.ResponseNewParams{
		Model: responses.ResponsesModel(p.model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:        "transaction_data",
					Description: openai.String("Beancount transaction data"),
					Schema:      generateTransactionSchema(),
					Strict:      openai.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return nil, history, fmt.Errorf("API call failed: %w", err)
	}

	content := strings.TrimSpace(resp.OutputText())
	if content == "" {
		return nil, history, fmt.Errorf("empty response from LLM")
	}

	logger.Infof("LLM Response (Responses Structured Output): %s", content)

	transactionData, err := p.parseResponse(content)
	if err != nil {
		return nil, history, err
	}

	newHistory := p.updateHistory(history, prompt, imageBase64, imageMIMEType, content)
	return transactionData, newHistory, nil
}

// callWithStructuredOutput 使用 Structured Outputs 调用
func (p *Parser) callWithStructuredOutput(
	messages []openai.ChatCompletionMessageParamUnion,
	history []beancount.ConversationMessage,
	prompt, imageBase64 string,
	imageMIMEType string,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: messages,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
				JSONSchema: openai.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:        "transaction_data",
					Description: openai.String("Beancount transaction data"),
					Schema:      generateTransactionSchema(),
					Strict:      openai.Bool(true),
				},
			},
		},
	})
	if err != nil {
		// 检查是否是不支持 Structured Outputs 的错误
		if isStructuredOutputUnsupported(err) {
			return nil, history, err // 触发降级
		}
		return nil, history, fmt.Errorf("API call failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return nil, history, fmt.Errorf("empty choices in LLM response")
	}
	content := completion.Choices[0].Message.Content
	if content == "" {
		return nil, history, fmt.Errorf("empty response from LLM")
	}

	// 打印 LLM 返回值
	logger.Infof("LLM Response (Structured Output): %s", content)

	// 解析响应
	transactionData, err := parseTransactionDataJSON(content)
	if err != nil {
		return nil, history, err
	}

	// 检查是否为空对象
	if transactionData.DateTime == "" && transactionData.Payee == "" && transactionData.Narration == "" {
		return nil, history, fmt.Errorf("no transaction data found")
	}

	// 更新历史（保存用户提示词、图片和助手响应）
	newHistory := p.updateHistory(history, prompt, imageBase64, imageMIMEType, content)

	return transactionData, newHistory, nil
}

// callWithJSONMode 降级方案：使用普通 JSON 模式
func (p *Parser) callWithJSONMode(
	messages []openai.ChatCompletionMessageParamUnion,
	history []beancount.ConversationMessage,
	prompt, imageBase64 string,
	imageMIMEType string,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: messages,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &openai.ResponseFormatJSONObjectParam{},
		},
	})
	if err != nil {
		return nil, history, fmt.Errorf("API call failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return nil, history, fmt.Errorf("empty choices in LLM response")
	}
	content := completion.Choices[0].Message.Content
	if content == "" {
		return nil, history, fmt.Errorf("empty response from LLM")
	}

	// 打印 LLM 返回值
	logger.Infof("LLM Response (JSON Mode): %s", content)

	// 使用现有的 parseResponse 方法处理响应
	transactionData, err := p.parseResponse(content)
	if err != nil {
		return nil, history, err
	}

	newHistory := p.updateHistory(history, prompt, imageBase64, imageMIMEType, content)
	return transactionData, newHistory, nil
}

// isStructuredOutputUnsupported 检查错误是否表示不支持 Structured Outputs
func isStructuredOutputUnsupported(err error) bool {
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "unsupported") ||
		strings.Contains(errStr, "not supported") ||
		strings.Contains(errStr, "invalid_request") ||
		strings.Contains(errStr, "schema") ||
		strings.Contains(errStr, "response_format")
}

// updateHistory 更新对话历史
func (p *Parser) updateHistory(history []beancount.ConversationMessage, userPrompt, imageBase64, imageMIMEType, assistantResponse string) []beancount.ConversationMessage {
	// 先添加用户消息（保存文本提示词和图片）
	if userPrompt != "" {
		history = append(history, beancount.ConversationMessage{
			Role:          "user",
			Content:       userPrompt,
			ImageBase64:   imageBase64,
			ImageMIMEType: imageMIMEType,
		})
	}
	// 再添加助手响应
	history = append(history, beancount.ConversationMessage{
		Role:    "assistant",
		Content: assistantResponse,
	})
	return history
}

// generateTransactionSchema 生成 TransactionData 的 JSON Schema
func generateTransactionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"datetime":  map[string]string{"type": "string"},
			"flag":      map[string]string{"type": "string"},
			"payee":     map[string]string{"type": "string"},
			"narration": map[string]string{"type": "string"},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]string{"type": "string"},
			},
			"postings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"account":  map[string]string{"type": "string"},
						"amount":   map[string]string{"type": "string"},
						"currency": map[string]string{"type": "string"},
						"flag":     map[string]string{"type": "string"},
					},
					"required": []string{"account", "amount", "currency", "flag"},
				},
			},
			"order_id": map[string]string{"type": "string"},
			"extra": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"k": map[string]string{"type": "string"},
						"v": map[string]string{"type": "string"},
					},
					"required": []string{"k", "v"},
				},
			},
			"special_directives": map[string]any{
				"type":  "array",
				"items": map[string]string{"type": "string"},
			},
		},
		"required": []string{"datetime", "flag", "payee", "narration", "tags", "postings", "order_id", "extra", "special_directives"},
	}
}

type transactionDataRaw struct {
	DateTime          string                  `json:"datetime"`
	Flag              string                  `json:"flag"`
	Payee             string                  `json:"payee"`
	Narration         string                  `json:"narration"`
	Tags              []string                `json:"tags"`
	Postings          []beancount.PostingData `json:"postings"`
	OrderID           string                  `json:"order_id"`
	Extra             json.RawMessage         `json:"extra"`
	SpecialDirectives []string                `json:"special_directives"`
}

type extraKV struct {
	K string `json:"k"`
	V string `json:"v"`
}

func parseTransactionDataJSON(response string) (*beancount.TransactionData, error) {
	var raw transactionDataRaw
	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	extra, err := parseExtra(raw.Extra)
	if err != nil {
		return nil, err
	}

	transactionData := &beancount.TransactionData{
		DateTime:          raw.DateTime,
		Flag:              raw.Flag,
		Payee:             raw.Payee,
		Narration:         raw.Narration,
		Tags:              raw.Tags,
		Postings:          raw.Postings,
		OrderID:           raw.OrderID,
		Extra:             extra,
		SpecialDirectives: raw.SpecialDirectives,
	}

	return transactionData, nil
}

func parseExtra(raw json.RawMessage) (map[string]string, error) {
	extra := map[string]string{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return extra, nil
	}

	var extraList []extraKV
	if err := json.Unmarshal(raw, &extraList); err == nil {
		for _, item := range extraList {
			if item.K == "" {
				continue
			}
			extra[item.K] = item.V
		}
		return extra, nil
	}

	if err := json.Unmarshal(raw, &extra); err == nil {
		return extra, nil
	}

	return nil, fmt.Errorf("invalid extra format: expected array of {k,v} or object")
}

// parseResponse 解析 LLM 响应
func (p *Parser) parseResponse(response string) (*beancount.TransactionData, error) {
	// 提取 JSON（可能包含 markdown 代码块）
	response = strings.TrimSpace(response)

	// 移除 markdown 代码块标记
	if after, ok := strings.CutPrefix(response, "```json"); ok {
		response = after
	}
	if after, ok := strings.CutPrefix(response, "```"); ok {
		response = after
	}
	if before, ok := strings.CutSuffix(response, "```"); ok {
		response = before
	}
	response = strings.TrimSpace(response)

	// 解析 JSON
	transactionData, err := parseTransactionDataJSON(response)
	if err != nil {
		return nil, err
	}

	// 检查是否为空对象
	if transactionData.DateTime == "" && transactionData.Payee == "" && transactionData.Narration == "" {
		return nil, fmt.Errorf("no transaction data found")
	}

	return transactionData, nil
}

// ParseTime 解析日期时间字符串
func (p *Parser) ParseTime(datetimeStr string) (time.Time, error) {
	if datetimeStr == "" {
		return time.Now(), nil
	}

	// 尝试解析完整的日期时间格式
	layout := "2006-01-02 15:04:05"
	t, err := time.Parse(layout, datetimeStr)
	if err == nil {
		return t, nil
	}

	// 尝试解析日期格式
	dateLayout := "2006-01-02"
	date, err := time.Parse(dateLayout, datetimeStr)
	if err == nil {
		// 使用当前时间
		now := time.Now()
		return time.Date(date.Year(), date.Month(), date.Day(), now.Hour(), now.Minute(), now.Second(), 0, now.Location()), nil
	}

	return time.Now(), fmt.Errorf("invalid datetime format: %s", datetimeStr)
}

// GetTransactionType 获取交易类型
func (p *Parser) GetTransactionType(transactionType string) beancount.TransactionType {
	switch strings.ToLower(transactionType) {
	case "expense":
		return beancount.TransactionTypeExpense
	case "income":
		return beancount.TransactionTypeIncome
	case "transfer":
		return beancount.TransactionTypeTransfer
	default:
		return ""
	}
}
