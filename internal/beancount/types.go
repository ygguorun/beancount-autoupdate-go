package beancount

// AccountType 账户类型枚举
type AccountType string

const (
	AccountTypeAssets      AccountType = "Assets"
	AccountTypeExpenses    AccountType = "Expenses"
	AccountTypeIncome      AccountType = "Income"
	AccountTypeLiabilities AccountType = "Liabilities"
	AccountTypeEquity      AccountType = "Equity"
)

// TransactionType 交易类型枚举
type TransactionType string

const (
	TransactionTypeExpense  TransactionType = "expense"
	TransactionTypeIncome   TransactionType = "income"
	TransactionTypeTransfer TransactionType = "transfer"
)

// PostingData 分录数据
type PostingData struct {
	Account  string `json:"account"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Flag     string `json:"flag"`
}

// TransactionData 交易数据
type TransactionData struct {
	DateTime          string            `json:"datetime"`
	Flag              string            `json:"flag"`
	Payee             string            `json:"payee"`
	Narration         string            `json:"narration"`
	Tags              []string          `json:"tags"`
	Postings          []PostingData     `json:"postings"`
	OrderID           string            `json:"order_id"`
	Extra             map[string]string `json:"extra"`
	SpecialDirectives []string          `json:"special_directives"` // 特殊指令，如 pad、balance 等
}

// ConversationMessage 对话消息（用于 LLM 对话历史）
type ConversationMessage struct {
	Role          string `json:"role"`         // "user" 或 "assistant"
	Content       string `json:"content"`      // 消息内容（文本）
	ImageBase64   string `json:"image_base64"` // 图片的 base64 编码（仅首次用户消息有）
	ImageMIMEType string `json:"image_mime_type,omitempty"`
}

// PendingTransaction 待确认的交易
type PendingTransaction struct {
	TransactionID                 string // 交易唯一标识符，用于支持多图片并发处理
	UserID                        int
	Date                          string
	Time                          string
	Flag                          string
	Payee                         string
	Narration                     string
	Tags                          []string
	Postings                      []PostingData
	OrderID                       string
	Extra                         map[string]string
	ImageURL                      string
	TempImageURL                  string
	TempWebDAVPath                string
	EditingPostingIndex           int
	AvailableAccounts             []string
	AccountPage                   int
	LastMessageID                 int                   // 最后一条消息的ID，用于编辑消息
	OriginalMessageID             int                   // 原始预览消息的ID，用于返回预览
	EditingPostingMessageID       int                   // 分录编辑界面的消息ID
	PreviousMessageIDs            []int                 // 之前需要删除的消息ID列表
	SpecialDirectives             []string              // 特殊指令，如 pad、balance 等
	SourceImageMessageID          int                   // 源图片消息ID（用户发送或Bot发送的图片）
	ConversationContextMessageIDs []int                 // 对话上下文消息ID列表（用户回复、Bot提示、预览消息等）
	OriginalTempFilePath          string                // 原始图片的临时文件路径，用于重新识别
	ConversationHistory           []ConversationMessage // LLM 对话历史，用于带引导的重试
}
