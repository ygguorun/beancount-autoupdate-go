package analysis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"beancount-autoupdate/internal/logger"
)

var (
	ErrDisabled          = errors.New("analysis feature is disabled")
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
	Enabled        bool
	BeanQueryBin   string
	BeanReportBin  string
	LedgerFile     string
	Timeout        time.Duration
	MaxOutputLines int
	Runner         CommandRunner
	Summarizer     Summarizer
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
	enabled        bool
	beanQueryBin   string
	beanReportBin  string
	ledgerFile     string
	timeout        time.Duration
	maxOutputLines int
	runner         CommandRunner
	summarizer     Summarizer
}

// NewService 创建报表分析服务。
func NewService(opts Options) *Service {
	if opts.BeanQueryBin == "" {
		opts.BeanQueryBin = "bean-query"
	}
	if opts.BeanReportBin == "" {
		opts.BeanReportBin = "bean-report"
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

	return &Service{
		enabled:        opts.Enabled,
		beanQueryBin:   opts.BeanQueryBin,
		beanReportBin:  opts.BeanReportBin,
		ledgerFile:     opts.LedgerFile,
		timeout:        opts.Timeout,
		maxOutputLines: opts.MaxOutputLines,
		runner:         opts.Runner,
		summarizer:     opts.Summarizer,
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

	incomeStatementCandidates := []commandSpec{
		{name: s.beanReportBin, args: []string{"-b", begin, "-e", end, s.ledgerFile, "income_statement"}},
		{name: s.beanReportBin, args: []string{s.ledgerFile, "income_statement", "-b", begin, "-e", end}},
		{name: s.beanReportBin, args: []string{s.ledgerFile, "income_statement"}},
	}

	expenseCandidates := []commandSpec{
		{name: s.beanReportBin, args: []string{"-b", begin, "-e", end, s.ledgerFile, "balances", "Expenses"}},
		{name: s.beanReportBin, args: []string{s.ledgerFile, "balances", "Expenses"}},
		{name: s.beanQueryBin, args: []string{s.ledgerFile, "SELECT account, sum(position) WHERE account ~ '^Expenses:' GROUP BY account ORDER BY sum(position)"}},
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
	for _, spec := range sec.candidates {
		stdout, stderr, err := s.runCommand(ctx, spec)
		if err != nil {
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

	stdout, stderr, err := s.runner.Run(cmdCtx, spec.name, spec.args...)
	if cmdCtx.Err() == context.DeadlineExceeded {
		return stdout, stderr, fmt.Errorf("command timeout")
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
