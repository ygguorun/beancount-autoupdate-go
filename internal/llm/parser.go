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
	baseURL string
	model   string
	apiKey  string
	timeout time.Duration
	client  *http.Client
	mu      sync.Mutex
}

// NewParser 创建 LLM 解析器
func NewParser(baseURL, model, apiKey string, timeout int) *Parser {
	return &Parser{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		timeout: time.Duration(timeout) * time.Second,
		client: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
	}
}

// ParseImage 解析图片中的交易信息
func (p *Parser) ParseImage(imagePath string, expenseCategories, incomeCategories, transferCategories, accountNames, tagNames []string) (*beancount.TransactionData, error) {
	fmt.Printf("[LLM] 开始解析图片: %s\n", imagePath)

	// 编码图片
	fmt.Printf("[LLM] 编码图片...\n")
	base64Image, err := p.encodeImage(imagePath)
	if err != nil {
		fmt.Printf("[LLM] 编码图片失败: %v\n", err)
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}
	fmt.Printf("[LLM] 图片编码完成，大小: %d bytes\n", len(base64Image))

	// 构建提示词
	fmt.Printf("[LLM] 构建提示词...\n")
	prompt := p.buildPrompt(accountNames, tagNames)
	fmt.Printf("[LLM] 提示词长度: %d\n", len(prompt))

	// 调用 LLM API
	fmt.Printf("[LLM] 调用 LLM API...\n")
	response, err := p.callLLM(prompt, base64Image)
	if err != nil {
		fmt.Printf("[LLM] 调用 LLM API 失败: %v\n", err)
		return nil, fmt.Errorf("failed to call LLM: %w", err)
	}
	fmt.Printf("[LLM] LLM API 响应长度: %d\n", len(response))
	fmt.Printf("[LLM] LLM API 响应内容: %s\n", response[:min(200, len(response))])

	// 解析响应
	fmt.Printf("[LLM] 解析响应...\n")
	transactionData, err := p.parseResponse(response)
	if err != nil {
		fmt.Printf("[LLM] 解析响应失败: %v\n", err)
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	fmt.Printf("[LLM] 解析成功\n")
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

	return prompt
}

// callLLM 调用 LLM API
func (p *Parser) callLLM(prompt, base64Image string) (string, error) {
	fmt.Printf("[LLM] 构建请求体...\n")
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
	fmt.Printf("[LLM] 请求体大小: %d bytes\n", len(jsonBody))

	// 创建 HTTP 请求
	url := p.baseURL + "/chat/completions"
	fmt.Printf("[LLM] 请求 URL: %s\n", url)
	fmt.Printf("[LLM] 模型: %s\n", p.model)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	if len(p.apiKey) > 0 {
		fmt.Printf("[LLM] API Key 长度: %d\n", len(p.apiKey))
	}

	// 发送请求
	fmt.Printf("[LLM] 发送请求...\n")
	startTime := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		fmt.Printf("[LLM] 发送请求失败: %v\n", err)
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(startTime)
	fmt.Printf("[LLM] 请求完成，耗时: %v, 状态码: %d\n", elapsed, resp.StatusCode)

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("[LLM] 读取响应失败: %v\n", err)
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	fmt.Printf("[LLM] 响应体大小: %d bytes\n", len(body))

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("[LLM] API 返回错误状态码 %d: %s\n", resp.StatusCode, string(body))
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	fmt.Printf("[LLM] 解析响应 JSON...\n")
	var apiResponse struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &apiResponse); err != nil {
		fmt.Printf("[LLM] 解析 JSON 失败: %v\n", err)
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(apiResponse.Choices) == 0 {
		fmt.Printf("[LLM] 响应中没有 choices\n")
		return "", fmt.Errorf("no choices in response")
	}

	fmt.Printf("[LLM] 解析成功，choices 数量: %d\n", len(apiResponse.Choices))
	return apiResponse.Choices[0].Message.Content, nil
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
