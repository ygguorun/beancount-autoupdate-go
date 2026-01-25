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
	DateTime       string            `json:"datetime"`
	Flag           string            `json:"flag"`
	Payee          string            `json:"payee"`
	Narration      string            `json:"narration"`
	Tags           []string          `json:"tags"`
	Postings       []PostingData     `json:"postings"`
	OrderID        string            `json:"order_id"`
	Discount       string            `json:"discount"`
	OriginalAmount string            `json:"original_amount"`
	Extra          map[string]string `json:"extra"`
}

// PendingTransaction 待确认的交易
type PendingTransaction struct {
	UserID              int
	Date                string
	Time                string
	Flag                string
	Payee               string
	Narration           string
	Tags                []string
	Postings            []PostingData
	OrderID             string
	Discount            string
	OriginalAmount      string
	ImageURL            string
	TempImageURL        string
	TempWebDAVPath      string
	EditingPostingIndex int
	AvailableAccounts   []string
	AccountPage         int
	LastMessageID       int // 最后一条消息的ID，用于编辑消息
}
