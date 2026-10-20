package ledgercli

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// toolOutputBudget B 轮全程工具输出总预算（字节）：多轮探索下防止回填内容撑爆前缀。
const toolOutputBudget = 6144

var orderIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)

type toolArgs struct {
	Keywords      []string `json:"keywords"`
	OrderID       string   `json:"order_id"`
	Amount        string   `json:"amount"`
	AccountPrefix string   `json:"account_prefix"`
	Since         string   `json:"since"`
	Until         string   `json:"until"`
	Limit         int      `json:"limit"`
}

// ToolLoop 一次识别会话内的有状态工具执行器：参数校验、同参去重、
// 输出总预算、金额后过滤与回归集同源剔除。实现 llm.ToolExecutor 契约——
// 一切失败都以错误文本回给模型自救，不 panic、不返回 error。
type ToolLoop struct {
	e         *Executor
	exclude   func(date, payee string) bool
	cache     map[string]string
	outputted int
}

// NewToolLoop 构建循环级执行器。seedExclude 非 nil 时剔除与当前截图同源的记录
// （回归测试防"抄自己答案"；生产传 nil）。
func (e *Executor) NewToolLoop(seedExclude func(date, payee string) bool) *ToolLoop {
	return &ToolLoop{e: e, exclude: seedExclude, cache: make(map[string]string)}
}

// Exec 执行一次工具调用（llm.ToolExecutor 实现）。
func (tl *ToolLoop) Exec(name, argsJSON string) string {
	if name != "query_ledger" {
		return errJSON("未知工具: " + name)
	}
	if !tl.e.Available() {
		return errJSON("账本查询后端不可用")
	}
	if cached, ok := tl.cache[argsJSON]; ok {
		return cached
	}
	var a toolArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return errJSON("参数解析失败: " + err.Error())
	}
	res := tl.run(a)
	tl.cache[argsJSON] = res
	return res
}

func (tl *ToolLoop) run(a toolArgs) string {
	orderID := strings.TrimSpace(a.OrderID)
	if orderID != "" && !orderIDPattern.MatchString(orderID) {
		return errJSON("order_id 需为 6~64 位字母数字或 _ -")
	}
	if orderID == "" && len(a.Keywords) == 0 {
		// 空条件不是"查无记录"，必须报参数错误——否则模型把调用失误读成历史缺失
		return errJSON("keywords 或 order_id 至少要提供一个")
	}
	for name, v := range map[string]string{"since": strings.TrimSpace(a.Since), "until": strings.TrimSpace(a.Until)} {
		if v != "" && !bqlMonthRe.MatchString(v) {
			return errJSON(name + " 需为 YYYY-MM 格式")
		}
	}

	hasAmount := false
	var amount float64
	if s := strings.TrimSpace(a.Amount); s != "" {
		v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
		if err != nil || v < 0 {
			return errJSON("amount 需为十进制金额")
		}
		amount, hasAmount = v, true
	}

	limit := a.Limit
	switch {
	case limit <= 0:
		limit = 3
	case limit > 5:
		limit = 5
	}
	rowsMax := 0
	if hasAmount && orderID == "" {
		// 金额是 Go 端后过滤：窄窗（limit*3 行、近端优先）会把老原单误报为查无
		rowsMax = 300
	}

	rows, err := tl.e.Query(QueryFilter{
		Keywords:      a.Keywords,
		OrderID:       orderID,
		AccountPrefix: a.AccountPrefix,
		Since:         strings.TrimSpace(a.Since),
		Until:         strings.TrimSpace(a.Until),
		Limit:         limit,
		MaxRows:       rowsMax,
	})
	if err != nil {
		return errJSON("查询失败: " + err.Error())
	}

	g0 := GroupRows(rows, 50)
	g1 := g0
	if hasAmount {
		g1 = g0[:0:0]
		for _, g := range g0 {
			if groupHasAmount(g, amount) {
				g1 = append(g1, g)
			}
		}
	}
	g2 := g1
	if tl.exclude != nil {
		g2 = g1[:0:0]
		for _, g := range g1 {
			if !tl.exclude(g.Date, g.Payee) {
				g2 = append(g2, g)
			}
		}
	}

	if len(g2) == 0 {
		if orderID != "" || len(g0) == 0 {
			return NoSeeds
		}
		// AND 语义保持精确，但空结果必须告诉模型是哪一段过滤清零，指导放宽
		funnel := fmt.Sprintf("%s (关键词命中 %d 组", NoSeeds, len(g0))
		if hasAmount {
			funnel += fmt.Sprintf(" → 金额过滤后 %d 组", len(g1))
		}
		if tl.exclude != nil {
			funnel += fmt.Sprintf(" → 同源剔除后 %d 组", len(g2))
		}
		return funnel + "）——放宽清零的那个条件（换词/去金额/扩时间窗）再查"
	}
	if len(g2) > limit {
		g2 = g2[:limit]
	}

	out := RenderSeeds(g2)
	if tl.outputted+len(out) > toolOutputBudget {
		return errJSON("工具输出总预算已用尽，请基于已有结果直接输出最终交易 JSON")
	}
	tl.outputted += len(out)
	return out
}

// OutputUsed 已消耗的工具输出字节数（日志观测用）。
func (tl *ToolLoop) OutputUsed() int { return tl.outputted }

// groupHasAmount 交易内任一分录的绝对值与目标金额相差 <0.005。
func groupHasAmount(g TxnSeed, amount float64) bool {
	for _, p := range g.Postings {
		v, err := strconv.ParseFloat(strings.ReplaceAll(p.Amount, ",", ""), 64)
		if err == nil && math.Abs(math.Abs(v)-amount) < 0.005 {
			return true
		}
	}
	return false
}

func errJSON(msg string) string {
	b, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, msg)
	}
	return string(b)
}
