package ledgercli

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MaxSeedBytes 种子文本总量上限（~1.5KB），控制进上下文的 token。
const MaxSeedBytes = 1536

// NoSeeds 检索无结果时的显式信号：让续写模型明确知道“没有历史可参考”，
// 而不是把空块解读成未知含义。
const NoSeeds = "matched: none"

// TxnSeed 按交易归组的历史种子。
type TxnSeed struct {
	Date      string
	Payee     string
	Narration string
	Postings  []Row
}

// GroupRows 将（已按日期倒序的）分录行归组为交易：相邻同 (date,payee,narration)
// 行归入同组，且组内金额累计归零即视为一笔交易结束——借此把同日同商户同
// narration 的多笔独立交易拆开（账本里真实存在，如两次一卡通刷卡）。
// ponytail: 一笔交易内若存在前缀恰好归零的分录组合会被误拆成两组，
// 本账本风格（单边收支）极罕见，且种子仅供风格参考，无害。
func GroupRows(rows []Row, maxTxns int) []TxnSeed {
	seeds := make([]TxnSeed, 0, maxTxns)
	var sum float64
	closed := true // 强制首行开新组
	for _, r := range rows {
		if closed ||
			seeds[len(seeds)-1].Date != r.Date ||
			seeds[len(seeds)-1].Payee != r.Payee ||
			seeds[len(seeds)-1].Narration != r.Narration {
			if len(seeds) >= maxTxns {
				break
			}
			seeds = append(seeds, TxnSeed{Date: r.Date, Payee: r.Payee, Narration: r.Narration})
			sum = 0
			closed = false
		}
		last := &seeds[len(seeds)-1]
		last.Postings = append(last.Postings, r)
		if v, err := strconv.ParseFloat(r.Amount, 64); err == nil {
			sum += v
			if math.Abs(sum) < 0.005 && len(last.Postings) >= 2 {
				closed = true
			}
		}
	}
	return seeds
}

// RenderSeeds 渲染紧凑种子文本。金额一律替换为 ±X.XX 占位符——
// 只暴露记账风格（科目/写法/符号方向），不暴露历史金额数值。
func RenderSeeds(seeds []TxnSeed) string {
	if len(seeds) == 0 {
		return NoSeeds
	}

	var b strings.Builder
	for _, s := range seeds {
		// 非交易指令（pad 等）与 balance 填充记录：还原指令形态 "日期 pad A B"。
		// 指令不含金额，顺带挡住 "(Padding inserted for Balance of X)" 摘要
		// 把历史余额泄给 LLM（脱敏承诺 rule 6）。
		if p, n := strings.TrimSpace(s.Payee), strings.TrimSpace(s.Narration); p == "" &&
			(n == "" || strings.HasPrefix(n, "(Padding inserted")) {
			directive := "entry"
			if len(s.Postings) > 0 {
				switch s.Postings[0].Flag {
				case "P":
					directive = "pad"
				case "B":
					directive = "balance"
				}
			}
			accounts := make([]string, 0, len(s.Postings))
			for _, p := range s.Postings {
				accounts = append(accounts, p.Account)
			}
			fmt.Fprintf(&b, "%s %s %s\n", s.Date, directive, strings.Join(accounts, " "))
			continue
		}
		fmt.Fprintf(&b, "%s %s %s\n", s.Date, s.Payee, s.Narration)
		for _, p := range s.Postings {
			fmt.Fprintf(&b, "  %s %s %s\n", p.Account, maskAmount(p.Amount), p.Currency)
		}
	}
	return truncateBytes(b.String(), MaxSeedBytes)
}

// maskAmount 保留正负号（符号方向是种子要教的风格），数字不外泄。
func maskAmount(amount string) string {
	if strings.HasPrefix(strings.TrimSpace(amount), "-") {
		return "-X.XX"
	}
	return "X.XX"
}

// truncateBytes 按行边界截断到 max 字节以内。
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndexByte(s[:max], '\n')
	if cut <= 0 {
		return strings.ToValidUTF8(s[:max], "") // 防切坏多字节字符
	}
	return s[:cut+1]
}
