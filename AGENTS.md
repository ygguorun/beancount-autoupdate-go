# Beancount AutoUpdate - Go 版本 - Agent 指南

本文档为 AI 助手提供关于 Beancount AutoUpdate Go 项目的详细上下文信息，用于指导代码理解、修改和开发。

## 项目概述

Beancount AutoUpdate 是一个自动化记账系统，通过 Telegram Bot 接收账单截图，使用大语言模型（LLM）识别交易信息，自动生成 Beancount 账本文件，并支持 Git 同步和 WebDAV 图片上传。

### 核心功能

- **Telegram Bot 交互**：接收用户发送的账单图片，提供交互式确认和修改界面
- **AI 智能识别**：使用 LLM 解析账单图片，提取交易日期、金额、商户、账户等关键信息
- **多轮对话引导**：支持用户通过自然语言引导 LLM 修正识别结果
- **Beancount 账本管理**：自动生成和管理 Beancount 格式的账本文件
- **Git 版本控制**：自动提交并推送账本变更到 Git 仓库
- **WebDAV 图片存储**：将账单图片上传到 WebDAV 服务器
- **高并发处理**：利用 Go 协程实现并发处理，提升性能

### 技术栈

- **语言**：Go 1.24.0
- **Telegram Bot API**：go-telegram-bot-api/v5
- **LLM API**：openai-go/v3（支持 OpenAI 兼容接口）
- **Git 操作**：go-git/go-git/v5
- **配置管理**：TOML（使用 BurntSushi/toml）
- **日志系统**：logrus + lumberjack（日志轮转）
- **环境变量**：godotenv

## 项目结构

```
beancount-autoupdate-go/
├── cmd/
│   └── main.go                 # 主程序入口
├── internal/
│   ├── beancount/              # Beancount 账本管理
│   │   ├── manager.go          # 账本管理器（核心）
│   │   └── types.go            # 数据类型定义
│   ├── config/                 # 配置管理
│   │   └── config.go           # 配置加载和验证
│   ├── embed/                  # 嵌入的模板文件
│   │   ├── embed.go            # 模板嵌入逻辑
│   │   └── templates/          # Beancount 模板
│   │       ├── assets.bean     # 资产账户模板
│   │       ├── expenses.bean   # 支出账户模板
│   │       ├── income.bean     # 收入账户模板
│   │       ├── liabilities.bean # 负债账户模板
│   │       ├── equity.bean     # 权益账户模板
│   │       ├── init.bean       # 初始化模板
│   │       └── receipt_image_recognition.txt # LLM 提示词模板
│   ├── git/                    # Git 操作管理
│   │   └── manager.go          # Git 提交和推送
│   ├── llm/                    # LLM 解析器
│   │   └── parser.go           # 图片识别和信息提取
│   ├── logger/                 # 日志管理
│   │   └── logger.go           # 日志初始化和配置
│   ├── telegram/               # Telegram Bot
│   │   └── bot.go              # Bot 核心逻辑
│   └── webdav/                 # WebDAV 客户端
│       └── manager.go          # WebDAV 文件上传
├── build/                      # 构建输出目录
├── scripts/                    # 部署脚本
├── go.mod                      # Go 模块定义
├── go.sum                      # 依赖校验和
├── Makefile                    # 构建和部署脚本
├── config.toml.example         # 配置文件示例
├── Dockerfile                  # Docker 镜像定义
└── .goreleaser.yml             # GoReleaser 配置
```

## 构建和运行

### 环境要求

- Go 1.24.0 或更高版本
- Git 仓库（用于存储账本）
- Telegram Bot Token
- LLM API（支持 OpenAI 兼容接口）
- WebDAV 服务器（可选）

### 配置文件

1. **复制配置文件**：
   ```bash
   cp config.toml.example config.toml
   ```

2. **设置环境变量**（推荐）：
   - `TELEGRAM_BOT_TOKEN`：Telegram Bot Token
   - `LLM_API_KEY`：LLM API 密钥
   - `WEBDAV_USERNAME`：WebDAV 用户名（可选）
   - `WEBDAV_PASSWORD`：WebDAV 密码（可选）

3. **编辑 config.toml**：
   - 配置 Telegram Bot 参数
   - 配置 LLM API 地址和模型
   - 配置 Beancount 数据目录
   - 配置 Git 仓库地址
   - 配置 WebDAV 参数（如需上传图片）

### 命令行参数

| 参数 | 说明 |
|------|------|
| `-config <path>` | 指定配置文件路径（默认: ./config.toml） |
| `-version` | 显示版本信息 |
| `-d` | 启用 debug 模式（日志级别设为 debug） |

### 常用命令

```bash
# 下载依赖
make deps

# 运行程序
make run

# 运行程序（debug 模式）
go run cmd/main.go -d

# 构建程序
make build

# 运行测试
make test

# 代码格式化
make fmt

# 代码检查
make lint

# 清理构建文件
make clean

# 构建 Docker 镜像
make docker-build

# 运行 Docker 容器
make docker-run
```

## 开发约定

### 代码组织

- **模块化设计**：每个功能模块独立在 `internal/` 目录下
- **接口优先**：尽量使用接口定义行为，便于测试和扩展
- **依赖注入**：通过构造函数注入依赖，避免全局变量
- **错误处理**：使用 `fmt.Errorf` 包装错误，提供上下文信息

### 日志规范

- **统一使用 `internal/logger` 包**：所有模块必须使用 `logger.Infof()` 等函数
- **禁止直接使用 logrus**：不要在模块中使用 `logrus.StandardLogger()`
- **日志级别**：Debug < Info < Warn < Error < Fatal
- **动态调整**：支持通过 `-d` 参数或 `logger.SetLevel()` 动态调整日志级别

### 并发处理

- **Worker Pool**：Telegram Bot 使用 worker pool 处理消息（默认 5 个 worker）
- **信号量控制**：LLM 调用使用信号量限制并发（当前限制为 1）
- **互斥锁**：Beancount 管理器使用 `sync.RWMutex` 保护共享状态
- **异步操作**：WebDAV 上传和 Git 推送使用 goroutine 异步执行

### 配置管理

- 所有配置项定义在 `internal/config/config.go`
- 支持从 TOML 文件和环境变量加载
- 敏感信息（Token、API Key）优先从环境变量读取
- 启动时验证配置，失败时显示详细错误信息

### Beancount 文件组织

```
beancount/
├── main.bean                    # 主文件，导入所有其他文件
├── init.bean                    # 资产初始化
├── account/                     # 账户定义
│   ├── assets.bean              # 资产账户
│   ├── expenses.bean            # 支出账户
│   ├── income.bean              # 收入账户
│   ├── liabilities.bean         # 负债账户
│   └── equity.bean              # 权益账户
└── beans/                       # 交易记录
    └── YYYY/
        └── MM.bean              # 按年月组织
```

## 关键模块说明

### 1. Telegram Bot (`internal/telegram/bot.go`)

**职责**：处理 Telegram 消息和交互

**核心功能**：
- 接收用户发送的图片
- 调用 LLM 解析图片
- 显示交易确认界面（内联键盘）
- 处理用户确认和修改
- 提交交易到 Beancount
- 删除对话消息（可选）

**重要概念**：
- `pendingTx`：存储待确认的交易（按 userID 组织）
- `ConversationHistory`：LLM 对话历史，支持多轮对话引导
- `llmSemaphore`：控制 LLM 并发调用
- `worker`：消息处理的 worker goroutine

**关键方法**：
- `Run()`：启动 Bot
- `worker()`：处理消息的 worker
- `handlePhoto()`：处理图片消息
- `handleCallback()`：处理内联键盘回调

### 2. LLM 解析器 (`internal/llm/parser.go`)

**职责**：使用 LLM 识别账单图片，支持多轮对话

**核心功能**：
- 发送图片到 LLM API（支持 Structured Outputs 和 JSON 模式）
- 解析 LLM 返回的 JSON
- 提取交易信息（日期、金额、商户等）
- 多轮对话历史管理

**对话历史结构**：
```
[0] User: [图片 + 提示词]           # 第一次识别
[1] Assistant: 第一次识别结果
[2] User: 用户引导文字              # 第一次引导重试
[3] Assistant: 第二次识别结果
[4] User: 用户引导文字              # 第二次引导重试
...
```

**关键方法**：
- `ParseImageWithHistory()`：带对话历史的图片解析
- `ParseWithGuidance()`：带引导文字的重新解析
- `buildMessages()`：构建 OpenAI API 消息列表

### 3. Beancount 管理器 (`internal/beancount/manager.go`)

**职责**：管理 Beancount 账本文件

**核心功能**：
- 初始化目录结构
- 创建账户定义文件
- 添加交易记录
- 解析账户文件
- 按日期组织交易文件

**重要概念**：
- 账户类型：Assets、Expenses、Income、Liabilities、Equity
- 交易文件路径：`beans/YYYY/MM.bean`
- 元数据：time、order-id、image-url、discount、original_amount

### 4. Git 管理器 (`internal/git/manager.go`)

**职责**：管理 Git 仓库的提交和推送

**核心功能**：
- 初始化 Git 仓库
- 添加文件到暂存区
- 提交变更
- 推送到远程仓库
- 处理冲突（支持 abort、ours、theirs 策略）

### 5. 日志管理器 (`internal/logger/logger.go`)

**职责**：统一管理日志输出

**核心功能**：
- 日志初始化和配置
- 日志轮转（使用 lumberjack）
- 同时输出到文件和标准输出
- 动态调整日志级别

**提供的函数**：
- `Init()`：初始化日志系统
- `SetLevel()`：设置日志级别
- `Info()`, `Debug()`, `Warn()`, `Error()`, `Fatal()`：日志输出
- `Infof()`, `Debugf()`, `Warnf()`, `Errorf()`, `Fatalf()`：格式化日志

### 6. 配置管理 (`internal/config/config.go`)

**职责**：加载和管理配置

**配置项**：
- `Telegram`：Bot Token、允许的用户 ID、消息删除策略
- `LLM`：API 地址、模型、超时、扩展提示词
- `Beancount`：数据目录、标题、货币
- `Git`：仓库地址、自动提交、推送超时、冲突策略
- `Logging`：日志级别、目录、文件大小、备份数量
- `WebDAV`：启用状态、URL、凭据、路径、文件名模板

## 数据类型

### ConversationMessage

用于 LLM 多轮对话历史：

```go
type ConversationMessage struct {
    Role        string `json:"role"`         // "user" 或 "assistant"
    Content     string `json:"content"`      // 消息内容（文本）
    ImageBase64 string `json:"image_base64"` // 图片的 base64 编码（仅首次用户消息有）
}
```

### PendingTransaction

待确认的交易数据：

```go
type PendingTransaction struct {
    TransactionID           string
    ConversationHistory     []ConversationMessage // LLM 对话历史
    UserInputMessageIDs     []int                 // 用户输入消息ID列表
    BotPromptMessageIDs     []int                 // Bot提示消息ID列表
    // ... 其他字段
}
```

## 数据流

### 典型交易流程

1. **用户发送图片**：Telegram Bot 接收图片
2. **LLM 解析**：调用 LLM API 提取交易信息
3. **显示确认界面**：显示内联键盘供用户确认或引导重试
4. **用户确认/引导重试**：
   - 确认：继续提交流程
   - 引导重试：用户输入自然语言引导 LLM 修正结果
5. **上传图片**（可选）：异步上传到 WebDAV
6. **写入账本**：将交易记录写入 Beancount 文件
7. **Git 提交**（可选）：自动提交变更
8. **Git 推送**（可选）：自动推送到远程仓库
9. **发送成功消息**：通知用户交易已记录
10. **删除对话消息**：清理确认/取消后的所有对话消息

### LLM 多轮对话流程

1. **第一次识别**：发送 `[图片 + 提示词]`，返回识别结果
2. **用户引导重试**：用户输入"金额应该是 50 元"
3. **第二次识别**：发送历史 + 用户引导，返回修正结果
4. **循环**：可多次引导重试，直到用户满意

## 测试

```bash
# 运行所有测试
make test

# 运行测试覆盖率
make test-coverage
```

## 部署

### 本地部署

```bash
make build
./build/beancount-autoupdate
```

### Docker 部署

```bash
make docker-build
make docker-run
```

### 服务部署

使用提供的脚本：
- `scripts/install.sh`：安装服务
- `scripts/service.sh`：配置 systemd 服务
- `scripts/update.sh`：更新服务

## 常见问题

### 配置相关

**Q：如何配置多个用户？**
A：在 `config.toml` 中设置 `telegram.allowed_user_ids`，或使用环境变量。

**Q：如何自定义 LLM 提示词？**
A：在 `config.toml` 中配置 `llm.extend_prompt`，支持 append 和 replace 模式。

**Q：如何启用 debug 日志？**
A：使用 `-d` 参数运行程序，或在 `config.toml` 中设置 `logging.level = "debug"`。

### 开发相关

**Q：如何统一日志输出？**
A：所有模块必须使用 `internal/logger` 包，禁止直接使用 `logrus`。

**Q：如何添加新的账户类型？**
A：在 `internal/beancount/types.go` 中添加类型，并在模板中添加对应文件。

**Q：如何修改 LLM 并发数？**
A：修改 `internal/telegram/bot.go` 中的 `llmSemaphore` 大小。

## 贡献指南

1. 遵循现有的代码风格和约定
2. 使用 `internal/logger` 包统一日志输出
3. 添加必要的注释和文档
4. 提交前运行 `make fmt` 和 `make lint`

## 许可证

MIT License

## 联系方式

- 项目地址：https://github.com/ygguorun/beancount-autoupdate-go.git
- 问题反馈：通过 GitHub Issues

---

**最后更新**：2026-03-19
