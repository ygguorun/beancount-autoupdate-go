// Package ledgercli 封装 rledger 命令行工具，提供账本历史检索（query）与
// 候选交易校验（check）。查询由 Go 拼装固定 BQL 模板：LIMIT、排序、输出
// 截断全部焊死在代码里，喂给 LLM 的上下文成本有稳定上限；调用方只能影响
// 过滤条件，不能影响查询形态。
package ledgercli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/logger"
)

const (
	// maxRunTimeout 单次 rledger 调用超时。本地进程通常毫秒级，
	// 但 Windows scoop shim 冷启动 + 全账本首次加载留足冗余。
	maxRunTimeout = 15 * time.Second
	// snippetFirstLine 校验临时文件中片段的起始行号（1 include，2 空行）。
	snippetFirstLine = 3
)

// Executor rledger 执行器。nil 表示功能未启用，所有方法 nil-safe（跳过）。
type Executor struct {
	bin      string
	mainFile string
	currency string
}

// New 构建执行器。path 为空时按 rledger、ag-rledger 顺序探测 PATH；
// 找不到不报错，由 Available() 汇报不可用，调用方统一降级。
func New(path, mainFile, currency string) *Executor {
	bin := strings.TrimSpace(path)
	if bin != "" {
		resolved, err := exec.LookPath(bin)
		if err != nil {
			// 置空使 Available()==false，调用方走干净降级而非每次 spawn 失败
			logger.Warnf("rledger 未找到(%s): %v，账本种子/校验功能停用", path, err)
			bin = ""
		} else {
			bin = resolved
		}
	} else {
		// rledger 优先：输出即裸 JSON；ag-rledger 带 envelope（见 unwrapEnvelope），
		// 仅作为备选（部分发行版只装 ag-rledger）。
		bin = ""
		for _, cand := range []string{"rledger", "ag-rledger"} {
			if resolved, err := exec.LookPath(cand); err == nil {
				bin = resolved
				break
			}
		}
		if bin == "" {
			logger.Warn("PATH 中没有 ag-rledger/rledger，账本种子/校验功能停用")
		}
	}

	if bin != "" {
		logger.Infof("rledger 执行器就绪: bin=%s, ledger=%s", bin, mainFile)
	}
	return &Executor{bin: bin, mainFile: mainFile, currency: currency}
}

// Available 报告执行器是否可用（nil 安全）。
func (e *Executor) Available() bool { return e != nil && e.bin != "" }

// QueryFilter 结构化查询条件。
type QueryFilter struct {
	Keywords      []string // 匹配 payee/narration，最多取 3 个
	OrderID       string   // 可选；非空时按 metadata order-id 精确检索，忽略 Keywords
	AccountPrefix string   // 可选，科目前缀
	Since         string   // 可选，起始月 YYYY-MM
	Until         string   // 可选，结束月 YYYY-MM（含）
	Limit         int      // 交易笔数，钳制到 [1,5]，默认 3
	MaxRows       int      // 可选，原始分录行上限；金额后过滤等宽检索场景调大（默认 limit*3，封顶 500）
}

// Row 一条账本分录（展平行）。
type Row struct {
	Date      string
	Payee     string
	Narration string
	Account   string
	Amount    string // 原始金额，仅在外部脱敏后使用，禁止直接进 LLM 上下文
	Currency  string
}

// Query 按条件检索已确认交易的分录行（按日期倒序）。
func (e *Executor) Query(f QueryFilter) ([]Row, error) {
	if !e.Available() {
		return nil, nil
	}

	var where []string
	if f.OrderID != "" {
		// 纵深校验：ToolLoop 已验过，这里兜住直接调用 Query 的其他路径
		if !orderIDPattern.MatchString(f.OrderID) {
			return nil, fmt.Errorf("order_id 格式非法: %q", f.OrderID)
		}
		where = append(where, fmt.Sprintf("any_meta('order-id') = '%s'", f.OrderID))
	} else {
		// 关键词组之间 OR（命中任一即可——若 AND，平台交易+新店名必然查空），
		// account/date 过滤条件与关键词组整体 AND。
		kwConds := make([]string, 0, 3)
		for _, kw := range f.Keywords {
			if len(kwConds) >= 3 {
				break
			}
			if s := sanitizeKeyword(kw); s != "" {
				kwConds = append(kwConds, fmt.Sprintf("payee ~ '%s' OR narration ~ '%s'", s, s))
			}
		}
		if len(kwConds) == 0 {
			return nil, nil
		}
		where = append(where, "("+strings.Join(kwConds, " OR ")+")")
	}
	if p := sanitizeKeyword(f.AccountPrefix); p != "" {
		where = append(where, fmt.Sprintf("account ~ '%s'", p))
	}
	// 日期字面量必须不带引号（实测 `date >= '2026-05-01'` 报 cannot compare values）
	if f.Since != "" {
		if bqlMonthRe.MatchString(f.Since) {
			where = append(where, fmt.Sprintf("date >= %s-01", f.Since))
		} else {
			logger.Warnf("忽略格式非法的 since（应为 YYYY-MM）: %q", f.Since)
		}
	}
	if f.Until != "" {
		if bqlMonthRe.MatchString(f.Until) {
			end, err := time.Parse("2006-01", f.Until)
			if err == nil {
				where = append(where, fmt.Sprintf("date < %s", end.AddDate(0, 1, 1).Format("2006-01-02")))
			}
		} else {
			logger.Warnf("忽略格式非法的 until（应为 YYYY-MM）: %q", f.Until)
		}
	}

	limit := f.Limit
	switch {
	case limit <= 0:
		limit = 3
	case limit > 5:
		limit = 5
	}
	rowsCap := limit * 3
	if f.MaxRows > rowsCap {
		rowsCap = f.MaxRows
	}
	if rowsCap > 500 {
		rowsCap = 500
	}

	q := fmt.Sprintf("SELECT date, payee, narration, account, position WHERE %s ORDER BY date DESC LIMIT %d",
		strings.Join(where, " AND "), rowsCap)

	out, stderr, err := e.run("query", e.mainFile, "-f", "json", q)
	if err != nil {
		return nil, fmt.Errorf("查询账本历史失败: %w", err)
	}

	var qo queryOutput
	payload, err := unwrapEnvelope(out)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &qo); err != nil {
		return nil, fmt.Errorf("解析 rledger query 输出失败: %w%s", err, decodeHint(out, stderr))
	}
	idx := make(map[string]int, len(qo.Columns))
	for i, c := range qo.Columns {
		idx[c] = i
	}

	rows := make([]Row, 0, len(qo.Rows))
	for _, r := range qo.Rows {
		row := Row{
			Date:      cellString(cellAt(r, idx, "date")),
			Payee:     cellString(cellAt(r, idx, "payee")),
			Narration: cellString(cellAt(r, idx, "narration")),
			Account:   cellString(cellAt(r, idx, "account")),
		}
		var pos struct {
			Units struct {
				Currency string          `json:"currency"`
				Number   json.RawMessage `json:"number"`
			} `json:"units"`
		}
		if err := json.Unmarshal(cellAt(r, idx, "position"), &pos); err == nil {
			row.Amount = cellString(pos.Units.Number)
			row.Currency = pos.Units.Currency
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Issue 一条校验问题。Field 指向候选 JSON 的字段路径（如 postings[1].account、
// special_directives[0]、transaction、账本交互）。
type Issue struct {
	Field   string
	Code    string
	Message string
}

// Validate 将候选交易合入完整账本校验（rledger check），并用基线差分把
// 既有账本问题从结果里剔除，只报候选交易引入的新错误；新错误按渲染期
// 行号布局表翻译成 JSON 字段路径。无问题时返回空切片。
func (e *Executor) Validate(txn *beancount.TransactionData) ([]Issue, error) {
	if !e.Available() || txn == nil {
		return nil, nil
	}

	baseline, err := e.checkErrorCounts(e.mainFile)
	if err != nil {
		return nil, fmt.Errorf("基线校验失败: %w", err)
	}

	snippet, layout := e.renderSnippet(txn)
	tmpDir, err := os.MkdirTemp("", "rlvalidate")
	if err != nil {
		return nil, fmt.Errorf("创建校验临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpFile := filepath.Join(tmpDir, "check.tmp.bean")
	content := fmt.Sprintf("include \"%s\"\n\n%s", filepath.ToSlash(e.mainFile), snippet)
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("写校验临时文件失败: %w", err)
	}

	diags, err := e.checkDiagnostics(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("校验执行失败: %w", err)
	}

	issues := make([]Issue, 0, len(diags))
	for _, d := range diags {
		key := diagKey{code: d.Code, message: d.Message}
		// ponytail: 基线按 (code,message) 多重集相减，前提是基线保持干净（当前
		// main.bean 基线 0 错误）；账本若带存量错误，候选引入的同文新错误会被误吞。
		if baseline[key] > 0 {
			// 与既有账本同 (code,message)：存量问题，不怪候选交易
			baseline[key]--
			continue
		}
		field := "transaction"
		if sameFile(d.File, tmpFile) {
			if mapped, ok := layout[d.Line]; ok && mapped != "" {
				field = mapped
			}
		} else {
			field = "账本交互" // 例如与既有 balance 断言冲突，错误定位在账本文件
		}
		issues = append(issues, Issue{Field: field, Code: d.Code, Message: d.Message})
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Field < issues[j].Field })
	return issues, nil
}

// renderSnippet 把交易渲染为 beancount 片段文本，并返回 行号→JSON字段路径 布局表。
// 片段内不含 metadata/extra：校验只关心分录与指令语义。
func (e *Executor) renderSnippet(txn *beancount.TransactionData) (string, map[int]string) {
	var b strings.Builder
	layout := map[int]string{}
	line := snippetFirstLine

	date := txn.DateTime
	if i := strings.IndexByte(date, ' '); i > 0 {
		date = date[:i]
	}
	if len(date) != 10 {
		date = time.Now().Format("2006-01-02")
	}
	flag := txn.Flag
	if flag == "" {
		flag = "*"
	}
	fmt.Fprintf(&b, "%s %s %s %s\n", date, flag, strconv.Quote(txn.Payee), strconv.Quote(txn.Narration))
	layout[line] = "transaction"
	line++

	currency := e.currency
	if currency == "" {
		currency = "CNY"
	}
	for i, p := range txn.Postings {
		amount := strings.TrimSpace(p.Amount)
		if amount == "" {
			amount = "0.00" // 与写入器 AddTransactionFromPostings 的默认值一致
		}
		pcur := p.Currency
		if pcur == "" {
			pcur = currency
		}
		prefix := ""
		if p.Flag != "" {
			prefix = p.Flag + " "
		}
		fmt.Fprintf(&b, "  %s%s %s %s\n", prefix, p.Account, amount, pcur)
		layout[line] = fmt.Sprintf("postings[%d].account", i)
		line++
	}
	for i, d := range txn.SpecialDirectives {
		// 防模型给出含换行的指令：会错位片段行号，导致布局表翻译失真
		d = strings.NewReplacer("\r", " ", "\n", " ").Replace(d)
		fmt.Fprintf(&b, "%s\n", d)
		layout[line] = fmt.Sprintf("special_directives[%d]", i)
		line++
	}
	return b.String(), layout
}

type diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type diagKey struct {
	code    string
	message string
}

type checkOutput struct {
	Diagnostics []diagnostic `json:"diagnostics"`
}

// checkDiagnostics 运行 rledger check 并返回 error 级诊断（warning 全部忽略：
// 用户 rledger 全局配置的 title option 会固定触发 E7009 噪音）。
func (e *Executor) checkDiagnostics(file string) ([]diagnostic, error) {
	out, stderr, err := e.run("check", "-f", "json", file)
	if err != nil {
		return nil, err
	}
	var co checkOutput
	payload, err := unwrapEnvelope(out)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &co); err != nil {
		return nil, fmt.Errorf("解析 rledger check 输出失败: %w%s", err, decodeHint(out, stderr))
	}
	errs := make([]diagnostic, 0, len(co.Diagnostics))
	for _, d := range co.Diagnostics {
		if d.Severity == "error" {
			errs = append(errs, d)
		}
	}
	return errs, nil
}

func (e *Executor) checkErrorCounts(file string) (map[diagKey]int, error) {
	diags, err := e.checkDiagnostics(file)
	if err != nil {
		return nil, err
	}
	counts := make(map[diagKey]int, len(diags))
	for _, d := range diags {
		counts[diagKey{code: d.Code, message: d.Message}]++
	}
	return counts, nil
}

type queryOutput struct {
	Columns []string            `json:"columns"`
	Rows    [][]json.RawMessage `json:"rows"`
}

// run 执行 rledger 子命令。check 在校验失败时退出码非 0 但 stdout JSON
// 仍有效，因此 ExitError 被忽略，只有进程无法启动/超时才返回错误。
// 返回 stdout 与 stderr（后者供调用方在解析失败时携带诊断信息）。
func (e *Executor) run(args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), maxRunTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, stderr.String(), fmt.Errorf("rledger %s 超时: %w", args[0], ctx.Err())
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return nil, stderr.String(), fmt.Errorf("rledger %s 无法执行: %w: %s", args[0], err, firstLine(stderr.String()))
	}
	return stdout.Bytes(), stderr.String(), nil
}

// decodeDiagnosticsJSON 解析 rledger -f json 输出；失败时把 stdout/stderr 头部
// 带进错误信息（空 stdout+非零退出往往是进程崩溃，需要现场证据定位）。
func decodeHint(out []byte, stderr string) string {
	var b strings.Builder
	if len(out) > 0 {
		head := string(out)
		if len(head) > 160 {
			head = head[:160]
		}
		fmt.Fprintf(&b, " stdout=%q", head)
	}
	if s := firstLine(stderr); s != "" {
		fmt.Fprintf(&b, " stderr=%q", s)
	}
	return b.String()
}

var (
	// BQL 字符串字面量的转义规则未验证：单引号/反斜杠直接去掉，其余正则元字符
	// 换成 '.'（任意字符）。关键词只用于 top-N 风格匹配，误伤可接受。
	// ponytail: 若将来需要精确匹配，改用 `-f json` 查询的转义透传并补测试。
	bqlStripRe = regexp.MustCompile(`['"\\\r\n\t]`)
	bqlMetaRe  = regexp.MustCompile(`[\^\$\.\*\+\?\(\)\[\]\{\}\|]`)
	bqlMonthRe = regexp.MustCompile(`^\d{4}-\d{2}$`)
)

func sanitizeKeyword(k string) string {
	k = bqlStripRe.ReplaceAllString(k, "")
	k = bqlMetaRe.ReplaceAllString(k, ".")
	return strings.TrimSpace(k)
}

func cellAt(row []json.RawMessage, idx map[string]int, col string) json.RawMessage {
	i, ok := idx[col]
	if !ok || i >= len(row) {
		return json.RawMessage("null")
	}
	return row[i]
}

func cellString(raw json.RawMessage) string {
	s := string(raw)
	if s == "null" {
		return ""
	}
	var v string
	if err := json.Unmarshal(raw, &v); err == nil {
		return v
	}
	return s // 非字符串标量（如数字）原样返回
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

// unwrapEnvelope 兼容 ag-rledger 的 JSON envelope：{"ok":...,"result":{"data":<内层输出>}}。
// ok:false 显式报错——否则按内层 schema 解析得到空结果，会把失败伪装成
// "查无记录/校验通过"的假阴性。rledger 裸 JSON（无 ok 字段）原样返回。
func unwrapEnvelope(out []byte) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(out, &probe); err != nil {
		return out, nil
	}
	rawOK, hasOK := probe["ok"]
	if !hasOK {
		return out, nil
	}
	var okv bool
	if err := json.Unmarshal(rawOK, &okv); err == nil && !okv {
		msg := firstLine(string(out))
		var env struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(out, &env) == nil && env.Error.Message != "" {
			msg = env.Error.Message + " (" + env.Error.Code + ")"
		}
		return nil, fmt.Errorf("ag-rledger 执行失败: %s", msg)
	}
	var res map[string]json.RawMessage
	if err := json.Unmarshal(probe["result"], &res); err != nil {
		return out, nil
	}
	if data, ok := res["data"]; ok {
		return data, nil
	}
	return out, nil
}

// sameFile 比较诊断里的文件路径与目标文件是否同一文件。
// Windows 下 rledger 输出带 \\?\ 前缀，需归一化。
func sameFile(diagFile, want string) bool {
	cleaned := strings.TrimPrefix(diagFile, `\\?\`)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(filepath.Dir(want), cleaned)
	}
	return strings.EqualFold(filepath.Clean(cleaned), filepath.Clean(want))
}
