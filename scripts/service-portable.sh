#!/bin/bash
#
# Beancount AutoUpdate 便携式服务管理脚本
# 用法: bash service-portable.sh {start|stop|restart|status|enable|disable|logs|help}
#

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置变量
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_NAME="beancount-autoupdate"
BIN_DIR="$SCRIPT_DIR/bin"
CONFIG_DIR="$SCRIPT_DIR/config"
LOG_DIR="$SCRIPT_DIR/logs"
SERVICE_NAME="beancount-autoupdate-$(whoami)"
PID_FILE="$SCRIPT_DIR/$APP_NAME.pid"

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

print_header() {
    echo -e "${BLUE}$1${NC}"
}

# 检查是否已安装
check_installed() {
    if [[ ! -f "$BIN_DIR/$APP_NAME" ]]; then
        print_error "$APP_NAME 未安装"
        echo "请先运行安装脚本: bash install-portable.sh"
        exit 1
    fi
}

# 检查操作系统
check_os() {
    OS=$(uname -s)
}

# 启动服务
start_service() {
    print_header "启动服务..."

    # 检查是否已经在运行
    if [[ -f "$PID_FILE" ]]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            print_warn "服务已经在运行 (PID: $PID)"
            return 0
        else
            print_info "清理旧的 PID 文件"
            rm -f "$PID_FILE"
        fi
    fi

    # 启动服务
    cd "$SCRIPT_DIR"
    nohup "$BIN_DIR/$APP_NAME" --config "$CONFIG_DIR/config.toml" > "$LOG_DIR/service.log" 2>&1 &
    PID=$!
    echo $PID > "$PID_FILE"

    sleep 2

    if ps -p "$PID" > /dev/null 2>&1; then
        print_info "服务启动成功 (PID: $PID)"
    else
        print_error "服务启动失败"
        print_info "查看错误日志: cat $LOG_DIR/service.log"
        rm -f "$PID_FILE"
        exit 1
    fi
}

# 停止服务
stop_service() {
    print_header "停止服务..."

    if [[ ! -f "$PID_FILE" ]]; then
        print_warn "服务未运行"
        return 0
    fi

    PID=$(cat "$PID_FILE")
    if ps -p "$PID" > /dev/null 2>&1; then
        kill "$PID"
        sleep 2

        if ps -p "$PID" > /dev/null 2>&1; then
            print_warn "服务未正常停止，强制终止..."
            kill -9 "$PID"
        fi

        print_info "服务已停止"
    else
        print_warn "服务未运行"
    fi

    rm -f "$PID_FILE"
}

# 重启服务
restart_service() {
    print_header "重启服务..."
    stop_service
    sleep 1
    start_service
}

# 显示服务状态
show_status() {
    print_header "服务状态"
    echo ""

    if [[ -f "$PID_FILE" ]]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            echo "状态: 运行中"
            echo "PID: $PID"
            echo ""
            echo "进程信息:"
            ps -p "$PID" -o pid,ppid,cmd
        else
            echo "状态: 已停止 (PID 文件存在但进程不存在)"
            rm -f "$PID_FILE"
        fi
    else
        echo "状态: 未运行"
    fi

    echo ""

    # 如果是 Linux，也显示 systemd 服务状态
    if [[ "$OS" == "Linux" ]]; then
        if systemctl --user is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
            echo "systemd 服务状态:"
            systemctl --user status "$SERVICE_NAME" --no-pager | head -15
        fi
    fi
}

# 查看日志
show_logs() {
    print_header "查看日志 (按 Ctrl+C 退出)..."

    if [[ ! -f "$LOG_DIR/service.log" ]]; then
        print_error "日志文件不存在: $LOG_DIR/service.log"
        return 1
    fi

    echo ""
    tail -f "$LOG_DIR/service.log"
}

# 查看最近日志
show_recent_logs() {
    print_header "最近 50 条日志"

    if [[ ! -f "$LOG_DIR/service.log" ]]; then
        print_error "日志文件不存在: $LOG_DIR/service.log"
        return 1
    fi

    echo ""
    tail -n 50 "$LOG_DIR/service.log"
}

# 启用开机自启（仅 Linux）
enable_service() {
    print_header "启用开机自启..."

    if [[ "$OS" != "Linux" ]]; then
        print_warn "开机自启仅在 Linux 系统上支持"
        return
    fi

    # 启用 lingering 以允许用户服务在用户未登录时运行
    if ! loginctl show-user "$USER" | grep -q "Linger=yes"; then
        print_info "启用 lingering..."
        sudo loginctl enable-linger "$USER" || {
            print_warn "无法启用 lingering，需要 sudo 权限"
            print_warn "请手动运行: sudo loginctl enable-linger $USER"
        }
    fi

    systemctl --user enable "$SERVICE_NAME"
    print_info "已设置开机自启"
}

# 禁用开机自启（仅 Linux）
disable_service() {
    print_header "禁用开机自启..."

    if [[ "$OS" != "Linux" ]]; then
        print_warn "开机自启仅在 Linux 系统上支持"
        return
    fi

    systemctl --user disable "$SERVICE_NAME"
    print_info "已禁用开机自启"
}

# 显示帮助信息
show_help() {
    echo "Beancount AutoUpdate 便携式服务管理脚本"
    echo ""
    echo "用法: bash $0 {command}"
    echo ""
    echo "可用命令:"
    echo "  start      - 启动服务"
    echo "  stop       - 停止服务"
    echo "  restart    - 重启服务"
    echo "  status     - 查看服务状态"
    echo "  logs       - 实时查看日志"
    echo "  recent     - 查看最近日志"
    echo "  enable     - 启用开机自启 (仅 Linux)"
    echo "  disable    - 禁用开机自启 (仅 Linux)"
    echo "  help       - 显示帮助信息"
    echo ""
    echo "示例:"
    echo "  bash $0 start"
    echo "  bash $0 status"
    echo "  bash $0 logs"
    echo ""
}

# 主函数
main() {
    local command=${1:-"help"}

    check_os
    check_installed

    case $command in
        start)
            start_service
            ;;
        stop)
            stop_service
            ;;
        restart)
            restart_service
            ;;
        status)
            show_status
            ;;
        logs)
            show_logs
            ;;
        recent)
            show_recent_logs
            ;;
        enable)
            enable_service
            ;;
        disable)
            disable_service
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            print_error "未知命令: $command"
            echo ""
            show_help
            exit 1
            ;;
    esac
}

# 运行主函数
main "$@"