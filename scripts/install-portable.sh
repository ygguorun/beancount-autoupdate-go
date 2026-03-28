#!/bin/bash
#
# Beancount AutoUpdate 便携式安装脚本
# 所有文件都放在脚本所在目录，使用当前用户运行
# 用法: bash install-portable.sh [version]
#

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 配置变量
APP_NAME="beancount-autoupdate"
REPO_URL="https://github.com/ygguorun/beancount-autoupdate-go"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="$SCRIPT_DIR/bin"
CONFIG_DIR="$SCRIPT_DIR/config"
DATA_DIR="$SCRIPT_DIR/data"
LOG_DIR="$SCRIPT_DIR/logs"
SERVICE_NAME="beancount-autoupdate-$(whoami)"
VERSION=${1:-"latest"}

# 打印信息函数
print_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检测系统架构
detect_arch() {
    ARCH=$(uname -m)
    case $ARCH in
        x86_64|i386|i686)
            ARCH="x86_64"
            ;;
        aarch64|arm64)
            ARCH="arm64"
            ;;
        *)
            print_error "不支持的架构: $ARCH"
            exit 1
            ;;
    esac
    print_info "检测到架构: $ARCH"
}

# 检测操作系统
detect_os() {
    OS=$(uname -s)
    case $OS in
        Linux)
            OS="Linux"
            ;;
        Darwin)
            OS="Darwin"
            ;;
        *)
            print_error "不支持的操作系统: $OS"
            exit 1
            ;;
    esac
    print_info "检测到操作系统: $OS"
}

# 下载二进制文件
download_binary() {
    print_info "正在下载 $APP_NAME $VERSION..."

    # 创建 bin 目录
    mkdir -p "$BIN_DIR"

    # 创建临时目录
    TMP_DIR=$(mktemp -d)
    cd "$TMP_DIR"

    # 确定下载 URL
    if [[ "$VERSION" == "latest" ]]; then
        DOWNLOAD_URL="$REPO_URL/releases/latest/download/${APP_NAME}_${OS}_${ARCH}.tar.gz"
    else
        DOWNLOAD_URL="$REPO_URL/releases/download/${VERSION}/${APP_NAME}_${OS}_${ARCH}.tar.gz"
    fi

    print_info "下载地址: $DOWNLOAD_URL"

    # 下载文件
    if ! curl -fsSL -o "${APP_NAME}.tar.gz" "$DOWNLOAD_URL"; then
        print_error "下载失败"
        rm -rf "$TMP_DIR"
        exit 1
    fi

    # 解压
    print_info "解压文件..."
    tar -xzf "${APP_NAME}.tar.gz"

    # 安装二进制文件
    print_info "安装二进制文件到 $BIN_DIR..."
    mv "$APP_NAME" "$BIN_DIR/"
    chmod +x "$BIN_DIR/$APP_NAME"

    # 清理临时目录
    rm -rf "$TMP_DIR"

    print_info "二进制文件安装完成"
}

# 创建必要的目录
create_directories() {
    print_info "创建必要的目录..."
    mkdir -p "$CONFIG_DIR"
    mkdir -p "$DATA_DIR"
    mkdir -p "$LOG_DIR"
    print_info "目录创建完成"
}

# 安装配置文件
install_config() {
    print_info "安装配置文件..."

    if [[ ! -f "$CONFIG_DIR/config.toml" ]]; then
        cat > "$CONFIG_DIR/config.toml" << 'EOF'
# Beancount AutoUpdate 配置文件

[beancount]
data_dir = "./data"
title = "我的账本"
operating_currency = "CNY"

[git]
repo_url = ""
auto_commit = true
auto_push = true
commit_message_prefix = "Update"

[llm]
base_url = "https://api.openai.com/v1"
model = "gpt-4o"
api_key = ""
timeout = 120

[telegram]
token = ""
allowed_users = []

[webdav]
enabled = false
url = ""
username = ""
password = ""
verify_ssl = true
filename_template = "{date}_{order_id}.jpg"
path = "/receipts"

[logging]
log_dir = "./logs"
log_file = "app.log"
level = "info"
max_bytes = 10485760
backup_count = 10

[http_server]
enabled = false
listen_addr = "127.0.0.1:8080"
target_user_id = 0
max_upload_size_mb = 20
read_timeout_sec = 15
write_timeout_sec = 30
EOF
        print_info "默认配置文件已创建"
        print_warn "请编辑 $CONFIG_DIR/config.toml 配置您的设置"
    else
        print_warn "配置文件已存在，跳过创建"
    fi
}

# 安装 systemd 用户服务（仅 Linux）
install_service() {
    # 只在 Linux 上安装 systemd 服务
    if [[ "$OS" != "Linux" ]]; then
        print_warn "systemd 服务仅在 Linux 上支持，跳过服务安装"
        return
    fi

    print_info "安装 systemd 用户服务..."

    mkdir -p "$HOME/.config/systemd/user"

    cat > "$HOME/.config/systemd/user/${SERVICE_NAME}.service" << EOF
[Unit]
Description=Beancount AutoUpdate Service
After=network.target

[Service]
Type=simple
WorkingDirectory=$SCRIPT_DIR
ExecStart=$BIN_DIR/$APP_NAME --config $CONFIG_DIR/config.toml
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
EOF

    # 重新加载 systemd
    systemctl --user daemon-reload

    print_info "systemd 用户服务已安装"
}

# 安装启动脚本
install_start_script() {
    print_info "安装启动脚本..."

    cat > "$SCRIPT_DIR/start.sh" << 'EOF'
#!/bin/bash
cd "$(dirname "$0")"
./bin/beancount-autoupdate --config ./config/config.toml
EOF
    chmod +x "$SCRIPT_DIR/start.sh"
    print_info "启动脚本已创建: $SCRIPT_DIR/start.sh"
}

# 设置权限
set_permissions() {
    print_info "设置文件权限..."
    chmod 755 "$BIN_DIR/$APP_NAME"
    chmod 755 "$CONFIG_DIR"
    chmod 755 "$DATA_DIR"
    chmod 755 "$LOG_DIR"
    chmod 644 "$CONFIG_DIR/config.toml"
    print_info "权限设置完成"
}

# 打印安装完成信息
print_completion() {
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}  安装完成！${NC}"
    echo -e "${GREEN}========================================${NC}"
    echo ""
    echo "安装位置: $SCRIPT_DIR"
    echo ""
    echo "目录结构:"
    echo "  $SCRIPT_DIR/"
    echo "  ├── bin/"
    echo "  │   └── $APP_NAME          # 可执行文件"
    echo "  ├── config/"
    echo "  │   └── config.toml        # 配置文件"
    echo "  ├── data/                  # 数据目录"
    echo "  ├── logs/                  # 日志目录"
    echo "  └── start.sh               # 快速启动脚本"
    echo ""

    if [[ "$OS" == "Linux" ]]; then
        echo "服务管理:"
        echo "  启动服务: systemctl --user start $SERVICE_NAME"
        echo "  停止服务: systemctl --user stop $SERVICE_NAME"
        echo "  重启服务: systemctl --user restart $SERVICE_NAME"
        echo "  查看状态: systemctl --user status $SERVICE_NAME"
        echo "  开机自启: systemctl --user enable $SERVICE_NAME"
        echo "  查看日志: journalctl --user -u $SERVICE_NAME -f"
        echo ""
    fi

    echo "后续步骤:"
    echo "  1. 编辑配置文件: nano $CONFIG_DIR/config.toml"
    echo ""
    echo "  2. 配置 Git 认证（如果需要推送代码）:"
    echo "     - 推荐使用 HTTPS 方式:"
    echo "       cd $SCRIPT_DIR"
    echo "       git config --global credential.helper store"
    echo "       # 然后在首次推送时输入 GitHub token"
    echo ""
    echo "     - 或使用 SSH 方式:"
    echo "       # 确保 SSH 密钥已添加到 GitHub"
    echo "       # 并在配置文件中使用 SSH URL"
    echo ""
    if [[ "$OS" == "Linux" ]]; then
        echo "  3. 启动服务: systemctl --user start $SERVICE_NAME"
        echo ""
        echo "或者直接运行:"
        echo "  $SCRIPT_DIR/start.sh"
    else
        echo "  3. 运行程序: $SCRIPT_DIR/start.sh"
    fi
    echo ""
    echo "更多信息请参考: $SCRIPT_DIR/../docs/GIT_SSH_AUTH.md"
    echo ""
}

# 主函数
main() {
    print_info "开始安装 $APP_NAME (便携模式)..."
    echo ""
    print_info "安装目录: $SCRIPT_DIR"
    echo ""

    detect_os
    detect_arch
    download_binary
    create_directories
    install_config
    install_service
    install_start_script
    set_permissions

    print_completion
}

# 运行主函数
main "$@"
