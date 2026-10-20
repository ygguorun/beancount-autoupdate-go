# Changelog

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
