# Beancount AutoUpdate - Go 版本总结

## 项目概述

已成功将 Python 版本的 Beancount AutoUpdate 项目用 Go 重写，充分利用 Go 的协程机制实现高性能并发处理。

## 项目结构

```
go/
├── cmd/
│   └── main.go              # 主程序入口
├── internal/
│   ├── config/              # 配置管理模块
│   │   └── config.go
│   ├── logger/              # 日志管理模块
│   │   └── logger.go
│   ├── beancount/           # Beancount 管理器
│   │   ├── types.go
│   │   └── manager.go
│   ├── git/                 # Git 管理器
│   │   └── manager.go
│   ├── webdav/              # WebDAV 管理器
│   │   └── manager.go
│   ├── llm/                 # LLM 解析器
│   │   ├── parser.go
│   │   └── template/
│   │       └── receipt_image_recognition.txt
│   └── telegram/            # Telegram Bot
│       └── bot.go
├── build/                   # 构建输出目录
├── dist/                    # 多平台构建输出目录
├── go.mod                   # Go 模块文件
├── go.sum                   # Go 依赖校验文件
├── Makefile                 # 构建和部署脚本
├── Dockerfile               # Docker 构建文件
├── deploy.sh                # 服务器部署脚本
├── config.toml.example      # 配置文件示例
├── .env.example             # 环境变量示例
├── .gitignore               # Git 忽略文件
└── README.md                # 项目说明文档
```

## 核心功能模块

### 1. 配置管理 (config)
- 支持从 TOML 文件和环境变量加载配置
- 配置验证功能
- 支持用户权限控制

### 2. 日志管理 (logger)
- 基于 logrus 的日志系统
- 支持日志轮转
- 同时输出到文件和控制台

### 3. Beancount 管理器 (beancount)
- 自动初始化目录结构
- 账户文件管理
- 交易记录生成
- 按年月组织交易文件

### 4. Git 管理器 (git)
- 自动提交和推送
- 支持冲突处理策略
- 远程仓库管理

### 5. WebDAV 管理器 (webdav)
- 图片上传功能
- 文件移动和重命名
- 目录自动创建
- 自定义文件名模板

### 6. LLM 解析器 (llm)
- 图片识别和解析
- 支持自定义提示词模板
- 交易数据结构化输出

### 7. Telegram Bot (telegram)
- 接收账单截图
- 交互式确认和修改界面
- 命令处理
- 使用 worker pool 实现并发处理

## 并发优化亮点

### 1. Worker Pool 模式
```go
// Telegram Bot 使用 5 个 worker 并发处理消息
workerCount := 5
workerChan := make(chan tgbotapi.Update, 100)

for i := 0; i < workerCount; i++ {
    go b.worker(workerChan)
}
```

### 2. 图片上传和解析并发
```go
// 使用 goroutine 并发处理图片上传和解析
var wg sync.WaitGroup
var uploadResult string
var parseResult *beancount.TransactionData

// 并发上传
wg.Add(1)
go func() {
    defer wg.Done()
    uploadResult, _ = b.webdavMgr.UploadFile(...)
}()

// 并发解析
wg.Add(1)
go func() {
    defer wg.Done()
    parseResult, _ = b.llmParser.ParseImage(...)
}()

wg.Wait()
```

### 3. Git 操作异步执行
```go
// Git 提交和推送使用 goroutine 异步执行
go func() {
    if b.config.Git.AutoCommit {
        b.gitMgr.CommitChanges(commitMessage)
        if b.config.Git.AutoPush {
            b.gitMgr.PushChanges()
        }
    }
}()
```

### 4. 读写锁
```go
// 使用 sync.RWMutex 保护共享数据
type Bot struct {
    pendingTx      map[int]*beancount.PendingTransaction
    waitingForInput map[int]string
    mu             sync.RWMutex
}
```

## 快速开始

### 安装依赖
```bash
cd go
go mod download
```

### 配置
```bash
cp config.toml.example config.toml
cp .env.example .env
# 编辑配置文件
```

### 运行
```bash
make run
```

### 构建
```bash
make build
```

### 多平台构建
```bash
make build-all
```

### Docker 部署
```bash
make docker-build
make docker-run
```

### 服务器部署
```bash
# 编辑 deploy.sh 中的配置变量
./deploy.sh --create-service  # 首次部署
./deploy.sh                   # 后续部署
```

## 技术栈

- **语言**: Go 1.23
- **Telegram Bot**: go-telegram-bot-api/v5
- **Git 操作**: go-git/v5
- **WebDAV**: gowebdav
- **配置**: TOML
- **日志**: logrus + lumberjack
- **容器化**: Docker + Alpine

## 性能优势

相比 Python 版本，Go 版本具有以下优势：

1. **更低的内存占用**: Go 的内存管理更高效
2. **更快的启动速度**: 编译型语言，无需解释执行
3. **更好的并发性能**: 原生支持 goroutine，轻量级线程
4. **更简单的部署**: 单一可执行文件，无需依赖
5. **跨平台支持**: 一次编译，多平台运行

## 配置说明

### 环境变量
- `TELEGRAM_BOT_TOKEN`: Telegram Bot 令牌
- `LLM_API_KEY`: LLM API 密钥
- `GIT_USERNAME`: Git 用户名（可选）
- `GIT_PASSWORD`: Git 密码（可选）
- `WEBDAV_USERNAME`: WebDAV 用户名
- `WEBDAV_PASSWORD`: WebDAV 密码

### 配置文件 (config.toml)
- Telegram Bot 配置
- LLM 配置
- Beancount 配置
- Git 配置
- 日志配置
- WebDAV 配置

## 部署建议

1. **开发环境**: 使用 `make run` 直接运行
2. **测试环境**: 使用 Docker 容器运行
3. **生产环境**: 使用 systemd 服务管理

### systemd 服务示例
```ini
[Unit]
Description=Beancount AutoUpdate Service
After=network.target

[Service]
Type=simple
User=your_user
WorkingDirectory=/opt/beancount-autoupdate
ExecStart=/opt/beancount-autoupdate/beancount-autoupdate
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

## 后续优化建议

1. **添加健康检查接口**: HTTP 端点用于监控服务状态
2. **实现优雅关闭**: 处理 SIGTERM 信号，等待正在处理的任务完成
3. **添加指标监控**: Prometheus 指标导出
4. **实现配置热重载**: 支持不重启服务更新配置
5. **添加数据库支持**: 使用 SQLite 或 PostgreSQL 存储交易历史
6. **实现批量处理**: 支持批量上传和解析图片
7. **添加缓存机制**: 缓存账户列表等频繁访问的数据

## 注意事项

1. 确保 Git 仓库有正确的访问权限
2. WebDAV 服务器需要支持基本认证
3. LLM API 需要有足够的配额
4. Telegram Bot Token 需要保密
5. 建议使用 HTTPS 连接 WebDAV 服务器

## 许可证

MIT License

## 贡献

欢迎提交 Issue 和 Pull Request！