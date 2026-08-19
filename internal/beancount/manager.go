package beancount

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"beancount-autoupdate/internal/embed"
	"beancount-autoupdate/internal/logger"
)

// Manager Beancount 管理器
type Manager struct {
	dataDir           string
	title             string
	operatingCurrency string
	accountDir        string
	beansDir          string
	mainFile          string
	isNewRepo         bool
	mu                sync.RWMutex
}

// NewManager 创建 Beancount 管理器
func NewManager(dataDir, title, operatingCurrency string) (*Manager, error) {
	m := &Manager{
		dataDir:           dataDir,
		title:             title,
		operatingCurrency: operatingCurrency,
		accountDir:        filepath.Join(dataDir, "account"),
		beansDir:          filepath.Join(dataDir, "beans"),
		mainFile:          filepath.Join(dataDir, "main.bean"),
	}

	// 检查是否为新仓库
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		m.isNewRepo = true
	} else if _, err := os.Stat(m.accountDir); os.IsNotExist(err) {
		m.isNewRepo = true
	}

	// 初始化目录结构
	if err := m.initDirectories(); err != nil {
		return nil, err
	}

	return m, nil
}

// IsNewRepo 是否为新仓库
func (m *Manager) IsNewRepo() bool {
	return m.isNewRepo
}

// initDirectories 初始化目录结构
func (m *Manager) initDirectories() error {
	// 创建目录
	dirs := []string{m.dataDir, m.accountDir, m.beansDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// 创建账户定义文件
	if err := m.initAccountFiles(); err != nil {
		return err
	}

	// 创建主文件
	if err := m.initMainFile(); err != nil {
		return err
	}

	return nil
}

// initAccountFiles 初始化账户定义文件
func (m *Manager) initAccountFiles() error {
	accountFiles := map[string]AccountType{
		"assets.bean":      AccountTypeAssets,
		"expenses.bean":    AccountTypeExpenses,
		"income.bean":      AccountTypeIncome,
		"liabilities.bean": AccountTypeLiabilities,
		"equity.bean":      AccountTypeEquity,
	}

	for filename, accountType := range accountFiles {
		filePath := filepath.Join(m.accountDir, filename)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			// 创建默认账户文件
			template := m.getAccountTemplate(accountType)
			if err := os.WriteFile(filePath, []byte(template), 0o644); err != nil {
				return fmt.Errorf("failed to create account file %s: %w", filename, err)
			}
		}
	}

	return nil
}

// getAccountTemplate 获取账户模板
func (m *Manager) getAccountTemplate(accountType AccountType) string {
	date := time.Now().Format("2006-01-02")

	// 从嵌入的模板文件读取
	templateName := ""
	switch accountType {
	case AccountTypeAssets:
		templateName = "assets.bean"
	case AccountTypeExpenses:
		templateName = "expenses.bean"
	case AccountTypeIncome:
		templateName = "income.bean"
	case AccountTypeLiabilities:
		templateName = "liabilities.bean"
	case AccountTypeEquity:
		templateName = "equity.bean"
	}

	// 读取嵌入的模板
	template, err := embed.GetTemplate(templateName)
	if err != nil {
		logger.Fatalf("Failed to load template %s: %v", templateName, err)
	}

	// 替换日期占位符
	return strings.ReplaceAll(template, "{current_date}", date)
}

// initMainFile 初始化主文件
func (m *Manager) initMainFile() error {
	// 创建 init.bean 文件
	initBeanPath := filepath.Join(m.dataDir, "init.bean")
	if _, err := os.Stat(initBeanPath); os.IsNotExist(err) {
		template, err := embed.GetTemplate("init.bean")
		if err != nil {
			return fmt.Errorf("failed to load init.bean template: %w", err)
		}

		if err := os.WriteFile(initBeanPath, []byte(template), 0o644); err != nil {
			return fmt.Errorf("failed to create init.bean file: %w", err)
		}
	}

	// 创建 main.bean 文件
	if _, err := os.Stat(m.mainFile); os.IsNotExist(err) {
		var builder strings.Builder

		// 配置选项
		fmt.Fprintf(&builder, `option "title" "%s"
option "operating_currency" "%s"

`, m.title, m.operatingCurrency)

		// 导入账户定义文件
		fmt.Fprintf(&builder, `; Include account definitions
include "account/assets.bean"
include "account/expenses.bean"
include "account/income.bean"
include "account/liabilities.bean"
include "account/equity.bean"
`)

		// 导入资产初始化文件（在账户定义之后）
		fmt.Fprintf(&builder, `; Include asset initialization
include "init.bean"
`)

		// 导入 beans 文件夹下的所有 bean 文件
		fmt.Fprintf(&builder, `; Include transaction files
include "beans/**/*.bean"
`)

		if err := os.WriteFile(m.mainFile, []byte(builder.String()), 0o644); err != nil {
			return fmt.Errorf("failed to create main file: %w", err)
		}
	}
	return nil
}

// GetAccountFile 获取账户类型对应的文件路径
func (m *Manager) GetAccountFile(accountType AccountType) string {
	fileMap := map[AccountType]string{
		AccountTypeAssets:      "assets.bean",
		AccountTypeExpenses:    "expenses.bean",
		AccountTypeIncome:      "income.bean",
		AccountTypeLiabilities: "liabilities.bean",
		AccountTypeEquity:      "equity.bean",
	}
	return filepath.Join(m.accountDir, fileMap[accountType])
}

// GetTransactionFilePath 根据日期获取交易文件路径
func (m *Manager) GetTransactionFilePath(date time.Time) string {
	year := date.Year()
	month := date.Month()

	// 按年月组织：beans/YYYY/MM.bean
	yearDir := filepath.Join(m.beansDir, fmt.Sprintf("%d", year))
	if err := os.MkdirAll(yearDir, 0o755); err != nil {
		logger.Errorf("创建目录失败: %v", err)
		return ""
	}

	monthFile := filepath.Join(yearDir, fmt.Sprintf("%02d.bean", month))
	return monthFile
}

// GetAllCategories 获取所有分类（账户）
func (m *Manager) GetAllCategories() map[AccountType][]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	accounts := make(map[AccountType][]string)
	for _, accountType := range []AccountType{
		AccountTypeAssets,
		AccountTypeExpenses,
		AccountTypeIncome,
		AccountTypeLiabilities,
		AccountTypeEquity,
	} {
		accounts[accountType] = m.parseAccountFile(accountType)
	}

	return accounts
}

// parseAccountFile 解析账户文件
func (m *Manager) parseAccountFile(accountType AccountType) []string {
	filePath := m.GetAccountFile(accountType)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return []string{}
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return []string{}
	}

	accounts := []string{}
	lines := strings.Split(string(content), "\n")

	// 匹配 open 语句的正则表达式
	re := regexp.MustCompile(`\bopen\s+(\S+)`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}

		matches := re.FindStringSubmatch(line)
		if len(matches) > 1 {
			accounts = append(accounts, matches[1])
		}
	}

	return accounts
}

// AddTransactionFromPostings 从分录列表添加交易记录
func (m *Manager) AddTransactionFromPostings(date time.Time, flag, payee, narration string, tags []string, postings []PostingData, orderID, imageURL string, extra map[string]string) (string, error) {
	if err := validateTransactionInput(flag, payee, narration, tags, postings, orderID, imageURL, extra); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	dateStr := date.Format("2006-01-02")
	timeStr := date.Format("15:04:05")

	var builder strings.Builder

	// 构建交易条目头部
	if payee != "" {
		fmt.Fprintf(&builder, "%s %s \"%s\" \"%s\"", dateStr, flag, escapeString(payee), escapeString(narration))
	} else {
		fmt.Fprintf(&builder, "%s %s \"%s\"", dateStr, flag, escapeString(narration))
	}

	// 添加标签
	if len(tags) > 0 {
		for _, tag := range tags {
			fmt.Fprintf(&builder, " #%s", tag)
		}
	}
	builder.WriteString("\n")

	// 添加时间元数据
	fmt.Fprintf(&builder, "  time: \"%s\"\n", timeStr)

	// 添加订单相关元数据
	if orderID != "" {
		fmt.Fprintf(&builder, "  order-id: \"%s\"\n", escapeString(orderID))
	}
	if imageURL != "" {
		fmt.Fprintf(&builder, "  image-url: \"%s\"\n", escapeString(imageURL))
	}

	// 添加 extra 字段（包括 discount 和 original_amount）
	// discount 和 original_amount 必须同时出现或不出现
	hasDiscount := extra["discount"] != ""
	hasOriginalAmount := extra["original_amount"] != ""

	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := extra[key]
		if value == "" {
			continue
		}

		// 检查是否为 discount 或 original_amount 字段
		if key == "discount" || key == "original_amount" {
			// 只有当两个字段都存在时才添加
			if hasDiscount && hasOriginalAmount {
				metadataKey := strings.ReplaceAll(key, "_", "-")
				fmt.Fprintf(&builder, "  %s: \"%s\"\n", metadataKey, escapeString(value))
			}
		} else {
			// 其他字段正常处理
			metadataKey := strings.ReplaceAll(key, "_", "-")
			fmt.Fprintf(&builder, "  %s: \"%s\"\n", metadataKey, escapeString(value))
		}
	}

	// 添加分录
	for _, posting := range postings {
		amount := posting.Amount
		if amount == "" {
			amount = "0.00"
		}
		currency := posting.Currency
		if currency == "" {
			currency = m.operatingCurrency
		}

		if posting.Flag != "" {
			fmt.Fprintf(&builder, "  %s %s %s %s\n", posting.Flag, posting.Account, amount, currency)
		} else {
			fmt.Fprintf(&builder, "  %s %s %s\n", posting.Account, amount, currency)
		}
	}

	builder.WriteString("\n")

	entry := builder.String()

	// 写入文件
	filePath := m.GetTransactionFilePath(date)

	// 确保文件存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if err := os.WriteFile(filePath, []byte(""), 0o644); err != nil {
			return "", fmt.Errorf("failed to create transaction file: %w", err)
		}
	}

	// 追加交易
	if err := m.appendToFile(filePath, entry); err != nil {
		return "", fmt.Errorf("failed to write transaction: %w", err)
	}

	return entry, nil
}

// appendToFile 追加内容到文件
func (m *Manager) appendToFile(filePath, content string) error {
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			logger.Errorf("关闭文件失败: %v", closeErr)
		}
	}()

	_, err = file.WriteString(content)
	return err
}

// AddTransactionWithDirectives 添加包含特殊指令的交易记录
func (m *Manager) AddTransactionWithDirectives(transaction *TransactionData) (string, error) {
	// 解析日期时间
	date, err := time.Parse("2006-01-02 15:04:05", transaction.DateTime)
	if err != nil {
		return "", fmt.Errorf("failed to parse datetime: %w", err)
	}

	var builder strings.Builder

	// 如果有特殊指令，只添加特殊指令，不添加基础交易条目
	if len(transaction.SpecialDirectives) > 0 {
		if err := validateDirectives(transaction.SpecialDirectives); err != nil {
			return "", err
		}

		m.mu.Lock()
		defer m.mu.Unlock()

		// 添加特殊指令
		for _, directive := range transaction.SpecialDirectives {
			builder.WriteString(directive)
			builder.WriteString("\n")
		}

		builder.WriteString("\n")
	} else {
		// 没有特殊指令，使用标准交易记录
		entry, err := m.AddTransactionFromPostings(
			date,
			transaction.Flag,
			transaction.Payee,
			transaction.Narration,
			transaction.Tags,
			transaction.Postings,
			transaction.OrderID,
			"",
			transaction.Extra,
		)
		return entry, err
	}

	entry := builder.String()

	// 写入文件
	filePath := m.GetTransactionFilePath(date)

	// 确保文件存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if err := os.WriteFile(filePath, []byte(""), 0o644); err != nil {
			return "", fmt.Errorf("failed to create transaction file: %w", err)
		}
	}

	// 追加交易
	if err := m.appendToFile(filePath, entry); err != nil {
		return "", fmt.Errorf("failed to write transaction: %w", err)
	}

	return entry, nil
}

var metadataKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

func validateTransactionInput(flag, payee, narration string, tags []string, postings []PostingData, orderID, imageURL string, extra map[string]string) error {
	if strings.TrimSpace(flag) == "" || strings.ContainsAny(flag, "\r\n \t") {
		return fmt.Errorf("invalid transaction flag")
	}
	for _, value := range []string{payee, narration, orderID, imageURL} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("transaction text cannot contain newlines")
		}
	}
	for _, tag := range tags {
		if tag == "" || strings.ContainsAny(tag, "\r\n \t") {
			return fmt.Errorf("invalid tag %q", tag)
		}
	}
	for _, posting := range postings {
		if posting.Account == "" || strings.ContainsAny(posting.Account, "\r\n \t") ||
			strings.ContainsAny(posting.Amount, "\r\n \t") || strings.ContainsAny(posting.Currency, "\r\n \t") ||
			strings.ContainsAny(posting.Flag, "\r\n \t") {
			return fmt.Errorf("invalid posting")
		}
	}
	for key, value := range extra {
		if !metadataKeyPattern.MatchString(key) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid metadata")
		}
	}
	return nil
}

func validateDirectives(directives []string) error {
	for _, directive := range directives {
		if strings.TrimSpace(directive) == "" || strings.ContainsAny(directive, "\r\n") {
			return fmt.Errorf("invalid special directive")
		}
	}
	return nil
}

func escapeString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
