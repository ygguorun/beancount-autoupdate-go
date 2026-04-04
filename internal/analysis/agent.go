package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"beancount-autoupdate/internal/logger"

	"github.com/openai/openai-go/v3"
)

const defaultAgentPrompt = `你是一个专业的 Beancount 账单分析助手。

工作原则：
1. 只基于工具返回结果回答，不得编造数据。
2. 优先使用 run_bean_query 回答明细和统计问题。
3. 需要检查账本有效性时使用 run_bean_check。
4. 需要快速总览指标时可使用 run_quick_analysis。
5. 当用户问题不清晰时，先给出你能确认的结果，再提出1个最小化澄清问题。
6. 使用用户语言回复；默认中文。

BQL 语法约束（必须遵守）：
- 使用 BQL，不要使用通用 SQL 方言。
- 聚合金额请使用 sum(position)，不要使用 SUM(amount) 或 SUM(postings.units)。
- 账户过滤请使用正则：account ~ '^Expenses:Food'；不要用 CONTAINS。
- 时间过滤优先使用 year/month，或 date >= 2026-04-01 AND date <= 2026-04-30。
- 不要使用 DURING、不要给日期加双引号。

可复用示例：
- SELECT payee, sum(position) AS total WHERE account ~ '^Expenses:Food' AND year = 2026 AND month = 4 GROUP BY payee ORDER BY total DESC LIMIT 5
- SELECT account, sum(position) WHERE account ~ '^Expenses:' GROUP BY account ORDER BY sum(position) DESC
- SELECT date, payee, narration, account, position WHERE account ~ '^Expenses:Food' AND date >= 2026-04-01 AND date <= 2026-04-30 ORDER BY date DESC LIMIT 30

输出风格：
- 先给结论，再给 2-4 条关键发现。
- 需要时给出下一步建议（例如可继续查询的方向）。
- 若工具报错，明确说明失败原因，并给出可执行的修复建议。`

type analysisSession struct {
	Turns      []conversationTurn
	LastActive time.Time
}

type conversationTurn struct {
	Role    string
	Content string
}

type beanQueryToolArgs struct {
	Query     string `json:"query"`
	Format    string `json:"format"`
	Numberify bool   `json:"numberify"`
}

type quickAnalysisToolArgs struct {
	Report string `json:"report"`
	Year   int    `json:"year"`
	TopN   int    `json:"top_n"`
}

func (s *Service) AnalyzeAgent(ctx context.Context, userID int, userText string, now time.Time) (*Result, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if !s.agentEnabled {
		return nil, ErrAgentDisabled
	}
	query := strings.TrimSpace(userText)
	if query == "" {
		return nil, fmt.Errorf("analysis query is empty")
	}

	logger.Debugf("分析 Agent 请求: userID=%d query=%q", userID, truncateLogText(query, 240))

	history := s.getSessionTurns(userID, now)
	logger.Debugf("分析 Agent 会话上下文: userID=%d turns=%d", userID, len(history))
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(history)+2)
	messages = append(messages, openai.SystemMessage(s.buildAgentPrompt(now)))
	for _, turn := range history {
		if turn.Role == "assistant" {
			messages = append(messages, openai.AssistantMessage(turn.Content))
			continue
		}
		messages = append(messages, openai.UserMessage(turn.Content))
	}
	messages = append(messages, openai.UserMessage(query))

	sections := make([]Section, 0, s.maxToolCalls)
	finalAnswer := ""

	for range s.maxToolCalls {
		logger.Debugf("分析 Agent LLM 调用: userID=%d model=%s messages=%d", userID, s.agentModel, len(messages))
		completion, err := s.agentClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:    s.agentModel,
			Messages: messages,
			Tools:    s.agentToolDefinitions(),
		})
		if err != nil {
			return nil, fmt.Errorf("agent llm call failed: %w", err)
		}
		if len(completion.Choices) == 0 {
			return nil, fmt.Errorf("agent llm returned empty choices")
		}

		choice := completion.Choices[0].Message
		if len(choice.ToolCalls) == 0 {
			finalAnswer = strings.TrimSpace(choice.Content)
			logger.Debugf("分析 Agent 生成最终回复: userID=%d content_len=%d", userID, len(finalAnswer))
			break
		}
		logger.Debugf("分析 Agent 需要工具调用: userID=%d tool_calls=%d", userID, len(choice.ToolCalls))

		messages = append(messages, choice.ToParam())
		for _, toolCall := range choice.ToolCalls {
			logger.Debugf("分析 Agent 工具调用: userID=%d tool=%s args=%s", userID, toolCall.Function.Name, truncateLogText(strings.TrimSpace(toolCall.Function.Arguments), 320))
			output, cmd, title, execErr := s.executeToolCall(ctx, strings.TrimSpace(toolCall.Function.Name), strings.TrimSpace(toolCall.Function.Arguments))
			if execErr != nil {
				logger.Warnf("分析 Agent 工具执行失败: tool=%s err=%v", toolCall.Function.Name, execErr)
			}
			logger.Debugf("分析 Agent 工具返回: userID=%d tool=%s output_len=%d", userID, toolCall.Function.Name, len(output))

			sections = append(sections, Section{
				Title:   title,
				Command: cmd,
				Output:  trimOutputLines(output, s.maxOutputLines),
			})
			messages = append(messages, openai.ToolMessage(output, toolCall.ID))
		}
	}

	if finalAnswer == "" {
		if len(sections) == 0 {
			return nil, fmt.Errorf("analysis agent did not produce a final response within %d tool steps", s.maxToolCalls)
		}

		logger.Warnf("分析 Agent 在 %d 次工具调用后未产出最终回复，回退为本地错误总结", s.maxToolCalls)
		finalAnswer = buildToolFailureSummary(query, sections)
	}

	s.updateSession(userID, query, finalAnswer, now)
	logger.Debugf("分析 Agent 会话更新: userID=%d", userID)

	return &Result{
		Skill:    Skill("agent_chat"),
		Title:    "账单分析问答",
		Summary:  finalAnswer,
		Sections: sections,
	}, nil
}

func (s *Service) IsSessionActive(userID int) bool {
	if s == nil || !s.Enabled() || !s.agentEnabled {
		return false
	}

	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	s.cleanupExpiredSessionsLocked(time.Now())
	_, ok := s.sessions[userID]
	logger.Debugf("分析 Agent 会话检查: userID=%d active=%v", userID, ok)
	return ok
}

func (s *Service) ResetSession(userID int) {
	if s == nil {
		return
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	delete(s.sessions, userID)
	logger.Debugf("分析 Agent 会话已重置: userID=%d", userID)
}

func (s *Service) buildAgentPrompt(now time.Time) string {
	return strings.TrimSpace(s.agentPrompt) + "\n\n当前时间: " + now.Format("2006-01-02 15:04:05") + "\n账本文件: " + s.ledgerFile
}

func (s *Service) getSessionTurns(userID int, now time.Time) []conversationTurn {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	s.cleanupExpiredSessionsLocked(now)
	session, ok := s.sessions[userID]
	if !ok {
		logger.Debugf("分析 Agent 会话不存在: userID=%d", userID)
		return nil
	}
	session.LastActive = now

	turns := make([]conversationTurn, len(session.Turns))
	copy(turns, session.Turns)
	return turns
}

func (s *Service) updateSession(userID int, userText, assistantText string, now time.Time) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()

	s.cleanupExpiredSessionsLocked(now)
	session, ok := s.sessions[userID]
	if !ok {
		session = &analysisSession{}
		s.sessions[userID] = session
	}

	session.Turns = append(session.Turns,
		conversationTurn{Role: "user", Content: userText},
		conversationTurn{Role: "assistant", Content: assistantText},
	)

	maxTurns := s.maxHistoryTurns * 2
	if maxTurns > 0 && len(session.Turns) > maxTurns {
		session.Turns = append([]conversationTurn(nil), session.Turns[len(session.Turns)-maxTurns:]...)
	}
	session.LastActive = now
	logger.Debugf("分析 Agent 会话写入: userID=%d turns=%d", userID, len(session.Turns))
}

func (s *Service) cleanupExpiredSessionsLocked(now time.Time) {
	if s.sessionTTL <= 0 {
		return
	}
	for userID, session := range s.sessions {
		if now.Sub(session.LastActive) > s.sessionTTL {
			delete(s.sessions, userID)
			logger.Debugf("分析 Agent 会话过期清理: userID=%d", userID)
		}
	}
}

func (s *Service) agentToolDefinitions() []openai.ChatCompletionToolUnionParam {
	return []openai.ChatCompletionToolUnionParam{
		openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "run_bean_query",
			Description: openai.String("执行 Beancount Query Language 查询，适合统计、排行、趋势问题"),
			Parameters: openai.FunctionParameters{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"query": map[string]string{"type": "string"},
					"format": map[string]interface{}{
						"type": "string",
						"enum": []string{"text", "csv", "beancount"},
					},
					"numberify": map[string]string{"type": "boolean"},
				},
				"required": []string{"query"},
			},
		}),
		openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "run_bean_check",
			Description: openai.String("检查账本语法与一致性"),
			Parameters: openai.FunctionParameters{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]interface{}{},
			},
		}),
		openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "run_quick_analysis",
			Description: openai.String("运行快速财务分析脚本，适合净资产、储蓄率、Top 支出等总览问题"),
			Parameters: openai.FunctionParameters{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"report": map[string]interface{}{
						"type": "string",
						"enum": []string{"all", "net_worth", "savings_rate", "top_expenses", "monthly_expenses"},
					},
					"year":  map[string]string{"type": "integer"},
					"top_n": map[string]string{"type": "integer"},
				},
				"required": []string{"report"},
			},
		}),
	}
}

func (s *Service) executeToolCall(ctx context.Context, toolName, args string) (string, string, string, error) {
	logger.Debugf("执行工具调用: tool=%s args=%s", toolName, truncateLogText(args, 320))
	switch toolName {
	case "run_bean_query":
		var parsed beanQueryToolArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return fmt.Sprintf("tool_error: invalid arguments for %s: %v", toolName, err), toolName, "BQL 查询", err
		}
		result, cmd, err := s.runBeanQueryTool(ctx, parsed)
		return result, cmd, "BQL 查询", err
	case "run_bean_check":
		result, cmd, err := s.runBeanCheckTool(ctx)
		return result, cmd, "账本校验", err
	case "run_quick_analysis":
		var parsed quickAnalysisToolArgs
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			return fmt.Sprintf("tool_error: invalid arguments for %s: %v", toolName, err), toolName, "快速分析", err
		}
		result, cmd, err := s.runQuickAnalysisTool(ctx, parsed)
		return result, cmd, "快速分析", err
	default:
		err := fmt.Errorf("unsupported tool: %s", toolName)
		return "tool_error: " + err.Error(), toolName, "工具调用", err
	}
}

func (s *Service) runBeanQueryTool(ctx context.Context, args beanQueryToolArgs) (string, string, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		err := fmt.Errorf("query is empty")
		return "tool_error: " + err.Error(), "run_bean_query", err
	}

	if err := validateBQLQuery(query); err != nil {
		return "tool_error: " + err.Error() + "\n" + bqlSyntaxHint(query), "run_bean_query", err
	}

	cliArgs := make([]string, 0, 6)
	format := strings.TrimSpace(strings.ToLower(args.Format))
	if format != "" {
		switch format {
		case "text", "csv", "beancount":
			cliArgs = append(cliArgs, "-f", format)
		default:
			err := fmt.Errorf("unsupported format: %s", format)
			return "tool_error: " + err.Error(), "run_bean_query", err
		}
	}
	if args.Numberify {
		cliArgs = append(cliArgs, "-m")
	}
	cliArgs = append(cliArgs, s.ledgerFile, query)

	spec := commandSpec{name: s.beanQueryBin, args: cliArgs}
	logger.Debugf("run_bean_query 参数: format=%s numberify=%v query=%q", format, args.Numberify, truncateLogText(query, 300))
	stdout, stderr, err := s.runCommand(ctx, spec)
	output := combineCommandOutput(stdout, stderr)
	if err != nil {
		compactErr := compactBeanQueryError(stderr, query)
		return "tool_error: " + compactErr, renderCommand(spec), err
	}

	return output, renderCommand(spec), nil
}

func (s *Service) runBeanCheckTool(ctx context.Context) (string, string, error) {
	spec := commandSpec{name: s.beanCheckBin, args: []string{s.ledgerFile}}
	logger.Debugf("run_bean_check 执行")
	stdout, stderr, err := s.runCommand(ctx, spec)
	output := combineCommandOutput(stdout, stderr)
	if err != nil {
		return "tool_error: " + err.Error() + "\n" + output, renderCommand(spec), err
	}

	if strings.TrimSpace(output) == "" {
		output = "bean-check completed without output"
	}

	return output, renderCommand(spec), nil
}

func (s *Service) runQuickAnalysisTool(ctx context.Context, args quickAnalysisToolArgs) (string, string, error) {
	pythonBin := filepath.Join(s.pythonVenvPath, "bin", "python")
	if _, err := os.Stat(pythonBin); err != nil {
		wrapped := fmt.Errorf("python interpreter not found at %s: %w", pythonBin, err)
		return "tool_error: " + wrapped.Error(), "run_quick_analysis", wrapped
	}
	if _, err := os.Stat(s.pythonScriptPath); err != nil {
		wrapped := fmt.Errorf("analysis script not found at %s: %w", s.pythonScriptPath, err)
		return "tool_error: " + wrapped.Error(), "run_quick_analysis", wrapped
	}

	report := strings.TrimSpace(strings.ToLower(args.Report))
	if report == "" {
		report = "all"
	}

	cliArgs := []string{s.pythonScriptPath, s.ledgerFile}
	switch report {
	case "all":
		cliArgs = append(cliArgs, "--all")
	case "net_worth":
		cliArgs = append(cliArgs, "--net-worth")
	case "savings_rate":
		cliArgs = append(cliArgs, "--savings-rate")
	case "top_expenses":
		topN := args.TopN
		if topN <= 0 {
			topN = 10
		}
		cliArgs = append(cliArgs, "--top-expenses", strconv.Itoa(topN))
	case "monthly_expenses":
		cliArgs = append(cliArgs, "--monthly-expenses")
	default:
		err := fmt.Errorf("unsupported report type: %s", report)
		return "tool_error: " + err.Error(), "run_quick_analysis", err
	}

	if args.Year > 0 {
		cliArgs = append(cliArgs, "--year", strconv.Itoa(args.Year))
	}

	logger.Debugf("run_quick_analysis 参数: report=%s year=%d top_n=%d", report, args.Year, args.TopN)

	spec := commandSpec{name: pythonBin, args: cliArgs}
	stdout, stderr, err := s.runCommand(ctx, spec)
	output := combineCommandOutput(stdout, stderr)
	if err != nil {
		return "tool_error: " + err.Error() + "\n" + output, renderCommand(spec), err
	}

	return output, renderCommand(spec), nil
}

func renderCommand(spec commandSpec) string {
	if len(spec.args) == 0 {
		return spec.name
	}
	return strings.TrimSpace(spec.name + " " + strings.Join(spec.args, " "))
}

func combineCommandOutput(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)

	if stdout == "" && stderr == "" {
		return ""
	}
	if stdout == "" {
		return stderr
	}
	if stderr == "" {
		return stdout
	}

	return stdout + "\n\n[stderr]\n" + stderr
}

func truncateLogText(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max] + "..."
}

func validateBQLQuery(query string) error {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return fmt.Errorf("query is empty")
	}

	invalidPatterns := []struct {
		token string
		hint  string
	}{
		{token: " contains ", hint: "请使用正则匹配 `account ~ '...'`，不要使用 CONTAINS"},
		{token: " during ", hint: "请改用 year/month 或 date 区间，不要使用 DURING"},
		{token: "sum(amount", hint: "请使用 `sum(position)` 聚合金额"},
		{token: "sum(postings.units", hint: "请使用 `sum(position)` 聚合金额"},
	}

	for _, item := range invalidPatterns {
		if strings.Contains(q, item.token) {
			return fmt.Errorf("query contains unsupported syntax: %s (%s)", strings.TrimSpace(item.token), item.hint)
		}
	}

	quotedDatePattern := regexp.MustCompile(`(?i)\bdate\s*(>=|<=|=|>|<)\s*"\d{4}-\d{2}(-\d{2})?"`)
	if quotedDatePattern.MatchString(query) {
		return fmt.Errorf("date comparisons should not quote dates; use date >= 2026-04-01")
	}

	return nil
}

func bqlSyntaxHint(query string) string {
	hints := []string{
		"BQL hint: 使用 `sum(position)`，不要使用 `SUM(amount)` 或 `SUM(postings.units)`。",
		"BQL hint: 账户过滤用 `account ~ '^Expenses:Food'`，不要用 CONTAINS。",
		"BQL hint: 时间过滤用 `year=YYYY AND month=MM`，或 `date >= YYYY-MM-DD AND date <= YYYY-MM-DD`。",
		"BQL example: SELECT payee, sum(position) AS total WHERE account ~ '^Expenses:Food' AND year = 2026 AND month = 4 GROUP BY payee ORDER BY total DESC LIMIT 5",
	}

	q := strings.ToLower(query)
	if strings.Contains(q, "contains") || strings.Contains(q, "during") || strings.Contains(q, "sum(amount") || strings.Contains(q, "sum(postings.units") {
		return strings.Join(hints, "\n")
	}

	return strings.Join(hints[:2], "\n")
}

func compactBeanQueryError(stderr, query string) string {
	errText := strings.TrimSpace(stderr)
	if errText == "" {
		return "bean-query failed without stderr output"
	}

	reason := "bean-query 执行失败"
	if strings.Contains(errText, "ParseError") || strings.Contains(strings.ToLower(errText), "syntax error") {
		reason = "BQL 语法错误"
	}

	firstLine := firstNonEmptyLine(errText)
	parseLine := firstLineContaining(errText, "ParseError")
	if parseLine == "" {
		parseLine = firstLineContaining(strings.ToLower(errText), "syntax error")
	}

	parts := []string{reason}
	if firstLine != "" {
		parts = append(parts, "detail: "+firstLine)
	}
	if parseLine != "" && parseLine != firstLine {
		parts = append(parts, "parser: "+parseLine)
	}
	parts = append(parts, bqlSyntaxHint(query))

	return strings.Join(parts, "\n")
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstLineContaining(text, needle string) string {
	if needle == "" {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func buildToolFailureSummary(query string, sections []Section) string {
	if len(sections) == 0 {
		return "这次分析未获取到可用数据，请稍后重试。"
	}

	last := sections[len(sections)-1]
	reason := firstNonEmptyLine(last.Output)
	if reason == "" {
		reason = "工具执行失败"
	}

	return "本次问题暂未成功得到结果。\n" +
		"- 用户问题: " + query + "\n" +
		"- 失败原因: " + reason + "\n" +
		"- 建议: 你可以直接让我执行一条 BQL 查询，例如\n" +
		"  SELECT payee, sum(position) AS total WHERE account ~ '^Expenses:Food' AND year = 2026 AND month = 4 GROUP BY payee ORDER BY total DESC LIMIT 5"
}
