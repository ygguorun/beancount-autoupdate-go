# Beancount AutoUpdate - Go 版本快速启动指南

## ✅ 构建成功！

项目已成功构建，生成了可执行文件：`build/beancount-autoupdate` (13MB)

## 🚀 快速开始

### 1. 配置环境

```bash
cd go

# 复制配置文件
cp config.toml.example config.toml
cp .env.example .env
```

### 2. 编辑配置

编辑 `config.toml` 和 `.env` 文件，填入你的配置：

**config.toml**:
```toml
[telegram]
token = "your_telegram_bot_token"

[llm]
base_url = "https://open.bigmodel.cn/api/paas/v4"
model = "glm-4.6v-flash"
api_key = ""  # 在 .env 中设置

[git]
repo_url = "git@github.com:username/beancount-data.git"

[webdav]
enabled = true
url = "https://your-webdav-server.com/dav"
path = "Baidu/beancount/receipts"
```

**.env**:
```bash
TELEGRAM_BOT_TOKEN=your_bot_token
LLM_API_KEY=your_llm_api_key
WEBDAV_USERNAME=your_webdav_username
WEBDAV_PASSWORD=your_webdav_password
```

### 3. 运行程序

```bash
# 直接运行
./build/beancount-autoupdate

# 或使用 make
make run
```

### 4. 在 Telegram 中测试

1. 找到你的 Bot
2. 发送 `/start` 命令
3. 发送账单截图
4. 查看识别结果并确认

## 📦 部署选项

### 本地运行
```bash
./build/beancount-autoupdate
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

### systemd 服务
```bash
# 安装
sudo cp build/beancount-autoupdate /usr/local/bin/

# 创建服务文件
sudo nano /etc/systemd/system/beancount-autoupdate.service
```

服务文件内容：
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

启动服务：
```bash
sudo systemctl daemon-reload
sudo systemctl enable beancount-autoupdate
sudo systemctl start beancount-autoupdate
sudo systemctl status beancount-autoupdate
```

## 🔧 常用命令

```bash
make build        # 构建程序
make run          # 运行程序
make clean        # 清理构建文件
make test         # 运行测试
make lint         # 代码检查
make fmt          # 格式化代码
make help         # 查看所有命令
```

## 📊 项目特点

- ✅ 单一可执行文件，无需依赖
- ✅ 使用 goroutine 实现高性能并发
- ✅ Worker Pool 模式处理 Telegram 消息
- ✅ 图片上传和解析并发执行
- ✅ Git 操作异步执行
- ✅ 跨平台支持（Linux, macOS, Windows）

## 📝 注意事项

1. 确保 Git 仓库有正确的访问权限
2. WebDAV 服务器需要支持基本认证
3. LLM API 需要有足够的配额
4. Telegram Bot Token 需要保密
5. 建议使用 HTTPS 连接 WebDAV 服务器

## 🐛 故障排查

### 程序无法启动
- 检查配置文件是否正确
- 检查环境变量是否设置
- 查看日志文件：`logs/beancount-autoupdate.log`

### Telegram Bot 无响应
- 检查 Bot Token 是否正确
- 检查网络连接
- 查看日志输出

### Git 提交失败
- 检查 Git 仓库 URL 和权限
- 检查 SSH 密钥配置
- 查看日志输出

### WebDAV 上传失败
- 检查 WebDAV 服务器地址和认证信息
- 检查网络连接
- 查看日志输出

## 📞 获取帮助

- 查看日志：`tail -f logs/beancount-autoupdate.log`
- 查看帮助：`make help`
- 查看文档：`README.md`

祝使用愉快！🎉