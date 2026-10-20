package ledgercli

import (
	"fmt"
	"strings"
	"time"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/llm"
	"beancount-autoupdate/internal/logger"
)

const correctionGuidanceFormat = `你上一版结果未通过账本校验（rledger check 合入全账本验证），存在以下问题：
%s
请只修正上述问题涉及的字段，其余字段保持不变，重新输出完整交易 JSON。
修正配平错误时必须调整既有分录的金额/科目使其归零，禁止新增一对互相抵消的分录来凑平衡。`

// CorrectionGuidance 生成校验错误回灌的引导文本。
func CorrectionGuidance(issues []Issue) string {
	return fmt.Sprintf(correctionGuidanceFormat, FormatIssues(issues))
}

// FormatIssues 将校验问题渲染为逐行文本。
func FormatIssues(issues []Issue) string {
	lines := make([]string, 0, len(issues))
	for _, iss := range issues {
		field := iss.Field
		if field == "" {
			field = "transaction"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s", field, iss.Code, iss.Message))
	}
	return strings.Join(lines, "\n")
}

// refineWithValidation 账本校验与修正轮：发现问题则以引导文本在同一会话续写修正一次，
// 失败保留原结果。程序质量闸——不经模型发起，模型无跳过权。
func refineWithValidation(
	p *llm.Parser,
	e *Executor,
	txn *beancount.TransactionData,
	history []beancount.ConversationMessage,
	imagePath string,
	accounts []string,
) (*beancount.TransactionData, []beancount.ConversationMessage) {
	t0 := time.Now()
	issues, err := e.Validate(txn)
	logger.Infof("账本校验耗时 %s: err=%v issues=%d", time.Since(t0).Round(time.Millisecond), err, len(issues))
	if err != nil {
		logger.Warnf("账本校验跳过: %v", err)
		return txn, history
	}
	if len(issues) == 0 {
		return txn, history
	}
	logger.Infof("账本校验发现 %d 个问题，引导修正一轮: %s", len(issues), FormatIssues(issues))
	fixed, newHist, err := p.ParseImageWithHistory(imagePath, accounts, history, CorrectionGuidance(issues))
	if err != nil || fixed == nil {
		logger.Warnf("校验修正失败，保留原结果: %v", err)
		return txn, history
	}
	logger.Info("校验修正完成")
	return fixed, newHist
}
