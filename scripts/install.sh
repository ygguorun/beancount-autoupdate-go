#!/bin/bash
#
# Beancount AutoUpdate 安装脚本
# 用法: sudo bash install.sh [version]
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
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/beancount-autoupdate"
DATA_DIR="/var/lib/beancount-autoupdate"
LOG_DIR="/var/log/beancount-autoupdate"
SERVICE_NAME="beancount-autoupdate"
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

# 检查是否为 root 用户
check_root() {
    if [[ $EUID -ne 0 ]]; then
        print_error "此脚本需要 root 权限运行"
        echo "请使用: sudo bash $0"
        exit 1
    fi
}

# 检测系统架构
detect_arch() {
    ARCH=$(uname -m)
    case $ARCH in
        x86_64)
            ARCH="amd64"
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

# 下载二进制文件
download_binary() {
    print_info "正在下载 $APP_NAME $VERSION..."

    # 创建临时目录
    TMP_DIR=$(mktemp -d)
    cd "$TMP_DIR"

    # 确定下载 URL
    if [[ "$VERSION" == "latest" ]]; then
        DOWNLOAD_URL="$REPO_URL/releases/latest/download/${APP_NAME}_Linux_${ARCH}.tar.gz"
    else
        DOWNLOAD_URL="$REPO_URL/releases/download/v${VERSION}/${APP_NAME}_Linux_${ARCH}.tar.gz"
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
    print_info "安装二进制文件到 $INSTALL_DIR..."
    mv "$APP_NAME" "$INSTALL_DIR/"
    chmod +x "$INSTALL_DIR/$APP_NAME"

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
data_dir = "/var/lib/beancount-autoupdate/data"
title = "我的账本"
operating_currency = "CNY"

[git]
repo_url = ""
auto_commit = true
auto_push = true
commit_message_prefix = "Update"
push_timeout = 60
conflict_strategy = "theirs"

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
log_dir = "/var/log/beancount-autoupdate"
log_file = "app.log"
level = "info"
max_bytes = 10485760
backup_count = 10
EOF
        print_info "默认配置文件已创建"
        print_warn "请编辑 $CONFIG_DIR/config.toml 配置您的设置"
    else
        print_warn "配置文件已存在，跳过创建"
    fi
}

# 安装 systemd 服务
install_service() {
    print_info "安装 systemd 服务..."

    cat > "/etc/systemd/system/${SERVICE_NAME}.service" << EOF
[Unit]
Description=Beancount AutoUpdate Service
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$DATA_DIR
ExecStart=$INSTALL_DIR/$APP_NAME --config $CONFIG_DIR/config.toml
Restart=always
RestartSec=10
StandardOutput=append:$LOG_DIR/service.log
StandardError=append:$LOG_DIR/service_error.log

# 安全设置
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=$DATA_DIR $LOG_DIR

[Install]
WantedBy=multi-user.target
EOF

    # 重新加载 systemd
    systemctl daemon-reload

    print_info "systemd 服务已安装"
}

# 设置权限
set_permissions() {
    print_info "设置文件权限..."
    chmod 755 "$INSTALL_DIR/$APP_NAME"
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
    echo "安装位置:"
    echo "  二进制文件: $INSTALL_DIR/$APP_NAME"
    echo "  配置文件: $CONFIG_DIR/config.toml"
    echo "  数据目录: $DATA_DIR"
    echo "  日志目录: $LOG_DIR"
    echo ""
    echo "后续步骤:"
    echo "  1. 编辑配置文件: nano $CONFIG_DIR/config.toml"
    echo "  2. 启动服务: systemctl start $SERVICE_NAME"
    echo "  3. 设置开机自启: systemctl enable $SERVICE_NAME"
    echo "  4. 查看服务状态: systemctl status $SERVICE_NAME"
    echo "  5. 查看日志: journalctl -u $SERVICE_NAME -f"
    echo ""
}

# 主函数
main() {
    print_info "开始安装 $APP_NAME..."
    echo ""

    check_root
    detect_arch
    download_binary
    create_directories
    install_config
    install_service
    set_permissions

    print_completion
}

# 运行主函数
main "$@"