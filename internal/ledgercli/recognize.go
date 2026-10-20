package ledgercli

import (
	"time"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/llm"
	"beancount-autoupdate/internal/logger"
)

// Recognize 工具探索识别：单会话发出 [记账提示词+图片]，模型自主调用 query_ledger
// 检索账本历史（Go 仅为执行器，≤llm.MaxExploreRounds 回合）后输出最终交易 JSON，
// 再经过程序校验修正闸。
//
// seedExclude 非 nil 时用于工具结果里剔除与当前截图同源的账本记录（回归防抄答案）；
// 生产传 nil。失败返回的 history 语义与 llm 层一致（API 错 nil / 格式错含纠错上下文）。
func Recognize(
	p *llm.Parser,
	e *Executor,
	imagePath string,
	accounts []string,
	seedExclude func(date, payee string) bool,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	prompt := p.BuildBookingPrompt(accounts)
	loop := e.NewToolLoop(seedExclude)

	t0 := time.Now()
	txn, history, err := p.RecognizeWithTools(imagePath, prompt, loop.Exec, llm.MaxExploreRounds)
	logger.Infof("工具探索识别完成（耗时 %s）err=%v 工具输出累计=%d 字节",
		time.Since(t0).Round(time.Millisecond), err, loop.OutputUsed())
	if err != nil || txn == nil {
		return txn, history, err
	}
	logger.Infof("识别结果 payee=%s narration=%s postings=%d", txn.Payee, txn.Narration, len(txn.Postings))

	txn, history = refineWithValidation(p, e, txn, history, imagePath, accounts)
	return txn, history, nil
}

// RecognizeContinue 引导续写（tools 模式）：回放稳定前缀 + 用户引导，模型可继续
// 自主调用 query_ledger（≤llm.MaxContinueRounds 回合），产出同样过校验修正闸。
// seedExclude 恒为 nil——引导针对当前待确认交易，同单历史正是该给模型看的查重信息。
func RecognizeContinue(
	p *llm.Parser,
	e *Executor,
	guidance string,
	history []beancount.ConversationMessage,
	imagePath string,
	accounts []string,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	loop := e.NewToolLoop(nil)
	t0 := time.Now()
	txn, hist, err := p.ContinueWithTools(history, guidance, loop.Exec, llm.MaxContinueRounds)
	logger.Infof("引导续写完成（耗时 %s）err=%v 工具输出累计=%d 字节",
		time.Since(t0).Round(time.Millisecond), err, loop.OutputUsed())
	if err != nil || txn == nil {
		return txn, hist, err
	}
	logger.Infof("引导续写结果 payee=%s narration=%s postings=%d", txn.Payee, txn.Narration, len(txn.Postings))
	txn, hist = refineWithValidation(p, e, txn, hist, imagePath, accounts)
	return txn, hist, nil
}
