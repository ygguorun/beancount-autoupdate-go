#!/bin/bash
#
# Beancount AutoUpdate 便携式更新脚本
# 用法: bash update-portable.sh [version]
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
SERVICE_NAME="beancount-autoupdate-$(whoami)"
PID_FILE="$SCRIPT_DIR/$APP_NAME.pid"
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

# 检查服务状态
check_service() {
    if [[ -f "$PID_FILE" ]]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            print_info "服务正在运行，将停止服务进行更新..."
            kill "$PID"
            sleep 2
            SERVICE_WAS_RUNNING=true
        else
            print_info "服务未运行"
            SERVICE_WAS_RUNNING=false
        fi
    else
        print_info "服务未运行"
        SERVICE_WAS_RUNNING=false
    fi

    # 检查 systemd 服务（Linux）
    if [[ "$OS" == "Linux" ]] && systemctl --user is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
        print_info "systemd 服务正在运行，将停止..."
        systemctl --user stop "$SERVICE_NAME"
        SYSTEMD_SERVICE_WAS_RUNNING=true
    else
        SYSTEMD_SERVICE_WAS_RUNNING=false
    fi
}

# 获取当前版本
get_current_version() {
    if [[ -f "$BIN_DIR/$APP_NAME" ]]; then
        CURRENT_VERSION=$("$BIN_DIR/$APP_NAME" --version 2>/dev/null | grep "Version:" | awk '{print $2}')
        print_info "当前版本: $CURRENT_VERSION"
    else
        print_warn "未找到已安装的程序"
        CURRENT_VERSION="unknown"
    fi
}

# 下载二进制文件
download_binary() {
    print_info "正在下载 $APP_NAME $VERSION..."

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

    # 备份旧版本
    if [[ -f "$BIN_DIR/$APP_NAME" ]]; then
        print_info "备份旧版本..."
        cp "$BIN_DIR/$APP_NAME" "$BIN_DIR/${APP_NAME}.bak"
    fi

    # 安装新版本
    print_info "安装新版本到 $BIN_DIR..."
    mv "$APP_NAME" "$BIN_DIR/"
    chmod +x "$BIN_DIR/$APP_NAME"

    # 清理临时目录
    rm -rf "$TMP_DIR"

    print_info "更新完成"
}

# 显示新版本
show_new_version() {
    if [[ -f "$BIN_DIR/$APP_NAME" ]]; then
        NEW_VERSION=$("$BIN_DIR/$APP_NAME" --version 2>/dev/null | grep "Version:" | awk '{print $2}')
        print_info "新版本: $NEW_VERSION"
    fi
}

# 恢复服务
restore_service() {
    if [[ "$SERVICE_WAS_RUNNING" == true ]]; then
        print_info "重新启动服务..."
        cd "$SCRIPT_DIR"
        nohup "$BIN_DIR/$APP_NAME" --config "$SCRIPT_DIR/config/config.toml" > "$SCRIPT_DIR/logs/service.log" 2>&1 &
        PID=$!
        echo $PID > "$PID_FILE"
        sleep 2

        if ps -p "$PID" > /dev/null 2>&1; then
            print_info "服务启动成功"
        else
            print_error "服务启动失败，请检查日志"
        fi
    fi

    if [[ "$SYSTEMD_SERVICE_WAS_RUNNING" == true ]]; then
        print_info "重新启动 systemd 服务..."
        systemctl --user start "$SERVICE_NAME"
        sleep 2

        if systemctl --user is-active --quiet "$SERVICE_NAME"; then
            print_info "systemd 服务启动成功"
        else
            print_error "systemd 服务启动失败"
        fi
    fi
}

# 打印更新完成信息
print_completion() {
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}  更新完成！${NC}"
    echo -e "${GREEN}========================================${NC}"
    echo ""
    echo "查看服务状态: bash $SCRIPT_DIR/service-portable.sh status"
    echo "查看日志: bash $SCRIPT_DIR/service-portable.sh logs"
    echo ""
}

# 主函数
main() {
    print_info "开始更新 $APP_NAME (便携模式)..."
    echo ""

    detect_os
    detect_arch
    get_current_version
    check_service
    download_binary
    show_new_version
    restore_service

    print_completion
}

# 运行主函数
main "$@"