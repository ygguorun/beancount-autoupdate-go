package analysis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"beancount-autoupdate/internal/embed"
	"beancount-autoupdate/internal/logger"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var (
	ErrDisabled          = errors.New("analysis feature is disabled")
	ErrAgentDisabled     = errors.New("analysis agent is disabled")
	ErrUnsupportedIntent = errors.New("unsupported analysis intent")
)

// Skill 固定报表技能。
type Skill string

const (
	SkillMonthlySummary Skill = "monthly_summary"
	SkillProfitLoss     Skill = "profit_loss"
	SkillTopExpenses    Skill = "top_expenses"
)

// Result 分析结果。
type Result struct {
	Skill    Skill
	Title    string
	Summary  string
	Sections []Section
}

// Section 命令执行输出。
type Section struct {
	Title   string
	Command string
	Output  string
}

// Options 服务初始化参数。
type Options struct {
	Enabled          bool
	BeanQueryBin     string
	LedgerFile       string
	Timeout          time.Duration
	MaxOutputLines   int
	Runner           CommandRunner
	Summarizer       Summarizer
	AgentEnabled     bool
	LLMBaseURL       string
	LLMAPIKey        string
	LLMModel         string
	SessionTTL       time.Duration
	MaxHistoryTurns  int
	MaxToolCalls     int
	PythonVenvPath   string
	PythonScriptPath string
}

// Summarizer 使用 LLM 生成文字总结。
type Summarizer interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// CommandRunner 执行外部命令。
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, stderr string, err error)
}

type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// Service 报表分析服务。
type Service struct {
	enabled          bool
	beanQueryBin     string
	ledgerFile       string
	timeout          time.Duration
	maxOutputLines   int
	runner           CommandRunner
	summarizer       Summarizer
	agentEnabled     bool
	agentClient      openai.Client
	agentModel       string
	agentPrompt      string
	beanCheckBin     string
	pythonVenvPath   string
	pythonScriptPath string
	sessionTTL       time.Duration
	maxHistoryTurns  int
	maxToolCalls     int
	sessions         map[int]*analysisSession
	sessionMu        sync.Mutex
}

// NewService 创建报表分析服务。
func NewService(opts Options) *Service {
	if opts.BeanQueryBin == "" {
		opts.BeanQueryBin = "bean-query"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxOutputLines <= 0 {
		opts.MaxOutputLines = 120
	}
	if opts.Runner == nil {
		opts.Runner = &execRunner{}
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 30 * time.Minute
	}
	if opts.MaxHistoryTurns <= 0 {
		opts.MaxHistoryTurns = 8
	}
	if opts.MaxToolCalls <= 0 {
		opts.MaxToolCalls = 4
	}
	if opts.PythonVenvPath == "" {
		opts.PythonVenvPath = filepath.Join("beancount-skill-0.1.0", ".venv")
	}
	if opts.PythonScriptPath == "" {
		opts.PythonScriptPath = filepath.Join("beancount-skill-0.1.0", "scripts", "analyze_beancount.py")
	}

	agentPrompt := defaultAgentPrompt
	if prompt, err := embed.GetTemplate("analysis_agent_system_prompt.txt"); err == nil {
		agentPrompt = strings.TrimSpace(prompt)
	}

	var agentClient openai.Client
	agentEnabled := opts.AgentEnabled
	if agentEnabled {
		if strings.TrimSpace(opts.LLMAPIKey) == "" || strings.TrimSpace(opts.LLMModel) == "" {
			logger.Warn("分析 Agent 已启用但 LLM API Key 或模型为空，自动禁用 Agent")
			agentEnabled = false
		} else {
			requestOpts := []option.RequestOption{option.WithAPIKey(opts.LLMAPIKey)}
			if strings.TrimSpace(opts.LLMBaseURL) != "" {
				requestOpts = append(requestOpts, option.WithBaseURL(opts.LLMBaseURL))
			}
			agentClient = openai.NewClient(requestOpts...)
		}
	}

	return &Service{
		enabled:          opts.Enabled,
		beanQueryBin:     opts.BeanQueryBin,
		ledgerFile:       opts.LedgerFile,
		timeout:          opts.Timeout,
		maxOutputLines:   opts.MaxOutputLines,
		runner:           opts.Runner,
		summarizer:       opts.Summarizer,
		agentEnabled:     agentEnabled,
		agentClient:      agentClient,
		agentModel:       opts.LLMModel,
		agentPrompt:      agentPrompt,
		beanCheckBin:     "bean-check",
		pythonVenvPath:   opts.PythonVenvPath,
		pythonScriptPath: opts.PythonScriptPath,
		sessionTTL:       opts.SessionTTL,
		maxHistoryTurns:  opts.MaxHistoryTurns,
		maxToolCalls:     opts.MaxToolCalls,
		sessions:         make(map[int]*analysisSession),
	}
}

// Enabled 是否启用分析能力。
func (s *Service) Enabled() bool {
	return s != nil && s.enabled
}

// Analyze 按用户文本自动识别技能并执行。
func (s *Service) Analyze(ctx context.Context, userText string, now time.Time) (*Result, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}

	skill, ok := DetectSkill(userText)
	if !ok {
		return nil, ErrUnsupportedIntent
	}

	return s.AnalyzeSkill(ctx, skill, userText, now)
}

// AnalyzeSkill 执行指定技能。
func (s *Service) AnalyzeSkill(ctx context.Context, skill Skill, userText string, now time.Time) (*Result, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}

	plan, err := s.buildPlan(skill, now)
	if err != nil {
		return nil, err
	}

	result := &Result{
		Skill: skill,
		Title: plan.title,
	}

	for _, sec := range plan.sections {
		section, secErr := s.runSection(ctx, sec)
		if secErr != nil {
			return nil, secErr
		}
		result.Sections = append(result.Sections, section)
	}

	prompt := buildSummaryPrompt(result, userText)
	if s.summarizer != nil {
		summary, sumErr := s.summarizer.Summarize(ctx, prompt)
		if sumErr != nil {
			logger.Warnf("分析总结失败，使用降级文案: %v", sumErr)
			result.Summary = fallbackSummary(result)
		} else {
			result.Summary = strings.TrimSpace(summary)
		}
	} else {
		result.Summary = fallbackSummary(result)
	}

	return result, nil
}

// ParseSkillAlias 支持命令行别名。
func ParseSkillAlias(text string) (Skill, bool) {
	normalized := strings.TrimSpace(strings.ToLower(text))
	switch normalized {
	case "monthly", "month", "本月", "本月分析", "monthly_summary":
		return SkillMonthlySummary, true
	case "pl", "profit", "loss", "profit_loss", "损益", "损益表":
		return SkillProfitLoss, true
	case "expenses", "top", "top_expenses", "支出排行", "支出":
		return SkillTopExpenses, true
	default:
		return "", false
	}
}

// DetectSkill 根据中文自然语言识别技能。
func DetectSkill(userText string) (Skill, bool) {
	normalized := strings.TrimSpace(strings.ToLower(userText))
	if normalized == "" {
		return "", false
	}

	if containsAny(normalized, "损益", "利润", "income statement", "profit", "p&l") {
		return SkillProfitLoss, true
	}

	if containsAny(normalized, "支出排行", "支出 top", "top 支出", "花销排行", "最大支出") {
		return SkillTopExpenses, true
	}

	if containsAny(normalized, "本月", "这个月", "月度") &&
		containsAny(normalized, "账单", "分析", "总结", "报告") {
		return SkillMonthlySummary, true
	}

	if containsAny(normalized, "账单分析", "月报", "本月分析") {
		return SkillMonthlySummary, true
	}

	return "", false
}

type skillPlan struct {
	title    string
	sections []sectionPlan
}

type sectionPlan struct {
	title      string
	candidates []commandSpec
}

type commandSpec struct {
	name string
	args []string
}

func (s *Service) buildPlan(skill Skill, now time.Time) (skillPlan, error) {
	if strings.TrimSpace(s.ledgerFile) == "" {
		return skillPlan{}, fmt.Errorf("ledger file is empty")
	}

	begin, end := monthBounds(now)

	incomeStatementQueryWithRange := fmt.Sprintf(
		"SELECT account, sum(position) FROM OPEN ON %s CLOSE ON %s WHERE account ~ '^(Income|Expenses):' GROUP BY account ORDER BY account",
		begin,
		end,
	)
	incomeStatementQueryAll := "SELECT account, sum(position) WHERE account ~ '^(Income|Expenses):' GROUP BY account ORDER BY account"

	incomeStatementCandidates := []commandSpec{
		{name: s.beanQueryBin, args: []string{s.ledgerFile, incomeStatementQueryWithRange}},
		{name: s.beanQueryBin, args: []string{s.ledgerFile, incomeStatementQueryAll}},
	}

	expenseQueryWithRange := fmt.Sprintf(
		"SELECT account, sum(position) FROM OPEN ON %s CLOSE ON %s WHERE account ~ '^Expenses:' GROUP BY account ORDER BY sum(position) DESC",
		begin,
		end,
	)
	expenseQueryAll := "SELECT account, sum(position) WHERE account ~ '^Expenses:' GROUP BY account ORDER BY sum(position) DESC"

	expenseCandidates := []commandSpec{
		{name: s.beanQueryBin, args: []string{s.ledgerFile, expenseQueryWithRange}},
		{name: s.beanQueryBin, args: []string{s.ledgerFile, expenseQueryAll}},
	}

	switch skill {
	case SkillMonthlySummary:
		return skillPlan{
			title: "本月账单分析",
			sections: []sectionPlan{
				{title: "本月损益", candidates: incomeStatementCandidates},
				{title: "支出分类概览", candidates: expenseCandidates},
			},
		}, nil
	case SkillProfitLoss:
		return skillPlan{
			title: "损益表",
			sections: []sectionPlan{
				{title: "损益明细", candidates: incomeStatementCandidates},
			},
		}, nil
	case SkillTopExpenses:
		return skillPlan{
			title: "支出排行",
			sections: []sectionPlan{
				{title: "支出账户汇总", candidates: expenseCandidates},
			},
		}, nil
	default:
		return skillPlan{}, ErrUnsupportedIntent
	}
}

func (s *Service) runSection(ctx context.Context, sec sectionPlan) (Section, error) {
	var errs []string
	logger.Debugf("分析 section 开始: title=%s candidates=%d", sec.title, len(sec.candidates))
	for _, spec := range sec.candidates {
		logger.Debugf("尝试执行 section 候选命令: title=%s cmd=%s", sec.title, renderCommand(spec))
		stdout, stderr, err := s.runCommand(ctx, spec)
		if err != nil {
			logger.Debugf("section 候选命令失败: title=%s cmd=%s err=%v", sec.title, renderCommand(spec), err)
			errs = append(errs, fmt.Sprintf("%s %s: %v", spec.name, strings.Join(spec.args, " "), err))
			if strings.TrimSpace(stderr) != "" {
				errs = append(errs, strings.TrimSpace(stderr))
			}
			continue
		}

		output := strings.TrimSpace(stdout)
		if output == "" {
			if strings.TrimSpace(stderr) != "" {
				output = strings.TrimSpace(stderr)
			} else {
				errs = append(errs, fmt.Sprintf("%s %s: empty output", spec.name, strings.Join(spec.args, " ")))
				continue
			}
		}

		return Section{
			Title:   sec.title,
			Command: strings.TrimSpace(spec.name + " " + strings.Join(spec.args, " ")),
			Output:  trimOutputLines(output, s.maxOutputLines),
		}, nil
	}

	return Section{}, fmt.Errorf("failed to run section %q: %s", sec.title, strings.Join(errs, " | "))
}

func (s *Service) runCommand(ctx context.Context, spec commandSpec) (string, string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cmdText := renderCommand(spec)
	start := time.Now()
	logger.Debugf("执行命令开始: %s", cmdText)

	stdout, stderr, err := s.runner.Run(cmdCtx, spec.name, spec.args...)
	duration := time.Since(start)
	if cmdCtx.Err() == context.DeadlineExceeded {
		logger.Debugf("执行命令超时: cmd=%s duration=%s", cmdText, duration)
		return stdout, stderr, fmt.Errorf("command timeout")
	}

	if err != nil {
		logger.Debugf("执行命令失败: cmd=%s duration=%s stdout_len=%d stderr_len=%d err=%v", cmdText, duration, len(stdout), len(stderr), err)
		stderrPreview := previewMultilineText(stderr, 10, 1200)
		if stderrPreview != "" {
			logger.Debugf("执行命令失败 stderr 摘录: cmd=%s preview=%q", cmdText, stderrPreview)
		}
	} else {
		logger.Debugf("执行命令成功: cmd=%s duration=%s stdout_len=%d stderr_len=%d", cmdText, duration, len(stdout), len(stderr))
	}

	return stdout, stderr, err
}

func monthBounds(now time.Time) (string, string) {
	loc := now.Location()
	begin := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	end := begin.AddDate(0, 1, 0)
	return begin.Format("2006-01-02"), end.Format("2006-01-02")
}

func trimOutputLines(raw string, maxLines int) string {
	if maxLines <= 0 {
		return strings.TrimSpace(raw)
	}

	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}

	trimmed := lines[:maxLines]
	trimmed = append(trimmed, fmt.Sprintf("... (已截断，原始 %d 行)", len(lines)))
	return strings.Join(trimmed, "\n")
}

func previewMultilineText(text string, maxLines int, maxChars int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}

	if maxLines > 0 {
		lines := strings.Split(trimmed, "\n")
		if len(lines) > maxLines {
			trimmed = strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-maxLines)
		}
	}

	if maxChars > 0 && len(trimmed) > maxChars {
		trimmed = trimmed[:maxChars] + fmt.Sprintf("... (truncated, total chars: %d)", len(text))
	}

	return trimmed
}

func buildSummaryPrompt(result *Result, userText string) string {
	var builder strings.Builder
	builder.WriteString("你是财务分析助手，请基于下面 bean-* 命令输出给出中文总结。\n")
	builder.WriteString("要求：\n")
	builder.WriteString("1) 先给结论，再给2-4条关键观察。\n")
	builder.WriteString("2) 输出不要超过8行。\n")
	builder.WriteString("3) 不要编造不存在的数据。\n\n")
	fmt.Fprintf(&builder, "用户请求: %s\n", strings.TrimSpace(userText))
	fmt.Fprintf(&builder, "报表标题: %s\n\n", result.Title)

	for _, section := range result.Sections {
		fmt.Fprintf(&builder, "[%s]\n", section.Title)
		fmt.Fprintf(&builder, "命令: %s\n", section.Command)
		builder.WriteString("输出:\n")
		builder.WriteString(section.Output)
		builder.WriteString("\n\n")
	}

	return builder.String()
}

func fallbackSummary(result *Result) string {
	if len(result.Sections) == 0 {
		return "未获取到可分析的报表输出。"
	}
	return fmt.Sprintf("已生成 %d 个报表输出。以下为原始结果，请结合账本上下文核对。", len(result.Sections))
}

func containsAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}
