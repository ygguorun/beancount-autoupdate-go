# Changelog

## 0.7.3 (2026-10-30)

### 变更

- LLM 识别请求的 `response_format` 统一改为 `json_object`（普通识别、工具探索、Responses 三条路径），移除 Structured Outputs（`json_schema` strict）与降级重试逻辑：`json_schema` 在 DeepSeek 等兼容网关上不可用（400 "This response_format type is unavailable now"），且工具探索路径原本没有降级。输出结构约束由提示词模板中的 schema 文本承担（此前降级路径已长期实测可行）。
- Git 同步：pull 增加 60s 冷却去重；push 改为非快进拒绝后快进重试；同步失败提示附带原因。

## 0.7.2 (2026-10-23)

修复工具探索识别中"账本对账史不可检索"导致的科目误选（如基金快照 pad 进 `Equity:*` 而非账本惯用的 `Income:FinanceAndInvestment:股票基金`），并消除探索回合的无谓损耗。识别提示词模板零改动。

### 修复

- 关键词科目维度改用 BQL entry 级 `has_account()`：行级 `account ~` 会把同一笔交易的分录切开，pad 对账史只有双边同框才教得出对手科目。
- balance 填充记录（`(Padding inserted for Balance of X)`）改按 `日期 pad 科目A 科目B` 指令形态渲染：与 `query_ledger` 工具描述一致，并封堵历史余额经摘要泄入 LLM 上下文（脱敏承诺）。
- 月格式 `until` off-by-one：旧实现边界 `AddDate(0,1,1)` 会把下月 1 号的记录放进入窗。

### 变更

- `query_ledger` 多关键词改为逐词检索再按覆盖词数分层提顶（老的全命中记录不再被高频词的行窗挤出候选池）。
- `since`/`until` 同时接受 `YYYY-MM` 与 `YYYY-MM-DD`（两端含；`2026-02-30` 这类假日拒绝），模型按天开探索窗不再消耗纠错回合。
- 查询行窗从 `limit*3` 启发式改为固定 500 行保险丝（`MaxRows` 旋钮移除）：进 LLM 的量本就由笔数钳制与种子/会话字节预算卡死，`has_account` 使每交易行数翻倍后浅窗反而凑不齐完整交易。

## 0.7.1 (2026-10-20)

- 关键词匹配扩展到 account 列，机构名可命中对账史（该目标由 0.7.2 的 `has_account` 改造才真正达成：行级匹配把配对科目切掉了）。

## 0.7.0 (2026-10-20)

新增**账本工具探索识别**能力，默认关闭，旧链路行为完全不变。

### 新增

- `internal/ledgercli`：rledger CLI 执行器——`query_ledger` 检索（BQL 形态/LIMIT/±X.XX 金额脱敏/输出预算全部 Go 侧焊死）、`rledger check` 全账本校验闸（基线差分 + 行号→字段路径翻译）、工具循环预算与同参去重。
- `internal/llm`：单会话工具循环 `RecognizeWithTools`（首轮 ≤5 探索回合）与 `ContinueWithTools`（引导续写 ≤3 回合，产出同样过校验闸）；独立 booking 模板 `transaction_booking.txt`（不受 `extend_prompt` 影响）。
- 配置：`[llm].tools_enabled`（默认 `false`）、`[ledger_cli].enabled / rledger_path`（默认关闭）。
- 识别输出消毒：非法 flag（如 `"auto"`）归一、非法/与顶层重名的 `extra` 键剔除——防止写坏账本或整笔确认失败。
- 引导重试续写的前缀逐字节稳定回放（网关前缀缓存友好），LLM 用量（耗时/prompt/completion/cached tokens）全量日志。
- e2e 与回归集：`make test-e2e IMAGE=...`、`make test-regset ARGS="-tools"`（回归截图本地存放，不入 Git）。

### 重要说明

- **0.7.0 中 `tools_enabled` 默认 `false`**：线上仍走旧单发识别链路；开启需服务端安装 `rledger`。
- 开启 tools 后，引导重试自动升级为可继续探索的续写轮（预算 3 回合），并补上旧版没有的校验闸。
- 已知限制：个别 OpenAI 兼容网关（如 token-plan 系 qwen）的 Structured Outputs 为提示词注入式实现，续写轮偶发把 schema 回显为内容（内置一次带具体错误的重试）；建议开启前用 `make test-e2e IMAGE=... -tools` 对目标模型实测。

### 计划（0.8.0）

- **移除旧单发识别链路与 `tools_enabled` 配置项**：工具探索识别将成为唯一路径（届时升级前请确认服务端已安装 rledger，否则识别不可用）。
- 现 `530` 行内置提示词将退役，识别规则由 booking 模板 + 账本历史（模型自主检索）承担。

## ≤0.6.x

此前无 CHANGELOG；功能集见 README。
