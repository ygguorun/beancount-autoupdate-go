// Package test 收录需手动执行的端到端测试（go test ./... 下默认跳过）。
//
// 单图端到端：默认 = 现网一次识别；-tools = 工具探索识别（单会话：模型自主
// query_ledger 检索账本历史 → 记账 → 程序校验闸）。使用真实 LLM 配置与本地账本，全程只读（不碰 Telegram/WebDAV/Git，不写账）。
//
// 运行方式：
//
//	go test -v ./test/ -run E2E -image 截图.png              # 单阶段现状链路
//	go test -v ./test/ -run E2E -image 截图.png -tools -guidance "把科目改成 Expenses:XX" # 识别后模拟引导续写
//	go test -v ./test/ -run Regset -regset ../testdata/regset -tools      # 回归集打分
//	go test -v ./test/ -run Regset -regset ../testdata/regset -repeat 3      # 一致性方差
//	注意：go test 的工作目录是 test/，regset 路径需相对该目录（../testdata/regset）。
package test

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/config"
	"beancount-autoupdate/internal/embed"
	"beancount-autoupdate/internal/ledgercli"
	"beancount-autoupdate/internal/llm"
	"beancount-autoupdate/internal/logger"
)

var (
	e2eImage  = flag.String("image", "", "端到端识别截图路径（为空则跳过单图测试）")
	e2eConfig = flag.String("config", "../config.toml", "配置文件路径")
	e2eNoVal  = flag.Bool("no-validate", false, "跳过 rledger 账本校验")
	e2eTools  = flag.Bool("tools", false, "使用工具探索识别（单会话：模型自主 query_ledger 检索→记账→校验）")
	e2eRegset = flag.String("regset", "", "回归集目录（含 index.json），非空则批量打分（配合 -run Regset）")
	e2eCats   = flag.String("categories", "", "回归集仅跑指定类别（逗号分隔，默认全部）")
	e2eRepeat = flag.Int("repeat", 1, "回归集每图重复次数（>1 时统计同图一致性）")
	e2eGuide  = flag.String("guidance", "", "单图识别完成后追加一次引导续写（模拟引导重试，配合 -image 使用）")
)

// e2eEnv 汇总一次运行所需的真实依赖（配置/解析器/账本/科目/校验器）。
type e2eEnv struct {
	cfg      *config.Config
	parser   *llm.Parser
	ledger   *ledgercli.Executor
	accounts []string
}

func TestRecognitionE2E(t *testing.T) {
	if *e2eImage == "" {
		t.Skip("未提供 -image，跳过端到端识别（需真实 LLM 调用，手动执行）")
	}
	env := setupE2E(t)

	var txn *beancount.TransactionData
	var history []beancount.ConversationMessage
	var err error
	if *e2eTools {
		t.Logf("[工具] 单会话：记账提示词+图片 → 模型自主 query_ledger 探索（≤5 轮）→ 校验修正（%d 个可用科目）...", len(env.accounts))
		txn, history, err = ledgercli.Recognize(env.parser, env.ledger, *e2eImage, env.accounts, nil)
		if err != nil || txn == nil {
			t.Fatalf("工具探索识别失败: %v", err)
		}
		logTxn(t, "[工具] 最终结果", txn)
		reportValidate(t, env, txn)
	} else {
		txn, history = runSingleStage(t, env, *e2eImage)
		if txn == nil {
			t.FailNow()
		}
		reportValidate(t, env, txn)
	}

	if g := strings.TrimSpace(*e2eGuide); g != "" {
		t.Logf("[引导] 续写指令: %s", g)
		var txn2 *beancount.TransactionData
		var err2 error
		if *e2eTools {
			txn2, history, err2 = ledgercli.RecognizeContinue(env.parser, env.ledger, g, history, *e2eImage, env.accounts)
		} else {
			txn2, history, err2 = env.parser.ParseImageWithHistory(*e2eImage, env.accounts, history, g)
		}
		if err2 != nil || txn2 == nil {
			t.Fatalf("引导续写失败: %v", err2)
		}
		_ = history
		txn = txn2
		logTxn(t, "[引导] 最终结果", txn)
		reportValidate(t, env, txn)
	}

	final, err := llm.SerializeTransaction(txn)
	if err != nil {
		t.Fatalf("序列化最终结果失败: %v", err)
	}
	t.Logf("最终结果:\n%s", final)
}

func TestRecognitionRegset(t *testing.T) {
	if *e2eRegset == "" {
		t.Skip("未提供 -regset，跳过回归集打分")
	}
	env := setupE2E(t)

	entries := loadRegset(t, filepath.Join(*e2eRegset, "index.json"))
	if want := *e2eCats; want != "" {
		set := map[string]bool{}
		for _, c := range strings.Split(want, ",") {
			set[strings.TrimSpace(c)] = true
		}
		filtered := entries[:0:0]
		for _, e := range entries {
			if set[e.Category] {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	if len(entries) == 0 {
		t.Fatal("回归集为空")
	}

	repeat := max(*e2eRepeat, 1)
	var totals [6]int
	perRun := [6]int{}
	scored := 0
	narDists := make([]int, 0, len(entries))
	var valIssues int
	start := time.Now()

	for _, e := range entries {
		image := filepath.Join(*e2eRegset, e.File)
		if _, err := os.Stat(image); err != nil {
			t.Errorf("[%s] 图片缺失: %v", e.Category, err)
			continue
		}
		exclude := func(date, payee string) bool {
			return date == e.Ground.Date && payee == e.Ground.Payee
		}

		runs := make([]*beancount.TransactionData, 0, repeat)
		for i := 0; i < repeat; i++ {
			var txn *beancount.TransactionData
			var err error
			if *e2eTools {
				txn, _, err = ledgercli.Recognize(env.parser, env.ledger, image, env.accounts, exclude)
			} else {
				txn, _ = runSingleStage(t, env, image)
			}
			if err != nil || txn == nil {
				t.Errorf("[%s] 第%d次识别失败: %v", e.Category, i+1, err)
				continue
			}
			runs = append(runs, txn)

			s := scoreAgainst(e.Ground, txn)
			scored++
			for j := range totals {
				totals[j] += s[j]
			}
			narDists = append(narDists, s[5])
			if issues, err := env.ledger.Validate(txn); err == nil {
				valIssues += len(issues)
			}
			t.Logf("[%s] payee=%v 科目=%v 金额=%v 日期=%v 标题=%v(距离%d)",
				e.Category, yesno(s[0]), yesno(s[1]), yesno(s[2]), yesno(s[3]), yesno(s[4]), s[5])
		}

		if repeat > 1 && len(runs) > 1 {
			for j := 0; j < 5; j++ {
				if consistentRuns(runs, j) {
					perRun[j]++
				}
			}
			t.Logf("[%s] 同图×%d 一致性: payee=%v 科目=%v 金额=%v 日期=%v 标题=%v",
				e.Category, len(runs), yesno(perRun[0]), yesno(perRun[1]), yesno(perRun[2]), yesno(perRun[3]), yesno(perRun[4]))
			perRun = [6]int{}
		}
	}

	n := scored
	avg := 0.0
	if len(narDists) > 0 {
		sum := 0
		for _, d := range narDists {
			sum += d
		}
		avg = float64(sum) / float64(len(narDists))
	}
	t.Logf("=== 回归汇总（%d 图 ×%d，%s，耗时 %s）===",
		len(entries), repeat, modeName(), time.Since(start).Round(time.Second))
	t.Logf("payee一致 %d/%d | 科目一致 %d/%d | 金额一致 %d/%d | 日期一致 %d/%d | 标题完全一致 %d/%d | 标题平均距离 %.1f | 校验问题 %d",
		totals[0], n, totals[1], n, totals[2], n, totals[3], n, totals[4], n, avg, valIssues)
}

// runSingleStage 现网单阶段链路：一次识别，无任何增强（对照组）。校验由调用方单独跑。
// 返回识别结果与其会话历史（供 -guidance 续写模拟引导重试）。
func runSingleStage(t *testing.T, env *e2eEnv, image string) (*beancount.TransactionData, []beancount.ConversationMessage) {
	t.Helper()
	txn, history, err := env.parser.ParseImageWithHistory(image, env.accounts, nil, "")
	if err != nil || txn == nil {
		// 批量回归中不能 Fatalf（会中止全 suite）：报错并交调用方跳过该图
		t.Errorf("首次识别失败: %v", err)
		return nil, nil
	}
	logTxn(t, "[单阶段] 识别结果", txn)
	return txn, history
}

func reportValidate(t *testing.T, env *e2eEnv, txn *beancount.TransactionData) {
	t.Helper()
	if *e2eNoVal || !env.ledger.Available() {
		return
	}
	issues, err := env.ledger.Validate(txn)
	switch {
	case err != nil:
		t.Errorf("[校验] 执行失败: %v", err)
	case len(issues) == 0:
		t.Log("[校验] 账本校验通过")
	default:
		t.Errorf("[校验] 发现问题:\n%s", ledgercli.FormatIssues(issues))
	}
}

func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()
	cfg, err := config.LoadConfig(*e2eConfig)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("配置验证失败: %v", errs)
	}
	if err := embed.InitTemplates(); err != nil {
		t.Fatalf("初始化模板失败: %v", err)
	}
	// 端到端与回归运行以离线分析为首要目的：恒定使用 debug 级日志，
	// 完整提示词、原始响应、token 用量、种子与校验细节全部落盘。
	level := "debug"
	t.Logf("日志文件: %s", filepath.Join(cfg.GetAbsPath(cfg.Logging.LogDir), cfg.Logging.LogFile))
	if err := logger.Init(
		cfg.GetAbsPath(cfg.Logging.LogDir),
		cfg.Logging.LogFile,
		level,
		cfg.Logging.MaxBytes,
		cfg.Logging.BackupCount,
	); err != nil {
		t.Fatalf("初始化日志失败: %v", err)
	}

	parser := llm.NewParser(
		cfg.LLM.BaseURL,
		cfg.LLM.Model,
		cfg.LLM.APIKey,
		cfg.LLM.Timeout,
		cfg.LLM.ExtendPrompt,
		cfg.LLM.MaxImageSize,
	)

	mgr, err := beancount.NewManager(
		cfg.GetAbsPath(cfg.Beancount.DataDir),
		cfg.Beancount.Title,
		cfg.Beancount.OperatingCurrency,
	)
	if err != nil {
		t.Fatalf("初始化 Beancount 管理器失败: %v", err)
	}

	env := &e2eEnv{
		cfg:      cfg,
		parser:   parser,
		ledger:   ledgercli.New(cfg.LedgerCLI.RledgerPath, mgr.MainFilePath(), cfg.Beancount.OperatingCurrency),
		accounts: allAccounts(mgr.GetAllCategories()),
	}
	if !cfg.LedgerCLI.Enabled {
		t.Log("[ledger_cli] 未启用（config [ledger_cli].enabled=false），种子/校验按不可用降级")
		env.ledger = ledgercli.New("rledger-missing-sentinel-does-not-exist", mgr.MainFilePath(), cfg.Beancount.OperatingCurrency)
	}
	return env
}

// regset 数据结构：与 testdata/regset/index.json 对应（ground truth 为账本原交易）。
type regEntry struct {
	Category string `json:"category"`
	File     string `json:"file"`
	Ground   gtTxn  `json:"ground_truth"`
}

type gtTxn struct {
	Date      string      `json:"date"`
	Payee     string      `json:"payee"`
	Narration string      `json:"narration"`
	Postings  []gtPosting `json:"postings"`
}

type gtPosting struct {
	Account  string `json:"account"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func loadRegset(t *testing.T, path string) []regEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取回归集失败: %v", err)
	}
	var entries []regEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("解析回归集失败: %v", err)
	}
	return entries
}

// scoreAgainst 返回 [payee, 科目集合, 金额, 日期, 标题完全一致, 标题编辑距离]。
func scoreAgainst(gt gtTxn, txn *beancount.TransactionData) [6]int {
	score := [6]int{}
	if txn.Payee == gt.Payee {
		score[0] = 1
	}

	gtAccs := map[string]float64{}
	for _, p := range gt.Postings {
		gtAccs[p.Account] += parseAmount(p.Amount)
	}
	txnAccs := map[string]float64{}
	for _, p := range txn.Postings {
		txnAccs[p.Account] += parseAmount(p.Amount)
	}
	if equalAccountSets(gtAccs, txnAccs) {
		score[1] = 1
		if equalAmounts(gtAccs, txnAccs) {
			score[2] = 1
		}
	}

	if dt := txn.DateTime; len(dt) >= 10 && dt[:10] == gt.Date {
		score[3] = 1
	}
	if txn.Narration == gt.Narration {
		score[4] = 1
	}
	score[5] = levenshtein([]rune(txn.Narration), []rune(gt.Narration))
	return score
}

func parseAmount(s string) float64 {
	v, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), 64)
	return v
}

func equalAccountSets(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func equalAmounts(a, b map[string]float64) bool {
	for k, v := range a {
		if math.Abs(v-b[k]) > 0.005 {
			return false
		}
	}
	return true
}

// consistentRuns 判断多次运行在某个维度是否一致（dim: 0 payee 1 科目 2 金额 3 日期 4 标题）。
func consistentRuns(runs []*beancount.TransactionData, dim int) bool {
	key := func(t *beancount.TransactionData) string {
		switch dim {
		case 0:
			return t.Payee
		case 1:
			accs := make([]string, 0, len(t.Postings))
			for _, p := range t.Postings {
				accs = append(accs, p.Account)
			}
			sort.Strings(accs)
			return strings.Join(accs, "|")
		case 2:
			amts := make([]string, 0, len(t.Postings))
			for _, p := range t.Postings {
				amts = append(amts, fmt.Sprintf("%s=%g", p.Account, parseAmount(p.Amount)))
			}
			sort.Strings(amts)
			return strings.Join(amts, "|")
		case 3:
			if len(t.DateTime) >= 10 {
				return t.DateTime[:10]
			}
			return t.DateTime
		default:
			return t.Narration
		}
	}
	first := key(runs[0])
	for _, r := range runs[1:] {
		if key(r) != first {
			return false
		}
	}
	return true
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func yesno(v int) string {
	if v == 1 {
		return "✓"
	}
	return "✗"
}

func modeName() string {
	if *e2eTools {
		return "工具探索"
	}
	return "单阶段"
}

func logTxn(t *testing.T, label string, txn *beancount.TransactionData) {
	t.Helper()
	s, err := llm.SerializeTransaction(txn)
	if err != nil {
		t.Errorf("%s 序列化失败: %v", label, err)
		return
	}
	t.Logf("%s:\n%s", label, s)
}

// allAccounts 与 telegram 包内 allAvailableAccounts 等价（其未导出，此处复刻；
// 拼接顺序固定，无 map 遍历随机性）。
func allAccounts(accounts map[beancount.AccountType][]string) []string {
	return append(
		append(
			append(accounts[beancount.AccountTypeAssets], accounts[beancount.AccountTypeLiabilities]...),
			accounts[beancount.AccountTypeExpenses]...,
		),
		accounts[beancount.AccountTypeIncome]...,
	)
}
