# Beancount AutoUpdate - Go Version

使用 Telegram Bot 上传图片并自动识别，实现 Beancount 自动记账，

## 功能特性

- 🤖 **Telegram Bot** - 接收账单截图，提供交互式确认和修改界面
- 🧠 **LLM 解析** - 使用 AI 识别账单图片，提取交易信息
- 📊 **Beancount 管理** - 自动生成和管理 Beancount 账本文件
- 🔄 **Git 同步** - 自动提交并推送到 Git 仓库
- ☁️ **WebDAV 上传** - 将账单图片上传到 WebDAV 服务器
- ⚡ **高性能** - 利用 Go 协程实现并发处理

## 项目结构

```
go/
├── cmd/
│   └── main.go          # 主程序入口
├── internal/
│   ├── config/          # 配置管理
│   ├── logger/          # 日志管理
│   ├── beancount/       # Beancount 管理器
│   ├── git/             # Git 管理器
│   ├── webdav/          # WebDAV 管理器
│   ├── llm/             # LLM 解析器
│   └── telegram/        # Telegram Bot
├── go.mod
├── go.sum
└── Makefile
```

## 快速开始

### 安装依赖

```bash
go mod download
```

### 配置

复制环境变量示例文件：

```bash
cp ../.env.example .env
```

编辑 `config.toml` 配置文件。

### 运行

```bash
make run
```

### 构建

```bash
make build
```

### 部署

```bash
make deploy
```

## 技术栈

- **语言**: Go 1.23
- **Telegram Bot**: go-telegram-bot-api
- **Git 操作**: go-git
- **WebDAV**: gowebdav
- **配置**: TOML
- **日志**: logrus

## 并发优化

利用 Go 的协程机制：

1. **图片上传** - 图片上传到 WebDAV 使用独立 goroutine，不阻塞主流程
2. **LLM 解析** - 支持并发解析多个图片
3. **Git 操作** - 提交和推送操作使用 goroutine 异步执行
4. **消息处理** - Telegram 消息使用 goroutine 池处理，提高并发能力

## 许可证

MIT License