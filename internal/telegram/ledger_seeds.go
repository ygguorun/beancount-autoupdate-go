package telegram

import (
	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/ledgercli"
)

// recognizeFirstPass 首轮识别入口：tools_enabled 开启且 rledger 可用时走工具探索
// 识别（模型自主检索账本历史 → 记账 → 程序校验闸），否则保持现网单发链路。
func (b *Bot) recognizeFirstPass(
	imagePath string,
	accounts []string,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	if b.config.LLM.ToolsEnabled && b.ledger.Available() {
		return ledgercli.Recognize(b.llmParser, b.ledger, imagePath, accounts, nil)
	}
	return b.llmParser.ParseImageWithHistory(imagePath, accounts, nil, "")
}

// guidanceRecognize 引导重试入口：tools 开启且 rledger 可用时走带探索能力的
// 续写轮（可查历史、产出过校验闸）；否则保持旧链路的纯文本续写，行为不变。
func (b *Bot) guidanceRecognize(
	guidance string,
	history []beancount.ConversationMessage,
	imagePath string,
	accounts []string,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	if b.config.LLM.ToolsEnabled && b.ledger.Available() {
		return ledgercli.RecognizeContinue(b.llmParser, b.ledger, guidance, history, imagePath, accounts)
	}
	return b.llmParser.ParseImageWithHistory(imagePath, accounts, history, guidance)
}
