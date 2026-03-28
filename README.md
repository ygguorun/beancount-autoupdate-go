# Beancount AutoUpdate - Go Version

使用 Telegram Bot 上传账单图片，自动识别并写入 Beancount 账本。

## 功能特性

- 🤖 **Telegram Bot 交互** - 图片识别、预览确认、引导重试、取消与待处理查询
- 🧠 **LLM 识别** - 从账单图片提取日期、对象、分录等交易信息
- 📊 **Beancount 管理** - 自动生成并维护账本文件结构
- 🔄 **Git 同步** - 支持自动提交和自动推送
- ☁️ **WebDAV 上传** - 识别图片可上传至 WebDAV 并在确认后重命名
- 🔒 **隐私友好回执** - 提交成功后返回脱敏提示（不回传完整分录明细）

## 项目结构

```text
go/
├── cmd/
│   └── main.go
├── internal/
│   ├── beancount/
│   ├── config/
│   ├── git/
│   ├── llm/
│   ├── logger/
│   ├── telegram/
│   └── webdav/
├── .env.example
├── config.toml.example
├── Makefile
└── AGENTS.md
```

## 快速开始

### 1) 安装依赖

```bash
make deps
```

### 2) 准备配置

```bash
cp .env.example .env
cp config.toml.example config.toml
```

然后填写以下关键配置：

- `TELEGRAM_BOT_TOKEN`（建议放 `.env`）
- `LLM_API_KEY`（建议放 `.env`）
- `git.repo_url`（`config.toml`）
- `beancount.data_dir`（`config.toml`）

### 3) 运行

```bash
make run
```

### 4) 测试与构建

```bash
make test
make build
```

## Telegram 交互说明

### 常用命令

- `/start` 显示欢迎信息
- `/help` 显示使用说明
- `/accounts` 查看账户与分类
- `/pending` 查看待处理交易
- `/cancel` 取消当前输入流程

### 典型流程

1. 发送账单图片或图片文件
2. Bot 返回交易预览
3. 点击：
   - `💬 引导重试`：告诉 Bot 如何修正识别结果
   - `🔄 重新识别`：不带引导重新解析图片
   - `✅ 确认提交`：写入账本并触发 Git 同步
   - `❌ 取消`：取消交易并清理上下文

### 提交成功回执（脱敏）

确认提交后，Bot 返回脱敏提示，仅包含：

- 交易 ID（后 6 位）
- 日期
- 脱敏后的交易对象

不会返回完整分录明细与金额，详细内容请在账本文件或 Git 记录中查看。

## 配置说明（节选）

`[telegram]` 关键项：

- `allowed_user_ids`：允许使用 Bot 的用户 ID 列表
- `delete_user_message`：确认后是否删除用户发送的原始图片消息

## 开发命令

- `make fmt`：格式化代码
- `make test`：运行所有测试
- `make lint`：运行 lint（本地安装 `golangci-lint` 时）
- `make run`：本地运行

## 技术栈

- **语言**：Go 1.24
- **Telegram Bot**：go-telegram-bot-api
- **Git 操作**：go-git
- **WebDAV**：自定义 `net/http` 实现
- **配置**：TOML + 环境变量覆盖
- **日志**：logrus

## 许可证

MIT License
