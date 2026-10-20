package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/embed"
	"beancount-autoupdate/internal/logger"
)

// ToolExecutor 工具执行函数：接收工具名与模型生成的参数 JSON，返回结果文本（原样回填给模型）。
// 实现方负责错误文本化（返回 {"error":...}），不 panic、不返回 error——工具循环把它当黑盒。
type ToolExecutor func(name, argsJSON string) string

const (
	// MaxExploreRounds 首轮识别的工具探索回合上限（每次含 tool_calls 的回复计 1 回合）。
	MaxExploreRounds = 5
	// MaxContinueRounds 引导续写轮的工具探索回合上限：窄修正，预算小于首轮。
	MaxContinueRounds = 3
)

// logLLMUsage 记录单次 LLM 请求的耗时与 token 用量（cached=0 时可能是网关未上报缓存）。
func logLLMUsage(label string, elapsed time.Duration, promptTokens, completionTokens, cachedTokens int64) {
	logger.Infof("LLM 用量[%s]: 耗时=%s prompt=%d completion=%d cached=%d",
		label, elapsed.Round(10*time.Millisecond), promptTokens, completionTokens, cachedTokens)
}

// logResponsesUsage 适配 responses 协议的用量记录。
func logResponsesUsage(label string, elapsed time.Duration, usage responses.ResponseUsage) {
	logLLMUsage(label, elapsed, usage.InputTokens, usage.OutputTokens, usage.InputTokensDetails.CachedTokens)
}

func currentNowString() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// BuildBookingPrompt 构建工具识别的记账指令（独立模板，不受 extend_prompt 影响）。
// 它作为首条 user 消息与图片同消息发出，成为整段会话逐字节稳定的前缀。
func (p *Parser) BuildBookingPrompt(accountNames []string) string {
	content, err := embed.GetTemplate("transaction_booking.txt")
	if err != nil {
		logger.Fatalf("Failed to load booking template: %v", err)
	}
	accountList := "None"
	if len(accountNames) > 0 {
		accountList = strings.Join(accountNames, "\n")
	}
	prompt := strings.ReplaceAll(content, "{current_time}", currentNowString())
	return strings.ReplaceAll(prompt, "{account_names}", accountList)
}

// queryLedgerToolSpec 模型可见的账本检索工具。参数是"意图槽位"，
// 查询形态/LIMIT/脱敏全部焊死在 ledgercli.ToolLoop 执行侧，模型摸不到 BQL。
func queryLedgerToolSpec() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name: "query_ledger",
		Description: openai.String(
			"查询已确认账本历史，返回渲染好的交易块文本（金额脱敏为 ±X.XX，最多 5 笔）。" +
				"用途：核对商户既有记账风格；按订单号找退款原单；按金额+关键词找关联/重复记录。" +
				"order_id 命中时忽略 keywords。查询失败返回 {\"error\":...}；空结果返回 matched: none，" +
				"并附带各过滤阶段命中统计（关键词→金额→剔除），据此只放宽清零的那一个条件。"),
		Parameters: shared.FunctionParameters{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"keywords": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "1~3 个检索词（商户/平台/人名/商品词，可用 OCR 变体拼写），匹配 payee 与 narration",
				},
				"order_id": map[string]any{
					"type":        "string",
					"description": "订单号精确检索（匹配交易/分录 metadata order-id）；限 6~64 位字母数字 _ -",
				},
				"amount": map[string]any{
					"type":        "string",
					"description": "可选。十进制金额；只返回任一分录绝对值与之相等(<0.005)的交易",
				},
				"account_prefix": map[string]any{
					"type":        "string",
					"description": "可选。科目前缀过滤，如 Expenses:FoodAndDrink",
				},
				"since": map[string]any{
					"type":        "string",
					"description": "可选。起始月 YYYY-MM（含）",
				},
				"until": map[string]any{
					"type":        "string",
					"description": "可选。结束月 YYYY-MM（含）",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "返回交易笔数 1~5，默认 3",
				},
			},
		},
	})
}

// RecognizeWithTools 单会话工具探索识别（首轮）：记账提示词+图片一次发出，
// 模型自主调用 query_ledger 检索账本历史（≤maxRounds 回合）后输出最终交易 JSON。
//
// 返回 history = [user(提示词+图), assistant(最终JSON)]——工具轨迹（assistant
// tool_calls / tool 结果 / 驳回与纠错消息）只存在于循环局部，引导续写
// （ContinueWithTools）回放该稳定前缀，不重放工具轮。
func (p *Parser) RecognizeWithTools(
	imagePath string,
	bookingPrompt string,
	exec ToolExecutor,
	maxRounds int,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	if maxRounds <= 0 {
		maxRounds = MaxExploreRounds
	}
	base64Image, imageMIMEType, err := p.encodeImage(imagePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encode image: %w", err)
	}
	msgs := p.buildMessages(bookingPrompt, base64Image, nil, "", imageMIMEType)
	return p.runToolLoop(msgs, nil, bookingPrompt, base64Image, imageMIMEType, exec, maxRounds, true)
}

// ContinueWithTools 引导续写轮：回放既有稳定前缀（含图片，不重发），追加用户引导，
// 继续带工具探索。nudge 关闭——用户引导文本本身即权威修正指令，不再强制模型先查询；
// 需要参考历史时模型可自行调用 query_ledger（预算 MaxContinueRounds）。
func (p *Parser) ContinueWithTools(
	history []beancount.ConversationMessage,
	guidance string,
	exec ToolExecutor,
	maxRounds int,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	if len(history) == 0 {
		return nil, nil, fmt.Errorf("引导续写需要已有对话历史（首轮请用 RecognizeWithTools）")
	}
	if maxRounds <= 0 {
		maxRounds = MaxContinueRounds
	}
	// buildMessages 对历史首条逐字节回放、其余加引导前缀，当前引导同样经其拼入尾部
	msgs := p.buildMessages("", "", history, guidance, "")
	return p.runToolLoop(msgs, history, guidance, "", "", exec, maxRounds, false)
}

// runToolLoop 工具循环核心（首轮与引导轮共用）。
// hist/userPrompt/image* 用于持久化历史与失败历史构造；API/网络错误原样返回 hist
// （与 ParseImageWithHistory 语义一致），格式失败返回含错误现场的续写历史。
func (p *Parser) runToolLoop(
	msgs []openai.ChatCompletionMessageParamUnion,
	hist []beancount.ConversationMessage,
	userPrompt, imageBase64, imageMIMEType string,
	exec ToolExecutor,
	maxRounds int,
	nudge bool,
) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
	if p.protocol != llmRequestProtocolChatCompletions {
		return nil, hist, fmt.Errorf("tools 探索循环目前仅支持 chat 协议（responses 未实现）")
	}
	schema := generateTransactionSchema()
	rounds, queried, nudged, echoed := 0, false, false, false

	failParse := func(content string, parseErr error) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
		return nil, p.buildFailedParseHistory(hist, userPrompt, imageBase64, imageMIMEType, content, parseErr),
			fmt.Errorf("failed to parse response: %w", parseErr)
	}
	done := func(content string) (*beancount.TransactionData, []beancount.ConversationMessage, error) {
		txn, parseErr := p.parseResponse(content)
		if parseErr != nil {
			return failParse(content, parseErr)
		}
		return txn, p.updateHistory(hist, userPrompt, imageBase64, imageMIMEType, content), nil
	}

	for {
		content, toolCalls, callErr := p.callBookingRound(msgs, schema, rounds)
		if callErr != nil {
			return nil, hist, callErr
		}
		logger.Debugf("识别/轮%d LLM 输出: %s", rounds, content)

		if len(toolCalls) > 0 {
			rounds++
			queried = true
			msgs = append(msgs, assistantToolCallsMessage(content, toolCalls))
			for _, call := range toolCalls {
				logger.Debugf("工具调用[%s] 参数: %s", call.Function.Name, call.Function.Arguments)
				result := exec(call.Function.Name, call.Function.Arguments)
				logger.Debugf("工具回填[%s](%d 字节): %s", call.Function.Name, len(result), result)
				msgs = append(msgs, openai.ToolMessage(result, call.ID))
			}
			if rounds >= maxRounds {
				break
			}
			continue
		}

		txn, parseErr := p.parseResponse(content)
		if parseErr != nil {
			if !echoed {
				echoed = true
				msgs = append(msgs,
					openai.AssistantMessage(content),
					openai.UserMessage(fmt.Sprintf("上一轮没有返回有效交易 JSON: %v。请严格按照 JSON Schema 只输出单个交易 JSON 对象，不要用数组包裹，也不要添加多余文本。", parseErr)),
				)
				continue
			}
			return failParse(content, parseErr)
		}
		if nudge && !queried && !nudged {
			nudged = true
			msgs = append(msgs,
				openai.AssistantMessage(content),
				openai.UserMessage("请先调用 query_ledger 检索账本历史（退款先查订单号，其余先按商户/平台关键词查），再输出最终交易 JSON。"),
			)
			continue
		}
		return txn, p.updateHistory(hist, userPrompt, imageBase64, imageMIMEType, content), nil
	}

	// 回合用尽：在同一会话尾部追加硬停机指令收敛。tools 参数保持不动——
	// 摘掉 tools 会改变请求前缀，网关前缀缓存整段作废（实测强制轮 cached=0）。
	logger.Warnf("工具探索达到 %d 回合上限，追加收敛指令", rounds)
	msgs = append(msgs, openai.UserMessage("探索回合已达上限。请立即基于已有信息只输出最终交易 JSON，不要再调用工具。"))
	for attempt := 0; attempt < 2; attempt++ {
		content, calls, callErr := p.callBookingRound(msgs, schema, rounds)
		if callErr != nil {
			return nil, hist, callErr
		}
		logger.Debugf("识别/收敛轮%d LLM 输出: %s", attempt, content)
		if len(calls) > 0 {
			// 模型仍在探索：回填本轮结果，最后一次坚持要最终输出
			msgs = append(msgs, assistantToolCallsMessage(content, calls))
			for _, c := range calls {
				msgs = append(msgs, openai.ToolMessage(exec(c.Function.Name, c.Function.Arguments), c.ID))
			}
			continue
		}
		return done(content)
	}
	return failParse("", fmt.Errorf("收敛指令后模型仍坚持调用工具，未产出最终输出"))
}

// callBookingRound 单次识别请求。tools 恒在请求里：摘除会破坏会话前缀一致性，
// 强制收敛也靠会话尾部的停机指令实现，不改请求形态。
func (p *Parser) callBookingRound(
	msgs []openai.ChatCompletionMessageParamUnion,
	schema map[string]any,
	round int,
) (string, []openai.ChatCompletionMessageToolCallUnion, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	t0 := time.Now()

	params := openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: msgs,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
				JSONSchema: openai.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:        "transaction_data",
					Description: openai.String("beancount transaction object"),
					Schema:      schema,
					Strict:      openai.Bool(true),
				},
			},
		},
		Tools: []openai.ChatCompletionToolUnionParam{queryLedgerToolSpec()},
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return "", nil, fmt.Errorf("API call failed: %w", err)
	}
	logLLMUsage(fmt.Sprintf("识别/轮%d", round), time.Since(t0),
		completion.Usage.PromptTokens, completion.Usage.CompletionTokens, completion.Usage.PromptTokensDetails.CachedTokens)

	if len(completion.Choices) == 0 {
		return "", nil, fmt.Errorf("empty choices in LLM response")
	}
	choice := completion.Choices[0]
	calls := choice.Message.ToolCalls
	content := choice.Message.Content
	if len(calls) == 0 && content == "" {
		return "", nil, fmt.Errorf("empty response from LLM")
	}
	return content, calls, nil
}

// assistantToolCallsMessage 回放模型的 assistant tool_calls 消息（OpenAI 协议要求
// tool 结果前必须存在对应的 assistant 消息）。
func assistantToolCallsMessage(content string, calls []openai.ChatCompletionMessageToolCallUnion) openai.ChatCompletionMessageParamUnion {
	am := openai.ChatCompletionAssistantMessageParam{}
	if content != "" {
		am.Content.OfString = openai.String(content)
	}
	for _, c := range calls {
		am.ToolCalls = append(am.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: c.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      c.Function.Name,
					Arguments: c.Function.Arguments,
				},
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &am}
}
