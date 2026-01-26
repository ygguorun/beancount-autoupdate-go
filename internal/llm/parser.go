package llm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/embed"
)

var logger = logrus.StandardLogger()

// Parser LLM 解析器
type Parser struct {
	baseURL      string
	model        string
	apiKey       string
	timeout      time.Duration
	client       *http.Client
	mu           sync.Mutex
	extendPrompt string
}

// NewParser 创建 LLM 解析器
func NewParser(baseURL, model, apiKey string, timeout int, extendPrompt string) *Parser {
	return &Parser{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		timeout: time.Duration(timeout) * time.Second,
		client: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
		extendPrompt: extendPrompt,
	}
}

// ParseImage 解析图片中的交易信息
func (p *Parser) ParseImage(imagePath string, expenseCategories, incomeCategories, transferCategories, accountNames, tagNames []string) (*beancount.TransactionData, error) {
	// 编码图片
	base64Image, err := p.encodeImage(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	// 构建提示词
	prompt := p.buildPrompt(accountNames, tagNames)

	// 调用 LLM API
	response, err := p.callLLM(prompt, base64Image)
	if err != nil {
		return nil, fmt.Errorf("failed to call LLM: %w", err)
	}

	// 解析响应
	transactionData, err := p.parseResponse(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return transactionData, nil
}

// ParseImageFromBytes 从字节数组解析图片
func (p *Parser) ParseImageFromBytes(imageData []byte, expenseCategories, incomeCategories, transferCategories, accountNames, tagNames []string) (*beancount.TransactionData, error) {
	// 编码图片
	base64Image := base64.StdEncoding.EncodeToString(imageData)

	// 构建提示词
	prompt := p.buildPrompt(accountNames, tagNames)

	// 调用 LLM API
	response, err := p.callLLM(prompt, base64Image)
	if err != nil {
		return nil, fmt.Errorf("failed to call LLM: %w", err)
	}

	// 解析响应
	transactionData, err := p.parseResponse(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return transactionData, nil
}

// encodeImage 将图片编码为 base64
func (p *Parser) encodeImage(imagePath string) (string, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
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
	prompt := string(templateContent)
	prompt = strings.ReplaceAll(prompt, "{current_time}", currentTime)
	prompt = strings.ReplaceAll(prompt, "{account_names}", accountList)
	prompt = strings.ReplaceAll(prompt, "{tags}", tagList)

	// 处理扩展 prompt
	if p.extendPrompt != "" {
		parts := strings.SplitN(p.extendPrompt, ":", 2)
		if len(parts) == 2 {
			mode := strings.TrimSpace(parts[0])
			content := strings.TrimSpace(parts[1])

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

// callLLM 调用 LLM API
func (p *Parser) callLLM(prompt, base64Image string) (string, error) {
	// 构建请求体
	requestBody := map[string]interface{}{
		"model": p.model,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": prompt,
					},
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url": fmt.Sprintf("data:image/jpeg;base64,%s", base64Image),
						},
					},
				},
			},
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// 创建 HTTP 请求
	url := p.baseURL + "/chat/completions"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	// 发送请求
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var apiResponse struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &apiResponse); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(apiResponse.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return apiResponse.Choices[0].Message.Content, nil
}

// parseResponse 解析 LLM 响应
func (p *Parser) parseResponse(response string) (*beancount.TransactionData, error) {
	// 打印 LLM 返回值
	logger.Infof("LLM Response: %s", response)

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
	var transactionData beancount.TransactionData
	if err := json.Unmarshal([]byte(response), &transactionData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	// 检查是否为空对象
	if transactionData.DateTime == "" && transactionData.Payee == "" && transactionData.Narration == "" {
		return nil, fmt.Errorf("no transaction data found")
	}

	return &transactionData, nil
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
